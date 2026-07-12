package orchestrator_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clishakehq/clishake/internal/adapter"
	"github.com/clishakehq/clishake/internal/adapter/mock"
	"github.com/clishakehq/clishake/internal/config"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/events"
	"github.com/clishakehq/clishake/internal/messaging"
	"github.com/clishakehq/clishake/internal/orchestrator"
	"github.com/clishakehq/clishake/internal/tmux"
	"github.com/clishakehq/clishake/internal/wire"
)

// ---------------------------------------------------------------------------
// fakeRunner: a scriptable tmux.Runner for orchestrator tests.
//
// It tracks enough pane bookkeeping that ordinary StartAgent/Poll/Reconcile
// flows work unassisted: "new-window" auto-creates a live pane record,
// "list-panes" renders the current records, and "respawn-pane" revives a
// pane. Tests reach into the pane table directly (same package) to simulate
// exits, missing panes, and other edge cases; hasSessionErr lets a test
// force the has-session/new-session fallback path.
// ---------------------------------------------------------------------------

type panestate struct {
	session, window, cmd string
	pid                  int
	dead                 bool
	deadStatus           int
}

type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string

	seq   int
	panes map[string]*panestate
	order []string

	hasSessionErr error // nil = has-session always succeeds

	// defaultPID is used as the PID for auto-created panes (see
	// "new-window"/"respawn-pane" below). It is the PID of a real,
	// disposable child process (see newFakeRunner) rather than a made-up
	// number: checkProcess's liveness check does a real kill(pid, 0), so a
	// fabricated PID would spuriously look dead. Being real also means
	// StopAgent's kill-by-PID path has a harmless, disposable target
	// instead of risking an unrelated (or worse, the test's own) process.
	defaultPID int
}

// newFakeRunner spawns one long-lived, disposable "sleep" child process and
// returns a fakeRunner backed by it (see defaultPID). The child is killed
// and reaped in t.Cleanup.
func newFakeRunner(t *testing.T) *fakeRunner {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn dummy process for fakeRunner: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return &fakeRunner{panes: map[string]*panestate{}, defaultPID: cmd.Process.Pid}
}

// Run implements tmux.Runner. args always starts with "-L", <socket>
// (prepended by tmux.Client before delegating), so the actual tmux
// subcommand is args[2], not args[0].
func (f *fakeRunner) Run(args ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	sub := ""
	if len(args) >= 3 {
		sub = args[2]
	}

	switch sub {
	case "has-session":
		err := f.hasSessionErr
		f.mu.Unlock()
		return "", err

	case "list-sessions":
		f.mu.Unlock()
		return "", nil

	case "new-window":
		f.seq++
		id := fmt.Sprintf("%%%d", f.seq)
		window := extractFlag(args, "-n")
		session := strings.TrimSuffix(extractFlag(args, "-t"), ":")
		f.panes[id] = &panestate{session: session, window: window, cmd: "clishake", pid: f.defaultPID, deadStatus: -1}
		f.order = append(f.order, id)
		f.mu.Unlock()
		return id, nil

	case "list-panes":
		var lines []string
		for _, id := range f.order {
			p := f.panes[id]
			if p == nil {
				continue
			}
			lines = append(lines, formatPaneLine(p.session, p.window, id, p.pid, p.dead, p.deadStatus, p.cmd))
		}
		f.mu.Unlock()
		return strings.Join(lines, "\n"), nil

	case "respawn-pane":
		paneID := extractFlag(args, "-t")
		if p := f.panes[paneID]; p != nil {
			p.dead = false
			p.deadStatus = -1
			p.pid = f.defaultPID
		}
		f.mu.Unlock()
		return "", nil

	default:
		f.mu.Unlock()
		return "", nil
	}
}

// addPane registers a synthetic pane not created via "new-window" (for tests
// that craft agent records directly with Store.SaveAgent). Returns the pane
// state so the test can mutate it (mark dead, etc).
func (f *fakeRunner) addPane(session, window, id string, pid int) *panestate {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := &panestate{session: session, window: window, cmd: "clishake", pid: pid, deadStatus: -1}
	f.panes[id] = p
	f.order = append(f.order, id)
	return p
}

// setDead marks paneID as dead with the given exit status.
func (f *fakeRunner) setDead(paneID string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.panes[paneID]; p != nil {
		p.dead = true
		p.deadStatus = status
	}
}

// callsFor returns every recorded call whose tmux subcommand is sub.
func (f *fakeRunner) callsFor(sub string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		if len(c) >= 3 && c[2] == sub {
			out = append(out, c)
		}
	}
	return out
}

// callCount returns the total number of Run invocations so far.
func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func extractFlag(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func formatPaneLine(session, window, id string, pid int, dead bool, deadStatus int, cmd string) string {
	d := "0"
	if dead {
		d = "1"
	}
	return fmt.Sprintf("%s|%s|%s|%d|%s|%d|%s", session, window, id, pid, d, deadStatus, cmd)
}

var _ tmux.Runner = (*fakeRunner)(nil)

// ---------------------------------------------------------------------------
// test harness
// ---------------------------------------------------------------------------

// newTestOrch opens a real Orchestrator (real state.Store + real event log,
// both in t.TempDir()) with the real mock adapter registered, then replaces
// its tmux.Client with one backed by a fakeRunner -- the seam
// orchestrator.Open leaves for tests.
func newTestOrch(t *testing.T) (*orchestrator.Orchestrator, *fakeRunner) {
	t.Helper()
	dir := t.TempDir()
	reg := adapter.NewRegistry()
	reg.Register(mock.New())

	o, err := orchestrator.Open(dir, reg)
	if err != nil {
		t.Fatalf("orchestrator.Open: %v", err)
	}
	t.Cleanup(o.Close)

	fr := newFakeRunner(t)
	o.Tmux = tmux.NewClientWithRunner("test-sock", fr)
	return o, fr
}

// startAgent registers and starts an agent named name, with default role
// and the mock adapter. The fakeRunner's auto pane bookkeeping means the
// resulting agent has a live pane and a resolved PID with no extra
// scripting required.
func startAgent(t *testing.T, o *orchestrator.Orchestrator, name string) *domain.Agent {
	t.Helper()
	if _, err := o.AddAgent(orchestrator.AgentSpec{Name: name, Role: "builder", Adapter: "mock"}); err != nil {
		t.Fatalf("AddAgent(%s): %v", name, err)
	}
	a, err := o.StartAgent(name)
	if err != nil {
		t.Fatalf("StartAgent(%s): %v", name, err)
	}
	return a
}

func writeOutputLog(t *testing.T, o *orchestrator.Orchestrator, a *domain.Agent, content string) {
	t.Helper()
	dir := o.AgentDir(a)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "output.log"), []byte(content), 0o644); err != nil {
		t.Fatalf("write output.log: %v", err)
	}
}

func readEvents(t *testing.T, o *orchestrator.Orchestrator) []domain.Event {
	t.Helper()
	evs, _, err := events.ReadAll(o.Log.Path())
	if err != nil {
		t.Fatalf("ReadAll events: %v", err)
	}
	return evs
}

func lastEventOfType(t *testing.T, o *orchestrator.Orchestrator, typ domain.EventType) domain.Event {
	t.Helper()
	evs := readEvents(t, o)
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == typ {
			return evs[i]
		}
	}
	t.Fatalf("no event of type %q found among %d events", typ, len(evs))
	return domain.Event{}
}

func eventsOfType(o *orchestrator.Orchestrator, typ domain.EventType) []domain.Event {
	evs, _, _ := events.ReadAll(o.Log.Path())
	var out []domain.Event
	for _, ev := range evs {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// a. AddAgent
// ---------------------------------------------------------------------------

func TestAddAgent_Defaults(t *testing.T) {
	o, _ := newTestOrch(t)

	a, err := o.AddAgent(orchestrator.AgentSpec{Name: "builder", Role: "builder", Adapter: "mock", Task: "build things"})
	if err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	if a.Status != domain.StatusStopped {
		t.Errorf("Status = %q, want stopped (registered but not started)", a.Status)
	}
	if a.Permissions != o.Cfg.Defaults.Permissions {
		t.Errorf("Permissions = %+v, want config defaults %+v", a.Permissions, o.Cfg.Defaults.Permissions)
	}
	wantCaps := mock.New().Capabilities()
	if !reflect.DeepEqual(a.Capabilities, wantCaps) {
		t.Errorf("Capabilities = %v, want %v", a.Capabilities, wantCaps)
	}

	stored, err := o.Store.GetAgentByName("builder")
	if err != nil || stored == nil {
		t.Fatalf("GetAgentByName: err=%v agent=%v", err, stored)
	}
	if stored.ID != a.ID {
		t.Errorf("stored.ID = %q, want %q", stored.ID, a.ID)
	}

	ev := lastEventOfType(t, o, domain.EvAgentCreated)
	if ev.Subject != "builder" {
		t.Errorf("agent.created Subject = %q, want builder", ev.Subject)
	}
}

func TestAddAgent_DuplicateNameRejected(t *testing.T) {
	o, _ := newTestOrch(t)
	if _, err := o.AddAgent(orchestrator.AgentSpec{Name: "builder", Adapter: "mock"}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	if _, err := o.AddAgent(orchestrator.AgentSpec{Name: "builder", Adapter: "mock"}); err == nil {
		t.Fatal("AddAgent(duplicate name) error = nil, want error")
	}
}

func TestAddAgent_InvalidNameRejected(t *testing.T) {
	o, _ := newTestOrch(t)
	for _, name := range []string{"Bad Name", "lead", "all"} {
		t.Run(name, func(t *testing.T) {
			if _, err := o.AddAgent(orchestrator.AgentSpec{Name: name, Adapter: "mock"}); err == nil {
				t.Fatalf("AddAgent(%q) error = nil, want error", name)
			}
		})
	}
}

func TestAddAgent_UnknownAdapterRejected(t *testing.T) {
	o, _ := newTestOrch(t)
	if _, err := o.AddAgent(orchestrator.AgentSpec{Name: "builder", Adapter: "does-not-exist"}); err == nil {
		t.Fatal("AddAgent(unknown adapter) error = nil, want error")
	}
}

// ---------------------------------------------------------------------------
// b/c. StartAgent
// ---------------------------------------------------------------------------

func TestStartAgent_HappyPath(t *testing.T) {
	o, fr := newTestOrch(t)
	fr.hasSessionErr = errors.New("no such session") // force EnsureSession's creation path

	if _, err := o.AddAgent(orchestrator.AgentSpec{Name: "builder", Adapter: "mock"}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}

	a, err := o.StartAgent("builder")
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	if got := len(fr.callsFor("new-session")); got != 1 {
		t.Errorf("new-session calls = %d, want 1 (has-session errored, forcing creation)", got)
	}
	if a.Tmux.PaneID == "" {
		t.Error("Tmux.PaneID is empty")
	}
	if a.Tmux.Session != o.Cfg.SessionName() {
		t.Errorf("Tmux.Session = %q, want %q", a.Tmux.Session, o.Cfg.SessionName())
	}
	if a.Tmux.Window != "builder" {
		t.Errorf("Tmux.Window = %q, want builder", a.Tmux.Window)
	}
	if a.PID <= 0 {
		t.Errorf("PID = %d, want > 0", a.PID)
	}
	if a.Status != domain.StatusStarting {
		t.Errorf("Status = %q, want starting", a.Status)
	}

	stored, err := o.Store.GetAgentByName("builder")
	if err != nil || stored == nil {
		t.Fatalf("GetAgentByName: err=%v agent=%v", err, stored)
	}
	if stored.Status != domain.StatusStarting || stored.PID != a.PID || stored.Tmux.PaneID != a.Tmux.PaneID {
		t.Errorf("persisted agent mismatch: %+v", stored)
	}

	ev := lastEventOfType(t, o, domain.EvAgentStarted)
	if ev.Subject != "builder" {
		t.Errorf("agent.started Subject = %q, want builder", ev.Subject)
	}

	if len(fr.callsFor("pipe-pane")) == 0 {
		t.Error("expected pipe-pane to have been called")
	}
}

func TestStartAgent_AlreadyLiveErrors(t *testing.T) {
	o, _ := newTestOrch(t)
	startAgent(t, o, "builder")

	if _, err := o.StartAgent("builder"); err == nil {
		t.Fatal("StartAgent(already live) error = nil, want error")
	}
}

func TestStartAgent_UnknownAgentErrors(t *testing.T) {
	o, _ := newTestOrch(t)
	if _, err := o.StartAgent("nobody"); err == nil {
		t.Fatal("StartAgent(unknown agent) error = nil, want error")
	}
}

// ---------------------------------------------------------------------------
// d/e/f/g. Send
// ---------------------------------------------------------------------------

func TestSend_LeadToAgent_WritesInbox(t *testing.T) {
	o, _ := newTestOrch(t)
	a := startAgent(t, o, "builder")

	msgs, err := o.Send(domain.LeadSender, "@builder", "please build the widget", messaging.SendOpts{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Delivery != domain.DeliveryDelivered {
		t.Errorf("Delivery = %q, want delivered", msgs[0].Delivery)
	}

	inboxPath := filepath.Join(o.AgentDir(a), "inbox.jsonl")
	b, err := os.ReadFile(inboxPath)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d inbox lines, want 1: %q", len(lines), string(b))
	}
	env, ok := wire.DecodeEnvelope(lines[0])
	if !ok {
		t.Fatalf("DecodeEnvelope failed on %q", lines[0])
	}
	if env.Text != "please build the widget" {
		t.Errorf("Envelope.Text = %q, want %q", env.Text, "please build the widget")
	}
	if env.From != domain.LeadSender {
		t.Errorf("Envelope.From = %q, want lead", env.From)
	}
	if env.MsgID != msgs[0].ID {
		t.Errorf("Envelope.MsgID = %q, want %q", env.MsgID, msgs[0].ID)
	}
}

func TestSend_ToLead_NoTmuxNoInbox(t *testing.T) {
	o, fr := newTestOrch(t)
	a := startAgent(t, o, "builder")

	before := fr.callCount()

	msgs, err := o.Send("builder", "@lead", "status update", messaging.SendOpts{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Recipient != domain.LeadSender || msgs[0].Delivery != domain.DeliveryDelivered {
		t.Fatalf("unexpected result: %+v", msgs)
	}

	if got := fr.callCount(); got != before {
		t.Errorf("tmux calls changed sending to @lead: %d -> %d", before, got)
	}

	inboxPath := filepath.Join(o.AgentDir(a), "inbox.jsonl")
	if _, err := os.Stat(inboxPath); !os.IsNotExist(err) {
		t.Errorf("expected no inbox file to exist for a @lead message, stat err = %v", err)
	}
}

func TestSend_UnknownSelector_ErrNoRecipients(t *testing.T) {
	o, _ := newTestOrch(t)
	_, err := o.Send(domain.LeadSender, "@nobody", "hi", messaging.SendOpts{})
	if !errors.Is(err, messaging.ErrNoRecipients) {
		t.Fatalf("err = %v, want messaging.ErrNoRecipients", err)
	}
}

func TestSend_LacksSendMessagesPermission(t *testing.T) {
	o, _ := newTestOrch(t)
	perms := domain.DefaultPermissions()
	perms.SendMessages = false
	if _, err := o.AddAgent(orchestrator.AgentSpec{Name: "builder", Adapter: "mock", Permissions: &perms}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	if _, err := o.StartAgent("builder"); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	if _, err := o.Send("builder", "@lead", "hi", messaging.SendOpts{}); err == nil {
		t.Fatal("Send() from agent lacking send_messages permission error = nil, want error")
	}
}

// ---------------------------------------------------------------------------
// h. consumeOutput via Poll
// ---------------------------------------------------------------------------

func TestPoll_ConsumesOutput(t *testing.T) {
	o, _ := newTestOrch(t)
	a := startAgent(t, o, "builder")

	task, err := o.Tasks.Create("lead", "Fix bug", "", "builder", 0, nil)
	if err != nil {
		t.Fatalf("Tasks.Create: %v", err)
	}
	if task.Status != domain.TaskAssigned {
		t.Fatalf("precondition: task status = %q, want assigned", task.Status)
	}

	lines := []string{
		wire.EncodeOut(wire.OutMsg{Type: "status", Status: "running"}),
		wire.EncodeOut(wire.OutMsg{Type: "message", To: "lead", Text: "hello lead"}),
		wire.EncodeOut(wire.OutMsg{Type: "subagent", Name: "helper", Role: "helper", Status: "running"}),
		wire.EncodeOut(wire.OutMsg{Type: "task", TaskID: task.ID, Status: "completed", Text: "done"}),
	}
	writeOutputLog(t, o, a, strings.Join(lines, "\n")+"\n")

	o.Poll()

	updated, err := o.Store.GetAgentByName("builder")
	if err != nil || updated == nil {
		t.Fatalf("GetAgentByName: err=%v agent=%v", err, updated)
	}
	if updated.Status != domain.StatusRunning {
		t.Errorf("Status = %q, want running", updated.Status)
	}

	msgs, err := o.Store.ListMessagesWith("builder", 0)
	if err != nil {
		t.Fatalf("ListMessagesWith: %v", err)
	}
	found := false
	for _, m := range msgs {
		if m.Sender == "builder" && m.Recipient == domain.LeadSender && m.Body == "hello lead" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a builder->lead message 'hello lead', got %+v", msgs)
	}

	child, err := o.Store.GetAgentByName("builder/helper")
	if err != nil || child == nil {
		t.Fatalf("GetAgentByName(builder/helper): err=%v agent=%v", err, child)
	}
	if child.Adapter != "observed" {
		t.Errorf("child.Adapter = %q, want observed", child.Adapter)
	}
	if child.ParentID != updated.ID {
		t.Errorf("child.ParentID = %q, want %q", child.ParentID, updated.ID)
	}

	finalTask, err := o.Tasks.Get(task.ID)
	if err != nil {
		t.Fatalf("Tasks.Get: %v", err)
	}
	if finalTask.Status != domain.TaskCompleted {
		t.Errorf("task status = %q, want completed (stepped through in_progress)", finalTask.Status)
	}
}

// ---------------------------------------------------------------------------
// i. Exit detection
// ---------------------------------------------------------------------------

func TestPoll_ExitDetection_NonZeroExit(t *testing.T) {
	o, fr := newTestOrch(t)
	a := startAgent(t, o, "builder")

	fr.setDead(a.Tmux.PaneID, 1)
	o.Poll()

	updated, _ := o.Store.GetAgentByName("builder")
	if updated == nil {
		t.Fatal("agent disappeared")
	}
	if updated.Status != domain.StatusFailed {
		t.Errorf("Status = %q, want failed", updated.Status)
	}
	if updated.ExitCode == nil || *updated.ExitCode != 1 {
		t.Errorf("ExitCode = %v, want 1", updated.ExitCode)
	}

	ev := lastEventOfType(t, o, domain.EvAgentExited)
	if intentional, _ := ev.Payload["intentional"].(bool); intentional {
		t.Errorf("agent.exited intentional = true, want false")
	}
	if code, _ := ev.Payload["exit_code"].(float64); int(code) != 1 {
		t.Errorf("agent.exited exit_code = %v, want 1", ev.Payload["exit_code"])
	}
}

func TestPoll_ExitDetection_ZeroExit(t *testing.T) {
	o, fr := newTestOrch(t)
	a := startAgent(t, o, "builder")

	fr.setDead(a.Tmux.PaneID, 0)
	o.Poll()

	updated, _ := o.Store.GetAgentByName("builder")
	if updated == nil {
		t.Fatal("agent disappeared")
	}
	if updated.Status != domain.StatusCompleted {
		t.Errorf("Status = %q, want completed", updated.Status)
	}
	if updated.ExitCode == nil || *updated.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0", updated.ExitCode)
	}
}

// ---------------------------------------------------------------------------
// j. StopAgent
// ---------------------------------------------------------------------------

func TestStopAgent_Intentional(t *testing.T) {
	o, _ := newTestOrch(t)
	startAgent(t, o, "builder")

	if err := o.StopAgent("builder", false); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}

	updated, err := o.Store.GetAgentByName("builder")
	if err != nil || updated == nil {
		t.Fatalf("GetAgentByName: err=%v agent=%v", err, updated)
	}
	if updated.Status != domain.StatusStopped {
		t.Errorf("Status = %q, want stopped", updated.Status)
	}
}

func TestStopAgent_NotRunningErrors(t *testing.T) {
	o, _ := newTestOrch(t)
	if _, err := o.AddAgent(orchestrator.AgentSpec{Name: "builder", Adapter: "mock"}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	if err := o.StopAgent("builder", false); err == nil {
		t.Fatal("StopAgent(never started) error = nil, want error")
	}
}

// ---------------------------------------------------------------------------
// k. Reconcile
// ---------------------------------------------------------------------------

func TestReconcile(t *testing.T) {
	o, fr := newTestOrch(t)
	sess := o.Cfg.SessionName()
	now := time.Now().UTC()

	mkAgent := func(name, paneID string) *domain.Agent {
		a := &domain.Agent{
			ID:           domain.NewID("ag"),
			Name:         name,
			Adapter:      "mock",
			Status:       domain.StatusRunning,
			WorkDir:      o.ProjectDir,
			Tmux:         domain.TmuxRef{Session: sess, Window: name, PaneID: paneID},
			CreatedAt:    now,
			LastActivity: now,
		}
		if err := o.Store.SaveAgent(a); err != nil {
			t.Fatalf("SaveAgent(%s): %v", name, err)
		}
		return a
	}

	// (1) live agent whose pane is missing entirely: never registered with
	// fr, so list-panes will never report it.
	mkAgent("recon-missing", "%900")

	// (2) live agent whose pane exists but is dead.
	mkAgent("recon-dead", "%901")
	deadPane := fr.addPane(sess, "recon-dead", "%901", 555)
	deadPane.dead = true
	deadPane.deadStatus = 9

	// (3) live agent with a live pane; PID should be refreshed from it.
	aliveAgent := mkAgent("recon-alive", "%902")
	aliveAgent.PID = 1
	if err := o.Store.SaveAgent(aliveAgent); err != nil {
		t.Fatalf("SaveAgent(recon-alive): %v", err)
	}
	fr.addPane(sess, "recon-alive", "%902", 7777)

	report, err := o.Reconcile()
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	joined := strings.Join(report, "\n")

	if !strings.Contains(joined, "recon-missing: marked disconnected") {
		t.Errorf("report missing disconnected line, got:\n%s", joined)
	}
	if !strings.Contains(joined, "recon-dead: exited while detached") {
		t.Errorf("report missing dead-agent line, got:\n%s", joined)
	}
	if !strings.Contains(joined, "recon-alive: alive") {
		t.Errorf("report missing alive line, got:\n%s", joined)
	}

	m1, _ := o.Store.GetAgentByName("recon-missing")
	if m1.Status != domain.StatusDisconnected {
		t.Errorf("recon-missing status = %q, want disconnected", m1.Status)
	}
	m2, _ := o.Store.GetAgentByName("recon-dead")
	if m2.Status != domain.StatusFailed {
		t.Errorf("recon-dead status = %q, want failed (nonzero exit)", m2.Status)
	}
	if m2.ExitCode == nil || *m2.ExitCode != 9 {
		t.Errorf("recon-dead ExitCode = %v, want 9", m2.ExitCode)
	}
	m3, _ := o.Store.GetAgentByName("recon-alive")
	if m3.PID != 7777 {
		t.Errorf("recon-alive PID = %d, want 7777 (updated from live pane)", m3.PID)
	}
	if m3.Status != domain.StatusRunning {
		t.Errorf("recon-alive status = %q, want unchanged (running)", m3.Status)
	}
}

// ---------------------------------------------------------------------------
// l. RemoveAgent
// ---------------------------------------------------------------------------

func TestRemoveAgent_ReparentsChildrenAndCleansUp(t *testing.T) {
	o, fr := newTestOrch(t)
	parent := startAgent(t, o, "builder")

	child := &domain.Agent{
		ID:           domain.NewID("ag"),
		Name:         "builder/helper",
		Adapter:      "observed",
		ParentID:     parent.ID,
		Status:       domain.StatusRunning,
		WorkDir:      o.ProjectDir,
		CreatedAt:    time.Now().UTC(),
		LastActivity: time.Now().UTC(),
	}
	if err := o.Store.SaveAgent(child); err != nil {
		t.Fatalf("SaveAgent(child): %v", err)
	}

	// Mark the parent's pane already-dead so RemoveAgent's graceful
	// StopAgent doesn't spend up to 3 real seconds waiting for it to exit.
	fr.setDead(parent.Tmux.PaneID, 0)

	if err := o.RemoveAgent("builder"); err != nil {
		t.Fatalf("RemoveAgent: %v", err)
	}

	if got, _ := o.Store.GetAgentByName("builder"); got != nil {
		t.Errorf("expected builder removed from store, still present: %+v", got)
	}
	if len(fr.callsFor("kill-window")) == 0 {
		t.Error("expected kill-window to have been called")
	}

	updatedChild, err := o.Store.GetAgent(child.ID)
	if err != nil || updatedChild == nil {
		t.Fatalf("GetAgent(child): err=%v agent=%v", err, updatedChild)
	}
	if updatedChild.ParentID != "" {
		t.Errorf("child.ParentID = %q, want empty (reparented on removal)", updatedChild.ParentID)
	}

	ev := lastEventOfType(t, o, domain.EvAgentRemoved)
	if ev.Subject != "builder" {
		t.Errorf("agent.removed Subject = %q, want builder", ev.Subject)
	}
}

// ---------------------------------------------------------------------------
// m. Approvals
// ---------------------------------------------------------------------------

func TestApprovals_ViaPollThenDecide(t *testing.T) {
	o, _ := newTestOrch(t)
	a := startAgent(t, o, "builder")

	line := wire.EncodeOut(wire.OutMsg{Type: "approval", Action: "run_command", Reason: "need to delete files", Risk: "high"})
	writeOutputLog(t, o, a, line+"\n")

	o.Poll()

	pending, err := o.Store.ListApprovals(domain.ApprovalPending)
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d pending approvals, want 1", len(pending))
	}
	ap := pending[0]
	if ap.AgentName != "builder" || ap.Action != "run_command" || ap.Risk != "high" {
		t.Errorf("approval = %+v, unexpected fields", ap)
	}

	waiting, _ := o.Store.GetAgentByName("builder")
	if waiting.Status != domain.StatusAwaitingApproval {
		t.Errorf("Status = %q, want awaiting_approval", waiting.Status)
	}

	decided, err := o.Decide(ap.ID, true)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decided.State != domain.ApprovalGranted {
		t.Errorf("State = %q, want granted", decided.State)
	}

	ev := lastEventOfType(t, o, domain.EvApprovalGranted)
	if ev.Subject != ap.ID {
		t.Errorf("approval.granted Subject = %q, want %q", ev.Subject, ap.ID)
	}

	resumed, _ := o.Store.GetAgentByName("builder")
	if resumed.Status != domain.StatusRunning {
		t.Errorf("Status after grant = %q, want running", resumed.Status)
	}

	if _, err := o.Decide(ap.ID, true); err == nil {
		t.Fatal("Decide() on an already-decided approval error = nil, want error")
	}
}

// ---------------------------------------------------------------------------
// n. crash-loop restart scheduling
// ---------------------------------------------------------------------------

func TestRestartAfterFailure_ScheduledAndRun(t *testing.T) {
	o, fr := newTestOrch(t)
	o.Cfg.Defaults.Restart = config.RestartPolicy{Mode: "on-failure", MaxRestarts: 1, WindowSec: 300, BackoffSec: 1}

	a := startAgent(t, o, "builder")

	fr.setDead(a.Tmux.PaneID, 1)
	o.Poll()

	failed, _ := o.Store.GetAgentByName("builder")
	if failed == nil || failed.Status != domain.StatusFailed {
		t.Fatalf("Status after crash = %v, want failed", failed)
	}

	// Documented exception to the "no arbitrary sleeps" rule: let the 1s
	// backoff actually elapse before polling again.
	time.Sleep(1100 * time.Millisecond)

	o.Poll() // runDueRestarts should respawn the agent now.

	restarted, err := o.Store.GetAgentByName("builder")
	if err != nil || restarted == nil {
		t.Fatalf("GetAgentByName: err=%v agent=%v", err, restarted)
	}
	if restarted.Status != domain.StatusStarting {
		t.Errorf("Status after restart = %q, want starting", restarted.Status)
	}
	if restarted.RestartCount != 1 {
		t.Errorf("RestartCount = %d, want 1", restarted.RestartCount)
	}

	var sawAutomaticRestart bool
	for _, ev := range eventsOfType(o, domain.EvAgentRestarted) {
		if ev.Subject != "builder" {
			continue
		}
		if auto, _ := ev.Payload["automatic"].(bool); auto {
			sawAutomaticRestart = true
		}
	}
	if !sawAutomaticRestart {
		t.Error("expected an automatic agent.restarted event for builder")
	}

	if len(fr.callsFor("respawn-pane")) == 0 {
		t.Error("expected respawn-pane to have been called")
	}
}
