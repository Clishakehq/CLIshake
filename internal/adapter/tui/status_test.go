package tui

import "testing"

// Real captured Copilot status line (see the copilot spec in internal/cli/root.go).
func TestReadStatus_Copilot(t *testing.T) {
	a := New(Spec{
		Name:               "copilot",
		Command:            "copilot",
		StatusModelPattern: `→\s+(\S+)`,
		StatusUsagePattern: `Session:\s+([\d.]+\s*AIC)`,
	})

	screen := " /private/var/folders/jb/T/tmp.Y7  Session: 1.64 AIC used\n" +
		"─────────────────────────────\n" +
		"❯\n" +
		"─────────────────────────────\n" +
		" / commands · ? help · → next tab                 Auto → gpt-5-mini\n"

	got := a.ReadStatus(screen)
	if got.Model != "gpt-5-mini" {
		t.Errorf("model = %q, want %q", got.Model, "gpt-5-mini")
	}
	if got.Usage != "1.64 AIC" {
		t.Errorf("usage = %q, want %q", got.Usage, "1.64 AIC")
	}
}

// A harness with no status patterns reports nothing rather than guess.
func TestReadStatus_NoPatterns(t *testing.T) {
	a := New(Spec{Name: "opencode", Command: "opencode"})
	got := a.ReadStatus("model: something → gpt-4\nSession: 5 AIC used\n")
	if got.Model != "" || got.Usage != "" {
		t.Errorf("expected empty status without patterns, got %+v", got)
	}
}
