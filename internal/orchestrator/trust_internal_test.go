package orchestrator

import "testing"

// Text captured live from real claude and codex first-run trust prompts.
func TestTrustDialogUp(t *testing.T) {
	claude := "Quick safety check: Is this a project you created or one you trust?\n" +
		" ❯ 1. Yes, I trust this folder\n   2. No, exit\n Enter to confirm · Esc to cancel"
	codex := "Do you trust the contents of this directory?\n" +
		" › 1. Yes, continue\n   2. No, quit\n Press enter to continue"
	for name, s := range map[string]string{"claude": claude, "codex": codex} {
		if !trustDialogUp(s) {
			t.Errorf("%s trust dialog not detected", name)
		}
	}

	negatives := map[string]string{
		"plain composer":     "❯ type your message here",
		"menu without trust": "Pick a model:\n ❯ 1. opus\n   2. sonnet",
		"trust text no menu": "This folder is already trusted. Ready.",
		"empty":              "",
	}
	for name, s := range negatives {
		if trustDialogUp(s) {
			t.Errorf("%s: false positive", name)
		}
	}
}
