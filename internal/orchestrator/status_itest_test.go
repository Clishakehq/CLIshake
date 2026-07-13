package orchestrator_test

// Real-copilot integration test for live model/usage reporting. Opt-in
// (CLISHAKE_STATUS_ITEST=1): needs tmux, git, and an authenticated `copilot`.
// It confirms the supervisor reads the model (and usage, when shown) from the
// harness status line and records it on the agent.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clishakehq/clishake/internal/adapter"
	adaptertui "github.com/clishakehq/clishake/internal/adapter/tui"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/orchestrator"
	"github.com/clishakehq/clishake/internal/tmux"
)

func TestIntegration_LiveStatus_RealCopilot(t *testing.T) {
	if os.Getenv("CLISHAKE_STATUS_ITEST") != "1" {
		t.Skip("set CLISHAKE_STATUS_ITEST=1 to run the real-copilot live-status test")
	}
	for _, bin := range []string{"tmux", "git", "copilot"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found in PATH", bin)
		}
	}

	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "itest@example.com")
	runGit(t, dir, "config", "user.name", "clishake itest")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# t\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-q", "-m", "init")

	socket := fmt.Sprintf("clishake-status-itest-%d", os.Getpid())
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })

	reg := adapter.NewRegistry()
	reg.Register(adaptertui.New(adaptertui.Spec{
		Name:               "copilot",
		Command:            "copilot",
		PermissionFlags:    map[string]string{"auto": "--allow-all-tools"},
		StatusModelPattern: `→\s+(\S+)`,
		StatusUsagePattern: `Session:\s+([\d.]+\s*AIC)`,
	}))

	o, err := orchestrator.Open(dir, reg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(o.Close)
	o.Cfg.Tmux.Socket = socket
	o.Tmux = tmux.NewClient(socket)

	if _, err := o.AddAgent(orchestrator.AgentSpec{
		Name: "cop", Adapter: "copilot", Config: map[string]string{"permissions": "auto"},
	}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	if _, err := o.StartAgent("cop"); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	t.Cleanup(func() { _ = o.StopAgent("cop", true) })

	// Poll until the supervisor records a live model (past the statusEvery
	// throttle). Copilot's default is a gpt-* model.
	if !pollUntil(t, 30*time.Second, func() bool {
		o.Poll()
		a, _ := o.Store.GetAgentByName("cop")
		return a != nil && a.Config[domain.ConfigLiveModel] != ""
	}) {
		a, _ := o.Store.GetAgentByName("cop")
		screen, _ := o.Tmux.CapturePane(a.Tmux.PaneID, 0)
		t.Fatalf("no live model recorded; config=%v screen:\n%s", a.Config, screen)
	}
	a, _ := o.Store.GetAgentByName("cop")
	if m := a.LiveModel(); !strings.Contains(m, "-") {
		t.Errorf("live model = %q, expected a model id (e.g. gpt-5-mini, claude-haiku-4.5)", m)
	}
	t.Logf("live model=%q usage=%q", a.LiveModel(), a.Usage())
}
