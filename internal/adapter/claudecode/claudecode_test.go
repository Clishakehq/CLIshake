package claudecode

import (
	"strings"
	"testing"

	"github.com/clishakehq/clishake/internal/domain"
)

func TestBuildLaunchIncludesBriefingAndTask(t *testing.T) {
	a := &domain.Agent{
		Name: "claude", WorkDir: "/proj",
		Task: "Fix the API",
		Config: map[string]string{
			"_briefing":       "You are \"claude\" in a clishake session.",
			"permission_mode": "acceptEdits",
		},
	}
	spec, err := New().BuildLaunch(a, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	got := spec.Command
	if got[0] != "claude" {
		t.Fatalf("command[0] = %q", got[0])
	}
	joined := strings.Join(got, "\x00")
	if !strings.Contains(joined, "--append-system-prompt\x00You are \"claude\" in a clishake session.") {
		t.Fatalf("briefing not passed via --append-system-prompt: %q", got)
	}
	if !strings.Contains(joined, "--permission-mode\x00acceptEdits") {
		t.Fatalf("permission mode missing: %q", got)
	}
	// The task must NOT be a launch argument — first-run dialogs swallow
	// launch prompts; the orchestrator delivers it after readiness.
	if strings.Contains(joined, "Fix the API") {
		t.Fatalf("task must not appear in launch command: %q", got)
	}
}

func TestBuildLaunchWithoutBriefing(t *testing.T) {
	a := &domain.Agent{Name: "claude", WorkDir: "/proj"}
	spec, err := New().BuildLaunch(a, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(spec.Command, " "), "--append-system-prompt") {
		t.Fatalf("unexpected briefing flag: %q", spec.Command)
	}
}

func TestFormatInputPrefixesSender(t *testing.T) {
	a := &domain.Agent{Name: "claude"}
	for _, sender := range []string{"lead", "codex"} {
		got, err := New().FormatInput(a, domain.Message{Sender: sender, Body: "hello"})
		if err != nil {
			t.Fatal(err)
		}
		want := "[clishake message from " + sender + "] hello"
		if got != want {
			t.Fatalf("FormatInput(%s) = %q, want %q", sender, got, want)
		}
	}
}

func TestDetectReadyDistinguishesComposerFromDialogs(t *testing.T) {
	a := &domain.Agent{Name: "claude"}
	ad := New()
	// Selection dialogs use "❯ 1. ..." as a cursor — NOT ready.
	dialog := "Quick safety check\n \x1b[36m❯ 1. Yes, I trust this folder\x1b[0m\n   2. No, exit\n"
	if ad.DetectReady(a, dialog) {
		t.Fatal("trust dialog must not count as ready")
	}
	// The empty composer prompt on its own line IS ready.
	composer := "────────\n\x1b[2m ❯ \x1b[0m\n────────\n"
	if !ad.DetectReady(a, composer) {
		t.Fatal("empty composer prompt should be ready")
	}
	// Idle hint lines are ready too; banner alone is not.
	if !ad.DetectReady(a, "type ? for shortcuts") {
		t.Fatal("shortcuts hint should be ready")
	}
	if ad.DetectReady(a, "Welcome banner v2.1\nloading...\n") {
		t.Fatal("banner-only output must not be ready")
	}
}

func TestBuildLaunchIncludesModel(t *testing.T) {
	a := &domain.Agent{Name: "claude", WorkDir: "/proj", Config: map[string]string{"model": "claude-fable-5"}}
	spec, err := New().BuildLaunch(a, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(spec.Command, " "); !strings.Contains(got, "--model claude-fable-5") {
		t.Fatalf("expected --model claude-fable-5 in launch, got %q", got)
	}
}

func TestBuildLaunchPermissionProfiles(t *testing.T) {
	cases := map[string]string{"auto": "--permission-mode acceptEdits", "full": "--dangerously-skip-permissions", "plan": "--permission-mode plan"}
	for profile, want := range cases {
		a := &domain.Agent{Name: "claude", WorkDir: "/proj", Config: map[string]string{"permissions": profile}}
		spec, _ := New().BuildLaunch(a, "/proj")
		if got := strings.Join(spec.Command, " "); !strings.Contains(got, want) {
			t.Errorf("permissions=%s: want %q in %q", profile, want, got)
		}
	}
	// default adds no permission flag; raw permission_mode overrides the profile
	a := &domain.Agent{Name: "c", WorkDir: "/proj", Config: map[string]string{"permissions": "full", "permission_mode": "plan"}}
	spec, _ := New().BuildLaunch(a, "/proj")
	got := strings.Join(spec.Command, " ")
	if !strings.Contains(got, "--permission-mode plan") || strings.Contains(got, "bypassPermissions") {
		t.Errorf("raw permission_mode should win: %q", got)
	}
}
