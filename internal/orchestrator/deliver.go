package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/clishakehq/clishake/internal/adapter"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/messaging"
	"github.com/clishakehq/clishake/internal/wire"
)

// Send routes a message from any sender (the lead or an agent) to a
// selector. Messages addressed to the lead ("@lead" or "lead") are
// persisted as delivered immediately — the lead reads them in the UI and
// history rather than via a terminal.
func (o *Orchestrator) Send(sender, selector, body string, opts messaging.SendOpts) ([]*domain.Message, error) {
	if sender != domain.LeadSender {
		if a, _ := o.Store.GetAgentByName(sender); a != nil && !a.Permissions.SendMessages {
			return nil, fmt.Errorf("agent %q lacks the send_messages permission", sender)
		}
	}
	target := strings.TrimPrefix(strings.TrimSpace(selector), "@")
	if target == domain.LeadSender {
		m := &domain.Message{
			ID:        domain.NewID("msg"),
			Sender:    sender,
			Selector:  "@" + domain.LeadSender,
			Recipient: domain.LeadSender,
			Type:      nonEmptyType(opts.Type),
			Body:      body,
			TaskID:    opts.TaskID,
			ReplyTo:   opts.ReplyTo,
			Delivery:  domain.DeliveryDelivered,
			Meta:      opts.Meta,
			CreatedAt: time.Now().UTC(),
		}
		if err := o.Store.SaveMessage(m); err != nil {
			return nil, err
		}
		o.emit(domain.EvMessageDelivered, sender, m.ID, map[string]any{
			"recipient": domain.LeadSender, "body": truncate(body, 200),
		})
		return []*domain.Message{m}, nil
	}
	agents, err := o.Store.ListAgents()
	if err != nil {
		return nil, err
	}
	return o.Router.Send(agents, sender, selector, body, opts)
}

func nonEmptyType(t domain.MessageType) domain.MessageType {
	if t == "" {
		return domain.MsgChat
	}
	return t
}

// Broadcast sends to every live agent.
func (o *Orchestrator) Broadcast(sender, body string) ([]*domain.Message, error) {
	return o.Send(sender, "@all", body, messaging.SendOpts{})
}

// deliver implements messaging.Deliverer: it hands one message to one
// agent through that agent's adapter (inbox file or typed keys).
func (o *Orchestrator) deliver(a *domain.Agent, m domain.Message) error {
	ad, ok := o.Registry.Get(a.Adapter)
	if !ok {
		return fmt.Errorf("adapter %q not registered", a.Adapter)
	}
	if !a.Status.IsLive() {
		return fmt.Errorf("agent %q is %s", a.Name, a.Status)
	}
	if a.Status == domain.StatusStarting && ad.InputMode() == adapter.InputSendKeys {
		// Typed input into a TUI that has not finished booting is silently
		// swallowed. Fail the delivery honestly so it can be resent.
		return fmt.Errorf("agent %q is still starting; retry when ready", a.Name)
	}
	payload, err := ad.FormatInput(a, m)
	if err != nil {
		return err
	}
	switch ad.InputMode() {
	case adapter.InputFile:
		if err := os.MkdirAll(o.AgentDir(a), 0o755); err != nil {
			return err
		}
		inbox := filepath.Join(o.AgentDir(a), "inbox.jsonl")
		f, err := os.OpenFile(inbox, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := f.WriteString(payload + "\n"); err != nil {
			return err
		}
		return nil
	case adapter.InputSendKeys:
		if a.Tmux.PaneID == "" {
			return fmt.Errorf("agent %q has no pane", a.Name)
		}
		// Typed input must be a single line: a literal newline submits the
		// composer mid-message in every TUI harness.
		payload = strings.Join(strings.Fields(strings.ReplaceAll(payload, "\n", " ")), " ")
		// Deliver as a bracketed paste (atomic — per-key injection races
		// some TUIs' input handling), pause, then submit. The pause scales
		// with payload size: TUIs process a paste asynchronously and drop
		// an Enter that arrives while it is still being ingested (observed
		// live: OpenCode kept a ~1800-char briefing unsubmitted at 400ms).
		if err := o.Tmux.PasteText(a.Tmux.PaneID, payload); err != nil {
			return err
		}
		delay := o.enterDelay(a) + time.Duration(min(len(payload), 4000))*time.Millisecond/2
		time.Sleep(delay)
		return o.submitComposer(a.Tmux.PaneID)
	default:
		return fmt.Errorf("adapter %q: unknown input mode", a.Adapter)
	}
}

// submitConfirmTries bounds how many times submitComposer re-sends Enter
// when the composer shows no reaction; submitConfirmWait is the pause
// between an Enter and re-checking the pane.
const (
	submitConfirmTries = 4
	submitConfirmWait  = 250 * time.Millisecond
)

// submitComposer presses Enter to submit the just-pasted text, then confirms
// the composer actually accepted it — instead of firing one Enter and hoping.
//
// A bracketed paste is ingested asynchronously; a single Enter can arrive
// before the TUI has finished reading the paste and is silently dropped,
// leaving the message sitting unsubmitted in the composer (observed with the
// Copilot and OpenCode CLIs). We snapshot the settled pane, send Enter, and if
// nothing on screen changed — the reliable "that keystroke did nothing" signal
// for an otherwise-idle composer — resend Enter and re-check, a few times.
//
// Re-sending Enter is safe where re-pasting is NOT: an empty composer ignores a
// stray Enter, whereas a second paste would double the text. So this retries
// the keypress only and never re-pastes; a genuinely failed delivery is left to
// the supervisor, which redelivers the whole message from scratch.
func (o *Orchestrator) submitComposer(paneID string) error {
	before, _ := o.Tmux.CapturePane(paneID, 0)
	if err := o.Tmux.SendKeys(paneID, "Enter"); err != nil {
		return err
	}
	for i := 0; i < submitConfirmTries; i++ {
		time.Sleep(submitConfirmWait)
		after, err := o.Tmux.CapturePane(paneID, 0)
		if err != nil {
			return nil // can't verify — assume the Enter landed
		}
		if after != before {
			return nil // the composer reacted: submitted (or now working)
		}
		// The pane is unchanged: the Enter was dropped (it raced the paste).
		// Re-send it; a no-op on an empty composer, a submit on a full one.
		if err := o.Tmux.SendKeys(paneID, "Enter"); err != nil {
			return err
		}
	}
	return nil // best effort; never re-paste (that would double the text)
}

// enterDelay is the pause between typed text and the Enter keypress for
// send-keys delivery. Override per agent or per adapter with the
// enter_delay_ms config option.
func (o *Orchestrator) enterDelay(a *domain.Agent) time.Duration {
	if v := o.launchView(a).Config["enter_delay_ms"]; v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms >= 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return 400 * time.Millisecond
}

// taskDeliveredKey marks that the agent's initial task was sent to it;
// briefedKey marks that the post-ready session briefing was. Both are
// cleared on respawn: a restarted harness is a fresh process with no
// memory of either.
const (
	taskDeliveredKey = "_task_delivered"
	briefedKey       = "_briefed"
	// restartedKey is set on respawn so the next briefing tells the agent
	// it was restarted (fresh process, no memory) instead of re-issuing
	// its initial task.
	restartedKey = "_restarted"
)

// readyAtKey records (unix millis) when the agent last turned ready, so
// first deliveries can wait out the settle period below.
const readyAtKey = "_ready_at"

// onAgentReady runs at the readiness transition; deliverPending is also
// retried from every Poll tick, because the settle gate usually defers the
// first delivery past this moment.
func (o *Orchestrator) onAgentReady(a *domain.Agent) {
	o.deliverPending(a)
}

// deliverPending sends whatever the agent is still owed — session briefing
// (for adapters without launch-time briefing), then its assigned task —
// once the agent is ready AND has settled. Settling matters: a TUI's
// composer is often DRAWN before its input handlers attach, and text typed
// into that gap is silently discarded (observed live with Copilot CLI and
// Antigravity: briefings vanished without a trace). Override per adapter
// or agent with settle_ms.
func (o *Orchestrator) deliverPending(a *domain.Agent) {
	if a.Adapter == "observed" || a.Status == domain.StatusStarting || !a.Status.IsLive() {
		return
	}
	if !o.settled(a) {
		return
	}
	o.deliverBriefing(a)
	o.deliverInitialTask(a)
	// The restart note (if any) has now been delivered — at launch for
	// launch-briefed adapters, or via deliverBriefing for the rest.
	if a.Config[restartedKey] == "1" {
		delete(a.Config, restartedKey)
		_ = o.Store.SaveAgent(a)
	}
}

// settled reports whether the post-ready settle period has elapsed.
func (o *Orchestrator) settled(a *domain.Agent) bool {
	v := a.Config[readyAtKey]
	if v == "" {
		return true
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return true
	}
	return time.Since(time.UnixMilli(ms)) >= o.settleDelay(a)
}

// settleDelay is how long after readiness the first typed delivery waits
// (config option settle_ms; default 2500ms).
func (o *Orchestrator) settleDelay(a *domain.Agent) time.Duration {
	if v := o.launchView(a).Config["settle_ms"]; v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms >= 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return 2500 * time.Millisecond
}

// deliverBriefing sends the session briefing as a message to agents whose
// adapter cannot inject it at launch (adapter.LaunchBriefer absent or
// false). Launch arguments are not an option for those harnesses — any
// first-run dialog swallows them — and a post-ready message also lands in
// the audit log.
func (o *Orchestrator) deliverBriefing(a *domain.Agent) {
	if a.Config[briefedKey] == "1" {
		return
	}
	ad, ok := o.Registry.Get(a.Adapter)
	if !ok {
		return
	}
	if lb, ok := ad.(adapter.LaunchBriefer); ok && lb.BriefsAtLaunch() {
		return
	}
	if a.Config == nil {
		a.Config = map[string]string{}
	}
	if _, err := o.Send("clishake", "@"+a.Name, o.briefing(a), messaging.SendOpts{Type: domain.MsgControl}); err != nil {
		return // retried on the next ready transition
	}
	a.Config[briefedKey] = "1"
	_ = o.Store.SaveAgent(a)
}

// deliverInitialTask sends the agent its assigned task as the first routed
// message once it is ready. Tasks are deliberately NOT passed as launch
// prompt arguments for interactive harnesses: any first-run dialog (folder
// trust, login) swallows launch arguments, whereas a message delivered
// after readiness is confirmed also lands in the audit log with
// attribution.
func (o *Orchestrator) deliverInitialTask(a *domain.Agent) {
	if a.Task == "" || a.Config[taskDeliveredKey] == "1" {
		return
	}
	if a.Config == nil {
		a.Config = map[string]string{}
	}
	body := "Your assigned task: " + a.Task
	if _, err := o.Send(domain.LeadSender, "@"+a.Name, body, messaging.SendOpts{Type: domain.MsgTask, TaskID: a.TaskID}); err != nil {
		return // stay undelivered; next ready transition retries
	}
	a.Config[taskDeliveredKey] = "1"
	_ = o.Store.SaveAgent(a)
}

// handleAgentMessage routes a structured message emitted by an agent
// (KindMessage parsed from its output).
func (o *Orchestrator) handleAgentMessage(from *domain.Agent, to, text string) {
	if to == "" || to == "clishake" {
		// Replies to system notifications (sender "clishake") go to the
		// lead's history — there is no clishake terminal to deliver to.
		to = domain.LeadSender
	}
	if _, err := o.Send(from.Name, to, text, messaging.SendOpts{}); err != nil {
		o.emit(domain.EvMessageFailed, from.Name, "", map[string]any{
			"to": to, "error": err.Error(), "body": truncate(text, 200),
		})
	}
}

// registerSubagent creates or updates an observed sub-agent under parent.
// Observed sub-agents have no pane/PID of their own; they exist so the
// hierarchy is visible and auditable.
func (o *Orchestrator) registerSubagent(parent *domain.Agent, info *adapter.SubagentInfo) {
	if info == nil || info.Name == "" {
		return
	}
	childName := parent.Name + "/" + info.Name
	existing, _ := o.Store.GetAgentByName(childName)
	status := info.Status
	if status == "" {
		status = domain.StatusRunning
	}
	if existing != nil {
		o.setStatus(existing, status, "reported by parent")
		return
	}
	child := &domain.Agent{
		ID:           domain.NewID("ag"),
		Name:         childName,
		Role:         info.Role,
		Adapter:      "observed",
		ParentID:     parent.ID,
		Team:         parent.Team,
		Status:       status,
		WorkDir:      parent.WorkDir,
		CreatedAt:    time.Now().UTC(),
		LastActivity: time.Now().UTC(),
	}
	if err := o.Store.SaveAgent(child); err != nil {
		return
	}
	o.emit(domain.EvSubagentDiscovered, parent.Name, childName, map[string]any{
		"role": info.Role, "status": string(status),
	})
}

// ReadInboxTail returns the last n lines of an agent's inbox (diagnostics).
func (o *Orchestrator) ReadInboxTail(a *domain.Agent, n int) []wire.Envelope {
	b, err := os.ReadFile(filepath.Join(o.AgentDir(a), "inbox.jsonl"))
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	var out []wire.Envelope
	for _, l := range lines {
		if e, ok := wire.DecodeEnvelope(l); ok {
			out = append(out, e)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
