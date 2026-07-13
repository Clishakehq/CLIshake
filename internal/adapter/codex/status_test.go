package codex

import "testing"

// Real captured Codex screens (see internal/adapter/codex/codex.go ReadStatus).
func TestReadStatus(t *testing.T) {
	a := New()

	// Composer footer that persists at rest.
	footer := "› Improve documentation in @filename\n" +
		"  gpt-5.6-sol medium · /private/var/folders/jb/T/tmp.ilbHtW8VMh\n"
	if got := a.ReadStatus(footer); got.Model != "gpt-5.6-sol medium" {
		t.Errorf("footer model = %q, want %q", got.Model, "gpt-5.6-sol medium")
	}

	// Startup box: "│ model:  <model>   /model to change │".
	splash := "│ >_ OpenAI Codex (v0.144.1)                       │\n" +
		"│ model:     gpt-5.6-sol medium   /model to change │\n" +
		"│ directory: /private/var/folders/jb/T/tmp.ilbHtW8VMh │\n"
	if got := a.ReadStatus(splash); got.Model != "gpt-5.6-sol medium" {
		t.Errorf("splash model = %q, want %q", got.Model, "gpt-5.6-sol medium")
	}

	// Codex shows no stable at-rest usage figure.
	if got := a.ReadStatus(footer); got.Usage != "" {
		t.Errorf("usage = %q, want empty", got.Usage)
	}

	// A screen with no model line yields nothing (no guessing).
	if got := a.ReadStatus("just some output\nwith a · bullet but no path\n"); got.Model != "" {
		t.Errorf("model = %q, want empty", got.Model)
	}
}
