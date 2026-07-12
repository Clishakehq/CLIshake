package codex

import (
	"strings"
	"testing"

	"github.com/clishakehq/clishake/internal/domain"
)

func TestBuildLaunchBriefingBecomesPromptPreamble(t *testing.T) {
	a := &domain.Agent{
		Name: "codex", WorkDir: "/proj",
		Task:   "Review the changes",
		Config: map[string]string{"_briefing": "You are \"codex\" in a clishake session."},
	}
	spec, err := New().BuildLaunch(a, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	prompt := spec.Command[len(spec.Command)-1]
	if !strings.HasPrefix(prompt, "You are \"codex\"") {
		t.Fatalf("briefing should lead the prompt, got %q", prompt)
	}
	// Task must NOT ride in the launch prompt (first-run dialogs swallow
	// launch args; the orchestrator delivers it after readiness).
	if strings.Contains(prompt, "Review the changes") {
		t.Fatalf("task must not appear in launch prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "wait for instructions") {
		t.Fatalf("wait note missing: %q", prompt)
	}
}

func TestBuildLaunchBriefingWithoutTaskAddsWaitNote(t *testing.T) {
	a := &domain.Agent{
		Name: "codex", WorkDir: "/proj",
		Config: map[string]string{"_briefing": "briefing text"},
	}
	spec, err := New().BuildLaunch(a, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	prompt := spec.Command[len(spec.Command)-1]
	if !strings.Contains(prompt, "wait for instructions") {
		t.Fatalf("wait note missing: %q", prompt)
	}
}

func TestBuildLaunchNoBriefingNoTask(t *testing.T) {
	a := &domain.Agent{Name: "codex", WorkDir: "/proj"}
	spec, err := New().BuildLaunch(a, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Command) != 1 || spec.Command[0] != "codex" {
		t.Fatalf("expected bare launch, got %q", spec.Command)
	}
}

func TestFormatInputPrefixesSender(t *testing.T) {
	a := &domain.Agent{Name: "codex"}
	got, err := New().FormatInput(a, domain.Message{Sender: "claude", Body: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "[clishake message from claude] ping" {
		t.Fatalf("FormatInput = %q", got)
	}
}
