package orchestrator

import (
	"testing"
	"time"

	"github.com/clishakehq/clishake/internal/adapter"
	adaptermock "github.com/clishakehq/clishake/internal/adapter/mock"
	"github.com/clishakehq/clishake/internal/domain"
)

// discAdapter wraps the mock adapter and adds a scriptable
// SubagentDiscoverer implementation.
type discAdapter struct {
	*adaptermock.Adapter
	infos []adapter.SubagentInfo
}

func (d *discAdapter) Name() string { return "disc" }

func (d *discAdapter) DiscoverSubagents(a *domain.Agent) []adapter.SubagentInfo {
	return d.infos
}

func TestDiscoverSubagentsLifecycle(t *testing.T) {
	dir := t.TempDir()
	reg := adapter.NewRegistry()
	da := &discAdapter{Adapter: adaptermock.New()}
	reg.Register(da)
	o, err := Open(dir, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Close)

	parent, err := o.AddAgent(AgentSpec{Name: "claude", Adapter: "disc"})
	if err != nil {
		t.Fatal(err)
	}
	parent.Status = domain.StatusReady // live so discovery runs
	if err := o.Store.SaveAgent(parent); err != nil {
		t.Fatal(err)
	}

	// Round 1: two teammates appear.
	da.infos = []adapter.SubagentInfo{
		{Name: "scout", Role: "general-purpose", Status: domain.StatusRunning},
		{Name: "tester", Role: "code-reviewer", Status: domain.StatusRunning},
	}
	o.discoverSubagents(parent)
	kids := observedChildren(t, o, parent.ID)
	if len(kids) != 2 {
		t.Fatalf("round 1: %d children, want 2", len(kids))
	}
	for _, c := range kids {
		if c.Status != domain.StatusRunning {
			t.Errorf("%s status = %s, want running", c.Name, c.Status)
		}
	}

	// Round 2 (past the throttle): tester left the roster -> completed;
	// scout stays running.
	o.mu.Lock()
	o.discovered[parent.ID] = time.Now().Add(-time.Minute)
	o.mu.Unlock()
	da.infos = da.infos[:1]
	o.discoverSubagents(parent)
	for _, c := range observedChildren(t, o, parent.ID) {
		switch c.Name {
		case "claude/scout":
			if c.Status != domain.StatusRunning {
				t.Errorf("scout should stay running, got %s", c.Status)
			}
		case "claude/tester":
			if c.Status != domain.StatusCompleted {
				t.Errorf("tester should be completed after leaving roster, got %s", c.Status)
			}
		}
	}

	// Throttle: an immediate third round with an empty roster must be a
	// no-op (scout stays running).
	da.infos = nil
	o.discoverSubagents(parent)
	for _, c := range observedChildren(t, o, parent.ID) {
		if c.Name == "claude/scout" && c.Status != domain.StatusRunning {
			t.Fatalf("throttled scan must not reap: scout = %s", c.Status)
		}
	}
}

func observedChildren(t *testing.T, o *Orchestrator, parentID string) []*domain.Agent {
	t.Helper()
	agents, err := o.Store.ListAgents()
	if err != nil {
		t.Fatal(err)
	}
	var out []*domain.Agent
	for _, a := range agents {
		if a.ParentID == parentID && a.Adapter == "observed" {
			out = append(out, a)
		}
	}
	return out
}
