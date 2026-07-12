package tmux

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// scriptedRunner is a fake Runner for unit tests: each call to Run pops the
// next queued response and records the args it was invoked with, so tests
// can assert on exact command construction.
type scriptedRunner struct {
	calls     [][]string
	responses []response
	next      int
}

type response struct {
	out string
	err error
}

func (r *scriptedRunner) Run(args ...string) (string, error) {
	got := append([]string{}, args...)
	r.calls = append(r.calls, got)
	if r.next >= len(r.responses) {
		return "", nil
	}
	res := r.responses[r.next]
	r.next++
	return res.out, res.err
}

func (r *scriptedRunner) queue(out string, err error) {
	r.responses = append(r.responses, response{out: out, err: err})
}

func assertCall(t *testing.T, r *scriptedRunner, idx int, want []string) {
	t.Helper()
	if idx >= len(r.calls) {
		t.Fatalf("call %d not recorded; only %d calls made: %#v", idx, len(r.calls), r.calls)
	}
	got := r.calls[idx]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("call %d args mismatch:\n got:  %#v\n want: %#v", idx, got, want)
	}
}

func TestClient_Socket(t *testing.T) {
	c := NewClientWithRunner("mysock", &scriptedRunner{})
	if got := c.Socket(); got != "mysock" {
		t.Fatalf("Socket() = %q, want %q", got, "mysock")
	}
}

func TestClient_Available(t *testing.T) {
	c := NewClientWithRunner("sock", &scriptedRunner{})
	_, lookErr := exec.LookPath("tmux")
	want := lookErr == nil
	if got := c.Available(); got != want {
		t.Fatalf("Available() = %v, want %v", got, want)
	}
}

func TestClient_NewSession(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("", nil)
	c := NewClientWithRunner("sock", r)

	if err := c.NewSession("myagent", "/work/dir"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	assertCall(t, r, 0, []string{"-L", "sock", "new-session", "-d", "-s", "myagent", "-c", "/work/dir"})
}

func TestClient_KillSession(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("", nil)
	c := NewClientWithRunner("sock", r)

	if err := c.KillSession("myagent"); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	assertCall(t, r, 0, []string{"-L", "sock", "kill-session", "-t", "myagent"})
}

func TestClient_ListSessions(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("clishake-a\nclishake-b\n", nil)
	c := NewClientWithRunner("sock", r)

	got, err := c.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	want := []string{"clishake-a", "clishake-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListSessions() = %#v, want %#v", got, want)
	}
	assertCall(t, r, 0, []string{"-L", "sock", "list-sessions", "-F", "#{session_name}"})
}

func TestClient_ServerAlive(t *testing.T) {
	t.Run("alive", func(t *testing.T) {
		r := &scriptedRunner{}
		r.queue("clishake-a", nil)
		c := NewClientWithRunner("sock", r)
		if !c.ServerAlive() {
			t.Fatalf("ServerAlive() = false, want true")
		}
	})
	t.Run("no server", func(t *testing.T) {
		r := &scriptedRunner{}
		r.queue("", errors.New("no server running on /tmp/tmux-501/sock"))
		c := NewClientWithRunner("sock", r)
		if c.ServerAlive() {
			t.Fatalf("ServerAlive() = true, want false on 'no server' error")
		}
	})
}

func TestClient_HasSession(t *testing.T) {
	t.Run("exists", func(t *testing.T) {
		r := &scriptedRunner{}
		r.queue("", nil)
		c := NewClientWithRunner("sock", r)
		if !c.HasSession("foo") {
			t.Fatalf("HasSession() = false, want true")
		}
		assertCall(t, r, 0, []string{"-L", "sock", "has-session", "-t", "foo"})
	})
	t.Run("no server running error", func(t *testing.T) {
		r := &scriptedRunner{}
		r.queue("", errors.New("tmux has-session -t foo: no server running on /tmp/tmux-501/sock"))
		c := NewClientWithRunner("sock", r)
		if c.HasSession("foo") {
			t.Fatalf("HasSession() = true, want false on 'no server running' error")
		}
	})
	t.Run("no such session error", func(t *testing.T) {
		r := &scriptedRunner{}
		r.queue("", errors.New("can't find session foo"))
		c := NewClientWithRunner("sock", r)
		if c.HasSession("foo") {
			t.Fatalf("HasSession() = true, want false on 'no such session' error")
		}
	})
}

func TestClient_NewWindow(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("%7", nil) // new-window -P output
	r.queue("", nil)   // set-option remain-on-exit
	c := NewClientWithRunner("sock", r)

	command := []string{"bash", "-c", `echo it's "quoted"`}
	paneID, err := c.NewWindow("proj", "builder", "/work", command)
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	if paneID != "%7" {
		t.Fatalf("paneID = %q, want %q", paneID, "%7")
	}

	wantCmd := ShellQuote("bash") + " " + ShellQuote("-c") + " " + ShellQuote(`echo it's "quoted"`)
	if !strings.Contains(wantCmd, `'\''`) {
		t.Fatalf("sanity check failed: expected escaped single quote in %q", wantCmd)
	}
	assertCall(t, r, 0, []string{
		"-L", "sock", "new-window", "-d", "-P", "-F", "#{pane_id}",
		"-t", "proj:", "-n", "builder", "-c", "/work", wantCmd,
	})
	assertCall(t, r, 1, []string{"-L", "sock", "set-option", "-p", "-t", "%7", "remain-on-exit", "on"})
}

func TestClient_NewWindow_EmptyPaneID(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("", nil) // new-window returns no pane id
	c := NewClientWithRunner("sock", r)

	if _, err := c.NewWindow("proj", "builder", "/work", []string{"true"}); err == nil {
		t.Fatalf("NewWindow: expected error on empty pane id, got nil")
	}
}

func TestClient_KillWindow(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("", nil)
	c := NewClientWithRunner("sock", r)

	if err := c.KillWindow("proj", "builder"); err != nil {
		t.Fatalf("KillWindow: %v", err)
	}
	assertCall(t, r, 0, []string{"-L", "sock", "kill-window", "-t", "proj:builder"})
}

func TestClient_RespawnPane(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("", nil)
	c := NewClientWithRunner("sock", r)

	if err := c.RespawnPane("%4", "/work/dir", []string{"node", "server.js"}); err != nil {
		t.Fatalf("RespawnPane: %v", err)
	}
	wantCmd := ShellQuote("node") + " " + ShellQuote("server.js")
	assertCall(t, r, 0, []string{"-L", "sock", "respawn-pane", "-k", "-c", "/work/dir", "-t", "%4", wantCmd})
}

func TestClient_RespawnPane_NoCommand(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("", nil)
	c := NewClientWithRunner("sock", r)

	if err := c.RespawnPane("%4", "/work/dir", nil); err != nil {
		t.Fatalf("RespawnPane: %v", err)
	}
	assertCall(t, r, 0, []string{"-L", "sock", "respawn-pane", "-k", "-c", "/work/dir", "-t", "%4"})
}

func TestClient_SendText(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("", nil)
	r.queue("", nil)
	c := NewClientWithRunner("sock", r)

	if err := c.SendText("%3", "hello world"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	assertCall(t, r, 0, []string{"-L", "sock", "send-keys", "-t", "%3", "-l", "--", "hello world"})
	assertCall(t, r, 1, []string{"-L", "sock", "send-keys", "-t", "%3", "Enter"})
}

func TestClient_SendText_LiteralStopsOnFirstError(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("", errors.New("boom"))
	c := NewClientWithRunner("sock", r)

	if err := c.SendText("%3", "hello"); err == nil {
		t.Fatalf("SendText: expected error, got nil")
	}
	if len(r.calls) != 1 {
		t.Fatalf("SendText: expected 1 call after literal-send failure, got %d", len(r.calls))
	}
}

func TestClient_SendKeys(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("", nil)
	c := NewClientWithRunner("sock", r)

	if err := c.SendKeys("%3", "C-c", "Escape"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	assertCall(t, r, 0, []string{"-L", "sock", "send-keys", "-t", "%3", "C-c", "Escape"})
}

func TestClient_PipePane(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("", nil) // close any existing pipe
	r.queue("", nil) // open the new pipe
	c := NewClientWithRunner("sock", r)

	path := "/tmp/log dir/agent's output.log"
	if err := c.PipePane("%2", path); err != nil {
		t.Fatalf("PipePane: %v", err)
	}
	// First call closes a possibly-open pipe (respawned panes keep the old
	// one registered); second opens the new pipe unconditionally.
	assertCall(t, r, 0, []string{"-L", "sock", "pipe-pane", "-t", "%2"})
	assertCall(t, r, 1, []string{"-L", "sock", "pipe-pane", "-t", "%2", "cat >> " + ShellQuote(path)})
}

func TestClient_CapturePane(t *testing.T) {
	t.Run("no history limit", func(t *testing.T) {
		r := &scriptedRunner{}
		r.queue("line1\nline2", nil)
		c := NewClientWithRunner("sock", r)

		got, err := c.CapturePane("%1", 0)
		if err != nil {
			t.Fatalf("CapturePane: %v", err)
		}
		if got != "line1\nline2" {
			t.Fatalf("CapturePane() = %q", got)
		}
		assertCall(t, r, 0, []string{"-L", "sock", "capture-pane", "-p", "-t", "%1"})
	})
	t.Run("with history limit", func(t *testing.T) {
		r := &scriptedRunner{}
		r.queue("", nil)
		c := NewClientWithRunner("sock", r)

		if _, err := c.CapturePane("%1", 500); err != nil {
			t.Fatalf("CapturePane: %v", err)
		}
		assertCall(t, r, 0, []string{"-L", "sock", "capture-pane", "-p", "-t", "%1", "-S", "-500"})
	})
}

func TestClient_SelectWindow(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("", nil)
	c := NewClientWithRunner("sock", r)

	if err := c.SelectWindow("proj", "builder"); err != nil {
		t.Fatalf("SelectWindow: %v", err)
	}
	assertCall(t, r, 0, []string{"-L", "sock", "select-window", "-t", "proj:builder"})
}

func TestClient_SetOption(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("", nil)
	c := NewClientWithRunner("sock", r)

	if err := c.SetOption("clishake-proj", "remain-on-exit", "on"); err != nil {
		t.Fatalf("SetOption: %v", err)
	}
	assertCall(t, r, 0, []string{"-L", "sock", "set-option", "-t", "clishake-proj", "remain-on-exit", "on"})
}

func TestClient_ListPanes(t *testing.T) {
	out := strings.Join([]string{
		"proj|builder|%1|1234|0||sh",
		"proj|reviewer|%2|5678|1|137|bash",
		"proj|tester|%3|9999|0||node",
		"not|enough|fields",
		"",
	}, "\n")
	r := &scriptedRunner{}
	r.queue(out, nil)
	c := NewClientWithRunner("sock", r)

	panes, err := c.ListPanes("proj")
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	if len(panes) != 3 {
		t.Fatalf("got %d panes, want 3: %+v", len(panes), panes)
	}

	want0 := PaneInfo{SessionName: "proj", WindowName: "builder", PaneID: "%1", PanePID: 1234, Dead: false, DeadStatus: -1, Command: "sh"}
	if panes[0] != want0 {
		t.Fatalf("pane[0] = %+v, want %+v", panes[0], want0)
	}
	want1 := PaneInfo{SessionName: "proj", WindowName: "reviewer", PaneID: "%2", PanePID: 5678, Dead: true, DeadStatus: 137, Command: "bash"}
	if panes[1] != want1 {
		t.Fatalf("pane[1] = %+v, want %+v", panes[1], want1)
	}
	want2 := PaneInfo{SessionName: "proj", WindowName: "tester", PaneID: "%3", PanePID: 9999, Dead: false, DeadStatus: -1, Command: "node"}
	if panes[2] != want2 {
		t.Fatalf("pane[2] = %+v, want %+v", panes[2], want2)
	}

	assertCall(t, r, 0, []string{"-L", "sock", "list-panes", "-s", "-t", "proj", "-F", paneListFormat})
}

func TestClient_ListPanes_Empty(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("", nil)
	c := NewClientWithRunner("sock", r)

	panes, err := c.ListPanes("proj")
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	if len(panes) != 0 {
		t.Fatalf("got %d panes, want 0", len(panes))
	}
}

func TestClient_AttachArgs(t *testing.T) {
	c := NewClientWithRunner("clishake", &scriptedRunner{})
	got := c.AttachArgs("clishake-myproj")
	want := []string{"tmux", "-L", "clishake", "attach-session", "-t", "clishake-myproj"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AttachArgs() = %#v, want %#v", got, want)
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "''"},
		{"simple", "'simple'"},
		{"a b", "'a b'"},
		{"it's", `'it'\''s'`},
		{`"double"`, `'"double"'`},
		{"$(rm -rf /)", `'$(rm -rf /)'`},
	}
	for _, tc := range cases {
		if got := ShellQuote(tc.in); got != tc.want {
			t.Errorf("ShellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClient_SendLiteral(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("", nil)
	c := NewClientWithRunner("sock", r)

	if err := c.SendLiteral("%3", "hello -l world"); err != nil {
		t.Fatalf("SendLiteral: %v", err)
	}
	// Literal text only — no Enter call may follow.
	assertCall(t, r, 0, []string{"-L", "sock", "send-keys", "-t", "%3", "-l", "--", "hello -l world"})
	if len(r.calls) != 1 {
		t.Fatalf("expected exactly 1 tmux call, got %d", len(r.calls))
	}
}

func TestClient_PaneTargetedWindowOps(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("", nil)
	r.queue("", nil)
	c := NewClientWithRunner("sock", r)

	if err := c.SelectWindowByPane("%4"); err != nil {
		t.Fatalf("SelectWindowByPane: %v", err)
	}
	if err := c.KillWindowByPane("%4"); err != nil {
		t.Fatalf("KillWindowByPane: %v", err)
	}
	// Pane-id targets are unambiguous even when window names collide.
	assertCall(t, r, 0, []string{"-L", "sock", "select-window", "-t", "%4"})
	assertCall(t, r, 1, []string{"-L", "sock", "kill-window", "-t", "%4"})
}

func TestClient_PasteText(t *testing.T) {
	r := &scriptedRunner{}
	r.queue("", nil)
	r.queue("", nil)
	c := NewClientWithRunner("sock", r)

	if err := c.PasteText("%5", "[clishake message from lead] hi"); err != nil {
		t.Fatalf("PasteText: %v", err)
	}
	assertCall(t, r, 0, []string{"-L", "sock", "set-buffer", "-b", "clishake-paste", "--", "[clishake message from lead] hi"})
	assertCall(t, r, 1, []string{"-L", "sock", "paste-buffer", "-p", "-d", "-b", "clishake-paste", "-t", "%5"})
}
