package domain

import "testing"

func TestValidAgentName(t *testing.T) {
	valid := []string{"claude", "codex-2", "Jean-Pierre", "reviewer_1", "A", "GPT5"}
	for _, n := range valid {
		if err := ValidAgentName(n); err != nil {
			t.Errorf("ValidAgentName(%q) = %v, want nil", n, err)
		}
	}

	invalid := []string{
		"",            // empty
		"Jean Pierre", // space
		"lead",        // reserved
		"Lead",        // reserved (case-insensitive)
		"ALL",         // reserved (case-insensitive)
		"team",        // reserved
		"jean.pierre", // '.' not allowed
		"a/b",         // '/' not allowed
		"café",        // non-ASCII letter
	}
	for _, n := range invalid {
		if err := ValidAgentName(n); err == nil {
			t.Errorf("ValidAgentName(%q) = nil, want error", n)
		}
	}
}
