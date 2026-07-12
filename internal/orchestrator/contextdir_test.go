package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clishakehq/clishake/internal/adapter"
	adaptermock "github.com/clishakehq/clishake/internal/adapter/mock"
	"github.com/clishakehq/clishake/internal/domain"
)

func openContextTestOrch(t *testing.T) *Orchestrator {
	t.Helper()
	dir := t.TempDir()
	reg := adapter.NewRegistry()
	reg.Register(adaptermock.New())
	o, err := Open(dir, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Close)
	return o
}

func readCtxFile(t *testing.T, o *Orchestrator, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(o.ContextDir(), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func TestContextFilesMaterializedOnOpen(t *testing.T) {
	o := openContextTestOrch(t)
	for _, f := range []string{"session.md", "roster.md", "tasks.md", "notes.md"} {
		if _, err := os.Stat(filepath.Join(o.ContextDir(), f)); err != nil {
			t.Errorf("%s missing after Open: %v", f, err)
		}
	}
	session := readCtxFile(t, o, "session.md")
	if !strings.Contains(session, o.Session.ID) || !strings.Contains(session, "send @<recipient>") {
		t.Fatalf("session.md lacks identity/protocol: %q", session)
	}
}

func TestContextRosterAndTasksStayCurrent(t *testing.T) {
	o := openContextTestOrch(t)
	if _, err := o.AddAgent(AgentSpec{Name: "builder", Role: "backend", Adapter: "mock", Task: "Build it"}); err != nil {
		t.Fatal(err)
	}
	roster := readCtxFile(t, o, "roster.md")
	if !strings.Contains(roster, "**builder**") || !strings.Contains(roster, "role: backend") {
		t.Fatalf("roster.md not synced after AddAgent: %q", roster)
	}
	task, err := o.Tasks.Create("lead", "Fix login", "desc", "builder", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	tasks := readCtxFile(t, o, "tasks.md")
	if !strings.Contains(tasks, task.ID) || !strings.Contains(tasks, "Fix login") {
		t.Fatalf("tasks.md not synced after task create: %q", tasks)
	}
}

func TestAddNoteAppendsAttributed(t *testing.T) {
	o := openContextTestOrch(t)
	if err := o.AddNote("claude", "we chose worktrees"); err != nil {
		t.Fatal(err)
	}
	if err := o.AddNote("lead", "ship friday"); err != nil {
		t.Fatal(err)
	}
	notes := readCtxFile(t, o, "notes.md")
	if !strings.Contains(notes, "**claude**: we chose worktrees") ||
		!strings.Contains(notes, "**lead**: ship friday") {
		t.Fatalf("notes.md missing attributed entries: %q", notes)
	}
	if err := o.AddNote("lead", "   "); err == nil {
		t.Fatal("empty note should error")
	}
}

func TestCoordinatorRoleGetsCoordinatorProfileAndBriefing(t *testing.T) {
	o := openContextTestOrch(t)
	a, err := o.AddAgent(AgentSpec{Name: "coord", Role: CoordinatorRole, Adapter: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Permissions.ModifyFiles {
		t.Fatal("coordinator must not get modify_files by default")
	}
	if !a.Permissions.SendMessages || !a.Permissions.ReadFiles {
		t.Fatalf("coordinator profile wrong: %+v", a.Permissions)
	}
	brief := o.briefing(a)
	if !strings.Contains(brief, "SESSION COORDINATOR") || !strings.Contains(brief, "task create") {
		t.Fatalf("coordinator briefing missing responsibilities: %q", brief)
	}
	// Explicit permissions still win over the coordinator profile.
	p := domain.DefaultPermissions()
	b, err := o.AddAgent(AgentSpec{Name: "coord2", Role: CoordinatorRole, Adapter: "mock", Permissions: &p})
	if err != nil {
		t.Fatal(err)
	}
	if !b.Permissions.ModifyFiles {
		t.Fatal("explicit permissions should override coordinator profile")
	}
}

func TestBriefingPointsToContextFiles(t *testing.T) {
	o := openContextTestOrch(t)
	a, err := o.AddAgent(AgentSpec{Name: "builder", Adapter: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	brief := o.briefing(a)
	for _, f := range []string{"session.md", "roster.md", "tasks.md", "notes.md"} {
		if !strings.Contains(brief, f) {
			t.Errorf("briefing missing context file %s", f)
		}
	}
}

func TestInitProjectWritesGitignore(t *testing.T) {
	dir := t.TempDir()
	if _, err := InitProject(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".clishake", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"state.db*", "events.jsonl", "worktrees/", "context/"} {
		if !strings.Contains(string(b), want) {
			t.Errorf(".clishake/.gitignore missing %q", want)
		}
	}
}

func TestAddAgentWithTaskCreatesBoardTask(t *testing.T) {
	o := openContextTestOrch(t)
	a, err := o.AddAgent(AgentSpec{Name: "reviewer", Role: "reviewer", Adapter: "mock", Task: "Review the API changes"})
	if err != nil {
		t.Fatal(err)
	}
	if a.TaskID == "" {
		t.Fatal("agent with an initial task should get a board task id")
	}
	tasks, err := o.Tasks.List()
	if err != nil {
		t.Fatal(err)
	}
	var found *domain.Task
	for _, tk := range tasks {
		if tk.ID == a.TaskID {
			found = tk
		}
	}
	if found == nil {
		t.Fatal("initial task not on the board")
	}
	if found.Owner != "reviewer" || found.Title != "Review the API changes" {
		t.Fatalf("board task wrong: %+v", found)
	}
	// No task, no board entry.
	b, _ := o.AddAgent(AgentSpec{Name: "idle", Adapter: "mock"})
	if b.TaskID != "" {
		t.Fatal("agent without a task should not create a board task")
	}
}

func TestBriefingRestartNote(t *testing.T) {
	o := openContextTestOrch(t)
	a, err := o.AddAgent(AgentSpec{Name: "builder", Adapter: "mock", Task: "Build it"})
	if err != nil {
		t.Fatal(err)
	}
	// Fresh briefing: no restart note.
	if strings.Contains(o.briefing(a), "YOU WERE JUST RESTARTED") {
		t.Fatal("fresh briefing must not claim a restart")
	}
	// After a (simulated) respawn the flag is set.
	if a.Config == nil {
		a.Config = map[string]string{}
	}
	a.Config[restartedKey] = "1"
	brief := o.briefing(a)
	if !strings.Contains(brief, "YOU WERE JUST RESTARTED") || !strings.Contains(brief, "task board") {
		t.Fatalf("restart briefing missing the restart guidance: %q", brief)
	}
	if !strings.Contains(brief, "Do ONLY what your task asks") {
		t.Fatal("restart note should discourage scope creep")
	}
}
