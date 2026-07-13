// Package orchestrator is clishake's core: it owns the session, the agent
// registry, terminals (via tmux), delivery, supervision, and reconciliation.
// It is the only package that composes state, tmux, adapters, messaging,
// and tasks together; vendor-specific behavior stays behind the adapter
// interface.
package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/clishakehq/clishake/internal/adapter"
	"github.com/clishakehq/clishake/internal/config"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/events"
	"github.com/clishakehq/clishake/internal/messaging"
	"github.com/clishakehq/clishake/internal/state"
	"github.com/clishakehq/clishake/internal/tasks"
	"github.com/clishakehq/clishake/internal/tmux"
)

// Orchestrator coordinates one project session.
type Orchestrator struct {
	ProjectDir string
	Cfg        config.Config
	Store      *state.Store
	Log        *events.Log
	Tmux       *tmux.Client
	Registry   *adapter.Registry
	Session    *domain.Session
	Router     *messaging.Router
	Tasks      *tasks.Service
	// PrevSeen is the session's LastSeen at Open time — i.e. when the lead
	// last looked — so UIs can summarize what happened while away.
	PrevSeen time.Time

	mu         sync.Mutex
	stopping   map[string]bool        // agent IDs being stopped intentionally
	restarts   map[string]time.Time   // agent ID -> not-before time for scheduled restart
	history    map[string][]time.Time // agent ID -> recent failure times (crash-loop window)
	discovered map[string]time.Time   // agent ID -> last sub-agent discovery scan
	statusAt   map[string]time.Time   // agent ID -> last live-status (model/usage) read
	loopNudged map[string]time.Time   // agent ID -> last team-loop nudge
}

// ClishakeDir returns the .clishake directory for a project.
func ClishakeDir(projectDir string) string {
	return filepath.Join(projectDir, config.Dir)
}

// InitProject creates .clishake/ with a default config if not present.
// Returns true if it newly initialized.
func InitProject(projectDir string) (bool, error) {
	dir := ClishakeDir(projectDir)
	cfgPath := filepath.Join(dir, config.FileName)
	if _, err := os.Stat(cfgPath); err == nil {
		return false, nil
	}
	cfg := config.Default(filepath.Base(projectDir))
	if err := config.Save(projectDir, cfg); err != nil {
		return false, err
	}
	for _, sub := range []string{"agents", "adapters", "logs", "worktrees", "context", "skills"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return false, err
		}
	}
	// Explain the shared-skills directory so it's discoverable and committable.
	skillsReadme := filepath.Join(dir, "skills", "README.md")
	if _, err := os.Stat(skillsReadme); os.IsNotExist(err) {
		_ = os.WriteFile(skillsReadme, []byte(skillsReadmeText), 0o644)
	}
	// Keep runtime state out of the project's git history while leaving
	// config.toml (and adapter definitions) committable for the team.
	gitignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gitignore); os.IsNotExist(err) {
		content := `# clishake runtime state — never commit
state.db*
events.jsonl
loop.json
agents/
logs/
worktrees/
context/
`
		if err := os.WriteFile(gitignore, []byte(content), 0o644); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Open loads (initializing if needed) the project session. It does NOT
// create the tmux session; call EnsureSession for that.
func Open(projectDir string, reg *adapter.Registry) (*Orchestrator, error) {
	if _, err := InitProject(projectDir); err != nil {
		return nil, fmt.Errorf("init project: %w", err)
	}
	cfg, err := config.Load(projectDir)
	if err != nil {
		return nil, err
	}
	st, err := state.Open(filepath.Join(ClishakeDir(projectDir), "state.db"))
	if err != nil {
		return nil, err
	}
	log, err := events.Open(filepath.Join(ClishakeDir(projectDir), "events.jsonl"))
	if err != nil {
		st.Close()
		return nil, err
	}
	o := &Orchestrator{
		ProjectDir: projectDir,
		Cfg:        cfg,
		Store:      st,
		Log:        log,
		Tmux:       tmux.NewClient(cfg.Tmux.Socket),
		Registry:   reg,
		stopping:   map[string]bool{},
		restarts:   map[string]time.Time{},
		history:    map[string][]time.Time{},
		discovered: map[string]time.Time{},
		statusAt:   map[string]time.Time{},
		loopNudged: map[string]time.Time{},
	}
	sess, err := st.GetSession()
	if err != nil {
		o.Close()
		return nil, err
	}
	if sess == nil {
		s := domain.Session{
			ID:          domain.NewID("cs"),
			ProjectPath: projectDir,
			TmuxSession: cfg.SessionName(),
			CreatedAt:   time.Now().UTC(),
			LastSeen:    time.Now().UTC(),
		}
		if err := st.SaveSession(s); err != nil {
			o.Close()
			return nil, err
		}
		sess = &s
		o.emit(domain.EvSessionCreated, "clishake", s.ID, map[string]any{"project": projectDir})
	}
	o.Session = sess
	o.PrevSeen = sess.LastSeen // before any touch: "what happened while away"
	o.Router = messaging.NewRouter(sess.ID, st, log, deliverFunc(o.deliver))
	// "@task:<id>" addresses whoever owns or contributes to a task.
	o.Router.TaskMembers = func(taskID string) []string {
		t, err := st.GetTask(taskID)
		if err != nil || t == nil {
			return nil
		}
		names := t.Contributors
		if t.Owner != "" {
			names = append([]string{t.Owner}, names...)
		}
		return names
	}
	o.Tasks = tasks.NewService(sess.ID, st, log)
	o.syncContext()  // materialize .clishake/context/ from current state
	o.watchContext() // keep it current as agents/tasks/approvals change
	return o, nil
}

// deliverFunc adapts a function to messaging.Deliverer.
type deliverFunc func(a *domain.Agent, m domain.Message) error

func (f deliverFunc) Deliver(a *domain.Agent, m domain.Message) error { return f(a, m) }

// Close releases resources (not the tmux session — agents keep running).
func (o *Orchestrator) Close() {
	if o.Store != nil {
		o.Store.Close()
	}
	if o.Log != nil {
		o.Log.Close()
	}
}

// emit appends an event, swallowing (but printing) log errors so a full
// disk never wedges orchestration.
func (o *Orchestrator) emit(t domain.EventType, actor, subject string, payload map[string]any) {
	sessID := ""
	if o.Session != nil {
		sessID = o.Session.ID
	}
	if err := o.Log.Append(domain.NewEvent(sessID, t, actor, subject, payload)); err != nil {
		fmt.Fprintf(os.Stderr, "clishake: event log error: %v\n", err)
	}
}

// EnsureSession creates the managed tmux session if missing. Returns true
// when it attached to an existing one.
func (o *Orchestrator) EnsureSession() (existing bool, err error) {
	name := o.Cfg.SessionName()
	if !o.Tmux.Available() {
		return false, fmt.Errorf("tmux not found in PATH; clishake requires tmux")
	}
	if o.Tmux.HasSession(name) {
		// No event here: EnsureSession runs on nearly every command, and
		// "attached to the session that already existed" is not a state
		// change worth an audit line (Reconcile records real attaches).
		o.configureSessionUX()
		return true, nil
	}
	if err := o.Tmux.NewSession(name, o.ProjectDir); err != nil {
		return false, fmt.Errorf("create tmux session: %w", err)
	}
	o.configureSessionUX()
	o.emit(domain.EvSessionAttached, "lead", o.Session.ID, map[string]any{"tmux_session": name, "created": true})
	return false, nil
}

// configureSessionUX makes the managed tmux server friendly for leads who
// don't speak tmux: F12 detaches back to the dashboard from any agent
// pane (root-table binding — the key never reaches the agent), and the
// status bar says so. Idempotent and scoped to clishake's own socket.
func (o *Orchestrator) configureSessionUX() {
	_ = o.Tmux.BindRootKey("F12", "detach-client")
	_ = o.Tmux.SetGlobalOption("status-style", "bg=colour236,fg=colour245")
	_ = o.Tmux.SetGlobalOption("status-left-length", "30")
	_ = o.Tmux.SetGlobalOption("status-left", "#[fg=colour213,bold] clishake #[default]▸ #S ")
	_ = o.Tmux.SetGlobalOption("status-right-length", "50")
	_ = o.Tmux.SetGlobalOption("status-right", "#[fg=colour213] F12#[default] back to dashboard ")
}

// AttachArgs returns the argv to exec for interactive tmux attach.
func (o *Orchestrator) AttachArgs() []string {
	return o.Tmux.AttachArgs(o.Cfg.SessionName())
}

// AgentDir returns the runtime directory for an agent (inbox, output log).
func (o *Orchestrator) AgentDir(a *domain.Agent) string {
	return filepath.Join(ClishakeDir(o.ProjectDir), "agents", a.ID)
}

// touchSession updates LastSeen.
func (o *Orchestrator) touchSession() {
	o.Session.LastSeen = time.Now().UTC()
	_ = o.Store.SaveSession(*o.Session)
}
