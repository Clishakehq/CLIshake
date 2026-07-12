package orchestrator_test

// Real-claude integration test for auto-answering the folder-trust dialog.
// Opt-in (CLISHAKE_TRUST_ITEST=1): needs tmux, git, and an authenticated
// `claude` on PATH; it launches a real Claude Code process.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/clishakehq/clishake/internal/adapter"
	adapterclaude "github.com/clishakehq/clishake/internal/adapter/claudecode"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/orchestrator"
	"github.com/clishakehq/clishake/internal/tmux"
)

func TestIntegration_TrustAutoAnswer_RealClaude(t *testing.T) {
	if os.Getenv("CLISHAKE_TRUST_ITEST") != "1" {
		t.Skip("set CLISHAKE_TRUST_ITEST=1 to run the real-claude trust auto-answer test")
	}
	for _, bin := range []string{"tmux", "git", "claude"} {
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

	socket := fmt.Sprintf("clishake-trust-itest-%d", os.Getpid())
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })

	reg := adapter.NewRegistry()
	reg.Register(adapterclaude.New())

	o, err := orchestrator.Open(dir, reg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(o.Close)
	o.Cfg.Tmux.Socket = socket
	o.Tmux = tmux.NewClient(socket)

	// Default permissions: the ONLY startup gate is the folder-trust dialog,
	// so reaching "ready" isolates the auto-answer under test. (--permissions
	// full adds a second "Bypass Permissions" warning that is intentionally
	// NOT auto-answered.)
	if _, err := o.AddAgent(orchestrator.AgentSpec{Name: "cl", Adapter: "claude-code"}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	if _, err := o.StartAgent("cl"); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	t.Cleanup(func() { _ = o.StopAgent("cl", true) })

	// Claude blocks at the trust dialog; reaching "ready" requires the
	// supervisor to have auto-answered it.
	if !pollUntil(t, 60*time.Second, func() bool {
		o.Poll()
		a, _ := o.Store.GetAgentByName("cl")
		return a != nil && a.Status == domain.StatusReady
	}) {
		a, _ := o.Store.GetAgentByName("cl")
		screen, _ := o.Tmux.CapturePane(a.Tmux.PaneID, 0)
		t.Fatalf("cl never became ready (status=%v); screen:\n%s", a.Status, screen)
	}
	if a, _ := o.Store.GetAgentByName("cl"); a.Config["_trust_answered"] != "1" {
		t.Errorf("expected _trust_answered=1, got config %v", a.Config)
	}
}
