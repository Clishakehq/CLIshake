package orchestrator_test

// Real-tmux, real-process integration test. Unlike orchestrator_test.go
// (which fakes tmux entirely), this test drives an actual tmux server and a
// real mock-agent subprocess end to end. It is opt-in (CLISHAKE_TMUX_ITEST=1)
// since it needs tmux on PATH, spawns real processes, and is slower/less
// hermetic than the fake-runner suite.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/clishakehq/clishake/internal/adapter"
	"github.com/clishakehq/clishake/internal/adapter/mock"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/messaging"
	"github.com/clishakehq/clishake/internal/orchestrator"
	"github.com/clishakehq/clishake/internal/tmux"
)

func TestIntegration_RealTmux_StartSendStop(t *testing.T) {
	if os.Getenv("CLISHAKE_TMUX_ITEST") != "1" {
		t.Skip("set CLISHAKE_TMUX_ITEST=1 to run the real-tmux integration test")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found in PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "itest@example.com")
	runGit(t, dir, "config", "user.name", "clishake itest")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# itest\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-q", "-m", "initial commit")

	// os.Executable() inside `go test` resolves to the test binary, which
	// has no "mock-agent" subcommand. Build a real clishake binary and
	// point the mock adapter at it via the test-only export.
	binPath := filepath.Join(t.TempDir(), "clishake")
	build := exec.Command("go", "build", "-o", binPath, "github.com/clishakehq/clishake/cmd/clishake")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build clishake binary: %v\n%s", err, out)
	}
	restore := mock.SetSelfExeForTesting(binPath)
	t.Cleanup(restore)

	socket := fmt.Sprintf("clishake-itest-orch-%d", os.Getpid())
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})

	reg := adapter.NewRegistry()
	reg.Register(mock.New())

	o, err := orchestrator.Open(dir, reg)
	if err != nil {
		t.Fatalf("orchestrator.Open: %v", err)
	}
	t.Cleanup(o.Close)
	o.Cfg.Tmux.Socket = socket
	o.Tmux = tmux.NewClient(socket)

	if _, err := o.AddAgent(orchestrator.AgentSpec{Name: "builder", Role: "builder", Adapter: "mock"}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	if _, err := o.StartAgent("builder"); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	if !pollUntil(t, 15*time.Second, func() bool {
		o.Poll()
		a, err := o.Store.GetAgentByName("builder")
		if err != nil || a == nil {
			return false
		}
		switch a.Status {
		case domain.StatusReady, domain.StatusWaiting, domain.StatusRunning:
			return true
		}
		return false
	}) {
		a, _ := o.Store.GetAgentByName("builder")
		t.Fatalf("timed out waiting for builder to become ready; last status = %v", a)
	}

	if _, err := o.Send(domain.LeadSender, "@builder", "hello there", messaging.SendOpts{}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if !pollUntil(t, 15*time.Second, func() bool {
		o.Poll()
		msgs, err := o.Store.ListMessagesWith("builder", 0)
		if err != nil {
			t.Fatalf("ListMessagesWith: %v", err)
		}
		for _, m := range msgs {
			if m.Sender == "builder" && m.Recipient == domain.LeadSender {
				return true
			}
		}
		return false
	}) {
		t.Fatal("timed out waiting for a reply message from builder")
	}

	if err := o.StopAgent("builder", true); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}

	stopped, err := o.Store.GetAgentByName("builder")
	if err != nil || stopped == nil {
		t.Fatalf("GetAgentByName: err=%v agent=%v", err, stopped)
	}
	if stopped.Status != domain.StatusStopped {
		t.Errorf("Status after StopAgent = %q, want stopped", stopped.Status)
	}
}

// pollUntil calls cond every 200ms until it returns true or timeout elapses,
// returning whether cond ever succeeded.
func pollUntil(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
