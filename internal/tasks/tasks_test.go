package tasks

import (
	"errors"
	"testing"

	"github.com/clishakehq/clishake/internal/domain"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

type fakeTaskStore struct {
	tasks   map[string]*domain.Task
	saveErr error
	getErr  error
}

func newFakeTaskStore() *fakeTaskStore {
	return &fakeTaskStore{tasks: map[string]*domain.Task{}}
}

func (f *fakeTaskStore) SaveTask(t *domain.Task) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	cp := *t
	f.tasks[t.ID] = &cp
	return nil
}

func (f *fakeTaskStore) GetTask(id string) (*domain.Task, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	t, ok := f.tasks[id]
	if !ok {
		return nil, nil
	}
	cp := *t
	return &cp, nil
}

func (f *fakeTaskStore) ListTasks() ([]*domain.Task, error) {
	out := make([]*domain.Task, 0, len(f.tasks))
	for _, t := range f.tasks {
		cp := *t
		out = append(out, &cp)
	}
	return out, nil
}

type fakeSink struct {
	events []domain.Event
}

func (f *fakeSink) Append(ev domain.Event) error {
	f.events = append(f.events, ev)
	return nil
}

func (f *fakeSink) last() domain.Event {
	return f.events[len(f.events)-1]
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestCreateBacklogNoOwner(t *testing.T) {
	store := newFakeTaskStore()
	sink := &fakeSink{}
	svc := NewService("sess1", store, sink)

	task, err := svc.Create("lead", "Do the thing", "desc", "", 0, nil)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if task.Status != domain.TaskBacklog {
		t.Errorf("Status = %q, want backlog", task.Status)
	}
	if task.ID == "" {
		t.Errorf("expected non-empty task ID")
	}
	if task.CreatedAt.IsZero() || task.UpdatedAt.IsZero() {
		t.Errorf("expected timestamps to be set")
	}

	if len(sink.events) != 1 {
		t.Fatalf("got %d events, want 1 (no task.assigned since no owner)", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Type != domain.EvTaskCreated {
		t.Errorf("event type = %q, want task.created", ev.Type)
	}
	if ev.Actor != "lead" {
		t.Errorf("event actor = %q, want lead", ev.Actor)
	}
	if ev.Subject != task.ID {
		t.Errorf("event subject = %q, want %q", ev.Subject, task.ID)
	}
}

func TestCreateAssignedWithOwner(t *testing.T) {
	store := newFakeTaskStore()
	sink := &fakeSink{}
	svc := NewService("sess1", store, sink)

	task, err := svc.Create("lead", "Do the thing", "desc", "alice", 1, []string{"task_dep"})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if task.Status != domain.TaskAssigned {
		t.Errorf("Status = %q, want assigned", task.Status)
	}
	if task.Owner != "alice" {
		t.Errorf("Owner = %q, want alice", task.Owner)
	}

	if len(sink.events) != 2 {
		t.Fatalf("got %d events, want 2 (created + assigned)", len(sink.events))
	}
	if sink.events[0].Type != domain.EvTaskCreated {
		t.Errorf("event[0].Type = %q, want task.created", sink.events[0].Type)
	}
	if sink.events[1].Type != domain.EvTaskAssigned {
		t.Errorf("event[1].Type = %q, want task.assigned", sink.events[1].Type)
	}
}

// ---------------------------------------------------------------------------
// Assign
// ---------------------------------------------------------------------------

func TestAssign(t *testing.T) {
	store := newFakeTaskStore()
	sink := &fakeSink{}
	svc := NewService("sess1", store, sink)

	task, err := svc.Create("lead", "T", "", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	sink.events = nil // reset for this assertion

	got, err := svc.Assign("lead", task.ID, "bob")
	if err != nil {
		t.Fatalf("Assign error: %v", err)
	}
	if got.Owner != "bob" {
		t.Errorf("Owner = %q, want bob", got.Owner)
	}
	if got.Status != domain.TaskAssigned {
		t.Errorf("Status = %q, want assigned", got.Status)
	}
	if len(sink.events) != 1 || sink.events[0].Type != domain.EvTaskAssigned {
		t.Fatalf("expected exactly one task.assigned event, got %+v", sink.events)
	}
}

func TestAssignMissingTask(t *testing.T) {
	store := newFakeTaskStore()
	sink := &fakeSink{}
	svc := NewService("sess1", store, sink)

	_, err := svc.Assign("lead", "task_nope", "bob")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// SetStatus — happy walk
// ---------------------------------------------------------------------------

func TestSetStatusHappyWalk(t *testing.T) {
	store := newFakeTaskStore()
	sink := &fakeSink{}
	svc := NewService("sess1", store, sink)

	task, err := svc.Create("lead", "T", "", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	walk := []domain.TaskStatus{
		domain.TaskAssigned,
		domain.TaskInProgress,
		domain.TaskInReview,
		domain.TaskCompleted,
	}

	for _, status := range walk {
		summary := ""
		if status == domain.TaskCompleted {
			summary = "all done"
		}
		got, err := svc.SetStatus("alice", task.ID, status, summary)
		if err != nil {
			t.Fatalf("SetStatus(%q) error: %v", status, err)
		}
		if got.Status != status {
			t.Errorf("Status = %q, want %q", got.Status, status)
		}
	}

	final, err := svc.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != domain.TaskCompleted {
		t.Errorf("final Status = %q, want completed", final.Status)
	}
	if final.Summary != "all done" {
		t.Errorf("Summary = %q, want %q", final.Summary, "all done")
	}

	// task.created + 4 task.updated events
	updatedCount := 0
	for _, ev := range sink.events {
		if ev.Type == domain.EvTaskUpdated {
			updatedCount++
			if ev.Actor != "alice" {
				t.Errorf("task.updated actor = %q, want alice", ev.Actor)
			}
		}
	}
	if updatedCount != len(walk) {
		t.Errorf("got %d task.updated events, want %d", updatedCount, len(walk))
	}
}

// ---------------------------------------------------------------------------
// SetStatus — invalid transitions
// ---------------------------------------------------------------------------

func TestSetStatusInvalidTransitions(t *testing.T) {
	cases := []struct {
		name string
		from domain.TaskStatus
		to   domain.TaskStatus
	}{
		{name: "completed to in_progress", from: domain.TaskCompleted, to: domain.TaskInProgress},
		{name: "backlog to in_review", from: domain.TaskBacklog, to: domain.TaskInReview},
		{name: "cancelled to anything", from: domain.TaskCancelled, to: domain.TaskInProgress},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newFakeTaskStore()
			sink := &fakeSink{}
			svc := NewService("sess1", store, sink)

			task, err := svc.Create("lead", "T", "", "", 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			// force the task into the "from" state directly via the store,
			// bypassing transition validation, so we can test the target
			// transition in isolation.
			forced, _ := store.GetTask(task.ID)
			forced.Status = c.from
			_ = store.SaveTask(forced)

			_, err = svc.SetStatus("lead", task.ID, c.to, "")
			if err == nil {
				t.Fatalf("expected error transitioning %q -> %q", c.from, c.to)
			}
		})
	}
}

func TestSetStatusMissingTask(t *testing.T) {
	store := newFakeTaskStore()
	sink := &fakeSink{}
	svc := NewService("sess1", store, sink)

	_, err := svc.SetStatus("lead", "task_nope", domain.TaskInProgress, "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// AddContributor
// ---------------------------------------------------------------------------

func TestAddContributorDedupe(t *testing.T) {
	store := newFakeTaskStore()
	sink := &fakeSink{}
	svc := NewService("sess1", store, sink)

	task, err := svc.Create("lead", "T", "", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	sink.events = nil

	got, err := svc.AddContributor("lead", task.ID, "carol")
	if err != nil {
		t.Fatalf("AddContributor error: %v", err)
	}
	if len(got.Contributors) != 1 || got.Contributors[0] != "carol" {
		t.Errorf("Contributors = %v, want [carol]", got.Contributors)
	}

	got2, err := svc.AddContributor("lead", task.ID, "carol")
	if err != nil {
		t.Fatalf("AddContributor (dup) error: %v", err)
	}
	if len(got2.Contributors) != 1 {
		t.Errorf("Contributors after dup add = %v, want length 1 (deduped)", got2.Contributors)
	}

	if len(sink.events) != 1 {
		t.Errorf("got %d events, want 1 (dedupe should not emit a second event)", len(sink.events))
	}
}

func TestAddContributorMissingTask(t *testing.T) {
	store := newFakeTaskStore()
	sink := &fakeSink{}
	svc := NewService("sess1", store, sink)

	_, err := svc.AddContributor("lead", "task_nope", "carol")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Get / List
// ---------------------------------------------------------------------------

func TestGetMissing(t *testing.T) {
	store := newFakeTaskStore()
	sink := &fakeSink{}
	svc := NewService("sess1", store, sink)

	_, err := svc.Get("task_nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestList(t *testing.T) {
	store := newFakeTaskStore()
	sink := &fakeSink{}
	svc := NewService("sess1", store, sink)

	if _, err := svc.Create("lead", "T1", "", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create("lead", "T2", "", "", 0, nil); err != nil {
		t.Fatal(err)
	}

	all, err := svc.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("got %d tasks, want 2", len(all))
	}
}
