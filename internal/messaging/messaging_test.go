package messaging

import (
	"errors"
	"testing"

	"github.com/clishakehq/clishake/internal/domain"
)

// ---------------------------------------------------------------------------
// ParseSelector
// ---------------------------------------------------------------------------

func TestParseSelector(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantOK  bool
		kind    SelectorKind
		arg     string
		wantRaw string
	}{
		{name: "all", in: "@all", wantOK: true, kind: SelAll, arg: "", wantRaw: "@all"},
		{name: "bare @name", in: "@alice", wantOK: true, kind: SelName, arg: "alice", wantRaw: "@alice"},
		{name: "role", in: "@role:reviewer", wantOK: true, kind: SelRole, arg: "reviewer", wantRaw: "@role:reviewer"},
		{name: "team", in: "@team:core", wantOK: true, kind: SelTeam, arg: "core", wantRaw: "@team:core"},
		{name: "no leading @", in: "alice", wantOK: true, kind: SelName, arg: "alice", wantRaw: "alice"},
		{name: "whitespace trim", in: "  @alice  ", wantOK: true, kind: SelName, arg: "alice", wantRaw: "@alice"},
		{name: "empty", in: "", wantOK: false},
		{name: "whitespace only", in: "   ", wantOK: false},
		{name: "@ alone", in: "@", wantOK: false},
		{name: "empty role", in: "@role:", wantOK: false},
		{name: "empty team", in: "@team:", wantOK: false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sel, err := ParseSelector(c.in)
			if c.wantOK {
				if err != nil {
					t.Fatalf("ParseSelector(%q) unexpected error: %v", c.in, err)
				}
				if sel.Kind != c.kind {
					t.Errorf("Kind = %q, want %q", sel.Kind, c.kind)
				}
				if sel.Arg != c.arg {
					t.Errorf("Arg = %q, want %q", sel.Arg, c.arg)
				}
				if sel.Raw != c.wantRaw {
					t.Errorf("Raw = %q, want %q", sel.Raw, c.wantRaw)
				}
			} else {
				if err == nil {
					t.Fatalf("ParseSelector(%q) expected error, got %+v", c.in, sel)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Resolve
// ---------------------------------------------------------------------------

func agent(name, role, team string, status domain.AgentStatus) *domain.Agent {
	return &domain.Agent{ID: "ag_" + name, Name: name, Role: role, Team: team, Status: status}
}

func names(agents []*domain.Agent) []string {
	out := make([]string, len(agents))
	for i, a := range agents {
		out[i] = a.Name
	}
	return out
}

func sameNames(got []*domain.Agent, want []string) bool {
	gn := names(got)
	if len(gn) != len(want) {
		return false
	}
	for i := range gn {
		if gn[i] != want[i] {
			return false
		}
	}
	return true
}

func TestResolve(t *testing.T) {
	alice := agent("alice", "reviewer", "core", domain.StatusRunning)
	bob := agent("bob", "reviewer", "core", domain.StatusRunning)
	carol := agent("carol", "builder", "core", domain.StatusRunning)
	stoppedNamedRole := agent("role", "role", "other", domain.StatusStopped) // name collides with a role value below
	dave := agent("dave", "role", "infra", domain.StatusRunning)             // role "role" fans out via bare selector "role"
	terminalAgent := agent("zed", "builder", "core", domain.StatusCompleted)

	agents := []*domain.Agent{alice, bob, carol, stoppedNamedRole, dave, terminalAgent}

	t.Run("name precedence over role", func(t *testing.T) {
		sel, err := ParseSelector("@alice")
		if err != nil {
			t.Fatal(err)
		}
		got := Resolve(sel, agents)
		if !sameNames(got, []string{"alice"}) {
			t.Errorf("got %v, want [alice]", names(got))
		}
	})

	t.Run("bare name matches case-insensitively", func(t *testing.T) {
		sel, err := ParseSelector("@ALICE")
		if err != nil {
			t.Fatal(err)
		}
		got := Resolve(sel, agents)
		if !sameNames(got, []string{"alice"}) {
			t.Errorf("got %v, want [alice] (@name must match regardless of case)", names(got))
		}
	})

	t.Run("bare name falls back to role fan-out", func(t *testing.T) {
		sel, err := ParseSelector("@reviewer")
		if err != nil {
			t.Fatal(err)
		}
		got := Resolve(sel, agents)
		if !sameNames(got, []string{"alice", "bob"}) {
			t.Errorf("got %v, want [alice bob]", names(got))
		}
	})

	t.Run("explicit role fan-out", func(t *testing.T) {
		sel, err := ParseSelector("@role:reviewer")
		if err != nil {
			t.Fatal(err)
		}
		got := Resolve(sel, agents)
		if !sameNames(got, []string{"alice", "bob"}) {
			t.Errorf("got %v, want [alice bob]", names(got))
		}
	})

	t.Run("explicit team fan-out", func(t *testing.T) {
		sel, err := ParseSelector("@team:core")
		if err != nil {
			t.Fatal(err)
		}
		got := Resolve(sel, agents)
		// alice, bob, carol are team core and live; terminalAgent (zed) is
		// team core but terminal and excluded; stoppedNamedRole is team
		// "other".
		if !sameNames(got, []string{"alice", "bob", "carol"}) {
			t.Errorf("got %v, want [alice bob carol]", names(got))
		}
	})

	t.Run("@all excludes terminal agents", func(t *testing.T) {
		sel, err := ParseSelector("@all")
		if err != nil {
			t.Fatal(err)
		}
		got := Resolve(sel, agents)
		if !sameNames(got, []string{"alice", "bob", "carol", "dave"}) {
			t.Errorf("got %v, want [alice bob carol dave]", names(got))
		}
	})

	t.Run("exact name match includes stopped agent", func(t *testing.T) {
		sel, err := ParseSelector("@role") // "role" is stoppedNamedRole's exact name
		if err != nil {
			t.Fatal(err)
		}
		got := Resolve(sel, agents)
		if !sameNames(got, []string{"role"}) {
			t.Errorf("got %v, want [role] (exact name match should include terminal agent)", names(got))
		}
	})

	t.Run("no match returns empty", func(t *testing.T) {
		sel, err := ParseSelector("@nobody")
		if err != nil {
			t.Fatal(err)
		}
		got := Resolve(sel, agents)
		if len(got) != 0 {
			t.Errorf("got %v, want empty", names(got))
		}
	})
}

// ---------------------------------------------------------------------------
// Router.Send
// ---------------------------------------------------------------------------

type fakeStore struct {
	saved []*domain.Message
	err   error
}

func (f *fakeStore) SaveMessage(m *domain.Message) error {
	if f.err != nil {
		return f.err
	}
	cp := *m
	f.saved = append(f.saved, &cp)
	return nil
}

type fakeSink struct {
	events []domain.Event
	err    error
}

func (f *fakeSink) Append(ev domain.Event) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, ev)
	return nil
}

type fakeDeliverer struct {
	fail  map[string]bool
	calls []string
}

func (f *fakeDeliverer) Deliver(a *domain.Agent, m domain.Message) error {
	f.calls = append(f.calls, a.Name)
	if f.fail[a.Name] {
		return errors.New("simulated delivery failure")
	}
	return nil
}

func TestRouterSendHappyPath(t *testing.T) {
	alice := agent("alice", "reviewer", "core", domain.StatusRunning)
	bob := agent("bob", "reviewer", "core", domain.StatusRunning)
	agents := []*domain.Agent{alice, bob}

	store := &fakeStore{}
	sink := &fakeSink{}
	deliverer := &fakeDeliverer{fail: map[string]bool{}}

	r := NewRouter("sess1", store, sink, deliverer)
	msgs, err := r.Send(agents, "lead", "@role:reviewer", "hello team", SendOpts{})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if len(store.saved) != 2 {
		t.Fatalf("got %d saved messages, want 2", len(store.saved))
	}

	wantRecipients := map[string]bool{"alice": false, "bob": false}
	groupIDs := map[string]bool{}
	for i, m := range msgs {
		if m.Delivery != domain.DeliveryDelivered {
			t.Errorf("message %d delivery = %q, want delivered", i, m.Delivery)
		}
		if _, ok := wantRecipients[m.Recipient]; !ok {
			t.Errorf("unexpected recipient %q", m.Recipient)
		}
		wantRecipients[m.Recipient] = true
		if m.Sender != "lead" {
			t.Errorf("Sender = %q, want lead", m.Sender)
		}
		if m.Selector != "@role:reviewer" {
			t.Errorf("Selector = %q, want @role:reviewer", m.Selector)
		}
		if m.Body != "hello team" {
			t.Errorf("Body = %q", m.Body)
		}
		if m.Type != domain.MsgChat {
			t.Errorf("Type = %q, want chat (default)", m.Type)
		}
		grp := m.Meta["group"]
		if grp == "" {
			t.Errorf("message %d missing Meta[group]", i)
		}
		groupIDs[grp] = true
		// each per-recipient message must have a distinct ID
		for j, other := range msgs {
			if i != j && other.ID == m.ID {
				t.Errorf("duplicate message ID %q", m.ID)
			}
		}
	}
	for name, seen := range wantRecipients {
		if !seen {
			t.Errorf("recipient %q did not get a message", name)
		}
	}
	if len(groupIDs) != 1 {
		t.Errorf("expected all messages to share one group id, got %d distinct: %v", len(groupIDs), groupIDs)
	}

	// Events: for each recipient, message.sent then message.delivered, in order.
	if len(sink.events) != 4 {
		t.Fatalf("got %d events, want 4", len(sink.events))
	}
	wantTypes := []domain.EventType{domain.EvMessageSent, domain.EvMessageDelivered, domain.EvMessageSent, domain.EvMessageDelivered}
	for i, ev := range sink.events {
		if ev.Type != wantTypes[i] {
			t.Errorf("event[%d].Type = %q, want %q", i, ev.Type, wantTypes[i])
		}
		if ev.Actor != "lead" {
			t.Errorf("event[%d].Actor = %q, want lead", i, ev.Actor)
		}
		if ev.SessionID != "sess1" {
			t.Errorf("event[%d].SessionID = %q, want sess1", i, ev.SessionID)
		}
		if ev.Payload["selector"] != "@role:reviewer" {
			t.Errorf("event[%d].Payload[selector] = %v", i, ev.Payload["selector"])
		}
		if ev.Payload["recipient"] == "" {
			t.Errorf("event[%d].Payload[recipient] missing", i)
		}
	}
	// message.sent and message.delivered for the same recipient share a subject (message ID).
	if sink.events[0].Subject != sink.events[1].Subject {
		t.Errorf("sent/delivered subject mismatch for first recipient: %q vs %q", sink.events[0].Subject, sink.events[1].Subject)
	}
	if sink.events[2].Subject != sink.events[3].Subject {
		t.Errorf("sent/delivered subject mismatch for second recipient: %q vs %q", sink.events[2].Subject, sink.events[3].Subject)
	}
}

func TestRouterSendDeliveryFailure(t *testing.T) {
	alice := agent("alice", "reviewer", "core", domain.StatusRunning)
	agents := []*domain.Agent{alice}

	store := &fakeStore{}
	sink := &fakeSink{}
	deliverer := &fakeDeliverer{fail: map[string]bool{"alice": true}}

	r := NewRouter("sess1", store, sink, deliverer)
	msgs, err := r.Send(agents, "lead", "@alice", "hi", SendOpts{})
	if err != nil {
		t.Fatalf("Send should not error on individual delivery failure, got: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Delivery != domain.DeliveryFailed {
		t.Errorf("Delivery = %q, want failed", msgs[0].Delivery)
	}
	if len(store.saved) != 1 || store.saved[0].Delivery != domain.DeliveryFailed {
		t.Errorf("persisted message delivery state incorrect: %+v", store.saved)
	}

	foundFailed := false
	for _, ev := range sink.events {
		if ev.Type == domain.EvMessageFailed {
			foundFailed = true
			if ev.Payload["error"] == "" {
				t.Errorf("message.failed event missing error payload")
			}
		}
		if ev.Type == domain.EvMessageDelivered {
			t.Errorf("should not emit message.delivered on failure")
		}
	}
	if !foundFailed {
		t.Errorf("expected message.failed event")
	}
}

func TestRouterSendNoRecipients(t *testing.T) {
	agents := []*domain.Agent{agent("alice", "reviewer", "core", domain.StatusRunning)}
	store := &fakeStore{}
	sink := &fakeSink{}
	deliverer := &fakeDeliverer{fail: map[string]bool{}}

	r := NewRouter("sess1", store, sink, deliverer)
	_, err := r.Send(agents, "lead", "@nobody", "hi", SendOpts{})
	if !errors.Is(err, ErrNoRecipients) {
		t.Fatalf("err = %v, want ErrNoRecipients", err)
	}
	if len(store.saved) != 0 {
		t.Errorf("expected no messages saved")
	}
}

func TestRouterSendParseFailure(t *testing.T) {
	agents := []*domain.Agent{agent("alice", "reviewer", "core", domain.StatusRunning)}
	store := &fakeStore{}
	sink := &fakeSink{}
	deliverer := &fakeDeliverer{fail: map[string]bool{}}

	r := NewRouter("sess1", store, sink, deliverer)
	_, err := r.Send(agents, "lead", "", "hi", SendOpts{})
	if err == nil {
		t.Fatalf("expected error for empty selector")
	}
	if errors.Is(err, ErrNoRecipients) {
		t.Errorf("empty selector should be a parse error, not ErrNoRecipients")
	}
}

func TestRouterSendStoreErrorPropagates(t *testing.T) {
	agents := []*domain.Agent{agent("alice", "reviewer", "core", domain.StatusRunning)}
	store := &fakeStore{err: errors.New("disk full")}
	sink := &fakeSink{}
	deliverer := &fakeDeliverer{fail: map[string]bool{}}

	r := NewRouter("sess1", store, sink, deliverer)
	_, err := r.Send(agents, "lead", "@alice", "hi", SendOpts{})
	if err == nil {
		t.Fatalf("expected store error to propagate")
	}
}

func TestRouterSendTaskSelector(t *testing.T) {
	agents := []*domain.Agent{
		{ID: "1", Name: "claude", Status: domain.StatusReady},
		{ID: "2", Name: "codex", Status: domain.StatusReady},
		{ID: "3", Name: "opencode", Status: domain.StatusStopped}, // terminal: excluded
	}
	store := &fakeStore{}
	r := NewRouter("s1", store, &fakeSink{}, &fakeDeliverer{})
	r.TaskMembers = func(taskID string) []string {
		if taskID == "task_x" {
			return []string{"claude", "opencode", "ghost"}
		}
		return nil
	}
	msgs, err := r.Send(agents, "lead", "@task:task_x", "sync up", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Recipient != "claude" {
		t.Fatalf("task selector should hit only live members: %+v", msgs)
	}
	// Unknown task or nil resolver -> no recipients.
	if _, err := r.Send(agents, "lead", "@task:nope", "x", SendOpts{}); err != ErrNoRecipients {
		t.Fatalf("unknown task should be ErrNoRecipients, got %v", err)
	}
	r.TaskMembers = nil
	if _, err := r.Send(agents, "lead", "@task:task_x", "x", SendOpts{}); err != ErrNoRecipients {
		t.Fatalf("nil resolver should be ErrNoRecipients, got %v", err)
	}
}
