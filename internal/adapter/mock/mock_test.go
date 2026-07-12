package mock

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/clishakehq/clishake/internal/adapter"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/wire"
)

func TestName(t *testing.T) {
	if got := New().Name(); got != "mock" {
		t.Errorf("Name() = %q, want %q", got, "mock")
	}
}

func TestCapabilities(t *testing.T) {
	want := map[domain.Capability]bool{
		domain.CapStructuredInput:   true,
		domain.CapStructuredOutput:  true,
		domain.CapSubagents:         true,
		domain.CapTaskEvents:        true,
		domain.CapGracefulInterrupt: true,
		domain.CapWorkdirOverride:   true,
	}
	got := New().Capabilities()
	if len(got) != len(want) {
		t.Fatalf("Capabilities() returned %d items, want %d: %v", len(got), len(want), got)
	}
	for _, c := range got {
		if !want[c] {
			t.Errorf("unexpected capability %q", c)
		}
		delete(want, c)
	}
	if len(want) != 0 {
		t.Errorf("missing capabilities: %v", want)
	}
}

func TestDetect(t *testing.T) {
	ok, version, err := New().Detect()
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if !ok {
		t.Error("Detect() installed = false, want true")
	}
	if version == "" {
		t.Error("Detect() version is empty")
	}
}

func TestValidateConfig(t *testing.T) {
	a := New()
	if err := a.ValidateConfig(nil); err != nil {
		t.Errorf("ValidateConfig(nil) error = %v, want nil", err)
	}
	if err := a.ValidateConfig(map[string]string{"anything": "goes"}); err != nil {
		t.Errorf("ValidateConfig(arbitrary) error = %v, want nil", err)
	}
	if err := a.ValidateConfig(map[string]string{"fail_validate": "true"}); err == nil {
		t.Error("ValidateConfig(fail_validate=true) error = nil, want non-nil")
	}
	if err := a.ValidateConfig(map[string]string{"fail_validate": "false"}); err != nil {
		t.Errorf("ValidateConfig(fail_validate=false) error = %v, want nil", err)
	}
}

func TestBuildLaunch(t *testing.T) {
	orig := selfExe
	selfExe = func() (string, error) { return "/usr/local/bin/clishake", nil }
	t.Cleanup(func() { selfExe = orig })

	a := New()
	ag := &domain.Agent{ID: "ag_1234", Name: "builder", Role: "builder"}

	spec, err := a.BuildLaunch(ag, "/proj")
	if err != nil {
		t.Fatalf("BuildLaunch() error = %v", err)
	}

	wantCmd := []string{
		"/usr/local/bin/clishake", "mock-agent",
		"--name", "builder",
		"--role", "builder",
		"--agent-dir", filepath.Join("/proj", ".clishake", "agents", "ag_1234"),
	}
	if !reflect.DeepEqual(spec.Command, wantCmd) {
		t.Errorf("Command = %v, want %v", spec.Command, wantCmd)
	}
	if spec.WorkDir != "/proj" {
		t.Errorf("WorkDir = %q, want %q (fallback to projectDir)", spec.WorkDir, "/proj")
	}

	// WorkDir override on the agent record should win.
	ag.WorkDir = "/proj/worktrees/builder"
	spec, err = a.BuildLaunch(ag, "/proj")
	if err != nil {
		t.Fatalf("BuildLaunch() error = %v", err)
	}
	if spec.WorkDir != "/proj/worktrees/builder" {
		t.Errorf("WorkDir = %q, want agent override", spec.WorkDir)
	}
}

func TestBuildLaunch_SelfExeError(t *testing.T) {
	orig := selfExe
	selfExe = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { selfExe = orig })

	_, err := New().BuildLaunch(&domain.Agent{ID: "ag_1", Name: "b"}, "/proj")
	if err == nil {
		t.Error("BuildLaunch() error = nil, want non-nil when selfExe fails")
	}
}

func TestInputMode(t *testing.T) {
	if got := New().InputMode(); got != adapter.InputFile {
		t.Errorf("InputMode() = %q, want %q", got, adapter.InputFile)
	}
}

func TestFormatInput_RoundTrip(t *testing.T) {
	a := New()
	ag := &domain.Agent{Name: "builder"}
	now := time.Now().UTC().Truncate(time.Second)

	msg := domain.Message{
		ID:        "msg_1",
		Sender:    "lead",
		Body:      "please implement the widget",
		TaskID:    "task_9",
		ReplyTo:   "msg_0",
		CreatedAt: now,
	}

	line, err := a.FormatInput(ag, msg)
	if err != nil {
		t.Fatalf("FormatInput() error = %v", err)
	}

	env, ok := wire.DecodeEnvelope(line)
	if !ok {
		t.Fatalf("DecodeEnvelope() failed to decode FormatInput output: %q", line)
	}
	if env.From != msg.Sender {
		t.Errorf("From = %q, want %q", env.From, msg.Sender)
	}
	if env.Text != msg.Body {
		t.Errorf("Text = %q, want %q", env.Text, msg.Body)
	}
	if env.MsgID != msg.ID {
		t.Errorf("MsgID = %q, want %q", env.MsgID, msg.ID)
	}
	if env.TaskID != msg.TaskID {
		t.Errorf("TaskID = %q, want %q", env.TaskID, msg.TaskID)
	}
	if env.ReplyTo != msg.ReplyTo {
		t.Errorf("ReplyTo = %q, want %q", env.ReplyTo, msg.ReplyTo)
	}
	if env.Type != "message" {
		t.Errorf("Type = %q, want %q", env.Type, "message")
	}
	if !env.Timestamp.Equal(now) {
		t.Errorf("Timestamp = %v, want %v", env.Timestamp, now)
	}
	if env.Summary != msg.Body {
		t.Errorf("Summary = %q, want %q (body under 60 chars)", env.Summary, msg.Body)
	}
}

func TestFormatInput_SummaryTruncatedTo60(t *testing.T) {
	a := New()
	ag := &domain.Agent{Name: "builder"}
	body := strings.Repeat("x", 200)

	line, err := a.FormatInput(ag, domain.Message{ID: "m1", Sender: "lead", Body: body})
	if err != nil {
		t.Fatalf("FormatInput() error = %v", err)
	}
	env, ok := wire.DecodeEnvelope(line)
	if !ok {
		t.Fatalf("DecodeEnvelope() failed: %q", line)
	}
	if len(env.Summary) != 60 {
		t.Errorf("Summary length = %d, want 60", len(env.Summary))
	}
	if env.Text != body {
		t.Errorf("Text was truncated but shouldn't be")
	}
}

func TestParseOutput_MixedChunk(t *testing.T) {
	a := New()
	ag := &domain.Agent{Name: "builder"}

	lines := []string{
		"just some plain terminal chatter",
		wire.EncodeOut(wire.OutMsg{Type: "status", Status: "running"}),
		"more plain chatter in between",
		wire.EncodeOut(wire.OutMsg{Type: "message", To: "lead", Text: "hello"}),
		wire.EncodeOut(wire.OutMsg{Type: "subagent", Name: "tester", Role: "helper", Status: "running"}),
		"##clishake:{not valid json", // garbage marker line
		wire.EncodeOut(wire.OutMsg{Type: "task", TaskID: "t1", Status: "completed", Text: "done"}),
		wire.EncodeOut(wire.OutMsg{Type: "approval", Action: "run_command", Reason: "need to rm files", Risk: "high"}),
		wire.EncodeOut(wire.OutMsg{Type: "log", Text: "a notable log line"}),
		wire.EncodeOut(wire.OutMsg{Type: "status", Status: "not-a-real-status"}), // unknown status -> skipped
		wire.EncodeOut(wire.OutMsg{Type: "mystery", Text: "unknown type"}),       // unrecognized type -> skipped
	}
	chunk := strings.Join(lines, "\n")

	events := a.ParseOutput(ag, chunk)

	wantKinds := []adapter.ParsedKind{
		adapter.KindStatus,
		adapter.KindMessage,
		adapter.KindSubagent,
		adapter.KindTask,
		adapter.KindApproval,
		adapter.KindLog,
	}
	if len(events) != len(wantKinds) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(wantKinds), events)
	}
	for i, ev := range events {
		if ev.Kind != wantKinds[i] {
			t.Errorf("event[%d].Kind = %q, want %q", i, ev.Kind, wantKinds[i])
		}
		if ev.Agent != "builder" {
			t.Errorf("event[%d].Agent = %q, want %q", i, ev.Agent, "builder")
		}
	}

	// Spot-check field mapping.
	st := events[0]
	if st.Status != domain.StatusRunning {
		t.Errorf("status event Status = %q, want %q", st.Status, domain.StatusRunning)
	}

	msg := events[1]
	if msg.To != "lead" || msg.Text != "hello" {
		t.Errorf("message event = %+v, want To=lead Text=hello", msg)
	}

	sub := events[2]
	if sub.Sub == nil || sub.Sub.Name != "tester" || sub.Sub.Role != "helper" || sub.Sub.Status != domain.StatusRunning {
		t.Errorf("subagent event = %+v", sub)
	}

	task := events[3]
	if task.TaskID != "t1" || task.Text != "done" || task.Fields["status"] != "completed" {
		t.Errorf("task event = %+v", task)
	}

	appr := events[4]
	if appr.Text != "need to rm files" || appr.Fields["action"] != "run_command" || appr.Fields["risk"] != "high" {
		t.Errorf("approval event = %+v", appr)
	}

	logEv := events[5]
	if logEv.Text != "a notable log line" {
		t.Errorf("log event = %+v", logEv)
	}
}

func TestParseOutput_NoMarkersYieldsNothing(t *testing.T) {
	a := New()
	ag := &domain.Agent{Name: "builder"}
	events := a.ParseOutput(ag, "hello\nworld\nnothing structured here\n")
	if len(events) != 0 {
		t.Errorf("expected no events, got %+v", events)
	}
}

func TestDetectReady(t *testing.T) {
	a := New()
	ag := &domain.Agent{Name: "builder"}

	readyChunk := "starting up...\n" + wire.EncodeOut(wire.OutMsg{Type: "status", Status: "ready"}) + "\nmore output"
	if !a.DetectReady(ag, readyChunk) {
		t.Error("DetectReady() = false, want true for chunk containing ready status")
	}

	notReadyChunk := "starting up...\n" + wire.EncodeOut(wire.OutMsg{Type: "status", Status: "running"})
	if a.DetectReady(ag, notReadyChunk) {
		t.Error("DetectReady() = true, want false for chunk without ready status")
	}

	plainChunk := "just some text, no markers at all"
	if a.DetectReady(ag, plainChunk) {
		t.Error("DetectReady() = true, want false for plain chunk")
	}
}

func TestCheckHealth(t *testing.T) {
	a := New()
	ag := &domain.Agent{Name: "builder"}

	cases := []struct {
		name         string
		processAlive bool
		ageSec       float64
		want         adapter.Health
	}{
		{"alive, no output yet", true, -1, adapter.HealthOK},
		{"alive, fresh output", true, 5, adapter.HealthOK},
		{"alive, right at boundary", true, 59.9, adapter.HealthOK},
		{"alive, stale output", true, 120, adapter.HealthUnresponsive},
		{"dead, no output", false, -1, adapter.HealthUnknown},
		{"dead, fresh output timestamp but process gone", false, 5, adapter.HealthUnknown},
		{"dead, stale output", false, 120, adapter.HealthUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := a.CheckHealth(ag, c.processAlive, c.ageSec)
			if got != c.want {
				t.Errorf("CheckHealth(alive=%v, age=%v) = %q, want %q", c.processAlive, c.ageSec, got, c.want)
			}
		})
	}
}

func TestInterruptKeys(t *testing.T) {
	got := New().InterruptKeys()
	want := []string{"C-c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("InterruptKeys() = %v, want %v", got, want)
	}
}
