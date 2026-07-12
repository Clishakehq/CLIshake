// Package mockagent implements the runtime for a simulated coding agent
// process. It is launched inside a tmux pane (typically as
// `clishake mock-agent --name ... --role ... --agent-dir ...`) and exists so
// clishake's orchestration layer can be developed and tested end-to-end
// without depending on a real coding-agent CLI.
//
// The mock agent watches an inbox JSONL file for wire.Envelope messages
// (append-only, written by the orchestrator) and reacts to a small command
// grammar carried in the envelope text. All structured status/output is
// emitted on stdout (or the configured Writer) as wire.EncodeOut lines;
// everything else is ordinary human-readable terminal chatter.
package mockagent

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/wire"
)

// Options configures a mock agent run.
type Options struct {
	Name           string           // agent display name (required)
	Role           string           // e.g. "builder"
	AgentDir       string           // directory containing inbox.jsonl (required)
	ProjectDir     string           // working dir context (informational)
	HeartbeatEvery time.Duration    // default 15s
	PollEvery      time.Duration    // inbox poll interval, default 300ms
	Out            io.Writer        // defaults to os.Stdout
	Clock          func() time.Time // defaults to time.Now (for tests)
}

// workStepDuration is how long one simulated work/spawn step "takes".
const workStepDuration = 700 * time.Millisecond

// sleepFn performs the per-step delay. It is a package variable so tests can
// stub it out to run fast; production code leaves it as time.Sleep.
var sleepFn = time.Sleep

func (o Options) withDefaults() Options {
	if o.HeartbeatEvery <= 0 {
		o.HeartbeatEvery = 15 * time.Second
	}
	if o.PollEvery <= 0 {
		o.PollEvery = 300 * time.Millisecond
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.AgentDir == "" {
		o.AgentDir = "."
	}
	if o.Name == "" {
		o.Name = "agent"
	}
	return o
}

// Run starts the mock agent loop and blocks until it exits (via the "stop"
// or "complete" or "fail!" commands, or SIGINT/SIGTERM). It returns the
// intended process exit code.
func Run(opts Options) int {
	opts = opts.withDefaults()

	ag := &agentState{
		name:  opts.Name,
		role:  opts.Role,
		out:   opts.Out,
		clock: opts.Clock,
	}

	ag.printf("[%s] mock agent starting (role=%s)\n", ag.name, ag.role)
	ag.setStatus(domain.StatusReady)
	ag.emitStatus(domain.StatusReady, "")

	reader := &inboxReader{path: filepath.Join(opts.AgentDir, "inbox.jsonl")}

	heartbeat := time.NewTicker(opts.HeartbeatEvery)
	defer heartbeat.Stop()
	poll := time.NewTicker(opts.PollEvery)
	defer poll.Stop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	for {
		select {
		case <-sigCh:
			ag.setStatus(domain.StatusStopped)
			ag.emitStatus(domain.StatusStopped, "")
			return 130

		case <-heartbeat.C:
			ag.printf("[%s] idle, waiting for instructions\n", ag.name)

		case <-poll.C:
			lines, err := reader.poll()
			if err != nil {
				// Transient read errors (e.g. file mid-write) are ignored;
				// we'll retry on the next tick.
				continue
			}
			for _, line := range lines {
				env, ok := wire.DecodeEnvelope(line)
				if !ok {
					continue // undecodable line: ignore, never guess
				}
				if code, done := ag.handleEnvelope(env); done {
					return code
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// agent state
// ---------------------------------------------------------------------------

type agentState struct {
	name  string
	role  string
	out   io.Writer
	clock func() time.Time

	mu     sync.Mutex
	status domain.AgentStatus
}

func (ag *agentState) printf(format string, args ...any) {
	fmt.Fprintf(ag.out, format, args...)
}

func (ag *agentState) writeOut(m wire.OutMsg) {
	fmt.Fprintln(ag.out, wire.EncodeOut(m))
}

func (ag *agentState) emitStatus(status domain.AgentStatus, detail string) {
	ag.writeOut(wire.OutMsg{Type: "status", Status: string(status), Detail: detail})
}

func (ag *agentState) emitTask(taskID, status, text string) {
	ag.writeOut(wire.OutMsg{Type: "task", TaskID: taskID, Status: status, Text: text})
}

func (ag *agentState) emitSubagent(name, role, status string) {
	ag.writeOut(wire.OutMsg{Type: "subagent", Name: name, Role: role, Status: status})
}

func (ag *agentState) sendMessage(to, text string) {
	ag.writeOut(wire.OutMsg{Type: "message", To: to, Text: text})
}

func (ag *agentState) setStatus(s domain.AgentStatus) {
	ag.mu.Lock()
	ag.status = s
	ag.mu.Unlock()
}

func (ag *agentState) currentStatus() domain.AgentStatus {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	return ag.status
}

// ---------------------------------------------------------------------------
// command grammar
// ---------------------------------------------------------------------------

// handleEnvelope processes one inbox envelope. It returns (exitCode, done);
// done is true when Run should stop its loop and return exitCode.
func (ag *agentState) handleEnvelope(env wire.Envelope) (int, bool) {
	ag.printf("[%s] <- message from %s: %s\n", ag.name, env.From, env.Text)

	cmd, rest := splitCommand(env.Text)
	switch cmd {
	case "work":
		ag.handleWork(env, rest)
		return 0, false

	case "status?":
		ag.sendMessage(env.From, fmt.Sprintf("status: %s", ag.currentStatus()))
		return 0, false

	case "fail!":
		ag.printf("[%s] ERROR: failing as instructed\n", ag.name)
		ag.setStatus(domain.StatusFailed)
		ag.emitStatus(domain.StatusFailed, "")
		return 1, true

	case "complete":
		if env.TaskID != "" {
			ag.emitTask(env.TaskID, "completed", "")
		}
		ag.setStatus(domain.StatusCompleted)
		ag.emitStatus(domain.StatusCompleted, "")
		ag.sendMessage(env.From, "task complete")
		return 0, true

	case "spawn":
		ag.handleSpawn(env, rest)
		return 0, false

	case "tell":
		// tell <agent> <text>: relay a message to another agent through
		// clishake (exercises agent-to-agent routing).
		fields := strings.SplitN(strings.TrimSpace(rest), " ", 2)
		if len(fields) < 2 || fields[0] == "" {
			ag.sendMessage(env.From, "usage: tell <agent> <text>")
			return 0, false
		}
		ag.printf("[%s] -> telling %s: %s\n", ag.name, fields[0], fields[1])
		ag.sendMessage(fields[0], fields[1])
		return 0, false

	case "stop":
		ag.setStatus(domain.StatusStopped)
		ag.emitStatus(domain.StatusStopped, "")
		return 0, true

	default:
		// Never reply to another agent's acknowledgement — replying to a
		// reply would bounce messages between mock agents forever.
		if strings.HasPrefix(strings.ToLower(env.Text), "ack:") {
			return 0, false
		}
		ag.sendMessage(env.From, fmt.Sprintf("ack: %s", env.Text))
		ag.setStatus(domain.StatusWaiting)
		ag.emitStatus(domain.StatusWaiting, "")
		return 0, false
	}
}

func (ag *agentState) handleWork(env wire.Envelope, rest string) {
	n := 3
	if fields := strings.Fields(rest); len(fields) > 0 {
		if v, err := strconv.Atoi(fields[0]); err == nil && v > 0 {
			n = v
		}
	}

	ag.setStatus(domain.StatusRunning)
	ag.emitStatus(domain.StatusRunning, "")

	for i := 1; i <= n; i++ {
		ag.printf("[%s] step %d/%d: working...\n", ag.name, i, n)
		sleepFn(workStepDuration)
	}

	if env.TaskID != "" {
		ag.emitTask(env.TaskID, "completed", "")
	}
	ag.setStatus(domain.StatusWaiting)
	ag.emitStatus(domain.StatusWaiting, "")
	ag.sendMessage(env.From, fmt.Sprintf("done: %s", env.Text))
}

func (ag *agentState) handleSpawn(env wire.Envelope, rest string) {
	subName := "helper"
	if fields := strings.Fields(rest); len(fields) > 0 {
		subName = fields[0]
	}

	ag.emitSubagent(subName, "helper", "running")
	const steps = 2
	for i := 0; i < steps; i++ {
		ag.printf("[%s/%s] working...\n", ag.name, subName)
		sleepFn(workStepDuration)
	}
	ag.emitSubagent(subName, "helper", "completed")
	ag.sendMessage(env.From, fmt.Sprintf("sub-agent %s finished", subName))
}

// splitCommand splits text into a lower-cased first word (the command) and
// the remainder (trimmed).
func splitCommand(text string) (cmd, rest string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	fields := strings.SplitN(text, " ", 2)
	cmd = strings.ToLower(fields[0])
	if len(fields) > 1 {
		rest = strings.TrimSpace(fields[1])
	}
	return cmd, rest
}

// ---------------------------------------------------------------------------
// inbox tailing
// ---------------------------------------------------------------------------

// inboxReader tails a JSONL file, remembering a byte offset across polls. A
// missing file reads as empty; a file that has shrunk (been truncated or
// replaced) causes the offset to reset to the start.
type inboxReader struct {
	path   string
	offset int64
}

// poll returns any complete new lines appended since the last call. Partial
// (not yet newline-terminated) trailing data is left for the next poll.
func (r *inboxReader) poll() ([]string, error) {
	f, err := os.Open(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size < r.offset {
		r.offset = 0 // file was truncated or replaced; start over
	}
	if size <= r.offset {
		return nil, nil
	}

	if _, err := f.Seek(r.offset, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, size-r.offset)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	buf = buf[:n]

	var lines []string
	start := 0
	for i, b := range buf {
		if b == '\n' {
			lines = append(lines, string(buf[start:i]))
			start = i + 1
		}
	}
	r.offset += int64(start)
	return lines, nil
}
