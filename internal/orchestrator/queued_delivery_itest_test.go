package orchestrator_test

// Real-claude integration test for supervisor delivery of a queued peer
// message. Opt-in (CLISHAKE_QUEUE_ITEST=1): needs tmux, git, and an
// authenticated `claude` on PATH.
//
// This exercises the half of Codex→Claude delivery that used to be flaky: a
// message queued by a sandboxed agent (persisted DeliveryPending) must be
// delivered to the recipient's real pane by the supervisor loop — the single
// process that owns the terminals — and marked delivered.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clishakehq/clishake/internal/adapter"
	adapterclaude "github.com/clishakehq/clishake/internal/adapter/claudecode"
	"github.com/clishakehq/clishake/internal/ansi"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/orchestrator"
	"github.com/clishakehq/clishake/internal/tmux"
)

func TestIntegration_QueuedDelivery_RealClaude(t *testing.T) {
	if os.Getenv("CLISHAKE_QUEUE_ITEST") != "1" {
		t.Skip("set CLISHAKE_QUEUE_ITEST=1 to run the real-claude queued-delivery test")
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

	socket := fmt.Sprintf("clishake-queue-itest-%d", os.Getpid())
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

	if _, err := o.AddAgent(orchestrator.AgentSpec{Name: "claude", Adapter: "claude-code"}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	if _, err := o.StartAgent("claude"); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	t.Cleanup(func() { _ = o.StopAgent("claude", true) })

	if !pollUntil(t, 60*time.Second, func() bool {
		o.Poll()
		a, _ := o.Store.GetAgentByName("claude")
		return a != nil && a.Status == domain.StatusReady
	}) {
		a, _ := o.Store.GetAgentByName("claude")
		t.Fatalf("claude never became ready (status=%v)", a.Status)
	}

	// Simulate what a sandboxed agent's `clishake send` records: a peer message
	// left in the queue (DeliveryPending) for the supervisor to deliver.
	const marker = "QUEUEDMARKER42"
	queued := &domain.Message{
		ID: domain.NewID("msg"), Sender: "codex", Recipient: "claude",
		Type: domain.MsgChat, Body: marker + " reply with ok",
		Delivery: domain.DeliveryPending, CreatedAt: time.Now().UTC(),
	}
	if err := o.Store.SaveMessage(queued); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}

	msgDelivered := func() bool {
		got, _ := o.Store.ListMessagesByDelivery(domain.DeliveryDelivered, 0)
		for _, m := range got {
			if m.ID == queued.ID {
				return true
			}
		}
		return false
	}

	// The supervisor loop must deliver it and mark it delivered.
	if !pollUntil(t, 25*time.Second, func() bool {
		o.Poll()
		return msgDelivered()
	}) {
		a, _ := o.Store.GetAgentByName("claude")
		screen, _ := o.Tmux.CapturePane(a.Tmux.PaneID, 0)
		t.Fatalf("queued message never delivered; claude screen:\n%s", ansi.Strip(screen))
	}

	// And it must actually reach Claude's pane, not just flip a DB flag.
	a, _ := o.Store.GetAgentByName("claude")
	if !pollUntil(t, 10*time.Second, func() bool {
		screen, _ := o.Tmux.CapturePane(a.Tmux.PaneID, 0)
		return strings.Contains(ansi.Strip(screen), marker)
	}) {
		screen, _ := o.Tmux.CapturePane(a.Tmux.PaneID, 0)
		t.Fatalf("marker %q not visible in claude's pane:\n%s", marker, ansi.Strip(screen))
	}
}
