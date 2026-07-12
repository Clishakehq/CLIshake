// Package codex adapts the OpenAI Codex CLI to clishake.
//
// Honest capability report: clishake launches the interactive `codex` CLI
// in a pane and delivers instructions by typing them (send-keys). There is
// no structured output stream for a running interactive session, so
// structured_output is NOT declared; status beyond readiness comes from
// process supervision only.
package codex

import (
	"os/exec"
	"strings"

	"github.com/clishakehq/clishake/internal/adapter"
	"github.com/clishakehq/clishake/internal/ansi"
	"github.com/clishakehq/clishake/internal/domain"
)

// A is the Codex adapter.
type A struct{}

// New returns the adapter.
func New() *A { return &A{} }

func (*A) Name() string { return "codex" }

func (*A) Capabilities() []domain.Capability {
	return []domain.Capability{
		domain.CapGracefulInterrupt,
		domain.CapWorkdirOverride,
	}
}

var lookPath = exec.LookPath

var runVersion = func(bin string) (string, error) {
	out, err := exec.Command(bin, "--version").CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (*A) Detect() (bool, string, error) {
	bin, err := lookPath("codex")
	if err != nil {
		return false, "", nil
	}
	v, err := runVersion(bin)
	if err != nil {
		return true, "unknown", nil
	}
	return true, v, nil
}

func (*A) ValidateConfig(cfg map[string]string) error { return nil }

func (*A) BuildLaunch(a *domain.Agent, projectDir string) (adapter.LaunchSpec, error) {
	cmd := []string{"codex"}
	if bin := a.Config["command"]; bin != "" {
		cmd = []string{bin}
	}
	// Model selection (flags must precede the positional briefing prompt).
	if m := a.Config["model"]; m != "" {
		cmd = append(cmd, "--model", m)
	}
	if extra := a.Config["args"]; extra != "" {
		cmd = append(cmd, strings.Fields(extra)...)
	}
	// Codex has no separate system-prompt flag for the interactive TUI, so
	// the orchestrator's session briefing becomes the initial prompt. The
	// assigned task is deliberately NOT included: it is delivered as the
	// first routed message after readiness (launch prompts are lost when a
	// first-run dialog intercepts startup).
	if b := a.Config["_briefing"]; b != "" {
		cmd = append(cmd, b+"\nRead the context above, then wait for instructions from the lead or a teammate.")
	}
	wd := a.WorkDir
	if wd == "" {
		wd = projectDir
	}
	return adapter.LaunchSpec{Command: cmd, WorkDir: wd}, nil
}

func (*A) InputMode() adapter.InputMode { return adapter.InputSendKeys }

func (*A) FormatInput(a *domain.Agent, msg domain.Message) (string, error) {
	// Sender prefix matches what the launch briefing tells the agent to
	// expect for clishake-routed traffic.
	return "[clishake message from " + msg.Sender + "] " + msg.Body, nil
}

func (*A) ParseOutput(a *domain.Agent, chunk string) []adapter.ParsedEvent { return nil }

func (*A) DetectReady(a *domain.Agent, chunk string) bool {
	// Wait for the composer prompt (›) rather than any output — text typed
	// before the composer exists is dropped by the TUI.
	return strings.Contains(ansi.Strip(chunk), "›")
}

func (*A) CheckHealth(a *domain.Agent, alive bool, lastOutputAgeSec float64) adapter.Health {
	if !alive {
		return adapter.HealthUnknown
	}
	return adapter.HealthOK
}

func (*A) InterruptKeys() []string { return []string{"C-c"} }

// BriefsAtLaunch: the briefing is the initial prompt preamble.
func (*A) BriefsAtLaunch() bool { return true }
