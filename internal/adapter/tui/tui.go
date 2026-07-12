// Package tui provides a generic adapter for interactive terminal-UI
// coding agents (OpenCode, GitHub Copilot CLI, Antigravity CLI, ...) that
// share one integration shape: launch the binary in a pane, type
// instructions in via send-keys, and supervise at the process level.
//
// Everything harness-specific lives in a Spec, and every Spec default can
// be overridden per project in config.toml ([adapters.<name>] command/args/
// options) — so a new TUI harness, or a version whose UI changed, is a
// config edit rather than a code change.
//
// These adapters do not claim structured output. The session briefing is
// NOT passed as a launch argument (first-run dialogs swallow launch args);
// the orchestrator delivers it as the first routed message after readiness.
package tui

import (
	"os/exec"
	"strings"

	"github.com/clishakehq/clishake/internal/adapter"
	"github.com/clishakehq/clishake/internal/ansi"
	"github.com/clishakehq/clishake/internal/domain"
)

// Spec parameterizes one TUI harness.
type Spec struct {
	// Name is the adapter registry key, e.g. "opencode".
	Name string
	// Command is the default executable (config "command" overrides).
	Command string
	// VersionArgs produce a version string, e.g. ["--version"].
	VersionArgs []string
	// ReadyMarkers are substrings that positively signal the composer is
	// ready (config "ready_marker" adds one). The prompt-glyph heuristic
	// below applies in addition to these.
	ReadyMarkers []string
	// PromptGlyphs are line-start glyphs that look like an input prompt.
	// Empty means DefaultPromptGlyphs.
	PromptGlyphs []string
	// InterruptKeys are the graceful-interrupt keys (default: Escape).
	InterruptKeys []string
	// ModelFlag selects a model at launch (default "--model"); the config
	// option "model_flag" overrides it, and setting it to "" disables model
	// selection for a harness that has no such flag.
	ModelFlag string
}

// DefaultPromptGlyphs cover the composer prompts of common TUI agents.
var DefaultPromptGlyphs = []string{"❯", "›", ">", "┃"}

// A is a generic TUI harness adapter.
type A struct{ spec Spec }

// New builds an adapter from a spec.
func New(spec Spec) *A {
	if len(spec.PromptGlyphs) == 0 {
		spec.PromptGlyphs = DefaultPromptGlyphs
	}
	if len(spec.InterruptKeys) == 0 {
		spec.InterruptKeys = []string{"Escape"}
	}
	if len(spec.VersionArgs) == 0 {
		spec.VersionArgs = []string{"--version"}
	}
	if spec.ModelFlag == "" {
		spec.ModelFlag = "--model" // the common case; per-agent "model_flag" config overrides
	}
	return &A{spec: spec}
}

func (a *A) Name() string { return a.spec.Name }

func (a *A) Capabilities() []domain.Capability {
	return []domain.Capability{
		domain.CapGracefulInterrupt,
		domain.CapWorkdirOverride,
	}
}

// lookPath and runVersion are swappable in tests.
var lookPath = exec.LookPath

var runVersion = func(bin string, args ...string) (string, error) {
	out, err := exec.Command(bin, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (a *A) Detect() (bool, string, error) {
	bin, err := lookPath(a.spec.Command)
	if err != nil {
		return false, "", nil
	}
	v, err := runVersion(bin, a.spec.VersionArgs...)
	if err != nil || v == "" {
		return true, "unknown", nil
	}
	// Keep the first line only; some CLIs print banners after the version.
	if i := strings.IndexByte(v, '\n'); i > 0 {
		v = v[:i]
	}
	return true, v, nil
}

func (a *A) ValidateConfig(cfg map[string]string) error { return nil }

func (a *A) BuildLaunch(ag *domain.Agent, projectDir string) (adapter.LaunchSpec, error) {
	cmd := []string{a.spec.Command}
	if bin := ag.Config["command"]; bin != "" {
		cmd = []string{bin}
	}
	// Model selection: `<model_flag> <model>` when a model is set. The flag
	// defaults to "--model" and is overridable (or disabled with "") via the
	// "model_flag" config for a harness that names it differently.
	if m := ag.Config["model"]; m != "" {
		flag := a.spec.ModelFlag
		if f, ok := ag.Config["model_flag"]; ok {
			flag = f
		}
		if flag != "" {
			cmd = append(cmd, strings.Fields(flag)...)
			cmd = append(cmd, m)
		}
	}
	if extra := ag.Config["args"]; extra != "" {
		cmd = append(cmd, strings.Fields(extra)...)
	}
	wd := ag.WorkDir
	if wd == "" {
		wd = projectDir
	}
	return adapter.LaunchSpec{Command: cmd, WorkDir: wd}, nil
}

func (a *A) InputMode() adapter.InputMode { return adapter.InputSendKeys }

func (a *A) FormatInput(ag *domain.Agent, msg domain.Message) (string, error) {
	return "[clishake message from " + msg.Sender + "] " + msg.Body, nil
}

func (a *A) ParseOutput(ag *domain.Agent, chunk string) []adapter.ParsedEvent { return nil }

// DetectReady reports readiness when the (ANSI-stripped) output shows a
// configured marker or a prompt-glyph line — but a selection dialog
// ANYWHERE on screen vetoes readiness outright. Dialogs (folder trust,
// tool approval) render as overlays while the composer glyph is still
// visible elsewhere on the same screen (observed live with Copilot CLI),
// and input delivered then lands in the dialog.
func (a *A) DetectReady(ag *domain.Agent, chunk string) bool {
	plain := ansi.Strip(chunk)
	promptSeen := false
	for _, line := range strings.Split(plain, "\n") {
		// Dialogs render inside box borders, so the veto scans every
		// glyph occurrence in the line, not just the line start.
		for _, g := range a.spec.PromptGlyphs {
			rest := line
			for {
				i := strings.Index(rest, g)
				if i < 0 {
					break
				}
				rest = rest[i+len(g):]
				if menuEntryFollows(rest) {
					return false // dialog cursor visible: never ready
				}
			}
		}
		t := strings.Trim(line, " │╭╮╰╯─\t")
		for _, g := range a.spec.PromptGlyphs {
			if rest, ok := strings.CutPrefix(t, g); ok && !menuEntryFollows(rest) {
				promptSeen = true
			}
		}
	}
	if promptSeen {
		return true
	}
	markers := a.spec.ReadyMarkers
	if extra := ag.Config["ready_marker"]; extra != "" {
		markers = append(append([]string{}, markers...), extra)
	}
	for _, mk := range markers {
		if mk != "" && strings.Contains(plain, mk) {
			return true
		}
	}
	return false
}

// menuEntryFollows reports whether s (text right after a prompt glyph)
// looks like a selection-dialog entry: optional spaces, digits, then a dot.
func menuEntryFollows(s string) bool {
	s = strings.TrimLeft(s, " ")
	d := 0
	for d < len(s) && s[d] >= '0' && s[d] <= '9' {
		d++
	}
	return d > 0 && d < len(s) && s[d] == '.'
}

func (a *A) CheckHealth(ag *domain.Agent, alive bool, lastOutputAgeSec float64) adapter.Health {
	if !alive {
		return adapter.HealthUnknown
	}
	return adapter.HealthOK // interactive CLIs may be legitimately quiet
}

func (a *A) InterruptKeys() []string { return a.spec.InterruptKeys }

// BriefsAtLaunch reports false: generic TUI harnesses have no reliable
// system-prompt or launch-prompt flag, so the orchestrator must deliver
// the session briefing as the first message after readiness.
func (a *A) BriefsAtLaunch() bool { return false }
