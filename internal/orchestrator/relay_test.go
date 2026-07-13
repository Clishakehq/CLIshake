package orchestrator

import (
	"fmt"
	"testing"
	"time"

	"github.com/clishakehq/clishake/internal/adapter"
	adaptermock "github.com/clishakehq/clishake/internal/adapter/mock"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/messaging"
)

// flakyAdapter wraps the mock adapter. With mode InputFile it delivers via an
// inbox file (no real tmux needed); FormatInput fails while armed, simulating a
// delivery that could not reach the recipient. With mode InputSendKeys it lets
// deliver() reach the "am I an agent process?" queue decision.
type flakyAdapter struct {
	*adaptermock.Adapter
	mode adapter.InputMode
	fail bool
}

func (f *flakyAdapter) Name() string { return "flaky" }
func (f *flakyAdapter) InputMode() adapter.InputMode {
	if f.mode != "" {
		return f.mode
	}
	return adapter.InputFile
}
func (f *flakyAdapter) FormatInput(a *domain.Agent, m domain.Message) (string, error) {
	if f.fail {
		return "", fmt.Errorf("simulated sandbox: cannot reach recipient")
	}
	return f.Adapter.FormatInput(a, m)
}

func readyRecipient(t *testing.T, o *Orchestrator, name string) *domain.Agent {
	t.Helper()
	a, err := o.AddAgent(AgentSpec{Name: name, Adapter: "flaky"})
	if err != nil {
		t.Fatal(err)
	}
	a.Status = domain.StatusReady
	_ = o.Store.SaveAgent(a)
	return a
}

// deliverQueued must deliver messages left in either DeliveryFailed (a direct
// attempt that failed) or DeliveryPending (queued by a sandboxed agent) — and
// keep retrying rather than dropping them after a fixed number of attempts.
func TestDeliverQueued_DeliversFailedAndPending(t *testing.T) {
	t.Setenv("CLISHAKE_AGENT", "") // this test plays the supervisor process

	dir := t.TempDir()
	reg := adapter.NewRegistry()
	fa := &flakyAdapter{Adapter: adaptermock.New(), fail: true}
	reg.Register(fa)
	o, err := Open(dir, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Close)
	readyRecipient(t, o, "claude")

	// A direct send whose delivery fails, as a relayed legacy message would.
	msgs, err := o.Send("codex", "@claude", "APPROVED", messaging.SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Delivery != domain.DeliveryFailed {
		t.Fatalf("expected one failed delivery, got %+v", msgs)
	}

	// Recipient reachable now: repeated ticks must eventually deliver it, and
	// never drop it in the meantime.
	fa.fail = false
	for i := 0; i < 5; i++ {
		o.DeliverQueued()
	}
	if !delivered(o, "claude", "APPROVED") {
		t.Fatal("deliverQueued did not deliver the failed message")
	}

	// A message sitting in DeliveryPending (as a sandboxed agent would queue
	// it) is delivered too.
	pending := &domain.Message{
		ID: domain.NewID("msg"), Sender: "codex", Recipient: "claude",
		Type: domain.MsgChat, Body: "QUEUED", Delivery: domain.DeliveryPending,
		CreatedAt: time.Now().UTC(),
	}
	if err := o.Store.SaveMessage(pending); err != nil {
		t.Fatal(err)
	}
	o.DeliverQueued()
	if !delivered(o, "claude", "QUEUED") {
		t.Fatal("deliverQueued did not deliver the pending (queued) message")
	}
}

// A send from an agent's own process (CLISHAKE_AGENT set) to a send-keys
// recipient must be QUEUED, not attempted — the sandbox can't reach the pane.
func TestDeliverQueued_AgentSendIsQueuedNotAttempted(t *testing.T) {
	t.Setenv("CLISHAKE_AGENT", "codex") // this Send runs inside an agent

	dir := t.TempDir()
	reg := adapter.NewRegistry()
	fa := &flakyAdapter{Adapter: adaptermock.New(), mode: adapter.InputSendKeys}
	reg.Register(fa)
	o, err := Open(dir, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Close)
	readyRecipient(t, o, "claude")

	msgs, err := o.Send("codex", "@claude", "PEER HELLO", messaging.SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Delivery != domain.DeliveryPending {
		t.Fatalf("expected the agent send to be queued (pending), got %+v", msgs)
	}

	// The agent process must NOT deliver from its own Poll — it can't reach
	// the terminals. deliverQueued is a no-op here.
	o.DeliverQueued()
	if delivered(o, "claude", "PEER HELLO") {
		t.Fatal("agent process delivered a queued message it could not reach")
	}
}

func delivered(o *Orchestrator, recipient, body string) bool {
	got, _ := o.Store.ListMessagesByDelivery(domain.DeliveryDelivered, 0)
	for _, m := range got {
		if m.Recipient == recipient && m.Body == body {
			return true
		}
	}
	return false
}
