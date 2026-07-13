// Package claudecode adapts the Claude Code CLI to clishake.
//
// Honest capability report: clishake launches the interactive `claude` CLI
// in a pane and delivers instructions by typing them (send-keys). Claude
// Code has no public structured-output stream for an already-running
// interactive session, so structured_output is NOT declared: status changes
// beyond readiness are inferred only from process state. Sub-agent/team
// discovery from Claude Code's on-disk team files (~/.claude/teams/...) is
// a documented future enhancement, not claimed here.
package claudecode

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/clishakehq/clishake/internal/adapter"
	"github.com/clishakehq/clishake/internal/ansi"
	"github.com/clishakehq/clishake/internal/domain"
)

// A is the Claude Code adapter.
type A struct{}

// New returns the adapter.
func New() *A { return &A{} }

func (*A) Name() string { return "claude-code" }

func (*A) Capabilities() []domain.Capability {
	return []domain.Capability{
		domain.CapGracefulInterrupt,
		domain.CapSessionResume,
		domain.CapWorkdirOverride,
		domain.CapSubagents,
		domain.CapAgentTeams, // teams discovered from ~/.claude/teams rosters (teams.go)
	}
}

// lookPath is swappable in tests.
var lookPath = exec.LookPath

// runVersion is swappable in tests.
var runVersion = func(bin string) (string, error) {
	out, err := exec.Command(bin, "--version").CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (*A) Detect() (bool, string, error) {
	bin, err := lookPath("claude")
	if err != nil {
		return false, "", nil
	}
	v, err := runVersion(bin)
	if err != nil {
		return true, "unknown", nil
	}
	return true, v, nil
}

func (*A) ValidateConfig(cfg map[string]string) error {
	if m := cfg["permission_mode"]; m != "" {
		switch m {
		case "default", "acceptEdits", "plan", "bypassPermissions":
		default:
			return fmt.Errorf("permission_mode %q not recognized", m)
		}
	}
	return nil
}

func (*A) BuildLaunch(a *domain.Agent, projectDir string) (adapter.LaunchSpec, error) {
	cmd := []string{"claude"}
	if bin := a.Config["command"]; bin != "" {
		cmd = []string{bin}
	}
	// Model selection (e.g. "opus", "sonnet", "claude-fable-5") — set via
	// `clishake agent add --model` or the agent's `model` config.
	if m := a.Config["model"]; m != "" {
		cmd = append(cmd, "--model", m)
	}
	// Permissions: the raw permission_mode config wins as a low-level
	// override; otherwise map the cross-harness `permissions` profile
	// (default|auto|full|plan) so agents don't re-prompt for approval.
	if m := a.Config["permission_mode"]; m != "" {
		cmd = append(cmd, "--permission-mode", m)
	} else {
		cmd = append(cmd, claudePermArgs(a.Config["permissions"])...)
	}
	// Session briefing (identity, roster, messaging protocol) composed by
	// the orchestrator rides in as an appended system prompt so the
	// conversation itself stays clean.
	if b := a.Config["_briefing"]; b != "" {
		cmd = append(cmd, "--append-system-prompt", b)
	}
	if extra := a.Config["args"]; extra != "" {
		cmd = append(cmd, strings.Fields(extra)...)
	}
	// The initial task is deliberately NOT a launch argument: first-run
	// dialogs (folder trust, login) swallow launch prompts. The
	// orchestrator delivers it as the first message once the composer is
	// ready.
	wd := a.WorkDir
	if wd == "" {
		wd = projectDir
	}
	return adapter.LaunchSpec{Command: cmd, WorkDir: wd}, nil
}

// claudePermArgs maps a cross-harness permission profile onto Claude Code's
// launch flags (validated against `claude --help`). "auto" auto-accepts edits;
// "full" uses --dangerously-skip-permissions to bypass in-session tool prompts;
// "plan" is read-only planning. Unknown/empty/"default" adds nothing.
//
// NOTE: none of these skip the one-time-per-directory folder-trust dialog —
// that is a separate startup gate the orchestrator auto-answers. "full" also
// makes Claude show a one-time "Bypass Permissions mode" danger warning
// (default cursor on "No, exit"); clishake deliberately does NOT auto-accept a
// danger acknowledgement — answer it once from the dashboard, or prefer "auto".
func claudePermArgs(profile string) []string {
	switch profile {
	case "auto":
		return []string{"--permission-mode", "acceptEdits"}
	case "full":
		return []string{"--dangerously-skip-permissions"}
	case "plan":
		return []string{"--permission-mode", "plan"}
	default:
		return nil
	}
}

func (*A) InputMode() adapter.InputMode { return adapter.InputSendKeys }

func (*A) FormatInput(a *domain.Agent, msg domain.Message) (string, error) {
	// Typed into the interactive prompt. Routed messages carry the sender
	// prefix the launch briefing tells the agent to expect; a slash command
	// from the lead is passed through verbatim so Claude executes it.
	return adapter.FormatRouted(msg.Sender, msg.Body), nil
}

// ParseOutput: no structured stream — we only surface nothing rather than
// guess. (Readiness is handled by DetectReady.)
func (*A) ParseOutput(a *domain.Agent, chunk string) []adapter.ParsedEvent { return nil }

func (*A) DetectReady(a *domain.Agent, chunk string) bool {
	// "Any output" is NOT readiness for Claude Code: the TUI draws its
	// banner well before the composer accepts input, and text typed into
	// that gap is silently swallowed. And a bare "❯" match is not enough
	// either — selection dialogs (folder trust, login) use "❯ 1. ..." as
	// their cursor, and typing into those is dangerous.
	//
	// The raw piped stream has no meaningful line boundaries (TUIs position
	// the cursor rather than print lines), so the test is per occurrence:
	// a "❯" NOT followed by a numbered menu entry ("1.") is the composer.
	plain := ansi.Strip(chunk)
	if strings.Contains(plain, "? for shortcuts") || strings.Contains(plain, "esc to interrupt") {
		return true
	}
	rest := plain
	for {
		i := strings.Index(rest, "❯")
		if i < 0 {
			return false
		}
		rest = rest[i+len("❯"):]
		if !menuEntryFollows(rest) {
			return true
		}
	}
}

// menuEntryFollows reports whether s (text right after a "❯" cursor) looks
// like a selection-dialog entry: optional spaces, digits, then a dot.
func menuEntryFollows(s string) bool {
	s = strings.TrimLeft(s, " ")
	d := 0
	for d < len(s) && s[d] >= '0' && s[d] <= '9' {
		d++
	}
	return d > 0 && d < len(s) && s[d] == '.'
}

func (*A) CheckHealth(a *domain.Agent, alive bool, lastOutputAgeSec float64) adapter.Health {
	if !alive {
		return adapter.HealthUnknown
	}
	return adapter.HealthOK // interactive CLIs may be legitimately quiet
}

func (*A) InterruptKeys() []string { return []string{"Escape"} }

// BriefsAtLaunch: the briefing rides in via --append-system-prompt.
func (*A) BriefsAtLaunch() bool { return true }
