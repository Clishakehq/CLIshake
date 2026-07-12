package state

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/clishakehq/clishake/internal/domain"
)

// openTestStore opens a fresh Store backed by a SQLite file in t.TempDir().
func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// tsEqual compares two times at RFC3339Nano (sub-millisecond) precision,
// avoiding false negatives from monotonic-clock reading differences that
// plague reflect.DeepEqual/time.Time comparisons.
func tsEqual(t *testing.T, label string, want, got time.Time) {
	t.Helper()
	ws := want.UTC().Format(time.RFC3339Nano)
	gs := got.UTC().Format(time.RFC3339Nano)
	if ws != gs {
		t.Errorf("%s: want %s, got %s", label, ws, gs)
	}
}

// ---------------------------------------------------------------------------
// Open / schema
// ---------------------------------------------------------------------------

func TestOpenCreatesSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	wantTables := []string{"schema_version", "sessions", "agents", "tasks", "messages", "approvals"}
	for _, tbl := range wantTables {
		var name string
		err := st.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, tbl).Scan(&name)
		if err != nil {
			t.Errorf("table %s: %v", tbl, err)
		}
	}

	var version int
	if err := st.db.QueryRow(`SELECT version FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("schema_version: %v", err)
	}
	if version != schemaVersion {
		t.Errorf("schema_version = %d, want %d", version, schemaVersion)
	}
}

func TestOpenIdempotentReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	sess := domain.Session{
		ID:          domain.NewID("ses"),
		ProjectPath: "/tmp/proj",
		TmuxSession: "clishake-proj",
		CreatedAt:   time.Now().UTC(),
		LastSeen:    time.Now().UTC(),
	}
	if err := st1.SaveSession(sess); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen the same file; schema creation must be idempotent and prior
	// data must survive.
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer st2.Close()

	var version int
	if err := st2.db.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("schema_version count: %v", err)
	}
	if version != 1 {
		t.Errorf("schema_version rows = %d, want 1 (idempotent)", version)
	}

	got, err := st2.GetSession()
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got == nil || got.ID != sess.ID {
		t.Errorf("GetSession after reopen = %v, want session %s", got, sess.ID)
	}
}

// ---------------------------------------------------------------------------
// Agents
// ---------------------------------------------------------------------------

func fullAgent() *domain.Agent {
	exitCode := 137
	return &domain.Agent{
		ID:       domain.NewID("ag"),
		Name:     "builder",
		Role:     "implementer",
		Adapter:  "mock",
		ParentID: "ag_parent01",
		Team:     "backend",
		Task:     "wire up the store",
		TaskID:   "tsk_abc123",
		Status:   domain.StatusRunning,
		Tmux: domain.TmuxRef{
			Session: "clishake-myproj",
			Window:  "builder",
			PaneID:  "%3",
		},
		PID:     4242,
		WorkDir: "/tmp/proj/worktrees/builder",
		Branch:  "agent/builder",
		Capabilities: []domain.Capability{
			domain.CapStructuredInput,
			domain.CapToolEvents,
			domain.CapSessionResume,
		},
		Permissions: domain.Permissions{
			ReadFiles:      true,
			ModifyFiles:    true,
			RunCommands:    true,
			NetworkAccess:  false,
			UseGit:         true,
			CommitChanges:  true,
			MergeChanges:   false,
			DeleteFiles:    false,
			ModifyConfig:   false,
			SpawnSubagents: true,
			SendMessages:   true,
			AccessSecrets:  false,
			OutsideProject: false,
		},
		Config: map[string]string{
			"model":      "sonnet",
			"max_tokens": "8192",
		},
		CreatedAt:    time.Date(2026, 7, 9, 12, 0, 0, 123456789, time.UTC),
		LastActivity: time.Date(2026, 7, 9, 12, 5, 30, 987654321, time.UTC),
		RestartCount: 2,
		ExitCode:     &exitCode,
		Health:       "ok",
	}
}

func assertAgentEqual(t *testing.T, want, got *domain.Agent) {
	t.Helper()
	if got == nil {
		t.Fatal("got nil agent")
	}
	if want.ID != got.ID ||
		want.Name != got.Name ||
		want.Role != got.Role ||
		want.Adapter != got.Adapter ||
		want.ParentID != got.ParentID ||
		want.Team != got.Team ||
		want.Task != got.Task ||
		want.TaskID != got.TaskID ||
		want.Status != got.Status ||
		want.PID != got.PID ||
		want.WorkDir != got.WorkDir ||
		want.Branch != got.Branch ||
		want.RestartCount != got.RestartCount ||
		want.Health != got.Health {
		t.Errorf("scalar fields mismatch:\nwant %+v\ngot  %+v", want, got)
	}
	if !reflect.DeepEqual(want.Tmux, got.Tmux) {
		t.Errorf("Tmux mismatch: want %+v, got %+v", want.Tmux, got.Tmux)
	}
	if !reflect.DeepEqual(want.Capabilities, got.Capabilities) {
		t.Errorf("Capabilities mismatch: want %+v, got %+v", want.Capabilities, got.Capabilities)
	}
	if !reflect.DeepEqual(want.Permissions, got.Permissions) {
		t.Errorf("Permissions mismatch: want %+v, got %+v", want.Permissions, got.Permissions)
	}
	if !reflect.DeepEqual(want.Config, got.Config) {
		t.Errorf("Config mismatch: want %+v, got %+v", want.Config, got.Config)
	}
	if want.ExitCode == nil || got.ExitCode == nil {
		if want.ExitCode != got.ExitCode {
			t.Errorf("ExitCode mismatch: want %v, got %v", want.ExitCode, got.ExitCode)
		}
	} else if *want.ExitCode != *got.ExitCode {
		t.Errorf("ExitCode mismatch: want %d, got %d", *want.ExitCode, *got.ExitCode)
	}
	tsEqual(t, "CreatedAt", want.CreatedAt, got.CreatedAt)
	tsEqual(t, "LastActivity", want.LastActivity, got.LastActivity)
}

func TestAgentRoundTrip(t *testing.T) {
	st := openTestStore(t)
	want := fullAgent()

	if err := st.SaveAgent(want); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	got, err := st.GetAgent(want.ID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	assertAgentEqual(t, want, got)

	byName, err := st.GetAgentByName(want.Name)
	if err != nil {
		t.Fatalf("GetAgentByName: %v", err)
	}
	assertAgentEqual(t, want, byName)
}

func TestAgentRoundTripNilExitCode(t *testing.T) {
	st := openTestStore(t)
	a := fullAgent()
	a.ExitCode = nil

	if err := st.SaveAgent(a); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	got, err := st.GetAgent(a.ID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.ExitCode != nil {
		t.Errorf("ExitCode = %v, want nil", got.ExitCode)
	}
}

func TestGetAgentNotFound(t *testing.T) {
	st := openTestStore(t)
	got, err := st.GetAgent("does-not-exist")
	if err != nil {
		t.Fatalf("GetAgent: unexpected error %v", err)
	}
	if got != nil {
		t.Errorf("GetAgent = %+v, want nil", got)
	}
}

func TestGetAgentByNameNotFound(t *testing.T) {
	st := openTestStore(t)
	got, err := st.GetAgentByName("nobody")
	if err != nil {
		t.Fatalf("GetAgentByName: unexpected error %v", err)
	}
	if got != nil {
		t.Errorf("GetAgentByName = %+v, want nil", got)
	}
}

func TestAgentUpsert(t *testing.T) {
	st := openTestStore(t)
	a := fullAgent()
	a.Status = domain.StatusStarting

	if err := st.SaveAgent(a); err != nil {
		t.Fatalf("SaveAgent (1): %v", err)
	}

	a.Status = domain.StatusReady
	a.Health = "healthy"
	a.RestartCount = 5
	if err := st.SaveAgent(a); err != nil {
		t.Fatalf("SaveAgent (2): %v", err)
	}

	got, err := st.GetAgent(a.ID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.Status != domain.StatusReady {
		t.Errorf("Status = %s, want %s (upsert should overwrite)", got.Status, domain.StatusReady)
	}
	if got.Health != "healthy" || got.RestartCount != 5 {
		t.Errorf("upsert did not update fields: %+v", got)
	}

	all, err := st.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("ListAgents len = %d, want 1 (upsert must not duplicate)", len(all))
	}
}

func TestListAgentsOrderedByCreatedAt(t *testing.T) {
	st := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	names := []string{"charlie", "alice", "bob"}
	for i, name := range names {
		a := fullAgent()
		a.ID = domain.NewID("ag")
		a.Name = name
		a.CreatedAt = base.Add(time.Duration(i) * time.Hour)
		if err := st.SaveAgent(a); err != nil {
			t.Fatalf("SaveAgent(%s): %v", name, err)
		}
	}

	got, err := st.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	wantOrder := []string{"charlie", "alice", "bob"} // insertion order == created_at order
	for i, w := range wantOrder {
		if got[i].Name != w {
			t.Errorf("position %d: got %s, want %s", i, got[i].Name, w)
		}
	}
}

func TestDeleteAgent(t *testing.T) {
	st := openTestStore(t)
	a := fullAgent()
	if err := st.SaveAgent(a); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	if err := st.DeleteAgent(a.ID); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	got, err := st.GetAgent(a.ID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got != nil {
		t.Errorf("GetAgent after delete = %+v, want nil", got)
	}
	all, err := st.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("ListAgents after delete len = %d, want 0", len(all))
	}
	// Deleting a nonexistent agent must not error.
	if err := st.DeleteAgent("nope"); err != nil {
		t.Errorf("DeleteAgent nonexistent: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tasks
// ---------------------------------------------------------------------------

func fullTask() *domain.Task {
	return &domain.Task{
		ID:           domain.NewID("tsk"),
		Title:        "Implement state store",
		Description:  "SQLite persistence layer for clishake",
		Owner:        "builder",
		Contributors: []string{"builder", "reviewer"},
		Status:       domain.TaskInProgress,
		Priority:     3,
		DependsOn:    []string{"tsk_foundation01", "tsk_foundation02"},
		Files:        []string{"internal/state/store.go", "internal/state/store_test.go"},
		Branch:       "agent/builder",
		CreatedAt:    time.Date(2026, 7, 9, 10, 0, 0, 111000000, time.UTC),
		UpdatedAt:    time.Date(2026, 7, 9, 11, 30, 0, 222000000, time.UTC),
		Summary:      "",
	}
}

func assertTaskEqual(t *testing.T, want, got *domain.Task) {
	t.Helper()
	if got == nil {
		t.Fatal("got nil task")
	}
	if want.ID != got.ID ||
		want.Title != got.Title ||
		want.Description != got.Description ||
		want.Owner != got.Owner ||
		want.Status != got.Status ||
		want.Priority != got.Priority ||
		want.Branch != got.Branch ||
		want.Summary != got.Summary {
		t.Errorf("scalar fields mismatch:\nwant %+v\ngot  %+v", want, got)
	}
	if !reflect.DeepEqual(want.Contributors, got.Contributors) {
		t.Errorf("Contributors mismatch: want %v, got %v", want.Contributors, got.Contributors)
	}
	if !reflect.DeepEqual(want.DependsOn, got.DependsOn) {
		t.Errorf("DependsOn mismatch: want %v, got %v", want.DependsOn, got.DependsOn)
	}
	if !reflect.DeepEqual(want.Files, got.Files) {
		t.Errorf("Files mismatch: want %v, got %v", want.Files, got.Files)
	}
	tsEqual(t, "CreatedAt", want.CreatedAt, got.CreatedAt)
	tsEqual(t, "UpdatedAt", want.UpdatedAt, got.UpdatedAt)
}

func TestTaskRoundTrip(t *testing.T) {
	st := openTestStore(t)
	want := fullTask()
	if err := st.SaveTask(want); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	got, err := st.GetTask(want.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	assertTaskEqual(t, want, got)
}

func TestGetTaskNotFound(t *testing.T) {
	st := openTestStore(t)
	got, err := st.GetTask("nope")
	if err != nil {
		t.Fatalf("GetTask: unexpected error %v", err)
	}
	if got != nil {
		t.Errorf("GetTask = %+v, want nil", got)
	}
}

func TestTaskUpsert(t *testing.T) {
	st := openTestStore(t)
	task := fullTask()
	task.Status = domain.TaskBacklog
	if err := st.SaveTask(task); err != nil {
		t.Fatalf("SaveTask (1): %v", err)
	}
	task.Status = domain.TaskCompleted
	task.Summary = "shipped"
	if err := st.SaveTask(task); err != nil {
		t.Fatalf("SaveTask (2): %v", err)
	}
	got, err := st.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != domain.TaskCompleted || got.Summary != "shipped" {
		t.Errorf("upsert did not update: %+v", got)
	}
	all, err := st.ListTasks()
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("ListTasks len = %d, want 1", len(all))
	}
}

func TestDeleteTask(t *testing.T) {
	st := openTestStore(t)
	task := fullTask()
	if err := st.SaveTask(task); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	if err := st.DeleteTask(task.ID); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	got, err := st.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got != nil {
		t.Errorf("GetTask after delete = %+v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

func TestMessageListOrderingAndLimit(t *testing.T) {
	st := openTestStore(t)
	base := time.Date(2026, 7, 9, 9, 0, 0, 0, time.UTC)
	bodies := []string{"m1", "m2", "m3", "m4", "m5"}
	for i, body := range bodies {
		m := &domain.Message{
			ID:        domain.NewID("msg"),
			Sender:    "lead",
			Selector:  "@builder",
			Recipient: "builder",
			Type:      domain.MsgChat,
			Body:      body,
			Delivery:  domain.DeliveryDelivered,
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := st.SaveMessage(m); err != nil {
			t.Fatalf("SaveMessage(%s): %v", body, err)
		}
	}

	all, err := st.ListMessages(0)
	if err != nil {
		t.Fatalf("ListMessages(0): %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("len = %d, want 5", len(all))
	}
	for i, want := range bodies {
		if all[i].Body != want {
			t.Errorf("position %d: got %s, want %s", i, all[i].Body, want)
		}
	}
	if all[len(all)-1].Body != "m5" {
		t.Errorf("newest-last violated: last = %s", all[len(all)-1].Body)
	}

	limited, err := st.ListMessages(2)
	if err != nil {
		t.Fatalf("ListMessages(2): %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("limited len = %d, want 2", len(limited))
	}
	if limited[0].Body != "m4" || limited[1].Body != "m5" {
		t.Errorf("limited = [%s, %s], want [m4, m5]", limited[0].Body, limited[1].Body)
	}
}

func TestListMessagesWithFiltersBySenderOrRecipient(t *testing.T) {
	st := openTestStore(t)
	base := time.Date(2026, 7, 9, 9, 0, 0, 0, time.UTC)
	msgs := []*domain.Message{
		{ID: domain.NewID("msg"), Sender: "lead", Recipient: "builder", Body: "to-builder", Type: domain.MsgChat, Delivery: domain.DeliveryDelivered, CreatedAt: base},
		{ID: domain.NewID("msg"), Sender: "builder", Recipient: "lead", Body: "from-builder", Type: domain.MsgChat, Delivery: domain.DeliveryDelivered, CreatedAt: base.Add(time.Minute)},
		{ID: domain.NewID("msg"), Sender: "lead", Recipient: "reviewer", Body: "to-reviewer", Type: domain.MsgChat, Delivery: domain.DeliveryDelivered, CreatedAt: base.Add(2 * time.Minute)},
		{ID: domain.NewID("msg"), Sender: "reviewer", Recipient: "builder", Body: "reviewer-to-builder", Type: domain.MsgChat, Delivery: domain.DeliveryDelivered, CreatedAt: base.Add(3 * time.Minute)},
	}
	for _, m := range msgs {
		if err := st.SaveMessage(m); err != nil {
			t.Fatalf("SaveMessage: %v", err)
		}
	}

	got, err := st.ListMessagesWith("builder", 0)
	if err != nil {
		t.Fatalf("ListMessagesWith: %v", err)
	}
	wantBodies := []string{"to-builder", "from-builder", "reviewer-to-builder"}
	if len(got) != len(wantBodies) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(wantBodies), got)
	}
	for i, w := range wantBodies {
		if got[i].Body != w {
			t.Errorf("position %d: got %s, want %s", i, got[i].Body, w)
		}
	}

	limited, err := st.ListMessagesWith("builder", 1)
	if err != nil {
		t.Fatalf("ListMessagesWith limited: %v", err)
	}
	if len(limited) != 1 || limited[0].Body != "reviewer-to-builder" {
		t.Errorf("limited = %v, want [reviewer-to-builder]", limited)
	}
}

func TestSaveMessageUpsertPreservesOrderAndMeta(t *testing.T) {
	st := openTestStore(t)
	base := time.Date(2026, 7, 9, 9, 0, 0, 0, time.UTC)
	m1 := &domain.Message{ID: domain.NewID("msg"), Sender: "lead", Recipient: "builder", Body: "first", Type: domain.MsgChat, Delivery: domain.DeliveryPending, Meta: map[string]string{"k": "v"}, CreatedAt: base}
	m2 := &domain.Message{ID: domain.NewID("msg"), Sender: "lead", Recipient: "builder", Body: "second", Type: domain.MsgChat, Delivery: domain.DeliveryPending, CreatedAt: base.Add(time.Minute)}
	if err := st.SaveMessage(m1); err != nil {
		t.Fatalf("SaveMessage m1: %v", err)
	}
	if err := st.SaveMessage(m2); err != nil {
		t.Fatalf("SaveMessage m2: %v", err)
	}

	// Update delivery state of m1 in place.
	m1.Delivery = domain.DeliveryDelivered
	if err := st.SaveMessage(m1); err != nil {
		t.Fatalf("SaveMessage m1 update: %v", err)
	}

	all, err := st.ListMessages(0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len = %d, want 2 (upsert must not duplicate)", len(all))
	}
	if all[0].Body != "first" || all[1].Body != "second" {
		t.Errorf("order changed after upsert: %v", all)
	}
	if all[0].Delivery != domain.DeliveryDelivered {
		t.Errorf("delivery not updated: %s", all[0].Delivery)
	}
	if !reflect.DeepEqual(all[0].Meta, map[string]string{"k": "v"}) {
		t.Errorf("Meta mismatch: %v", all[0].Meta)
	}
}

// ---------------------------------------------------------------------------
// Approvals
// ---------------------------------------------------------------------------

func TestApprovalRoundTripAndStateFiltering(t *testing.T) {
	st := openTestStore(t)
	base := time.Date(2026, 7, 9, 8, 0, 0, 0, time.UTC)
	decided := base.Add(5 * time.Minute)

	pending := &domain.Approval{
		ID: domain.NewID("apr"), AgentName: "builder", Action: "run_command",
		Command: "rm -rf build/", Reason: "clean build dir", Resources: []string{"build/"},
		Risk: "medium", State: domain.ApprovalPending, CreatedAt: base,
	}
	granted := &domain.Approval{
		ID: domain.NewID("apr"), AgentName: "builder", Action: "merge",
		Command: "git merge agent/builder", Reason: "task complete", Resources: []string{"main"},
		Risk: "high", State: domain.ApprovalGranted, CreatedAt: base.Add(time.Minute), DecidedAt: &decided,
	}
	denied := &domain.Approval{
		ID: domain.NewID("apr"), AgentName: "reviewer", Action: "delete_files",
		Reason: "unused", Risk: "high", State: domain.ApprovalDenied, CreatedAt: base.Add(2 * time.Minute), DecidedAt: &decided,
	}

	for _, ap := range []*domain.Approval{pending, granted, denied} {
		if err := st.SaveApproval(ap); err != nil {
			t.Fatalf("SaveApproval(%s): %v", ap.ID, err)
		}
	}

	got, err := st.GetApproval(granted.ID)
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if got == nil {
		t.Fatal("GetApproval returned nil")
	}
	if !reflect.DeepEqual(got.Resources, granted.Resources) {
		t.Errorf("Resources mismatch: want %v, got %v", granted.Resources, got.Resources)
	}
	if got.DecidedAt == nil {
		t.Fatal("DecidedAt = nil, want set")
	}
	tsEqual(t, "DecidedAt", decided, *got.DecidedAt)

	gotPending, err := st.GetApproval(pending.ID)
	if err != nil {
		t.Fatalf("GetApproval pending: %v", err)
	}
	if gotPending.DecidedAt != nil {
		t.Errorf("pending DecidedAt = %v, want nil", gotPending.DecidedAt)
	}

	onlyPending, err := st.ListApprovals(domain.ApprovalPending)
	if err != nil {
		t.Fatalf("ListApprovals(pending): %v", err)
	}
	if len(onlyPending) != 1 || onlyPending[0].ID != pending.ID {
		t.Errorf("ListApprovals(pending) = %v, want just %s", onlyPending, pending.ID)
	}

	all, err := st.ListApprovals("")
	if err != nil {
		t.Fatalf("ListApprovals(\"\"): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("ListApprovals(\"\") len = %d, want 3", len(all))
	}
}

func TestGetApprovalNotFound(t *testing.T) {
	st := openTestStore(t)
	got, err := st.GetApproval("nope")
	if err != nil {
		t.Fatalf("GetApproval: unexpected error %v", err)
	}
	if got != nil {
		t.Errorf("GetApproval = %+v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

func TestSessionSaveGetMostRecentWins(t *testing.T) {
	st := openTestStore(t)

	got, err := st.GetSession()
	if err != nil {
		t.Fatalf("GetSession (empty): %v", err)
	}
	if got != nil {
		t.Errorf("GetSession (empty) = %+v, want nil", got)
	}

	t1 := time.Date(2026, 7, 9, 8, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 7, 9, 9, 0, 0, 0, time.UTC)

	sessA := domain.Session{ID: domain.NewID("ses"), ProjectPath: "/proj/a", TmuxSession: "clishake-a", CreatedAt: t1, LastSeen: t1}
	if err := st.SaveSession(sessA); err != nil {
		t.Fatalf("SaveSession A: %v", err)
	}
	got, err = st.GetSession()
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got == nil || got.ID != sessA.ID {
		t.Fatalf("GetSession = %v, want %s", got, sessA.ID)
	}

	sessB := domain.Session{ID: domain.NewID("ses"), ProjectPath: "/proj/b", TmuxSession: "clishake-b", CreatedAt: t2, LastSeen: t2}
	if err := st.SaveSession(sessB); err != nil {
		t.Fatalf("SaveSession B: %v", err)
	}
	got, err = st.GetSession()
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got == nil || got.ID != sessB.ID {
		t.Fatalf("GetSession = %v, want most recent %s", got, sessB.ID)
	}
	if got.ProjectPath != "/proj/b" {
		t.Errorf("ProjectPath = %s, want /proj/b", got.ProjectPath)
	}

	// Upsert sessA to a later created_at than sessB: it must now win.
	t3 := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	sessA.CreatedAt = t3
	sessA.LastSeen = t3
	if err := st.SaveSession(sessA); err != nil {
		t.Fatalf("SaveSession A update: %v", err)
	}
	got, err = st.GetSession()
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got == nil || got.ID != sessA.ID {
		t.Fatalf("GetSession = %v, want updated %s to win", got, sessA.ID)
	}
	tsEqual(t, "LastSeen", t3, got.LastSeen)

	// Confirm upsert did not duplicate rows.
	var count int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 2 {
		t.Errorf("sessions row count = %d, want 2 (A upserted, B inserted)", count)
	}
}

// ---------------------------------------------------------------------------
// Concurrency smoke test
// ---------------------------------------------------------------------------

func TestConcurrentAccess(t *testing.T) {
	st := openTestStore(t)
	const n = 20
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			a := fullAgent()
			a.ID = domain.NewID("ag")
			a.Name = a.Name + string(rune('a'+i%26))
			errCh <- st.SaveAgent(a)
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("concurrent SaveAgent: %v", err)
		}
	}
	all, err := st.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(all) != n {
		t.Errorf("ListAgents len = %d, want %d", len(all), n)
	}
}
