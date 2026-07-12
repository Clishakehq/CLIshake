// Package adapter defines the harness adapter contract: the seam between
// clishake's vendor-neutral orchestration core and any specific coding-agent
// CLI (Claude Code, OpenAI Codex, mocks, ...).
//
// Design rules:
//   - The orchestration core only talks to this interface; nothing
//     vendor-specific leaks upward.
//   - An adapter declares its capabilities honestly. clishake must not offer
//     a feature unless the active adapter declares it or a documented
//     fallback exists (e.g. plain send-keys input for harnesses without
//     structured input).
//   - Adapters never touch tmux directly; they describe what to launch and
//     how to interpret output, and the orchestrator owns terminals.
package adapter

import (
	"github.com/clishakehq/clishake/internal/domain"
)

// LaunchSpec describes how to start a harness process inside a terminal
// pane. The orchestrator materializes it with tmux.
type LaunchSpec struct {
	Command []string          // argv; Command[0] is the executable
	Env     map[string]string // extra environment variables
	WorkDir string            // working directory for the process
}

// InputSpec describes how to deliver text to a running harness.
type InputMode string

const (
	// InputSendKeys types the text into the pane followed by Enter. Works
	// for any interactive CLI; this is the universal fallback.
	InputSendKeys InputMode = "send-keys"
	// InputFile appends a structured envelope to the agent's inbox file;
	// the harness (or its wrapper) watches that file. Requires
	// CapStructuredInput.
	InputFile InputMode = "file"
)

// ParsedEvent is a structured fact an adapter extracted from raw output.
// The orchestrator maps these onto agent status changes, messages, tasks,
// sub-agent registration, and audit events.
type ParsedEvent struct {
	Kind   ParsedKind
	Agent  string             // reporting agent name (attribution)
	Status domain.AgentStatus // for KindStatus
	Text   string             // freeform payload (message body, log line, reason)
	To     string             // recipient/selector for KindMessage
	TaskID string             // related task, when stated
	Sub    *SubagentInfo      // for KindSubagent
	Fields map[string]string  // any extra structured fields
}

// ParsedKind classifies a ParsedEvent.
type ParsedKind string

const (
	KindStatus   ParsedKind = "status"   // agent reports a lifecycle/status change
	KindMessage  ParsedKind = "message"  // agent addresses another agent or the lead
	KindTask     ParsedKind = "task"     // task progress/completion report
	KindSubagent ParsedKind = "subagent" // agent spawned/discovered a sub-agent
	KindApproval ParsedKind = "approval" // agent requests approval
	KindLog      ParsedKind = "log"      // notable but unstructured output
)

// SubagentInfo describes a sub-agent discovered under a parent.
type SubagentInfo struct {
	Name   string
	Role   string
	Status domain.AgentStatus
}

// Health is an adapter's judgment of a running agent based on its output
// and process state.
type Health string

const (
	HealthOK           Health = "ok"
	HealthUnresponsive Health = "unresponsive"
	HealthUnknown      Health = "unknown"
)

// Adapter is the contract every harness integration implements.
//
// Adapters must be stateless with respect to individual agents: all
// per-agent context arrives via the domain.Agent record, so one adapter
// instance serves many agents.
type Adapter interface {
	// Name is the stable registry key, e.g. "mock", "claude-code", "codex".
	Name() string

	// Capabilities reports what this adapter genuinely supports.
	Capabilities() []domain.Capability

	// Detect reports whether the underlying harness is installed and, if
	// so, its version string. Adapters that need no external binary (mock)
	// return installed=true.
	Detect() (installed bool, version string, err error)

	// ValidateConfig checks adapter-specific agent configuration before
	// launch. Returning nil means the config is usable.
	ValidateConfig(cfg map[string]string) error

	// BuildLaunch constructs the launch specification for an agent.
	BuildLaunch(a *domain.Agent, projectDir string) (LaunchSpec, error)

	// InputMode says how text should be delivered to this harness.
	InputMode() InputMode

	// FormatInput renders a message for delivery. For InputSendKeys the
	// result is typed into the pane; for InputFile it is the JSON line to
	// append to the agent's inbox.
	FormatInput(a *domain.Agent, msg domain.Message) (string, error)

	// ParseOutput scans a chunk of captured pane output (one or more
	// lines) and extracts structured events. Unparseable output yields no
	// events; adapters must never guess.
	ParseOutput(a *domain.Agent, chunk string) []ParsedEvent

	// DetectReady reports whether output indicates the harness finished
	// starting up and can accept input.
	DetectReady(a *domain.Agent, chunk string) bool

	// CheckHealth judges agent health from recent output age and process
	// liveness. lastOutputAgeSec < 0 means "no output observed yet".
	CheckHealth(a *domain.Agent, processAlive bool, lastOutputAgeSec float64) Health

	// InterruptArgs returns how to interrupt gracefully: the literal keys
	// to send (e.g. "C-c" for SIGINT semantics, "Escape"). Empty slice
	// means "no graceful interrupt; kill only".
	InterruptKeys() []string
}

// SubagentDiscoverer is an optional interface for adapters whose harness
// exposes sub-agents/teams through durable artifacts (files, APIs) rather
// than parseable output. The orchestrator polls it for live agents and
// registers the results as observed sub-agents; members that disappear
// from a later discovery are marked completed.
type SubagentDiscoverer interface {
	DiscoverSubagents(a *domain.Agent) []SubagentInfo
}

// LaunchBriefer is an optional interface: adapters that inject the session
// briefing at launch time (system prompt, prompt preamble, or a briefing-
// native protocol) return true, and the orchestrator leaves briefing to
// them. Adapters that do NOT implement it — or return false — get the
// briefing delivered as the first routed message once the agent is ready.
type LaunchBriefer interface {
	BriefsAtLaunch() bool
}

// Registry maps adapter names to implementations.
type Registry struct {
	adapters map[string]Adapter
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{adapters: map[string]Adapter{}}
}

// Register adds an adapter. Later registrations with the same name replace
// earlier ones (used by tests).
func (r *Registry) Register(a Adapter) {
	r.adapters[a.Name()] = a
}

// Get returns the adapter by name.
func (r *Registry) Get(name string) (Adapter, bool) {
	a, ok := r.adapters[name]
	return a, ok
}

// Names returns registered adapter names, unordered.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.adapters))
	for n := range r.adapters {
		out = append(out, n)
	}
	return out
}
