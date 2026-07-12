package ask

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Validate
// ---------------------------------------------------------------------------

func TestValidateGoodPlans(t *testing.T) {
	good := [][]string{
		{"agent", "add", "claude", "--adapter", "claude-code", "--role", "backend", "--task", "Fix the login bug"},
		{"agent", "add", "Jean-Pierre", "--adapter", "codex", "--task", "Join the chat"},
		{"agent", "start", "claude"},
		{"agent", "stop", "claude"},
		{"agent", "restart", "claude"},
		{"agent", "remove", "claude"},
		{"send", "@claude", "hello there"},
		{"broadcast", "standup in 5"},
		{"task", "create", "--title", "Fix bug", "--assign", "claude", "--priority", "2"},
		{"task", "assign", "task_ab12", "claude"},
		{"task", "update", "task_ab12", "--status", "in_progress", "--summary", "started"},
		{"tasks"},
		{"agents"},
		{"status"},
		{"messages"},
		{"events"},
		{"note", "remember to check the deploy"},
		{"approvals"},
		{"approvals", "grant", "ap_123"},
		{"approvals", "deny", "ap_123"},
	}
	for _, argv := range good {
		p := Plan{Commands: [][]string{argv}, Explanation: "test"}
		if err := Validate(p); err != nil {
			t.Errorf("expected %q to be valid, got error: %v", strings.Join(argv, " "), err)
		}
	}
}

func TestValidateRejectsBadPlans(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"unknown command", []string{"doctor"}},
		{"bad subcommand", []string{"agent", "explode", "claude"}},
		{"empty argv", []string{}},
		{"newline injection", []string{"send", "@claude", "hi\nrm -rf /"}},
		{"kill session flag", []string{"agent", "stop", "claude", "--kill-session"}},
		{"missing required subcommand", []string{"agent"}},
		{"missing required subcommand task", []string{"task"}},
		{"disallowed flag on agent add", []string{"agent", "add", "claude", "--dangerous"}},
		{"disallowed flag on task create", []string{"task", "create", "--title", "x", "--force"}},
		{"agent name with a space", []string{"agent", "add", "Jean Pierre", "--adapter", "codex", "--task", "Join the chat"}},
		{"reserved agent name (any case)", []string{"agent", "add", "Team"}},
		{"agent add with a flag where the name should be", []string{"agent", "add", "--adapter", "codex"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Plan{Commands: [][]string{c.argv}, Explanation: "test"}
			if err := Validate(p); err == nil {
				t.Errorf("expected %q to be rejected, got no error", strings.Join(c.argv, " "))
			}
		})
	}
}

func TestValidateRejectsEmptyPlan(t *testing.T) {
	if err := Validate(Plan{}); err == nil {
		t.Error("expected empty plan (no commands) to be rejected")
	}
}

// ---------------------------------------------------------------------------
// ExtractPlan
// ---------------------------------------------------------------------------

func TestExtractPlanPlainJSON(t *testing.T) {
	raw := `{"commands": [["status"]], "explanation": "check status"}`
	p, err := ExtractPlan(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Commands) != 1 || p.Commands[0][0] != "status" {
		t.Errorf("unexpected plan: %+v", p)
	}
	if p.Explanation != "check status" {
		t.Errorf("unexpected explanation: %q", p.Explanation)
	}
}

func TestExtractPlanFencedJSON(t *testing.T) {
	raw := "Here you go:\n```json\n{\"commands\": [[\"tasks\"]], \"explanation\": \"list tasks\"}\n```\n"
	p, err := ExtractPlan(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Commands) != 1 || p.Commands[0][0] != "tasks" {
		t.Errorf("unexpected plan: %+v", p)
	}
}

func TestExtractPlanEmbeddedInProse(t *testing.T) {
	raw := "Sure, here's the plan:\n\n" +
		`{"commands": [["broadcast","hi team"]], "explanation": "say hi"}` +
		"\n\nLet me know if that works!"
	p, err := ExtractPlan(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Commands) != 1 || p.Commands[0][1] != "hi team" {
		t.Errorf("unexpected plan: %+v", p)
	}
}

func TestExtractPlanGarbageErrors(t *testing.T) {
	if _, err := ExtractPlan("this is not json at all, sorry"); err == nil {
		t.Error("expected error for garbage input")
	}
}

func TestExtractPlanEmptyCommandsErrors(t *testing.T) {
	raw := `{"commands": [], "explanation": "nothing to do"}`
	if _, err := ExtractPlan(raw); err == nil {
		t.Error("expected error for empty commands")
	}
}

// ---------------------------------------------------------------------------
// BuildPrompt
// ---------------------------------------------------------------------------

func TestBuildPrompt(t *testing.T) {
	sc := SessionContext{
		ProjectDir: "/tmp/myproj",
		Agents:     []string{"claude (role backend, adapter claude-code, status ready)"},
		Tasks:      []string{"task_ab12 [assigned→claude] Fix login bug"},
		Messages:   []string{"lead → claude: please investigate"},
		Adapters:   []string{"mock", "claude-code", "codex"},
	}
	prompt := BuildPrompt(sc, "add a new backend agent")

	mustContain := []string{
		"agent add",               // whitelist mention
		"/tmp/myproj",             // context: project dir
		"claude (role backend",    // context: agents
		"task_ab12",               // context: tasks
		"lead → claude",           // context: messages
		"add a new backend agent", // the query
		"JSON",                    // output format instruction
	}
	for _, s := range mustContain {
		if !strings.Contains(prompt, s) {
			t.Errorf("expected prompt to contain %q, got:\n%s", s, prompt)
		}
	}
}

// ---------------------------------------------------------------------------
// Translate
// ---------------------------------------------------------------------------

func withStubs(t *testing.T, lp func(string) (string, error), rb func(context.Context, string, ...string) (string, error)) {
	t.Helper()
	origLookPath, origRunBackend := lookPath, runBackend
	lookPath = lp
	runBackend = rb
	t.Cleanup(func() {
		lookPath = origLookPath
		runBackend = origRunBackend
	})
}

func fencedPlanOutput(cmd string) string {
	return fmt.Sprintf("```json\n{\"commands\": [[%q]], \"explanation\": \"ran via stub\"}\n```", cmd)
}

func TestTranslateUsesClaudeWhenAvailable(t *testing.T) {
	withStubs(t,
		func(bin string) (string, error) { return "/usr/bin/" + bin, nil },
		func(ctx context.Context, name string, args ...string) (string, error) {
			if name != "claude" {
				t.Fatalf("expected claude to be invoked first, got %q", name)
			}
			return fencedPlanOutput("status"), nil
		},
	)
	plan, backend, err := Translate("do something")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if backend != "claude" {
		t.Errorf("expected backend %q, got %q", "claude", backend)
	}
	if len(plan.Commands) != 1 || plan.Commands[0][0] != "status" {
		t.Errorf("unexpected plan: %+v", plan)
	}
}

func TestTranslateFallsBackWhenClaudeMissing(t *testing.T) {
	withStubs(t,
		func(bin string) (string, error) {
			if bin == "claude" {
				return "", errors.New("not found")
			}
			return "/usr/bin/" + bin, nil
		},
		func(ctx context.Context, name string, args ...string) (string, error) {
			if name != "codex" {
				t.Fatalf("expected codex to be invoked, got %q", name)
			}
			return fencedPlanOutput("agents"), nil
		},
	)
	plan, backend, err := Translate("do something")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if backend != "codex" {
		t.Errorf("expected backend %q, got %q", "codex", backend)
	}
	if len(plan.Commands) != 1 || plan.Commands[0][0] != "agents" {
		t.Errorf("unexpected plan: %+v", plan)
	}
}

func TestTranslateFallsBackWhenClaudeExecFails(t *testing.T) {
	// Both binaries are "present" in PATH, but invoking claude fails, so
	// Translate should fall through to codex.
	withStubs(t,
		func(bin string) (string, error) { return "/usr/bin/" + bin, nil },
		func(ctx context.Context, name string, args ...string) (string, error) {
			if name == "claude" {
				return "", errors.New("claude: rate limited")
			}
			return fencedPlanOutput("messages"), nil
		},
	)
	plan, backend, err := Translate("do something")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if backend != "codex" {
		t.Errorf("expected fallback backend %q, got %q", "codex", backend)
	}
	if len(plan.Commands) != 1 || plan.Commands[0][0] != "messages" {
		t.Errorf("unexpected plan: %+v", plan)
	}
}

func TestTranslateErrorsWhenNeitherInstalled(t *testing.T) {
	withStubs(t,
		func(bin string) (string, error) { return "", errors.New("not found") },
		func(ctx context.Context, name string, args ...string) (string, error) {
			t.Fatalf("runBackend should not be called when nothing is on PATH")
			return "", nil
		},
	)
	_, _, err := Translate("do something")
	if err == nil {
		t.Fatal("expected error when neither backend is installed")
	}
	if !strings.Contains(err.Error(), "claude") || !strings.Contains(err.Error(), "codex") {
		t.Errorf("expected error to name both backends, got: %v", err)
	}
}
