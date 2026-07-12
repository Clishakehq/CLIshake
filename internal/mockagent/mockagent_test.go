package mockagent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clishakehq/clishake/internal/wire"
)

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

// syncBuf is an io.Writer safe for concurrent writes (Run's goroutine) and
// reads (the test goroutine's polling assertions).
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// waitFor polls cond until it returns true or the deadline elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s (buffer content unknown)", timeout)
}

func appendInbox(t *testing.T, dir string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, "inbox.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func envLine(id, from, text, taskID string) string {
	return wire.EncodeEnvelope(wire.Envelope{
		From:      from,
		Text:      text,
		Timestamp: time.Now(),
		MsgID:     id,
		Type:      "message",
		TaskID:    taskID,
	})
}

func testOptions(t *testing.T, dir string, out *syncBuf) Options {
	t.Helper()
	return Options{
		Name:           "builder",
		Role:           "builder",
		AgentDir:       dir,
		HeartbeatEvery: time.Hour, // effectively disabled unless a test wants it
		PollEvery:      10 * time.Millisecond,
		Out:            out,
	}
}

// stubSleep replaces the package-level sleepFn with a no-op for the duration
// of the calling test.
func stubSleep(t *testing.T) {
	t.Helper()
	orig := sleepFn
	sleepFn = func(time.Duration) {}
	t.Cleanup(func() { sleepFn = orig })
}

// runAndStop starts Run in a goroutine and returns a function that sends a
// "stop" command and waits for Run to exit, failing the test if it doesn't
// exit in time. It also returns the exit-code channel directly for tests
// that want to trigger exit themselves (e.g. via "fail!").
func runAndStop(t *testing.T, opts Options) (dir string, done chan int) {
	t.Helper()
	done = make(chan int, 1)
	go func() { done <- Run(opts) }()
	return opts.AgentDir, done
}

func stopAndWait(t *testing.T, dir string, done chan int, wantCode int) {
	t.Helper()
	appendInbox(t, dir, envLine("stop1", "lead", "stop", ""))
	select {
	case code := <-done:
		if code != wantCode {
			t.Fatalf("exit code = %d, want %d", code, wantCode)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit after stop command")
	}
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestRun_ReadyOnStart(t *testing.T) {
	dir := t.TempDir()
	out := &syncBuf{}
	opts := testOptions(t, dir, out)

	_, done := runAndStop(t, opts)

	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), "[builder] mock agent starting (role=builder)")
	})
	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), wire.Marker) &&
			strings.Contains(out.String(), `"type":"status"`) &&
			strings.Contains(out.String(), `"status":"ready"`)
	})

	stopAndWait(t, dir, done, 0)
	if !strings.Contains(out.String(), `"status":"stopped"`) {
		t.Errorf("expected stopped status in output, got: %s", out.String())
	}
}

func TestRun_UnknownCommandProducesAck(t *testing.T) {
	dir := t.TempDir()
	out := &syncBuf{}
	opts := testOptions(t, dir, out)

	_, done := runAndStop(t, opts)

	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"status":"ready"`)
	})

	appendInbox(t, dir, envLine("m1", "lead", "hello there", ""))

	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), "[builder] <- message from lead: hello there")
	})
	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"type":"message"`) &&
			strings.Contains(out.String(), `"text":"ack: hello there"`)
	})

	stopAndWait(t, dir, done, 0)
}

func TestRun_WorkProducesRunningThenWaitingAndDone(t *testing.T) {
	stubSleep(t)
	dir := t.TempDir()
	out := &syncBuf{}
	opts := testOptions(t, dir, out)

	_, done := runAndStop(t, opts)

	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"status":"ready"`)
	})

	appendInbox(t, dir, envLine("m1", "lead", "work 2", "task_abc"))

	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"status":"running"`)
	})
	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"type":"task"`) &&
			strings.Contains(out.String(), `"task_id":"task_abc"`) &&
			strings.Contains(out.String(), `"status":"completed"`)
	})
	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"type":"message"`) &&
			strings.Contains(out.String(), `"text":"done: work 2"`)
	})
	waitFor(t, 2*time.Second, func() bool {
		// waiting status should appear after running, as the final status line
		s := out.String()
		lastRunning := strings.LastIndex(s, `"status":"running"`)
		lastWaiting := strings.LastIndex(s, `"status":"waiting"`)
		return lastWaiting > lastRunning
	})
	if strings.Count(out.String(), "[builder] step") != 2 {
		t.Errorf("expected 2 step lines, got output: %s", out.String())
	}

	stopAndWait(t, dir, done, 0)
}

func TestRun_FailReturnsExitCode1(t *testing.T) {
	dir := t.TempDir()
	out := &syncBuf{}
	opts := testOptions(t, dir, out)

	_, done := runAndStop(t, opts)

	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"status":"ready"`)
	})

	appendInbox(t, dir, envLine("m1", "lead", "fail!", ""))

	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit after fail!")
	}

	if !strings.Contains(out.String(), `"status":"failed"`) {
		t.Errorf("expected failed status in output, got: %s", out.String())
	}
	if !strings.Contains(out.String(), "ERROR") {
		t.Errorf("expected an error line in output, got: %s", out.String())
	}
}

func TestRun_CompleteReturnsExitCode0(t *testing.T) {
	dir := t.TempDir()
	out := &syncBuf{}
	opts := testOptions(t, dir, out)

	_, done := runAndStop(t, opts)

	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"status":"ready"`)
	})

	appendInbox(t, dir, envLine("m1", "lead", "complete", "task_xyz"))

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit after complete")
	}

	s := out.String()
	if !strings.Contains(s, `"status":"completed"`) {
		t.Errorf("expected completed status in output, got: %s", s)
	}
	if !strings.Contains(s, `"text":"task complete"`) {
		t.Errorf("expected task complete message in output, got: %s", s)
	}
	if !strings.Contains(s, `"task_id":"task_xyz"`) {
		t.Errorf("expected task id in output, got: %s", s)
	}
}

func TestRun_SpawnEmitsSubagentLifecycle(t *testing.T) {
	stubSleep(t)
	dir := t.TempDir()
	out := &syncBuf{}
	opts := testOptions(t, dir, out)

	_, done := runAndStop(t, opts)

	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"status":"ready"`)
	})

	appendInbox(t, dir, envLine("m1", "lead", "spawn tester", ""))

	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"type":"subagent"`) &&
			strings.Contains(out.String(), `"name":"tester"`) &&
			strings.Contains(out.String(), `"status":"running"`)
	})
	waitFor(t, 2*time.Second, func() bool {
		s := out.String()
		return strings.Contains(s, "[builder/tester] working...")
	})
	waitFor(t, 2*time.Second, func() bool {
		s := out.String()
		return strings.Contains(s, `"type":"subagent"`) && strings.Contains(s, `"status":"completed"`) &&
			strings.Contains(s, `"text":"sub-agent tester finished"`)
	})

	stopAndWait(t, dir, done, 0)
}

func TestRun_GarbageInboxLinesIgnored(t *testing.T) {
	dir := t.TempDir()
	out := &syncBuf{}
	opts := testOptions(t, dir, out)

	_, done := runAndStop(t, opts)

	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"status":"ready"`)
	})

	// Garbage / undecodable lines interleaved with one valid envelope.
	appendInbox(t, dir,
		"not json at all",
		`{"broken": `,
		`{"from":"lead","text":"missing msg id","type":"message"}`, // missing msg_id -> undecodable
		envLine("good1", "lead", "hello", ""),
	)

	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"text":"ack: hello"`)
	})

	// The garbage lines must never have produced attributed message lines.
	if strings.Contains(out.String(), "not json at all") {
		t.Errorf("garbage line leaked into output: %s", out.String())
	}
	if strings.Contains(out.String(), "missing msg id") {
		t.Errorf("undecodable envelope (missing msg_id) was processed: %s", out.String())
	}

	stopAndWait(t, dir, done, 0)
}

func TestRun_InboxTruncationResetsOffset(t *testing.T) {
	dir := t.TempDir()
	out := &syncBuf{}
	opts := testOptions(t, dir, out)

	_, done := runAndStop(t, opts)

	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"status":"ready"`)
	})

	appendInbox(t, dir, envLine("m1", "lead", "hello first", ""))
	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"text":"ack: hello first"`)
	})

	// Truncate/replace the inbox file with new, shorter content.
	if err := os.WriteFile(filepath.Join(dir, "inbox.jsonl"), []byte(envLine("m2", "lead", "second", "")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"text":"ack: second"`)
	})

	stopAndWait(t, dir, done, 0)
}

func TestRun_MissingInboxFileTreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	out := &syncBuf{}
	opts := testOptions(t, dir, out)

	// Note: no inbox.jsonl file created at all yet.
	_, done := runAndStop(t, opts)

	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"status":"ready"`)
	})

	// Give the poll loop a couple of cycles to prove it doesn't error/crash
	// on a missing file.
	time.Sleep(50 * time.Millisecond)

	appendInbox(t, dir, envLine("m1", "lead", "hi", ""))
	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"text":"ack: hi"`)
	})

	stopAndWait(t, dir, done, 0)
}

func TestRun_StatusQueryReflectsCurrentStatus(t *testing.T) {
	dir := t.TempDir()
	out := &syncBuf{}
	opts := testOptions(t, dir, out)

	_, done := runAndStop(t, opts)

	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"status":"ready"`)
	})

	appendInbox(t, dir, envLine("m1", "lead", "status?", ""))
	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"text":"status: ready"`)
	})

	stopAndWait(t, dir, done, 0)
}
