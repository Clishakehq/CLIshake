package orchestrator

import (
	"fmt"
	"testing"

	"github.com/clishakehq/clishake/internal/adapter"
	adaptermock "github.com/clishakehq/clishake/internal/adapter/mock"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/messaging"
)

// flakyAdapter wraps the mock adapter but fails FormatInput until armed,
// simulating an agent-side send that could not reach the recipient's pane.
type flakyAdapter struct {
	*adaptermock.Adapter
	fail bool
}

func (f *flakyAdapter) Name() string { return "flaky" }
func (f *flakyAdapter) InputMode() adapter.InputMode {
	return adapter.InputFile // deliver via inbox file so no real tmux is needed
}
func (f *flakyAdapter) FormatInput(a *domain.Agent, m domain.Message) (string, error) {
	if f.fail {
		return "", fmt.Errorf("simulated sandbox: cannot reach recipient")
	}
	return f.Adapter.FormatInput(a, m)
}

func TestRelayFailedDeliveries(t *testing.T) {
	dir := t.TempDir()
	reg := adapter.NewRegistry()
	fa := &flakyAdapter{Adapter: adaptermock.New(), fail: true}
	reg.Register(fa)
	o, err := Open(dir, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Close)

	recipient, err := o.AddAgent(AgentSpec{Name: "claude", Adapter: "flaky"})
	if err != nil {
		t.Fatal(err)
	}
	recipient.Status = domain.StatusReady
	_ = o.Store.SaveAgent(recipient)

	// A send that fails delivery (as an agent-side send would from a sandbox).
	msgs, err := o.Send("codex", "@claude", "APPROVED", messaging.SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Delivery != domain.DeliveryFailed {
		t.Fatalf("expected one failed delivery, got %+v", msgs)
	}

	// Supervisor can reach the recipient now: relay should deliver it.
	fa.fail = false
	o.relayFailedDeliveries()

	got, _ := o.Store.ListMessagesByDelivery(domain.DeliveryDelivered, 0)
	found := false
	for _, m := range got {
		if m.Recipient == "claude" && m.Body == "APPROVED" {
			found = true
		}
	}
	if !found {
		t.Fatal("relay did not re-deliver the failed message")
	}

	// A permanently-failing message stops after relayMax attempts.
	fa.fail = true
	if _, err := o.Send("codex", "@claude", "never lands", messaging.SendOpts{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < relayMax+2; i++ {
		o.relayFailedDeliveries()
	}
	stillFailed, _ := o.Store.ListMessagesByDelivery(domain.DeliveryFailed, 0)
	var attempts string
	for _, m := range stillFailed {
		if m.Body == "never lands" {
			attempts = m.Meta[relayKey]
		}
	}
	if attempts == "" {
		t.Fatal("expected relay attempts to be recorded and capped")
	}
}
