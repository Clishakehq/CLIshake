// Package domain holds clishake's core types: agents, tasks, messages,
// events, capabilities, and their state machines. It has no dependencies on
// other clishake packages so every layer can import it freely.
package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// IDs
// ---------------------------------------------------------------------------

// NewID returns a short random identifier with the given prefix, e.g.
// NewID("ag") -> "ag_3f9c2a1b".
func NewID(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Agent
// ---------------------------------------------------------------------------

// AgentStatus is the lifecycle state of a managed agent.
type AgentStatus string

const (
	StatusStarting         AgentStatus = "starting"
	StatusReady            AgentStatus = "ready"
	StatusRunning          AgentStatus = "running"
	StatusWaiting          AgentStatus = "waiting"
	StatusBlocked          AgentStatus = "blocked"
	StatusAwaitingApproval AgentStatus = "awaiting_approval"
	StatusCompleted        AgentStatus = "completed"
	StatusFailed           AgentStatus = "failed"
	StatusStopped          AgentStatus = "stopped"
	StatusDisconnected     AgentStatus = "disconnected"
	StatusUnknown          AgentStatus = "unknown"
)

// IsTerminal reports whether the status is an end state (process not running).
func (s AgentStatus) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusStopped:
		return true
	}
	return false
}

// IsLive reports whether the agent is expected to have a running process.
func (s AgentStatus) IsLive() bool {
	switch s {
	case StatusStarting, StatusReady, StatusRunning, StatusWaiting,
		StatusBlocked, StatusAwaitingApproval:
		return true
	}
	return false
}

// TmuxRef locates an agent's terminal within the clishake tmux server.
type TmuxRef struct {
	Session string `json:"session"` // e.g. "clishake-myproj"
	Window  string `json:"window"`  // window name, e.g. "builder"
	PaneID  string `json:"pane_id"` // tmux unique pane id, e.g. "%3"
}

// Permissions is the per-agent permission profile. Enforcement depends on the
// adapter; clishake records intent and gates its own operations (e.g. message
// sending, worktree assignment) on these flags.
type Permissions struct {
	ReadFiles      bool `json:"read_files" toml:"read_files"`
	ModifyFiles    bool `json:"modify_files" toml:"modify_files"`
	RunCommands    bool `json:"run_commands" toml:"run_commands"`
	NetworkAccess  bool `json:"network_access" toml:"network_access"`
	UseGit         bool `json:"use_git" toml:"use_git"`
	CommitChanges  bool `json:"commit_changes" toml:"commit_changes"`
	MergeChanges   bool `json:"merge_changes" toml:"merge_changes"`
	DeleteFiles    bool `json:"delete_files" toml:"delete_files"`
	ModifyConfig   bool `json:"modify_config" toml:"modify_config"`
	SpawnSubagents bool `json:"spawn_subagents" toml:"spawn_subagents"`
	SendMessages   bool `json:"send_messages" toml:"send_messages"`
	AccessSecrets  bool `json:"access_secrets" toml:"access_secrets"`
	OutsideProject bool `json:"outside_project" toml:"outside_project"`
}

// DefaultPermissions is a reasonable profile for a working agent: it can
// read, edit, run, and message, but not merge, delete, touch secrets, or
// leave the project.
func DefaultPermissions() Permissions {
	return Permissions{
		ReadFiles:      true,
		ModifyFiles:    true,
		RunCommands:    true,
		UseGit:         true,
		CommitChanges:  true,
		SpawnSubagents: true,
		SendMessages:   true,
	}
}

// Agent is the durable record for one managed participant.
type Agent struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Role         string            `json:"role"`
	Adapter      string            `json:"adapter"` // adapter name, e.g. "mock", "claude-code"
	ParentID     string            `json:"parent_id,omitempty"`
	Team         string            `json:"team,omitempty"`
	Task         string            `json:"task,omitempty"` // free-text current task description
	TaskID       string            `json:"task_id,omitempty"`
	Status       AgentStatus       `json:"status"`
	Tmux         TmuxRef           `json:"tmux"`
	PID          int               `json:"pid,omitempty"`
	WorkDir      string            `json:"work_dir"`
	Branch       string            `json:"branch,omitempty"`
	Capabilities []Capability      `json:"capabilities,omitempty"`
	Permissions  Permissions       `json:"permissions"`
	Config       map[string]string `json:"config,omitempty"` // adapter-specific settings
	CreatedAt    time.Time         `json:"created_at"`
	LastActivity time.Time         `json:"last_activity"`
	RestartCount int               `json:"restart_count"`
	ExitCode     *int              `json:"exit_code,omitempty"` // last observed exit code
	Health       string            `json:"health,omitempty"`    // freeform health note
}

// Config keys clishake maintains on an Agent (underscore-prefixed keys are
// clishake-internal, not adapter config). ConfigModel is the launch-time model
// the user chose; the others are read live from the harness status line.
const (
	ConfigModel     = "model"
	ConfigLiveModel = "_live_model"
	ConfigUsage     = "_usage"
)

// LiveModel returns the model the agent is running: the live model read from
// its status line when known, otherwise the launch-time model the user chose.
func (a *Agent) LiveModel() string {
	if m := a.Config[ConfigLiveModel]; m != "" {
		return m
	}
	return a.Config[ConfigModel]
}

// Usage returns the short usage/context note last read from the agent's status
// line (empty when the harness doesn't report one).
func (a *Agent) Usage() string { return a.Config[ConfigUsage] }

// ---------------------------------------------------------------------------
// Adapter capabilities
// ---------------------------------------------------------------------------

// Capability names a harness feature an adapter can genuinely provide.
// clishake must not offer UI affordances for capabilities the active adapter
// does not declare (unless a documented fallback exists).
type Capability string

const (
	CapStructuredInput    Capability = "structured_input"
	CapStructuredOutput   Capability = "structured_output"
	CapSubagents          Capability = "subagents"
	CapAgentTeams         Capability = "agent_teams"
	CapTaskEvents         Capability = "task_events"
	CapToolEvents         Capability = "tool_events"
	CapRuntimeReconfig    Capability = "runtime_reconfiguration"
	CapGracefulInterrupt  Capability = "graceful_interrupt"
	CapSessionResume      Capability = "session_resume"
	CapWorkdirOverride    Capability = "working_directory_override"
	CapPermissionControls Capability = "permission_controls"
)

// HasCapability reports whether c appears in caps.
func HasCapability(caps []Capability, c Capability) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

// MessageType distinguishes freeform chat from structured control traffic.
// Mirrors the layered encoding observed in the Claude teams mailbox protocol:
// control payloads ride inside ordinary message envelopes.
type MessageType string

const (
	MsgChat     MessageType = "chat"
	MsgControl  MessageType = "control"
	MsgStatus   MessageType = "status"
	MsgTask     MessageType = "task"
	MsgApproval MessageType = "approval"
)

// DeliveryState tracks a message through the routing pipeline.
type DeliveryState string

const (
	DeliveryPending   DeliveryState = "pending"
	DeliveryDelivered DeliveryState = "delivered"
	DeliveryFailed    DeliveryState = "failed"
)

// Message is one structured envelope on the clishake bus. Sender/recipients
// are agent names, or the reserved sender "lead" for the human team lead.
// Selector preserves the original addressing expression (e.g. "@reviewers").
type Message struct {
	ID        string            `json:"id"`
	Sender    string            `json:"sender"`
	Selector  string            `json:"selector"`            // original selector: "@name", "@role:x", "@all", ...
	Recipient string            `json:"recipient,omitempty"` // resolved concrete recipient (per delivery)
	Type      MessageType       `json:"type"`
	Body      string            `json:"body"`
	TaskID    string            `json:"task_id,omitempty"`
	ReplyTo   string            `json:"reply_to,omitempty"`
	Delivery  DeliveryState     `json:"delivery"`
	Meta      map[string]string `json:"meta,omitempty"` // harness-specific metadata
	CreatedAt time.Time         `json:"created_at"`
}

// LeadSender is the reserved sender name for the human team lead.
const LeadSender = "lead"

// AgentNameHint is the human-readable naming rule, reused in help text,
// dashboard usage, and validation errors so they never drift.
const AgentNameHint = "letters, digits, '-' and '_' (no spaces)"

// ValidAgentName enforces addressable agent names: letters (either case),
// digits, '-', '_', and not a reserved word. Names must survive as git branch
// segments and @selectors, so spaces and punctuation are disallowed, but a
// human-friendly name like "Jean-Pierre" is fine. Name matching elsewhere is
// case-insensitive (see GetAgentByName and the messaging resolver), so this
// is the single source of truth — the orchestrator enforces it and the
// natural-language front-end (internal/ask) pre-checks against it.
func ValidAgentName(n string) error {
	if n == "" {
		return fmt.Errorf("agent name required")
	}
	for _, reserved := range []string{LeadSender, "all", "team", "role"} {
		if strings.EqualFold(n, reserved) {
			return fmt.Errorf("agent name %q is reserved", n)
		}
	}
	for _, r := range n {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("agent name %q: use %s", n, AgentNameHint)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Tasks
// ---------------------------------------------------------------------------

// TaskStatus is the coordination state of a tracked task.
type TaskStatus string

const (
	TaskBacklog    TaskStatus = "backlog"
	TaskAssigned   TaskStatus = "assigned"
	TaskInProgress TaskStatus = "in_progress"
	TaskBlocked    TaskStatus = "blocked"
	TaskInReview   TaskStatus = "in_review"
	TaskCompleted  TaskStatus = "completed"
	TaskCancelled  TaskStatus = "cancelled"
)

// Task is one unit of coordinated work.
type Task struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Description  string     `json:"description,omitempty"`
	Owner        string     `json:"owner,omitempty"` // agent name
	Contributors []string   `json:"contributors,omitempty"`
	Status       TaskStatus `json:"status"`
	Priority     int        `json:"priority"` // 0 = default; higher = more urgent
	DependsOn    []string   `json:"depends_on,omitempty"`
	Files        []string   `json:"files,omitempty"`
	Branch       string     `json:"branch,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	Summary      string     `json:"summary,omitempty"` // completion summary
}

// ---------------------------------------------------------------------------
// Approvals
// ---------------------------------------------------------------------------

// ApprovalState tracks an approval request lifecycle.
type ApprovalState string

const (
	ApprovalPending ApprovalState = "pending"
	ApprovalGranted ApprovalState = "granted"
	ApprovalDenied  ApprovalState = "denied"
)

// Approval is a request from an agent for permission to perform a risky
// operation.
type Approval struct {
	ID        string        `json:"id"`
	AgentName string        `json:"agent_name"`
	Action    string        `json:"action"`
	Command   string        `json:"command,omitempty"`
	Reason    string        `json:"reason,omitempty"`
	Resources []string      `json:"resources,omitempty"`
	Risk      string        `json:"risk,omitempty"` // low|medium|high
	State     ApprovalState `json:"state"`
	CreatedAt time.Time     `json:"created_at"`
	DecidedAt *time.Time    `json:"decided_at,omitempty"`
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// EventType names an auditable state change.
type EventType string

const (
	EvAgentCreated       EventType = "agent.created"
	EvAgentStarted       EventType = "agent.started"
	EvAgentReady         EventType = "agent.ready"
	EvAgentStatusChanged EventType = "agent.status_changed"
	EvAgentExited        EventType = "agent.exited"
	EvAgentRestarted     EventType = "agent.restarted"
	EvAgentRemoved       EventType = "agent.removed"
	EvSubagentDiscovered EventType = "agent.subagent_discovered"
	EvMessageSent        EventType = "message.sent"
	EvMessageDelivered   EventType = "message.delivered"
	EvMessageFailed      EventType = "message.failed"
	EvTaskCreated        EventType = "task.created"
	EvTaskAssigned       EventType = "task.assigned"
	EvTaskUpdated        EventType = "task.updated"
	EvFileChanged        EventType = "repo.file_changed"
	EvBranchChanged      EventType = "repo.branch_changed"
	EvConflictDetected   EventType = "repo.conflict_detected"
	EvApprovalRequested  EventType = "approval.requested"
	EvApprovalGranted    EventType = "approval.granted"
	EvApprovalDenied     EventType = "approval.denied"
	EvSessionCreated     EventType = "session.created"
	EvSessionAttached    EventType = "session.attached"
	EvSessionDetached    EventType = "session.detached"
	EvConfigChanged      EventType = "config.changed"
	EvNoteAdded          EventType = "note.added"
)

// Event is one append-only audit record. Actor is who caused it ("lead", an
// agent name, or "clishake" for the supervisor itself); Subject is what it
// happened to (an agent name, task ID, message ID, ...).
type Event struct {
	ID            string         `json:"id"`
	Type          EventType      `json:"type"`
	Timestamp     time.Time      `json:"ts"`
	Actor         string         `json:"actor"`
	Subject       string         `json:"subject,omitempty"`
	SessionID     string         `json:"session_id"`
	Payload       map[string]any `json:"payload,omitempty"`
	CorrelationID string         `json:"correlation_id,omitempty"`
}

// NewEvent builds an event with a fresh ID and current timestamp.
func NewEvent(sessionID string, t EventType, actor, subject string, payload map[string]any) Event {
	return Event{
		ID:        NewID("ev"),
		Type:      t,
		Timestamp: time.Now().UTC(),
		Actor:     actor,
		Subject:   subject,
		SessionID: sessionID,
		Payload:   payload,
	}
}

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

// Session identifies one clishake orchestration session for a project.
type Session struct {
	ID          string    `json:"id"`
	ProjectPath string    `json:"project_path"`
	TmuxSession string    `json:"tmux_session"`
	CreatedAt   time.Time `json:"created_at"`
	LastSeen    time.Time `json:"last_seen"`
}
