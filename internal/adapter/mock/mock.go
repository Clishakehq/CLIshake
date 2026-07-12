// Package mock implements adapter.Adapter for clishake's built-in mock
// coding agent (internal/mockagent). It requires no external binary: the
// "harness" is `clishake mock-agent ...`, launched via the same executable
// that is orchestrating, using the structured_input/structured_output wire
// protocols end to end. It exists so the rest of clishake can be developed
// and tested without depending on a real coding-agent CLI.
package mock

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/clishakehq/clishake/internal/adapter"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/wire"
)

// selfExe resolves the executable to launch as the mock agent subprocess.
// It is a package variable so tests can stub it without touching the real
// filesystem/os.Executable.
var selfExe = os.Executable

// Adapter implements adapter.Adapter for the built-in mock agent.
type Adapter struct{}

// New returns a mock Adapter.
func New() *Adapter { return &Adapter{} }

// Name is the stable registry key.
func (a *Adapter) Name() string { return "mock" }

// Capabilities reports what the mock agent genuinely supports.
func (a *Adapter) Capabilities() []domain.Capability {
	return []domain.Capability{
		domain.CapStructuredInput,
		domain.CapStructuredOutput,
		domain.CapSubagents,
		domain.CapTaskEvents,
		domain.CapGracefulInterrupt,
		domain.CapWorkdirOverride,
	}
}

// Detect always reports the mock agent as installed: it is built into the
// clishake binary itself, so there is no external tool to find.
func (a *Adapter) Detect() (installed bool, version string, err error) {
	return true, "builtin", nil
}

// ValidateConfig accepts any config, except the test-only escape hatch
// cfg["fail_validate"] == "true", which is used to exercise error paths.
func (a *Adapter) ValidateConfig(cfg map[string]string) error {
	if cfg["fail_validate"] == "true" {
		return fmt.Errorf("mock: config requested validation failure (fail_validate=true)")
	}
	return nil
}

// BuildLaunch constructs the launch spec for `clishake mock-agent ...`,
// running from the same executable as the orchestrator.
func (a *Adapter) BuildLaunch(ag *domain.Agent, projectDir string) (adapter.LaunchSpec, error) {
	exe, err := selfExe()
	if err != nil {
		return adapter.LaunchSpec{}, fmt.Errorf("mock: resolve self executable: %w", err)
	}

	workDir := ag.WorkDir
	if workDir == "" {
		workDir = projectDir
	}

	agentDir := filepath.Join(projectDir, ".clishake", "agents", ag.ID)

	return adapter.LaunchSpec{
		Command: []string{
			exe, "mock-agent",
			"--name", ag.Name,
			"--role", ag.Role,
			"--agent-dir", agentDir,
		},
		WorkDir: workDir,
	}, nil
}

// InputMode says input is delivered via the agent's inbox file.
func (a *Adapter) InputMode() adapter.InputMode { return adapter.InputFile }

// FormatInput renders msg as a wire.Envelope JSONL line for the inbox.
func (a *Adapter) FormatInput(ag *domain.Agent, msg domain.Message) (string, error) {
	env := wire.Envelope{
		From:      msg.Sender,
		Text:      msg.Body,
		Summary:   truncateRunes(msg.Body, 60),
		Timestamp: msg.CreatedAt,
		MsgID:     msg.ID,
		Type:      "message",
		TaskID:    msg.TaskID,
		ReplyTo:   msg.ReplyTo,
	}
	return wire.EncodeEnvelope(env), nil
}

// ParseOutput scans a chunk of captured pane output line by line, decoding
// wire.OutMsg marker lines into adapter.ParsedEvent. Non-marker lines and
// unrecognized/invalid marker lines produce nothing.
func (a *Adapter) ParseOutput(ag *domain.Agent, chunk string) []adapter.ParsedEvent {
	var events []adapter.ParsedEvent

	for _, line := range strings.Split(chunk, "\n") {
		m, ok := wire.DecodeOut(line)
		if !ok {
			continue
		}

		switch m.Type {
		case "status":
			st := domain.AgentStatus(m.Status)
			if !isKnownStatus(st) {
				continue
			}
			events = append(events, adapter.ParsedEvent{
				Kind:   adapter.KindStatus,
				Agent:  ag.Name,
				Status: st,
				Text:   m.Detail,
			})

		case "message":
			events = append(events, adapter.ParsedEvent{
				Kind:  adapter.KindMessage,
				Agent: ag.Name,
				To:    m.To,
				Text:  m.Text,
			})

		case "task":
			events = append(events, adapter.ParsedEvent{
				Kind:   adapter.KindTask,
				Agent:  ag.Name,
				TaskID: m.TaskID,
				Text:   m.Text,
				Fields: map[string]string{"status": m.Status},
			})

		case "subagent":
			events = append(events, adapter.ParsedEvent{
				Kind:  adapter.KindSubagent,
				Agent: ag.Name,
				Sub: &adapter.SubagentInfo{
					Name:   m.Name,
					Role:   m.Role,
					Status: domain.AgentStatus(m.Status),
				},
			})

		case "approval":
			events = append(events, adapter.ParsedEvent{
				Kind:  adapter.KindApproval,
				Agent: ag.Name,
				Text:  m.Reason,
				Fields: map[string]string{
					"action": m.Action,
					"risk":   m.Risk,
				},
			})

		case "log":
			events = append(events, adapter.ParsedEvent{
				Kind:  adapter.KindLog,
				Agent: ag.Name,
				Text:  m.Text,
			})

			// Unknown m.Type: not one of the documented wire types. Never
			// guess; produce nothing.
		}
	}

	return events
}

// DetectReady reports whether chunk contains a status line reporting
// domain.StatusReady.
func (a *Adapter) DetectReady(ag *domain.Agent, chunk string) bool {
	for _, line := range strings.Split(chunk, "\n") {
		m, ok := wire.DecodeOut(line)
		if ok && m.Type == "status" && m.Status == string(domain.StatusReady) {
			return true
		}
	}
	return false
}

// CheckHealth judges health from process liveness and output recency.
func (a *Adapter) CheckHealth(ag *domain.Agent, processAlive bool, lastOutputAgeSec float64) adapter.Health {
	if !processAlive {
		return adapter.HealthUnknown
	}
	if lastOutputAgeSec < 0 || lastOutputAgeSec < 60 {
		return adapter.HealthOK
	}
	return adapter.HealthUnresponsive
}

// InterruptKeys returns the tmux key(s) that deliver a graceful interrupt.
func (a *Adapter) InterruptKeys() []string { return []string{"C-c"} }

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func isKnownStatus(s domain.AgentStatus) bool {
	switch s {
	case domain.StatusStarting, domain.StatusReady, domain.StatusRunning, domain.StatusWaiting,
		domain.StatusBlocked, domain.StatusAwaitingApproval, domain.StatusCompleted,
		domain.StatusFailed, domain.StatusStopped, domain.StatusDisconnected, domain.StatusUnknown:
		return true
	}
	return false
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

var _ adapter.Adapter = (*Adapter)(nil)

// BriefsAtLaunch: mock agents speak the wire protocol natively and need no
// prose briefing message.
func (a *Adapter) BriefsAtLaunch() bool { return true }
