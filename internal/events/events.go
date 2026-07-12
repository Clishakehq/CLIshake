// Package events provides clishake's append-only JSONL event log and a
// small in-process pub/sub bus. The log is the audit trail and recovery
// source; the SQLite store holds materialized current state.
package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/clishakehq/clishake/internal/domain"
)

// Log is an append-only JSONL event log. Safe for concurrent use within one
// process. Cross-process appends are safe on POSIX because writes are
// O_APPEND single write() calls.
type Log struct {
	mu   sync.Mutex
	path string
	f    *os.File
	subs []func(domain.Event)
}

// Open opens (creating if needed) the event log at path.
func Open(path string) (*Log, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open event log: %w", err)
	}
	return &Log{path: path, f: f}, nil
}

// Close closes the underlying file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// Append writes one event as a single JSONL line and notifies subscribers.
func (l *Log) Append(ev domain.Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	l.mu.Lock()
	if l.f == nil {
		l.mu.Unlock()
		return fmt.Errorf("event log closed")
	}
	_, err = l.f.Write(append(b, '\n'))
	subs := make([]func(domain.Event), len(l.subs))
	copy(subs, l.subs)
	l.mu.Unlock()
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	for _, s := range subs {
		s(ev)
	}
	return nil
}

// Subscribe registers a callback invoked (synchronously, after a successful
// append) for every subsequent event. Callbacks must be fast and must not
// call Append.
func (l *Log) Subscribe(fn func(domain.Event)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.subs = append(l.subs, fn)
}

// Path returns the log file path.
func (l *Log) Path() string { return l.path }

// ReadAll parses every event in the file at path. Unparseable lines are
// skipped and counted, never fatal — the log must stay readable even if a
// line was corrupted mid-write.
func ReadAll(path string) (evs []domain.Event, skipped int, err error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev domain.Event
		if json.Unmarshal(line, &ev) != nil || ev.Type == "" {
			skipped++
			continue
		}
		evs = append(evs, ev)
	}
	return evs, skipped, sc.Err()
}

// Tail returns up to n most recent events from the file at path.
func Tail(path string, n int) ([]domain.Event, error) {
	evs, _, err := ReadAll(path)
	if err != nil {
		return nil, err
	}
	if n > 0 && len(evs) > n {
		evs = evs[len(evs)-n:]
	}
	return evs, nil
}
