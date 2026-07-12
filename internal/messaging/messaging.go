// Package messaging parses agent-addressing selectors and routes messages
// to the agents they resolve to, persisting per-recipient copies and
// emitting audit events as it goes.
//
// This package does not depend on internal/state: persistence and event
// logging are expressed as small consumer-side interfaces (MessageStore,
// EventSink) that any concrete store can satisfy.
package messaging

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/clishakehq/clishake/internal/domain"
)

// ---------------------------------------------------------------------------
// Selectors
// ---------------------------------------------------------------------------

// SelectorKind classifies a parsed Selector.
type SelectorKind string

const (
	SelName SelectorKind = "name"
	SelRole SelectorKind = "role"
	SelTeam SelectorKind = "team"
	SelTask SelectorKind = "task"
	SelAll  SelectorKind = "all"
)

// Selector is a parsed addressing expression.
type Selector struct {
	Raw  string       // the trimmed original expression, e.g. "@role:reviewer"
	Kind SelectorKind // name | role | team | all
	Arg  string       // the name/role/team value; empty for all
}

// ParseSelector parses "@all", "@name", "@role:reviewer", "@team:core".
// A bare "@x" is Kind SelName (resolution may fall back to role/team — see
// Resolve). Input without a leading "@" is also accepted as a bare name.
// Errors on empty selector (including "@" alone, or an empty role/team
// value like "@role:").
func ParseSelector(s string) (Selector, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return Selector{}, errors.New("messaging: empty selector")
	}

	body := trimmed
	if after, ok := strings.CutPrefix(body, "@"); ok {
		body = after
	}
	if body == "" {
		return Selector{}, fmt.Errorf("messaging: empty selector %q", trimmed)
	}

	if body == "all" {
		return Selector{Raw: trimmed, Kind: SelAll, Arg: ""}, nil
	}
	if rest, ok := strings.CutPrefix(body, "role:"); ok {
		if rest == "" {
			return Selector{}, fmt.Errorf("messaging: empty role in selector %q", trimmed)
		}
		return Selector{Raw: trimmed, Kind: SelRole, Arg: rest}, nil
	}
	if rest, ok := strings.CutPrefix(body, "team:"); ok {
		if rest == "" {
			return Selector{}, fmt.Errorf("messaging: empty team in selector %q", trimmed)
		}
		return Selector{Raw: trimmed, Kind: SelTeam, Arg: rest}, nil
	}
	if rest, ok := strings.CutPrefix(body, "task:"); ok {
		if rest == "" {
			return Selector{}, fmt.Errorf("messaging: empty task id in selector %q", trimmed)
		}
		return Selector{Raw: trimmed, Kind: SelTask, Arg: rest}, nil
	}

	return Selector{Raw: trimmed, Kind: SelName, Arg: body}, nil
}

// Resolve returns the live agents a selector addresses, in stable order
// (the order they appear in agents).
//
// Resolution order for bare names (Kind SelName): exact agent name match
// wins; if none, all agents with that role; if none, all agents with that
// team. Explicit @role:/@team: forms only match their dimension. SelAll
// matches every agent. Terminal-status agents (status.IsTerminal()) are
// EXCLUDED unless exact-name-addressed.
func Resolve(sel Selector, agents []*domain.Agent) []*domain.Agent {
	switch sel.Kind {
	case SelAll:
		return matchAll(agents)
	case SelRole:
		return matchRole(agents, sel.Arg)
	case SelTeam:
		return matchTeam(agents, sel.Arg)
	case SelName:
		if a := matchExactName(agents, sel.Arg); a != nil {
			return []*domain.Agent{a}
		}
		if out := matchRole(agents, sel.Arg); len(out) > 0 {
			return out
		}
		return matchTeam(agents, sel.Arg)
	default:
		return nil
	}
}

func matchAll(agents []*domain.Agent) []*domain.Agent {
	var out []*domain.Agent
	for _, a := range agents {
		if !a.Status.IsTerminal() {
			out = append(out, a)
		}
	}
	return out
}

func matchExactName(agents []*domain.Agent, name string) *domain.Agent {
	for _, a := range agents {
		if strings.EqualFold(a.Name, name) {
			return a
		}
	}
	return nil
}

func matchRole(agents []*domain.Agent, role string) []*domain.Agent {
	var out []*domain.Agent
	for _, a := range agents {
		if a.Role == role && !a.Status.IsTerminal() {
			out = append(out, a)
		}
	}
	return out
}

func matchTeam(agents []*domain.Agent, team string) []*domain.Agent {
	var out []*domain.Agent
	for _, a := range agents {
		if a.Team == team && !a.Status.IsTerminal() {
			out = append(out, a)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Router
// ---------------------------------------------------------------------------

// MessageStore is what the router needs from persistence.
type MessageStore interface {
	SaveMessage(m *domain.Message) error
}

// EventSink is what the router needs from the event log.
type EventSink interface {
	Append(ev domain.Event) error
}

// Deliverer performs the harness-specific delivery of one message to one
// agent (implemented by the orchestrator via the agent's adapter).
type Deliverer interface {
	Deliver(a *domain.Agent, m domain.Message) error
}

// ErrNoRecipients is returned by Send when the selector matched no agents.
var ErrNoRecipients = errors.New("selector matched no agents")

// SendOpts carries the optional fields of an outgoing message.
type SendOpts struct {
	Type    domain.MessageType // default MsgChat
	TaskID  string
	ReplyTo string
	Meta    map[string]string
}

// Router parses selectors, resolves recipients, persists per-recipient
// message copies, attempts delivery, and records the audit trail.
type Router struct {
	sessionID string
	store     MessageStore
	sink      EventSink
	deliverer Deliverer

	// TaskMembers resolves "@task:<id>" selectors to the names of the
	// task's owner and contributors. Optional: when nil, task selectors
	// match nothing.
	TaskMembers func(taskID string) []string
}

// NewRouter builds a Router bound to one session's store, event sink, and
// deliverer.
func NewRouter(sessionID string, store MessageStore, sink EventSink, d Deliverer) *Router {
	return &Router{sessionID: sessionID, store: store, sink: sink, deliverer: d}
}

// Send parses selector, resolves against agents, and for EACH recipient
// persists a per-recipient copy of the message (a distinct domain.NewID
// ("msg") per copy, sharing Meta["group"] set to a shared group ID),
// attempts delivery, updates Delivery state (delivered/failed), persists
// the final state, and appends message.sent / message.delivered /
// message.failed events (actor = msg.Sender, subject = message ID, payload
// includes recipient + selector).
//
// Returns the per-recipient messages (with final delivery states) and an
// error only for systemic failures (parse failure, no recipients — returns
// ErrNoRecipients, store/sink failure). Individual delivery failures are
// NOT a Send error; they're reflected in the message's Delivery state.
func (r *Router) Send(agents []*domain.Agent, sender, selector, body string, opts SendOpts) ([]*domain.Message, error) {
	sel, err := ParseSelector(selector)
	if err != nil {
		return nil, err
	}

	var recipients []*domain.Agent
	if sel.Kind == SelTask {
		if r.TaskMembers != nil {
			members := map[string]bool{}
			for _, n := range r.TaskMembers(sel.Arg) {
				members[n] = true
			}
			for _, a := range agents {
				if members[a.Name] && !a.Status.IsTerminal() {
					recipients = append(recipients, a)
				}
			}
		}
	} else {
		recipients = Resolve(sel, agents)
	}
	if len(recipients) == 0 {
		return nil, ErrNoRecipients
	}

	msgType := opts.Type
	if msgType == "" {
		msgType = domain.MsgChat
	}

	groupID := domain.NewID("msg")

	out := make([]*domain.Message, 0, len(recipients))
	for _, a := range recipients {
		m := &domain.Message{
			ID:        domain.NewID("msg"),
			Sender:    sender,
			Selector:  sel.Raw,
			Recipient: a.Name,
			Type:      msgType,
			Body:      body,
			TaskID:    opts.TaskID,
			ReplyTo:   opts.ReplyTo,
			Delivery:  domain.DeliveryPending,
			Meta:      mergeMeta(opts.Meta, groupID),
			CreatedAt: time.Now().UTC(),
		}

		derr := r.deliverer.Deliver(a, *m)
		if derr != nil {
			m.Delivery = domain.DeliveryFailed
		} else {
			m.Delivery = domain.DeliveryDelivered
		}

		if err := r.store.SaveMessage(m); err != nil {
			return nil, fmt.Errorf("messaging: save message: %w", err)
		}

		sentPayload := map[string]any{"recipient": a.Name, "selector": selector}
		if err := r.sink.Append(domain.NewEvent(r.sessionID, domain.EvMessageSent, m.Sender, m.ID, sentPayload)); err != nil {
			return nil, fmt.Errorf("messaging: append sent event: %w", err)
		}

		if derr != nil {
			failPayload := map[string]any{"recipient": a.Name, "selector": selector, "error": derr.Error()}
			if err := r.sink.Append(domain.NewEvent(r.sessionID, domain.EvMessageFailed, m.Sender, m.ID, failPayload)); err != nil {
				return nil, fmt.Errorf("messaging: append failed event: %w", err)
			}
		} else {
			deliveredPayload := map[string]any{"recipient": a.Name, "selector": selector}
			if err := r.sink.Append(domain.NewEvent(r.sessionID, domain.EvMessageDelivered, m.Sender, m.ID, deliveredPayload)); err != nil {
				return nil, fmt.Errorf("messaging: append delivered event: %w", err)
			}
		}

		out = append(out, m)
	}

	return out, nil
}

func mergeMeta(opts map[string]string, group string) map[string]string {
	m := make(map[string]string, len(opts)+1)
	for k, v := range opts {
		m[k] = v
	}
	m["group"] = group
	return m
}
