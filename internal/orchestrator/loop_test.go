package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/clishakehq/clishake/internal/adapter"
	adaptermock "github.com/clishakehq/clishake/internal/adapter/mock"
	"github.com/clishakehq/clishake/internal/domain"
)

func nudgeCount(o *Orchestrator, name string) int {
	msgs, _ := o.Store.ListMessagesWith(name, 0)
	n := 0
	for _, m := range msgs {
		if m.Sender == domain.LeadSender && strings.Contains(m.Body, "Team loop") {
			n++
		}
	}
	return n
}

func TestTeamLoop_StartStopAndNudge(t *testing.T) {
	t.Setenv("CLISHAKE_AGENT", "") // supervisor process

	dir := t.TempDir()
	reg := adapter.NewRegistry()
	reg.Register(&flakyAdapter{Adapter: adaptermock.New()}) // InputFile: no tmux needed
	o, err := Open(dir, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Close)

	a := readyRecipient(t, o, "claude")
	a.LastActivity = time.Now().Add(-time.Minute) // long idle → eligible for a nudge
	_ = o.Store.SaveAgent(a)

	if o.Loop().Active {
		t.Fatal("loop should start inactive")
	}

	if err := o.StartLoop("solve the bug"); err != nil {
		t.Fatal(err)
	}
	if ls := o.Loop(); !ls.Active || ls.Goal != "solve the bug" {
		t.Fatalf("loop state = %+v", ls)
	}

	o.driveLoop()
	if nudgeCount(o, "claude") != 1 {
		t.Fatalf("idle agent nudges = %d, want 1", nudgeCount(o, "claude"))
	}

	// Immediately again: rate-limited, no additional nudge.
	o.driveLoop()
	if got := nudgeCount(o, "claude"); got != 1 {
		t.Errorf("nudges after re-run = %d, want still 1 (rate-limited)", got)
	}

	o.StopLoop()
	if o.Loop().Active {
		t.Fatal("loop should be stopped")
	}
	// Stopped loop drives nothing even for an idle agent.
	before := nudgeCount(o, "claude")
	o.driveLoop()
	if got := nudgeCount(o, "claude"); got != before {
		t.Errorf("stopped loop nudged (%d → %d)", before, got)
	}
}

func TestTeamLoop_RecentlyActiveNotNudged(t *testing.T) {
	t.Setenv("CLISHAKE_AGENT", "")

	dir := t.TempDir()
	reg := adapter.NewRegistry()
	reg.Register(&flakyAdapter{Adapter: adaptermock.New()})
	o, err := Open(dir, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Close)

	a := readyRecipient(t, o, "claude")
	a.LastActivity = time.Now() // busy right now
	_ = o.Store.SaveAgent(a)

	if err := o.StartLoop("keep working"); err != nil {
		t.Fatal(err)
	}
	o.driveLoop()
	if got := nudgeCount(o, "claude"); got != 0 {
		t.Errorf("busy agent nudged %d times, want 0", got)
	}
}
