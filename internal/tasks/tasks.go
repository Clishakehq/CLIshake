// Package tasks provides the task coordination service: creating tasks,
// assigning owners, walking status transitions, and tracking contributors,
// with an audit event emitted for every mutation.
//
// This package does not depend on internal/state: persistence and event
// logging are expressed as small consumer-side interfaces (TaskStore,
// EventSink) that any concrete store can satisfy.
package tasks

import (
	"errors"
	"fmt"
	"time"

	"github.com/clishakehq/clishake/internal/domain"
)

// TaskStore is what the service needs from persistence.
type TaskStore interface {
	SaveTask(t *domain.Task) error
	GetTask(id string) (*domain.Task, error) // (nil,nil) when missing
	ListTasks() ([]*domain.Task, error)
}

// EventSink is what the service needs from the event log.
type EventSink interface {
	Append(ev domain.Event) error
}

// ErrNotFound is returned when a task ID does not resolve to any task.
var ErrNotFound = errors.New("task not found")

// allowedTransitions enumerates the valid outgoing TaskStatus transitions.
// completed and cancelled have no entries: they are terminal.
var allowedTransitions = map[domain.TaskStatus][]domain.TaskStatus{
	domain.TaskBacklog:    {domain.TaskAssigned, domain.TaskInProgress, domain.TaskCancelled},
	domain.TaskAssigned:   {domain.TaskInProgress, domain.TaskBlocked, domain.TaskBacklog, domain.TaskCancelled},
	domain.TaskInProgress: {domain.TaskBlocked, domain.TaskInReview, domain.TaskCompleted, domain.TaskCancelled},
	domain.TaskBlocked:    {domain.TaskInProgress, domain.TaskAssigned, domain.TaskCancelled},
	domain.TaskInReview:   {domain.TaskInProgress, domain.TaskCompleted, domain.TaskCancelled},
	domain.TaskCompleted:  {},
	domain.TaskCancelled:  {},
}

func isAllowedTransition(from, to domain.TaskStatus) bool {
	for _, s := range allowedTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Service coordinates tasks: creation, assignment, status transitions, and
// contributor tracking, persisting through TaskStore and auditing through
// EventSink.
type Service struct {
	sessionID string
	store     TaskStore
	sink      EventSink
}

// NewService builds a Service bound to one session's store and event sink.
func NewService(sessionID string, store TaskStore, sink EventSink) *Service {
	return &Service{sessionID: sessionID, store: store, sink: sink}
}

// Create makes a task (ID domain.NewID("task"), status backlog or assigned
// when owner != ""), persists it, and emits task.created (plus
// task.assigned when owner is set).
func (s *Service) Create(actor, title, description, owner string, priority int, dependsOn []string) (*domain.Task, error) {
	status := domain.TaskBacklog
	if owner != "" {
		status = domain.TaskAssigned
	}

	now := time.Now().UTC()
	t := &domain.Task{
		ID:          domain.NewID("task"),
		Title:       title,
		Description: description,
		Owner:       owner,
		Status:      status,
		Priority:    priority,
		DependsOn:   dependsOn,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.store.SaveTask(t); err != nil {
		return nil, fmt.Errorf("tasks: save task: %w", err)
	}

	if err := s.sink.Append(domain.NewEvent(s.sessionID, domain.EvTaskCreated, actor, t.ID, map[string]any{
		"title":  title,
		"owner":  owner,
		"status": string(status),
	})); err != nil {
		return nil, fmt.Errorf("tasks: append created event: %w", err)
	}

	if owner != "" {
		if err := s.sink.Append(domain.NewEvent(s.sessionID, domain.EvTaskAssigned, actor, t.ID, map[string]any{
			"owner": owner,
		})); err != nil {
			return nil, fmt.Errorf("tasks: append assigned event: %w", err)
		}
	}

	return t, nil
}

// Assign sets owner, moves backlog to assigned, and emits task.assigned.
func (s *Service) Assign(actor, taskID, owner string) (*domain.Task, error) {
	t, err := s.get(taskID)
	if err != nil {
		return nil, err
	}

	t.Owner = owner
	if t.Status == domain.TaskBacklog {
		t.Status = domain.TaskAssigned
	}
	t.UpdatedAt = time.Now().UTC()

	if err := s.store.SaveTask(t); err != nil {
		return nil, fmt.Errorf("tasks: save task: %w", err)
	}

	if err := s.sink.Append(domain.NewEvent(s.sessionID, domain.EvTaskAssigned, actor, t.ID, map[string]any{
		"owner":  owner,
		"status": string(t.Status),
	})); err != nil {
		return nil, fmt.Errorf("tasks: append assigned event: %w", err)
	}

	return t, nil
}

// SetStatus validates the transition and updates the task's status,
// emitting task.updated. Completing a task sets its Summary from summary.
func (s *Service) SetStatus(actor, taskID string, status domain.TaskStatus, summary string) (*domain.Task, error) {
	t, err := s.get(taskID)
	if err != nil {
		return nil, err
	}

	if !isAllowedTransition(t.Status, status) {
		return nil, fmt.Errorf("tasks: invalid transition from %q to %q", t.Status, status)
	}

	t.Status = status
	if status == domain.TaskCompleted {
		t.Summary = summary
	}
	t.UpdatedAt = time.Now().UTC()

	if err := s.store.SaveTask(t); err != nil {
		return nil, fmt.Errorf("tasks: save task: %w", err)
	}

	payload := map[string]any{"status": string(status)}
	if status == domain.TaskCompleted {
		payload["summary"] = summary
	}
	if err := s.sink.Append(domain.NewEvent(s.sessionID, domain.EvTaskUpdated, actor, t.ID, payload)); err != nil {
		return nil, fmt.Errorf("tasks: append updated event: %w", err)
	}

	return t, nil
}

// AddContributor appends a contributor if absent, emitting task.updated.
// If the contributor is already recorded, this is a no-op (no persist, no
// event) and returns the unchanged task.
func (s *Service) AddContributor(actor, taskID, contributor string) (*domain.Task, error) {
	t, err := s.get(taskID)
	if err != nil {
		return nil, err
	}

	for _, c := range t.Contributors {
		if c == contributor {
			return t, nil
		}
	}

	t.Contributors = append(t.Contributors, contributor)
	t.UpdatedAt = time.Now().UTC()

	if err := s.store.SaveTask(t); err != nil {
		return nil, fmt.Errorf("tasks: save task: %w", err)
	}

	if err := s.sink.Append(domain.NewEvent(s.sessionID, domain.EvTaskUpdated, actor, t.ID, map[string]any{
		"contributor": contributor,
	})); err != nil {
		return nil, fmt.Errorf("tasks: append updated event: %w", err)
	}

	return t, nil
}

// Get returns the task by ID, or ErrNotFound if it does not exist.
func (s *Service) Get(taskID string) (*domain.Task, error) {
	return s.get(taskID)
}

// List returns all tasks known to the store.
func (s *Service) List() ([]*domain.Task, error) {
	ts, err := s.store.ListTasks()
	if err != nil {
		return nil, fmt.Errorf("tasks: list tasks: %w", err)
	}
	return ts, nil
}

func (s *Service) get(taskID string) (*domain.Task, error) {
	t, err := s.store.GetTask(taskID)
	if err != nil {
		return nil, fmt.Errorf("tasks: get task: %w", err)
	}
	if t == nil {
		return nil, ErrNotFound
	}
	return t, nil
}
