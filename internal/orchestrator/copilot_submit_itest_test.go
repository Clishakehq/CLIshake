package orchestrator_test

// Real-copilot integration test for confirmed message submission. Opt-in
// (CLISHAKE_COPILOT_ITEST=1): needs tmux, git, and an authenticated
// `copilot` on PATH; it launches a real GitHub Copilot CLI process and
// exercises the paste → confirmed-Enter delivery path.
//
// Regression under test: a bracketed paste is ingested asynchronously and a
// single fire-and-forget Enter can be dropped, leaving the message sitting
// unsubmitted in the composer. submitComposer re-sends Enter until the
// composer reacts, so the message must leave the composer.

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
	"github.com/clishakehq/clishake/internal/ansi"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/messaging"
	"github.com/clishakehq/clishake/internal/orchestrator"
	"github.com/clishakehq/clishake/internal/tmux"
)

func TestIntegration_CopilotSubmit_RealCopilot(t *testing.T) {
	if os.Getenv("CLISHAKE_COPILOT_ITEST") != "1" {
		t.Skip("set CLISHAKE_COPILOT_ITEST=1 to run the real-copilot submit test")
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

	socket := fmt.Sprintf("clishake-copilot-itest-%d", os.Getpid())
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })

	reg := adapter.NewRegistry()
	reg.Register(adaptertui.New(adaptertui.Spec{
		Name:            "copilot",
		Command:         "copilot",
		PermissionFlags: map[string]string{"auto": "--allow-all-tools", "full": "--allow-all-tools --allow-all-paths"},
	}))

	o, err := orchestrator.Open(dir, reg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(o.Close)
	o.Cfg.Tmux.Socket = socket
	o.Tmux = tmux.NewClient(socket)

	// auto permissions → --allow-all-tools; the supervisor auto-answers the
	// folder-trust dialog so the agent can reach readiness.
	if _, err := o.AddAgent(orchestrator.AgentSpec{
		Name:    "cop",
		Adapter: "copilot",
		Config:  map[string]string{"permissions": "auto"},
	}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	if _, err := o.StartAgent("cop"); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	t.Cleanup(func() { _ = o.StopAgent("cop", true) })

	if !pollUntil(t, 60*time.Second, func() bool {
		o.Poll()
		a, _ := o.Store.GetAgentByName("cop")
		return a != nil && a.Status == domain.StatusReady
	}) {
		a, _ := o.Store.GetAgentByName("cop")
		screen, _ := o.Tmux.CapturePane(a.Tmux.PaneID, 0)
		t.Fatalf("cop never became ready (status=%v); screen:\n%s", a.Status, screen)
	}

	const marker = "ZQXMARKER7"
	if _, err := o.Send(domain.LeadSender, "@cop", marker+" please acknowledge briefly", messaging.SendOpts{}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// The message must LEAVE the composer: it should appear in the transcript
	// while the active composer line no longer holds it.
	a, _ := o.Store.GetAgentByName("cop")
	if !pollUntil(t, 25*time.Second, func() bool {
		o.Poll()
		screen, _ := o.Tmux.CapturePane(a.Tmux.PaneID, 0)
		return copilotSubmitted(screen, marker)
	}) {
		screen, _ := o.Tmux.CapturePane(a.Tmux.PaneID, 0)
		t.Fatalf("message never left the composer (still unsubmitted); screen:\n%s", ansi.Strip(screen))
	}
}

// copilotSubmitted reports whether marker was entered AND submitted: it must
// be present on screen, but no longer sitting on the active composer line
// (the bottom-most prompt-glyph line).
func copilotSubmitted(screen, marker string) bool {
	plain := ansi.Strip(screen)
	if !strings.Contains(plain, marker) {
		return false // not even typed in yet
	}
	lines := strings.Split(plain, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			continue
		}
		for _, g := range []string{"❯", "›", ">", "┃"} {
			if strings.HasPrefix(t, g) {
				rest := strings.TrimSpace(strings.TrimPrefix(t, g))
				return !strings.Contains(rest, marker) // composer cleared ⇒ submitted
			}
		}
		// status bar / divider — keep scanning upward for the composer line.
	}
	return false
}
