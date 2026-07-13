package adapter

import "testing"

func TestIsSlashCommand(t *testing.T) {
	cmds := []string{"/loop", "/model gpt-5", "/compact", "  /help  ", "/clear\n"}
	for _, s := range cmds {
		if !IsSlashCommand(s) {
			t.Errorf("IsSlashCommand(%q) = false, want true", s)
		}
	}
	notCmds := []string{
		"/Users/foo/bar",  // path
		"/",               // bare slash
		"/ leading",       // space after slash
		"/123",            // not a letter
		"hello",           // plain text
		"",                // empty
		"tell @bob /loop", // slash not at start
	}
	for _, s := range notCmds {
		if IsSlashCommand(s) {
			t.Errorf("IsSlashCommand(%q) = true, want false", s)
		}
	}
}

func TestFormatRouted(t *testing.T) {
	// Lead slash command passes through verbatim (harness executes it).
	if got := FormatRouted("lead", "/loop until solved"); got != "/loop until solved" {
		t.Errorf("lead slash = %q, want passthrough", got)
	}
	if got := FormatRouted("lead", "  /compact  "); got != "/compact" {
		t.Errorf("lead slash (trimmed) = %q, want %q", got, "/compact")
	}
	// Ordinary lead message keeps the attribution prefix.
	if got := FormatRouted("lead", "hello there"); got != "[clishake message from lead] hello there" {
		t.Errorf("lead text = %q", got)
	}
	// A path is not a command.
	if got := FormatRouted("lead", "/etc/hosts is broken"); got != "[clishake message from lead] /etc/hosts is broken" {
		t.Errorf("lead path = %q, want prefixed", got)
	}
	// A peer cannot drive another agent's harness with a slash command.
	if got := FormatRouted("codex", "/loop"); got != "[clishake message from codex] /loop" {
		t.Errorf("peer slash = %q, want prefixed (no passthrough)", got)
	}
}
