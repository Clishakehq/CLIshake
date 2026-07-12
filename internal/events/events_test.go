package events

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/clishakehq/clishake/internal/domain"
)

func TestAppendAndReadAllOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	want := []domain.EventType{domain.EvAgentCreated, domain.EvAgentStarted, domain.EvAgentReady}
	for i, typ := range want {
		ev := domain.NewEvent("sess1", typ, "lead", "agent-x", map[string]any{"i": i})
		if err := l.Append(ev); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}

	evs, skipped, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(evs) != len(want) {
		t.Fatalf("got %d events, want %d", len(evs), len(want))
	}
	for i, ev := range evs {
		if ev.Type != want[i] {
			t.Errorf("evs[%d].Type = %q, want %q", i, ev.Type, want[i])
		}
		if ev.SessionID != "sess1" {
			t.Errorf("evs[%d].SessionID = %q, want %q", i, ev.SessionID, "sess1")
		}
	}
}

func TestSubscribeFiresPerAppend(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	var got []domain.Event
	l.Subscribe(func(ev domain.Event) { got = append(got, ev) })

	for i := 0; i < 3; i++ {
		if err := l.Append(domain.NewEvent("s", domain.EvAgentCreated, "lead", "a", nil)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	if len(got) != 3 {
		t.Fatalf("subscriber fired %d times, want 3", len(got))
	}
}

func TestSubscribeMultipleCallbacks(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	var count1, count2 int
	l.Subscribe(func(domain.Event) { count1++ })
	l.Subscribe(func(domain.Event) { count2++ })

	if err := l.Append(domain.NewEvent("s", domain.EvAgentCreated, "lead", "a", nil)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if count1 != 1 || count2 != 1 {
		t.Errorf("count1=%d count2=%d, want 1 and 1", count1, count2)
	}
}

func TestReadAllSkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := l.Append(domain.NewEvent("s", domain.EvAgentCreated, "lead", "a", nil)); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Inject a garbage line directly (bypassing Append) between valid ones.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for corrupt write: %v", err)
	}
	if _, err := f.WriteString("{not valid json at all\n"); err != nil {
		t.Fatalf("write corrupt line: %v", err)
	}
	// Also a structurally valid JSON line but missing the required "type"
	// field, which ReadAll treats as unparseable (never guess the type).
	if _, err := f.WriteString(`{"id":"ev_x","actor":"lead"}` + "\n"); err != nil {
		t.Fatalf("write no-type line: %v", err)
	}
	f.Close()

	if err := l.Append(domain.NewEvent("s", domain.EvAgentStarted, "lead", "a", nil)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	l.Close()

	evs, skipped, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2 (corrupt lines skipped, not fatal)", len(evs))
	}
	if evs[0].Type != domain.EvAgentCreated || evs[1].Type != domain.EvAgentStarted {
		t.Errorf("evs = %+v, want created then started", evs)
	}
}

func TestReadAllMissingFile(t *testing.T) {
	evs, skipped, err := ReadAll(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil {
		t.Fatalf("ReadAll(missing) error = %v, want nil", err)
	}
	if evs != nil || skipped != 0 {
		t.Errorf("ReadAll(missing) = (%v, %d), want (nil, 0)", evs, skipped)
	}
}

func TestTailReturnsLastN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	for i := 0; i < 5; i++ {
		if err := l.Append(domain.NewEvent("s", domain.EvAgentCreated, "lead", string(rune('a'+i)), nil)); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}
	l.Close()

	tail, err := Tail(path, 2)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(tail) != 2 {
		t.Fatalf("got %d events, want 2", len(tail))
	}
	if tail[0].Subject != "d" || tail[1].Subject != "e" {
		t.Errorf("tail subjects = [%s, %s], want [d, e]", tail[0].Subject, tail[1].Subject)
	}

	// n <= 0 or n larger than available: no truncation should be applied
	// beyond what's requested by Tail's "n > 0 && len > n" guard.
	all, err := Tail(path, 0)
	if err != nil {
		t.Fatalf("Tail(0): %v", err)
	}
	if len(all) != 5 {
		t.Errorf("Tail(0) len = %d, want 5 (0 means unlimited)", len(all))
	}

	more, err := Tail(path, 100)
	if err != nil {
		t.Fatalf("Tail(100): %v", err)
	}
	if len(more) != 5 {
		t.Errorf("Tail(100) len = %d, want 5", len(more))
	}
}

func TestAppendAfterCloseErrors(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err = l.Append(domain.NewEvent("s", domain.EvAgentCreated, "lead", "a", nil))
	if err == nil {
		t.Error("Append() after Close error = nil, want error")
	}
}

func TestCloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close (idempotent) error = %v, want nil", err)
	}
}

func TestPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	if l.Path() != path {
		t.Errorf("Path() = %q, want %q", l.Path(), path)
	}
}
