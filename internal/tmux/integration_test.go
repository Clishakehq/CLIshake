package tmux

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestIntegration_RealTmux exercises Client against a real tmux binary on a
// disposable, dedicated socket. It is skipped unless tmux is on PATH and
// CLISHAKE_TMUX_ITEST=1 is set, so normal `go test ./...` runs never spawn
// a real tmux server.
func TestIntegration_RealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found in PATH")
	}
	if os.Getenv("CLISHAKE_TMUX_ITEST") != "1" {
		t.Skip("set CLISHAKE_TMUX_ITEST=1 to run the real-tmux integration test")
	}

	socket := "clishake-itest-" + strconv.Itoa(os.Getpid())
	t.Cleanup(func() {
		// Belt-and-suspenders cleanup: kill the whole dedicated server on
		// this socket directly (bypassing Client, which has no kill-server
		// method by design), regardless of where the test failed.
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})

	c := NewClient(socket)
	dir := t.TempDir()
	session := "itest-session"

	if err := c.NewSession(session, dir); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if !c.HasSession(session) {
		t.Fatalf("HasSession(%q) = false right after NewSession", session)
	}
	if !c.ServerAlive() {
		t.Fatalf("ServerAlive() = false with a live session")
	}

	// Window 1: a long-running process we can find via ListPanes with a
	// live PID.
	paneID, err := c.NewWindow(session, "worker", dir, []string{"sh", "-c", "echo hello; sleep 30"})
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	if !strings.HasPrefix(paneID, "%") {
		t.Fatalf("NewWindow: pane id %q does not look like a tmux pane id", paneID)
	}

	var found *PaneInfo
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		panes, err := c.ListPanes(session)
		if err != nil {
			t.Fatalf("ListPanes: %v", err)
		}
		found = nil
		for i := range panes {
			if panes[i].PaneID == paneID {
				found = &panes[i]
				break
			}
		}
		if found != nil && found.PanePID > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if found == nil {
		t.Fatalf("ListPanes: pane %s not found in session %s", paneID, session)
	}
	if found.Dead {
		t.Fatalf("pane %s reported dead while its sleep should still be running", paneID)
	}
	if found.PanePID <= 0 {
		t.Fatalf("pane %s reported non-positive pid %d", paneID, found.PanePID)
	}
	if found.SessionName != session || found.WindowName != "worker" {
		t.Fatalf("pane info mismatch: %+v", *found)
	}

	// Window 2: SendText/CapturePane round trip against `cat`, which echoes
	// whatever it reads on stdin back to the pane.
	echoPaneID, err := c.NewWindow(session, "echo", dir, []string{"cat"})
	if err != nil {
		t.Fatalf("NewWindow (cat): %v", err)
	}
	const marker = "clishake-roundtrip-hello"
	if err := c.SendText(echoPaneID, marker); err != nil {
		t.Fatalf("SendText: %v", err)
	}

	var captured string
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out, err := c.CapturePane(echoPaneID, 0)
		if err != nil {
			t.Fatalf("CapturePane: %v", err)
		}
		captured = out
		if strings.Contains(captured, marker) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(captured, marker) {
		t.Fatalf("CapturePane output does not contain sent text %q; got:\n%s", marker, captured)
	}

	if err := c.KillSession(session); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	if c.HasSession(session) {
		t.Fatalf("HasSession(%q) = true after KillSession", session)
	}
}
