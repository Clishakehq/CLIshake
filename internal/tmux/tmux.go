// Package tmux wraps the tmux(1) CLI to manage clishake's agents inside a
// dedicated tmux server, reached via a private socket (tmux -L <socket>).
// Using a private server means clishake never lists, attaches to, or kills
// anything in the user's own tmux sessions.
package tmux

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Runner abstracts command execution for testability.
type Runner interface {
	// Run executes tmux with the given args (socket flag already included
	// by Client) and returns trimmed stdout.
	Run(args ...string) (string, error)
}

// execRunner is the real, os/exec-backed Runner.
type execRunner struct{}

// Run shells out to the tmux binary on PATH, capturing stdout and stderr
// separately. On a non-zero exit it returns an error that includes stderr
// (falling back to stdout, then the raw exec error, if stderr is empty).
func (execRunner) Run(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = out
		}
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("tmux %s: %s", strings.Join(args, " "), msg)
	}
	return out, nil
}

// Client is a handle to clishake's dedicated tmux server.
type Client struct {
	socket string
	runner Runner
}

// NewClient returns a Client backed by the real tmux binary, talking to the
// dedicated server on socket (tmux -L <socket>).
func NewClient(socket string) *Client {
	return &Client{socket: socket, runner: execRunner{}}
}

// NewClientWithRunner returns a Client backed by a custom Runner, for tests.
func NewClientWithRunner(socket string, r Runner) *Client {
	return &Client{socket: socket, runner: r}
}

// Socket returns the dedicated tmux server socket name this client uses.
func (c *Client) Socket() string { return c.socket }

// run prepends the socket flag and delegates to the configured Runner.
func (c *Client) run(args ...string) (string, error) {
	full := make([]string, 0, len(args)+2)
	full = append(full, "-L", c.socket)
	full = append(full, args...)
	return c.runner.Run(full...)
}

// Available reports whether the tmux binary is present on PATH.
func (c *Client) Available() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// ServerAlive reports whether any session exists on our dedicated socket.
// A "no server running" condition is treated as false, not an error.
func (c *Client) ServerAlive() bool {
	_, err := c.run("list-sessions")
	return err == nil
}

// HasSession reports whether a session named name exists on our socket.
// "no such session" and "no server running" are treated as false, not
// errors.
func (c *Client) HasSession(name string) bool {
	_, err := c.run("has-session", "-t", name)
	return err == nil
}

// NewSession creates a detached session named name rooted at startDir.
func (c *Client) NewSession(name, startDir string) error {
	_, err := c.run("new-session", "-d", "-s", name, "-c", startDir)
	return err
}

// KillSession destroys the named session and everything running in it.
func (c *Client) KillSession(name string) error {
	_, err := c.run("kill-session", "-t", name)
	return err
}

// ListSessions returns the names of all sessions on our socket.
func (c *Client) ListSessions() ([]string, error) {
	out, err := c.run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		return nil, err
	}
	return splitNonEmptyLines(out), nil
}

// NewWindow creates a detached window in session running command, and
// returns the tmux unique pane id (e.g. "%3"). The pane is left with
// remain-on-exit enabled so that when command exits, the pane goes dead
// (exit status readable via ListPanes) instead of the window being
// destroyed.
func (c *Client) NewWindow(session, windowName, startDir string, command []string) (paneID string, err error) {
	out, err := c.run(
		"new-window", "-d", "-P", "-F", "#{pane_id}",
		"-t", session+":",
		"-n", windowName,
		"-c", startDir,
		shellJoin(command),
	)
	if err != nil {
		return "", err
	}
	paneID = strings.TrimSpace(out)
	if paneID == "" {
		return "", fmt.Errorf("tmux new-window: no pane id returned")
	}
	if _, err := c.run("set-option", "-p", "-t", paneID, "remain-on-exit", "on"); err != nil {
		return paneID, err
	}
	return paneID, nil
}

// KillWindow destroys the named window in session. Prefer
// KillWindowByPane when a pane id is known: name lookups are ambiguous
// when several windows share a name (tmux then refuses the target).
func (c *Client) KillWindow(session, windowName string) error {
	_, err := c.run("kill-window", "-t", session+":"+windowName)
	return err
}

// KillWindowByPane destroys the window containing paneID. Pane ids are
// unique per server, so this can never hit the wrong window.
func (c *Client) KillWindowByPane(paneID string) error {
	_, err := c.run("kill-window", "-t", paneID)
	return err
}

// RespawnPane replaces the process running in paneID with command, rooted
// at startDir. -k forces any existing (possibly dead) process in the pane
// to be killed first.
func (c *Client) RespawnPane(paneID, startDir string, command []string) error {
	args := []string{"respawn-pane", "-k", "-c", startDir, "-t", paneID}
	if len(command) > 0 {
		args = append(args, shellJoin(command))
	}
	_, err := c.run(args...)
	return err
}

// SendText types text literally into paneID, then presses Enter as a
// separate step. Sending literally (-l) ensures text is never interpreted
// as tmux key names.
func (c *Client) SendText(paneID, text string) error {
	if _, err := c.run("send-keys", "-t", paneID, "-l", "--", text); err != nil {
		return err
	}
	_, err := c.run("send-keys", "-t", paneID, "Enter")
	return err
}

// SendKeys sends one or more raw tmux key names (e.g. "C-c", "Escape") to
// paneID.
func (c *Client) SendKeys(paneID string, keys ...string) error {
	args := append([]string{"send-keys", "-t", paneID}, keys...)
	_, err := c.run(args...)
	return err
}

// SendLiteral types text into the pane WITHOUT a trailing Enter. Callers
// that need submission send Enter separately (possibly after a delay —
// some TUI harnesses drop an Enter that arrives in the same burst as the
// pasted text).
func (c *Client) SendLiteral(paneID, text string) error {
	_, err := c.run("send-keys", "-t", paneID, "-l", "--", text)
	return err
}

// PasteText delivers text to a pane as a bracketed paste (tmux buffer +
// paste-buffer -p). Unlike send-keys, the TUI receives one atomic paste
// event: character-by-character key injection races some frameworks'
// input handling and silently drops text (observed live with Antigravity).
// No trailing Enter — submit separately.
func (c *Client) PasteText(paneID, text string) error {
	if _, err := c.run("set-buffer", "-b", "clishake-paste", "--", text); err != nil {
		return err
	}
	_, err := c.run("paste-buffer", "-p", "-d", "-b", "clishake-paste", "-t", paneID)
	return err
}

// PipePane streams paneID's output by appending it to filePath. Any
// existing pipe on the pane is closed first: after a respawn-pane, tmux may
// still consider the previous pipe open, and `pipe-pane -o` would then
// silently skip attaching a new one, losing all subsequent output.
func (c *Client) PipePane(paneID, filePath string) error {
	if _, err := c.run("pipe-pane", "-t", paneID); err != nil {
		return err
	}
	_, err := c.run("pipe-pane", "-t", paneID, "cat >> "+ShellQuote(filePath))
	return err
}

// CapturePane returns the contents of paneID as plain text. When lines > 0,
// the capture includes that many lines of scrollback history in addition to
// the visible pane.
func (c *Client) CapturePane(paneID string, lines int) (string, error) {
	args := []string{"capture-pane", "-p", "-t", paneID}
	if lines > 0 {
		args = append(args, "-S", "-"+strconv.Itoa(lines))
	}
	return c.run(args...)
}

// SelectWindow makes windowName the active window in session. Prefer
// SelectWindowByPane when a pane id is known (name lookups are ambiguous
// when several windows share a name).
func (c *Client) SelectWindow(session, windowName string) error {
	_, err := c.run("select-window", "-t", session+":"+windowName)
	return err
}

// SelectWindowByPane makes the window containing paneID the active window.
func (c *Client) SelectWindowByPane(paneID string) error {
	_, err := c.run("select-window", "-t", paneID)
	return err
}

// SetOption sets a tmux option on target, using tmux's own -t target
// resolution (session, window, or pane depending on the option's scope).
func (c *Client) SetOption(target, option, value string) error {
	_, err := c.run("set-option", "-t", target, option, value)
	return err
}

// SetGlobalOption sets a server/session-global option. Safe on clishake's
// dedicated socket: it can never affect the user's own tmux server.
func (c *Client) SetGlobalOption(option, value string) error {
	_, err := c.run("set-option", "-g", option, value)
	return err
}

// BindRootKey binds key in the root table (no prefix needed) to a tmux
// command — again scoped to clishake's dedicated server only.
func (c *Client) BindRootKey(key string, command ...string) error {
	args := append([]string{"bind-key", "-n", key}, command...)
	_, err := c.run(args...)
	return err
}

// PaneInfo describes one pane on our server.
type PaneInfo struct {
	SessionName string
	WindowName  string
	PaneID      string
	PanePID     int    // #{pane_pid}
	Dead        bool   // #{pane_dead}
	DeadStatus  int    // #{pane_dead_status}; -1 when not dead/unknown
	Command     string // #{pane_current_command}
}

// paneListFormat is the tmux -F format string used by ListPanes. Fields are
// pipe-separated and must stay in sync with the parsing in ListPanes.
const paneListFormat = "#{session_name}|#{window_name}|#{pane_id}|#{pane_pid}|#{pane_dead}|#{pane_dead_status}|#{pane_current_command}"

// ListPanes lists all panes in session. Lines that don't parse into the
// expected number of fields are skipped defensively rather than causing an
// error.
func (c *Client) ListPanes(session string) ([]PaneInfo, error) {
	out, err := c.run("list-panes", "-s", "-t", session, "-F", paneListFormat)
	if err != nil {
		return nil, err
	}
	lines := splitNonEmptyLines(out)
	panes := make([]PaneInfo, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, "|")
		if len(fields) != 7 {
			continue // malformed line; skip defensively
		}
		pid, _ := strconv.Atoi(fields[3])
		dead := fields[4] == "1"
		deadStatus := -1
		if fields[5] != "" {
			if v, err := strconv.Atoi(fields[5]); err == nil {
				deadStatus = v
			}
		}
		panes = append(panes, PaneInfo{
			SessionName: fields[0],
			WindowName:  fields[1],
			PaneID:      fields[2],
			PanePID:     pid,
			Dead:        dead,
			DeadStatus:  deadStatus,
			Command:     fields[6],
		})
	}
	return panes, nil
}

// AttachArgs returns the argv a caller can exec to attach interactively to
// session on our dedicated server, e.g.
// ["tmux","-L",socket,"attach-session","-t",session].
func (c *Client) AttachArgs(session string) []string {
	return []string{"tmux", "-L", c.socket, "attach-session", "-t", session}
}

// ShellQuote quotes s for safe inclusion as a single word in a POSIX shell
// command line, using single quotes (escaping any embedded single quotes).
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellJoin quotes and space-joins argv into one shell command string,
// suitable for tmux commands (new-window, respawn-pane, ...) that hand a
// single "shell-command" argument off to the user's shell.
func shellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = ShellQuote(a)
	}
	return strings.Join(parts, " ")
}

// splitNonEmptyLines splits s on newlines, dropping empty/whitespace-only
// lines.
func splitNonEmptyLines(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	raw := strings.Split(s, "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
