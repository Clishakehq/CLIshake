package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/clishakehq/clishake/internal/adapter"
	"github.com/clishakehq/clishake/internal/ansi"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/tmux"
)

// offsetKey stores the processed-output byte offset inside the agent's
// Config map so catch-up survives clishake restarts. Underscore prefix
// marks it as clishake-internal (not adapter config).
const offsetKey = "_log_offset"

// Poll runs one supervision cycle: consume new output, detect exits,
// schedule restarts. Cheap enough to call every second.
func (o *Orchestrator) Poll() {
	agents, err := o.Store.ListAgents()
	if err != nil {
		return
	}
	// Only trust pane state when the session actually answered our query.
	// clishake may be invoked from restricted contexts (e.g. an agent's
	// sandboxed shell) where the tmux server is unreachable — concluding
	// "pane disappeared" from there would falsely fail healthy agents.
	var panes []tmux.PaneInfo
	panesTrusted := false
	if o.Tmux.ServerAlive() && o.Tmux.HasSession(o.Cfg.SessionName()) {
		if got, err := o.Tmux.ListPanes(o.Cfg.SessionName()); err == nil {
			panes = got
			panesTrusted = true
		}
	}
	paneByID := map[string]tmux.PaneInfo{}
	for _, p := range panes {
		paneByID[p.PaneID] = p
	}
	for _, a := range agents {
		if a.Adapter == "observed" {
			continue // no process of our own to supervise
		}
		o.consumeOutput(a)
		if panesTrusted {
			o.checkProcess(a, paneByID)
			o.checkStartingReady(a, paneByID)
			// First deliveries (briefing, task) are usually deferred past
			// the readiness moment by the settle gate; retry each tick.
			o.deliverPending(a)
		}
		o.discoverSubagents(a)
	}
	if panesTrusted {
		o.DeliverQueued()
	}
	o.runDueRestarts()
}

// relayMaxAge caps how long a queued message keeps being retried before it is
// abandoned (the recipient is presumed unreachable).
const relayMaxAge = 10 * time.Minute

// deliverQueued delivers every message waiting for the supervisor: the ones an
// agent queued because its sandboxed `clishake send` has no terminal access
// (DeliveryPending), plus any left over from a genuinely failed direct
// delivery (DeliveryFailed). The supervisor is the single process that owns the
// terminals, so all peer delivery flows through here — enqueue then deliver —
// rather than each agent racing a pane write it cannot reliably complete.
//
// A queued message is retried on every tick until it is delivered, its
// recipient leaves (a terminal status), or it ages out; it is never dropped
// after a fixed number of tries. Messages to the lead never queue (they are
// DB-backed and delivered on Send). Delivery is idempotent-safe to retry: a
// failed attempt pastes nothing new, and the confirmed-submit path only reports
// success once the composer accepts the message.
func (o *Orchestrator) DeliverQueued() {
	// Only the supervisor process owns the terminals. An agent's own process
	// also runs Poll (via `clishake send`), but it cannot reach the panes, so
	// it must not try — it would just fail and re-queue.
	if os.Getenv("CLISHAKE_AGENT") != "" {
		return
	}
	var queued []*domain.Message
	for _, state := range []domain.DeliveryState{domain.DeliveryPending, domain.DeliveryFailed} {
		msgs, err := o.Store.ListMessagesByDelivery(state, 50)
		if err != nil {
			continue
		}
		queued = append(queued, msgs...)
	}
	// Nothing to deliver: return before doing any more work, so the fast
	// delivery tick that calls this every ~150ms is almost free (two indexed
	// reads) when the queue is empty. A genuinely unreachable terminal just
	// makes the deliver() below fail and the message stay queued.
	if len(queued) == 0 {
		return
	}
	for _, m := range queued {
		if m.Recipient == "" || m.Recipient == domain.LeadSender {
			continue
		}
		if time.Since(m.CreatedAt) > relayMaxAge {
			continue // presumed unreachable; stop retrying
		}
		a, err := o.Store.GetAgentByName(m.Recipient)
		if err != nil || a == nil {
			continue
		}
		if a.Status.IsTerminal() {
			continue // recipient is gone; it can never receive this
		}
		if !a.Status.IsLive() || a.Status == domain.StatusStarting {
			continue // not ready yet — leave queued and retry next tick
		}
		if err := o.deliver(a, *m); err != nil {
			continue // still undeliverable; stays queued for the next tick
		}
		m.Delivery = domain.DeliveryDelivered
		_ = o.Store.SaveMessage(m)
		o.emit(domain.EvMessageDelivered, m.Sender, m.ID, map[string]any{
			"recipient": m.Recipient, "queued": true,
		})
	}
}

// discoverEvery throttles per-agent sub-agent discovery scans.
const discoverEvery = 5 * time.Second

// discoverSubagents polls adapters that expose durable sub-agent/team
// artifacts (adapter.SubagentDiscoverer): new members are registered as
// observed sub-agents; previously discovered members that left the roster
// are marked completed.
func (o *Orchestrator) discoverSubagents(a *domain.Agent) {
	if !a.Status.IsLive() {
		return
	}
	ad, ok := o.Registry.Get(a.Adapter)
	if !ok {
		return
	}
	disc, ok := ad.(adapter.SubagentDiscoverer)
	if !ok {
		return
	}
	o.mu.Lock()
	if last, ok := o.discovered[a.ID]; ok && time.Since(last) < discoverEvery {
		o.mu.Unlock()
		return
	}
	o.discovered[a.ID] = time.Now()
	o.mu.Unlock()

	infos := disc.DiscoverSubagents(a)
	current := map[string]bool{}
	for i := range infos {
		current[a.Name+"/"+infos[i].Name] = true
		o.registerSubagent(a, &infos[i])
	}
	// Reap children that vanished from the roster. Only discoverer-backed
	// parents get this: wire-reported sub-agents (mock) manage their own
	// lifecycle through status lines.
	children, err := o.Store.ListAgents()
	if err != nil {
		return
	}
	for _, c := range children {
		if c.ParentID == a.ID && c.Adapter == "observed" && c.Status.IsLive() && !current[c.Name] {
			o.setStatus(c, domain.StatusCompleted, "left the team roster")
		}
	}
}

// checkStartingReady re-evaluates readiness for agents stuck in "starting"
// against the RENDERED pane screen. The piped stream only yields readiness
// when new output arrives — an agent whose composer appeared while the
// heuristic missed it (or before a clishake upgrade) would otherwise stay
// "starting" forever even though its screen plainly shows it is ready.
func (o *Orchestrator) checkStartingReady(a *domain.Agent, paneByID map[string]tmux.PaneInfo) {
	if a.Status != domain.StatusStarting || a.Tmux.PaneID == "" {
		return
	}
	p, ok := paneByID[a.Tmux.PaneID]
	if !ok || p.Dead {
		return
	}
	ad, okA := o.Registry.Get(a.Adapter)
	if !okA {
		return
	}
	screen, err := o.Tmux.CapturePane(a.Tmux.PaneID, 0)
	if err != nil {
		return
	}
	if ad.DetectReady(a, screen) {
		if a.Config == nil {
			a.Config = map[string]string{}
		}
		a.Config[readyAtKey] = strconv.FormatInt(time.Now().UnixMilli(), 10)
		o.setStatus(a, domain.StatusReady, "composer visible on rendered screen")
		o.emit(domain.EvAgentReady, a.Name, a.Name, nil)
		o.onAgentReady(a)
		return
	}
	// Not ready — the harness may be blocked on the one-time folder-trust
	// dialog. Every agent runs in its own worktree, so this would otherwise
	// prompt once per agent; clear it automatically (opt out per agent with
	// auto_trust=false).
	o.maybeAnswerTrust(a, screen)
}

// trustAnsweredKey guards against re-pressing Enter on a trust dialog while
// it is still on screen; cleared on respawn so a fresh worktree re-answers.
const trustAnsweredKey = "_trust_answered"

// maybeAnswerTrust accepts a folder-trust selection dialog (option 1 "Yes")
// with Enter. It fires only when a numbered selection dialog whose text
// mentions "trust" is on screen — never a generic dialog — so the choice is
// always the safe "trust this folder" for a worktree derived from the lead's
// own project.
func (o *Orchestrator) maybeAnswerTrust(a *domain.Agent, screen string) {
	if a.Config[trustAnsweredKey] == "1" || a.Tmux.PaneID == "" {
		return
	}
	if o.launchView(a).Config["auto_trust"] == "false" {
		return
	}
	if !trustDialogUp(ansi.Strip(screen)) {
		return
	}
	// The cursor defaults to option 1 ("Yes, trust"); Enter confirms it.
	if err := o.Tmux.SendKeys(a.Tmux.PaneID, "Enter"); err != nil {
		return
	}
	if a.Config == nil {
		a.Config = map[string]string{}
	}
	a.Config[trustAnsweredKey] = "1"
	_ = o.Store.SaveAgent(a)
	o.emit(domain.EvAgentStatusChanged, "clishake", a.Name, map[string]any{"auto_trusted": true})
}

// trustDialogUp reports whether the rendered (ANSI-stripped) screen shows a
// folder-trust selection dialog: a prompt-glyph cursor on a numbered menu
// entry, on a screen whose text mentions "trust".
func trustDialogUp(plain string) bool {
	if !strings.Contains(strings.ToLower(plain), "trust") {
		return false
	}
	for _, glyph := range []string{"❯", "›", ">", "┃"} {
		rest := plain
		for {
			i := strings.Index(rest, glyph)
			if i < 0 {
				break
			}
			rest = strings.TrimLeft(rest[i+len(glyph):], " ")
			d := 0
			for d < len(rest) && rest[d] >= '0' && rest[d] <= '9' {
				d++
			}
			if d > 0 && d < len(rest) && rest[d] == '.' {
				return true // "❯ 1." — a selection dialog cursor
			}
		}
	}
	return false
}

// RunSupervisor polls until ctx is done. overlapEvery controls how often
// cross-agent file-overlap detection runs (0 disables it).
func (o *Orchestrator) RunSupervisor(ctx context.Context, pollEvery, overlapEvery time.Duration) {
	if pollEvery <= 0 {
		pollEvery = time.Second
	}
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	var overlapTick <-chan time.Time
	if overlapEvery > 0 {
		t := time.NewTicker(overlapEvery)
		defer t.Stop()
		overlapTick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			o.Poll()
		case <-overlapTick:
			_, _ = o.DetectOverlaps()
		}
	}
}

// consumeOutput reads newly appended pane output and applies the adapter's
// parsed events.
func (o *Orchestrator) consumeOutput(a *domain.Agent) {
	ad, ok := o.Registry.Get(a.Adapter)
	if !ok {
		return
	}
	logPath := filepath.Join(o.AgentDir(a), "output.log")
	fi, err := os.Stat(logPath)
	if err != nil {
		return
	}
	offset := int64(0)
	if a.Config != nil {
		if v, ok := a.Config[offsetKey]; ok {
			offset, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if fi.Size() < offset {
		offset = 0 // log truncated/rotated; reprocess from start
	}
	if fi.Size() == offset {
		return
	}
	f, err := os.Open(logPath)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(offset, 0); err != nil {
		return
	}
	buf := make([]byte, fi.Size()-offset)
	n, _ := f.Read(buf)
	chunk := string(buf[:n])
	newOffset := offset + int64(n)

	if a.Config == nil {
		a.Config = map[string]string{}
	}
	a.Config[offsetKey] = strconv.FormatInt(newOffset, 10)
	a.LastActivity = time.Now().UTC()
	_ = o.Store.SaveAgent(a)

	if a.Status == domain.StatusStarting && ad.DetectReady(a, chunk) {
		a.Config[readyAtKey] = strconv.FormatInt(time.Now().UnixMilli(), 10)
		o.setStatus(a, domain.StatusReady, "adapter detected readiness")
		o.emit(domain.EvAgentReady, a.Name, a.Name, nil)
		o.onAgentReady(a)
	}
	for _, ev := range ad.ParseOutput(a, chunk) {
		o.applyParsed(a, ev)
	}
}

// applyParsed maps one adapter-parsed event onto orchestration state.
func (o *Orchestrator) applyParsed(a *domain.Agent, ev adapter.ParsedEvent) {
	switch ev.Kind {
	case adapter.KindStatus:
		if ev.Status != "" {
			o.setStatus(a, ev.Status, "agent reported")
		}
	case adapter.KindMessage:
		o.handleAgentMessage(a, ev.To, ev.Text)
	case adapter.KindTask:
		o.applyTaskReport(a, ev)
	case adapter.KindSubagent:
		o.registerSubagent(a, ev.Sub)
	case adapter.KindApproval:
		o.createApproval(a, ev)
	case adapter.KindLog:
		o.emit(domain.EvAgentStatusChanged, a.Name, a.Name, map[string]any{
			"log": truncate(ev.Text, 300),
		})
	}
}

// applyTaskReport applies an agent's task progress/completion report.
func (o *Orchestrator) applyTaskReport(a *domain.Agent, ev adapter.ParsedEvent) {
	if ev.TaskID == "" {
		return
	}
	status := domain.TaskStatus(ev.Fields["status"])
	if status == "" {
		status = domain.TaskInProgress
	}
	// An agent reporting completion implies it was working on the task:
	// step assigned tasks through in_progress so the report isn't rejected
	// by the state machine.
	if status == domain.TaskCompleted {
		if t, _ := o.Tasks.Get(ev.TaskID); t != nil &&
			(t.Status == domain.TaskAssigned || t.Status == domain.TaskBacklog) {
			_, _ = o.Tasks.SetStatus(a.Name, ev.TaskID, domain.TaskInProgress, "")
		}
	}
	if _, err := o.Tasks.SetStatus(a.Name, ev.TaskID, status, ev.Text); err != nil {
		o.emit(domain.EvTaskUpdated, a.Name, ev.TaskID, map[string]any{
			"error": err.Error(), "requested_status": string(status),
		})
	}
}

// checkProcess detects exits via dead panes / missing panes / dead PIDs.
// Pane existence is NOT proof of health: we verify the PID too.
func (o *Orchestrator) checkProcess(a *domain.Agent, paneByID map[string]tmux.PaneInfo) {
	if !a.Status.IsLive() {
		return
	}
	if a.Tmux.PaneID == "" {
		o.setStatus(a, domain.StatusUnknown, "live status but no pane recorded")
		return
	}
	p, ok := paneByID[a.Tmux.PaneID]
	if !ok {
		// Window/pane gone entirely (killed externally or server death).
		o.recordExit(a, -1, "pane disappeared")
		return
	}
	if p.Dead {
		o.recordExit(a, p.DeadStatus, "process exited")
		return
	}
	if a.PID > 0 && !processAlive(a.PID) {
		// remain-on-exit should make this rare; belt and suspenders.
		o.recordExit(a, -1, "pid not alive")
	}
}

// recordExit distinguishes intentional stops from crashes, emits the exit
// event, and applies the restart policy for crashes.
func (o *Orchestrator) recordExit(a *domain.Agent, exitCode int, why string) {
	o.mu.Lock()
	intentional := o.stopping[a.ID]
	delete(o.stopping, a.ID)
	o.mu.Unlock()

	a.ExitCode = &exitCode
	o.emit(domain.EvAgentExited, "clishake", a.Name, map[string]any{
		"exit_code": exitCode, "reason": why, "intentional": intentional,
	})
	switch {
	case intentional:
		o.setStatus(a, domain.StatusStopped, "intentional stop")
	case exitCode == 0:
		o.setStatus(a, domain.StatusCompleted, "exited cleanly")
	default:
		o.setStatus(a, domain.StatusFailed, fmt.Sprintf("exit code %d (%s)", exitCode, why))
		o.maybeScheduleRestart(a)
	}
}

// maybeScheduleRestart applies the configured restart policy with
// crash-loop protection and exponential backoff.
func (o *Orchestrator) maybeScheduleRestart(a *domain.Agent) {
	pol := o.Cfg.Defaults.Restart
	if pol.Mode != "on-failure" && pol.Mode != "always" {
		return
	}
	now := time.Now()
	window := time.Duration(pol.WindowSec) * time.Second
	if window <= 0 {
		window = 5 * time.Minute
	}
	o.mu.Lock()
	hist := append(o.history[a.ID], now)
	recent := hist[:0]
	for _, t := range hist {
		if now.Sub(t) <= window {
			recent = append(recent, t)
		}
	}
	o.history[a.ID] = recent
	count := len(recent)
	max := pol.MaxRestarts
	if max <= 0 {
		max = 3
	}
	if count > max {
		o.mu.Unlock()
		o.emit(domain.EvAgentStatusChanged, "clishake", a.Name, map[string]any{
			"crash_loop": true, "failures_in_window": count,
		})
		return // stay failed; human intervention required
	}
	backoff := time.Duration(pol.BackoffSec) * time.Second
	if backoff <= 0 {
		backoff = 2 * time.Second
	}
	for i := 1; i < count; i++ {
		backoff *= 2
	}
	o.restarts[a.ID] = now.Add(backoff)
	o.mu.Unlock()
}

// runDueRestarts respawns agents whose backoff has elapsed.
func (o *Orchestrator) runDueRestarts() {
	o.mu.Lock()
	var due []string
	now := time.Now()
	for id, t := range o.restarts {
		if now.After(t) {
			due = append(due, id)
			delete(o.restarts, id)
		}
	}
	o.mu.Unlock()
	for _, id := range due {
		a, err := o.Store.GetAgent(id)
		if err != nil || a == nil || a.Status != domain.StatusFailed {
			continue
		}
		if restarted, err := o.respawn(a); err == nil {
			restarted.RestartCount++
			_ = o.Store.SaveAgent(restarted)
			o.emit(domain.EvAgentRestarted, "clishake", restarted.Name, map[string]any{
				"restart_count": restarted.RestartCount, "automatic": true,
			})
		}
	}
}

// Reconcile aligns persisted agent state with live tmux reality. Called on
// open/reattach. Missing panes for live agents -> disconnected (or exited
// if pane died while we were away); orphan panes are reported.
func (o *Orchestrator) Reconcile() (report []string, err error) {
	agents, err := o.Store.ListAgents()
	if err != nil {
		return nil, err
	}
	var panes []tmux.PaneInfo
	serverUp := o.Tmux.ServerAlive() && o.Tmux.HasSession(o.Cfg.SessionName())
	if serverUp {
		panes, err = o.Tmux.ListPanes(o.Cfg.SessionName())
		if err != nil {
			return nil, err
		}
	}
	paneByID := map[string]tmux.PaneInfo{}
	for _, p := range panes {
		paneByID[p.PaneID] = p
	}
	known := map[string]bool{}
	for _, a := range agents {
		if a.Adapter == "observed" {
			continue
		}
		if a.Tmux.PaneID != "" {
			known[a.Tmux.PaneID] = true
		}
		if !a.Status.IsLive() {
			continue
		}
		p, ok := paneByID[a.Tmux.PaneID]
		switch {
		case !serverUp || !ok:
			o.setStatus(a, domain.StatusDisconnected, "no live pane after reattach")
			report = append(report, fmt.Sprintf("%s: marked disconnected (pane missing)", a.Name))
		case p.Dead:
			o.recordExit(a, p.DeadStatus, "found dead on reconcile")
			report = append(report, fmt.Sprintf("%s: exited while detached (code %d)", a.Name, p.DeadStatus))
		default:
			if a.PID != p.PanePID && p.PanePID > 0 {
				a.PID = p.PanePID
				_ = o.Store.SaveAgent(a)
			}
			line := fmt.Sprintf("%s: alive (pane %s, pid %d)", a.Name, p.PaneID, p.PanePID)
			if a.Status == domain.StatusStarting && time.Since(a.LastActivity) > time.Minute {
				// A harness stuck at a first-run dialog (folder trust,
				// login, ...) produces no output and never turns ready.
				line += fmt.Sprintf(" — ⚠ still starting after %s; attach with `clishake agent focus %s`: the harness may be waiting on a first-run dialog",
					time.Since(a.LastActivity).Round(time.Second), a.Name)
			}
			report = append(report, line)
		}
	}
	for _, p := range panes {
		if !known[p.PaneID] && p.WindowName != "" && p.WindowName != "bash" && p.WindowName != "zsh" {
			report = append(report, fmt.Sprintf("orphan pane %s (window %q) not owned by any agent", p.PaneID, p.WindowName))
		}
	}
	o.emit(domain.EvSessionAttached, "lead", o.Session.ID, map[string]any{"reconciled": len(report)})
	o.touchSession()
	return report, nil
}

// processAlive reports whether pid exists (signal 0). EPERM means the
// process exists but we may not signal it (e.g. probing from a sandboxed
// context) — that is alive, not dead.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	sigErr := p.Signal(syscall.Signal(0))
	return sigErr == nil || errors.Is(sigErr, syscall.EPERM)
}
