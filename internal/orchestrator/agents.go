package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/clishakehq/clishake/internal/adapter"
	"github.com/clishakehq/clishake/internal/domain"
)

// AgentSpec describes a new agent to register.
type AgentSpec struct {
	Name        string
	Role        string
	Adapter     string
	Task        string
	Team        string
	ParentID    string
	Permissions *domain.Permissions
	Config      map[string]string
}

// nameValid enforces addressable names: [a-z0-9-_]+, not reserved. The rule
// lives in domain so the natural-language front-end can pre-check the same way.
func nameValid(n string) error {
	return domain.ValidAgentName(n)
}

// AddAgent registers a new agent record (not started). If the config
// defines an agent template with the same name, unset spec fields are
// filled from the template.
func (o *Orchestrator) AddAgent(spec AgentSpec) (*domain.Agent, error) {
	if err := nameValid(spec.Name); err != nil {
		return nil, err
	}
	for _, t := range o.Cfg.Agents {
		if t.Name != spec.Name {
			continue
		}
		if spec.Role == "" {
			spec.Role = t.Role
		}
		if spec.Adapter == "" {
			spec.Adapter = t.Adapter
		}
		if spec.Task == "" {
			spec.Task = t.Task
		}
		if spec.Permissions == nil {
			spec.Permissions = t.Permissions
		}
		if spec.Config == nil {
			spec.Config = t.Config
		} else if t.Config != nil {
			// Merge: template config is the base, spec (e.g. --model) overrides.
			merged := map[string]string{}
			for k, v := range t.Config {
				merged[k] = v
			}
			for k, v := range spec.Config {
				merged[k] = v
			}
			spec.Config = merged
		}
		break
	}
	if existing, err := o.Store.GetAgentByName(spec.Name); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, fmt.Errorf("agent %q already exists", spec.Name)
	}
	adapterName := spec.Adapter
	if adapterName == "" {
		adapterName = o.Cfg.Defaults.Adapter
	}
	ad, ok := o.Registry.Get(adapterName)
	if !ok {
		return nil, fmt.Errorf("unknown adapter %q (available: %s)",
			adapterName, strings.Join(o.Registry.Names(), ", "))
	}
	if err := ad.ValidateConfig(spec.Config); err != nil {
		return nil, fmt.Errorf("adapter %s config: %w", adapterName, err)
	}
	perms := o.Cfg.Defaults.Permissions
	if spec.Role == CoordinatorRole {
		perms = CoordinatorPermissions()
	}
	if spec.Permissions != nil {
		perms = *spec.Permissions
	}
	a := &domain.Agent{
		ID:           domain.NewID("ag"),
		Name:         spec.Name,
		Role:         spec.Role,
		Adapter:      adapterName,
		ParentID:     spec.ParentID,
		Team:         spec.Team,
		Task:         spec.Task,
		Status:       domain.StatusStopped, // registered but not started
		WorkDir:      o.ProjectDir,
		Capabilities: ad.Capabilities(),
		Permissions:  perms,
		Config:       spec.Config,
		CreatedAt:    time.Now().UTC(),
		LastActivity: time.Now().UTC(),
	}
	if err := o.Store.SaveAgent(a); err != nil {
		return nil, err
	}
	o.emit(domain.EvAgentCreated, domain.LeadSender, a.Name, map[string]any{
		"id": a.ID, "role": a.Role, "adapter": a.Adapter, "task": a.Task,
	})
	// An initial task becomes a real entry on the shared task board,
	// owned by this agent — so the board reflects who is working on what,
	// and a restarted agent can see its assignment and its status there.
	if a.Task != "" {
		if t, err := o.Tasks.Create(domain.LeadSender, a.Task, "", a.Name, 0, nil); err == nil {
			a.TaskID = t.ID
			_ = o.Store.SaveAgent(a)
		}
	}
	return a, nil
}

// withEnv wraps a launch command with env(1) so agent processes inherit
// CLISHAKE_PROJECT (letting them run clishake commands from worktrees or
// subdirectories and still address this session) and CLISHAKE_AGENT (so
// messages they send are attributed to them, not to the lead), plus any
// adapter-requested variables.
func withEnv(spec adapter.LaunchSpec, projectDir, agentName string) []string {
	envArgs := []string{"env",
		"CLISHAKE_PROJECT=" + projectDir,
		"CLISHAKE_AGENT=" + agentName,
	}
	for k, v := range spec.Env {
		envArgs = append(envArgs, k+"="+v)
	}
	return append(envArgs, spec.Command...)
}

// launchView returns a copy of the agent whose Config has the project-level
// [adapters.<name>] settings merged in (agent's own settings win). The copy
// is what BuildLaunch sees; the stored record keeps only per-agent config.
func (o *Orchestrator) launchView(a *domain.Agent) *domain.Agent {
	view := *a
	view.Config = map[string]string{}
	if ac, ok := o.Cfg.Adapters[a.Adapter]; ok {
		if ac.Command != "" {
			view.Config["command"] = ac.Command
		}
		if len(ac.Args) > 0 {
			view.Config["args"] = strings.Join(ac.Args, " ")
		}
		for k, v := range ac.Options {
			view.Config[k] = v
		}
	}
	for k, v := range a.Config {
		view.Config[k] = v
	}
	return &view
}

// adapterEnabled reports whether the adapter is enabled in config
// (default: enabled).
func (o *Orchestrator) adapterEnabled(name string) bool {
	ac, ok := o.Cfg.Adapters[name]
	if !ok || ac.Enabled == nil {
		return true
	}
	return *ac.Enabled
}

// StartAgent launches a registered agent in a new tmux window.
func (o *Orchestrator) StartAgent(name string) (*domain.Agent, error) {
	a, err := o.Store.GetAgentByName(name)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, fmt.Errorf("no agent named %q", name)
	}
	if a.Status.IsLive() {
		return nil, fmt.Errorf("agent %q is already %s", name, a.Status)
	}
	ad, ok := o.Registry.Get(a.Adapter)
	if !ok {
		return nil, fmt.Errorf("adapter %q not registered", a.Adapter)
	}
	if !o.adapterEnabled(a.Adapter) {
		return nil, fmt.Errorf("adapter %q is disabled in config.toml", a.Adapter)
	}
	// A configured command override is the availability statement — Detect
	// only knows the default binary. A wrong override fails visibly in the
	// pane rather than silently.
	if o.launchView(a).Config["command"] == "" {
		if installed, _, err := ad.Detect(); err != nil || !installed {
			return nil, fmt.Errorf("adapter %q: harness not available (run `clishake doctor`)", a.Adapter)
		}
	}
	if _, err := o.EnsureSession(); err != nil {
		return nil, err
	}

	// Workspace: dedicated worktree for editing agents when configured.
	workDir, branch, err := o.ensureWorkspace(a)
	if err != nil {
		return nil, fmt.Errorf("workspace for %s: %w", name, err)
	}
	a.WorkDir, a.Branch = workDir, branch

	// Runtime dir (inbox + output log).
	dir := o.AgentDir(a)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	view := o.launchView(a)
	view.Config[BriefingKey] = o.briefing(a)
	spec, err := ad.BuildLaunch(view, o.ProjectDir)
	if err != nil {
		return nil, fmt.Errorf("build launch: %w", err)
	}
	if spec.WorkDir == "" {
		spec.WorkDir = workDir
	}
	cmd := withEnv(spec, o.ProjectDir, a.Name)

	sessName := o.Cfg.SessionName()
	paneID, err := o.Tmux.NewWindow(sessName, a.Name, spec.WorkDir, cmd)
	if err != nil {
		return nil, fmt.Errorf("tmux window: %w", err)
	}
	logPath := filepath.Join(dir, "output.log")
	if err := o.Tmux.PipePane(paneID, logPath); err != nil {
		return nil, fmt.Errorf("pipe-pane: %w", err)
	}

	// Resolve the PID for supervision.
	pid := 0
	if panes, err := o.Tmux.ListPanes(sessName); err == nil {
		for _, p := range panes {
			if p.PaneID == paneID {
				pid = p.PanePID
			}
		}
	}

	a.Tmux = domain.TmuxRef{Session: sessName, Window: a.Name, PaneID: paneID}
	a.PID = pid
	a.Status = domain.StatusStarting
	a.LastActivity = time.Now().UTC()
	a.ExitCode = nil
	if err := o.Store.SaveAgent(a); err != nil {
		return nil, err
	}
	o.emit(domain.EvAgentStarted, domain.LeadSender, a.Name, map[string]any{
		"pane": paneID, "pid": pid, "workdir": spec.WorkDir, "branch": branch,
	})
	// No "@" in typed message bodies: Claude Code's composer expands "@"
	// into a file-mention autocomplete, which resolved "@opencode" to an
	// unrelated file path (bug reported BY a claude agent mid-session).
	// Bare names are valid clishake selectors.
	o.notifyPeers(a, fmt.Sprintf("roster update: agent %q (role: %s, harness: %s) joined the session — message it with: clishake send %s \"...\"",
		a.Name, orDash(a.Role), a.Adapter, a.Name))
	o.touchSession()
	return a, nil
}

// StopAgent stops an agent. graceful=true sends the adapter's interrupt
// keys first and gives the process a moment; otherwise (or after the grace
// period) the pane process is killed via respawn-kill... we kill the window
// only on RemoveAgent; StopAgent keeps the dead pane for inspection.
func (o *Orchestrator) StopAgent(name string, graceful bool) error {
	a, err := o.Store.GetAgentByName(name)
	if err != nil {
		return err
	}
	if a == nil {
		return fmt.Errorf("no agent named %q", name)
	}
	if !a.Status.IsLive() {
		return fmt.Errorf("agent %q is not running (status %s)", name, a.Status)
	}
	ad, _ := o.Registry.Get(a.Adapter)

	o.mu.Lock()
	o.stopping[a.ID] = true
	o.mu.Unlock()

	if graceful && ad != nil {
		if keys := ad.InterruptKeys(); len(keys) > 0 && a.Tmux.PaneID != "" {
			_ = o.Tmux.SendKeys(a.Tmux.PaneID, keys...)
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if !o.paneAlive(a) {
					break
				}
				time.Sleep(150 * time.Millisecond)
			}
		}
	}
	if o.paneAlive(a) && a.PID > 0 {
		// Kill the process; remain-on-exit keeps the dead pane visible.
		if p, err := os.FindProcess(a.PID); err == nil {
			_ = p.Kill()
		}
	}
	o.setStatus(a, domain.StatusStopped, "stopped by lead")
	o.notifyPeers(a, fmt.Sprintf("roster update: agent %q was stopped by the lead and can no longer receive messages", a.Name))
	return nil
}

// RestartAgent restarts a stopped/failed/running agent in its existing
// window (respawn) or a fresh one when the window is gone.
func (o *Orchestrator) RestartAgent(name string) (*domain.Agent, error) {
	a, err := o.Store.GetAgentByName(name)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, fmt.Errorf("no agent named %q", name)
	}
	if a.Status.IsLive() {
		if err := o.StopAgent(name, true); err != nil {
			return nil, err
		}
		a, _ = o.Store.GetAgentByName(name)
	}
	restarted, err := o.respawn(a)
	if err != nil {
		return nil, err
	}
	restarted.RestartCount++
	if err := o.Store.SaveAgent(restarted); err != nil {
		return nil, err
	}
	o.emit(domain.EvAgentRestarted, domain.LeadSender, restarted.Name,
		map[string]any{"restart_count": restarted.RestartCount})
	return restarted, nil
}

// respawn relaunches an agent's process, reusing its window when possible.
func (o *Orchestrator) respawn(a *domain.Agent) (*domain.Agent, error) {
	ad, ok := o.Registry.Get(a.Adapter)
	if !ok {
		return nil, fmt.Errorf("adapter %q not registered", a.Adapter)
	}
	view := o.launchView(a)
	view.Config[BriefingKey] = o.briefing(a)
	spec, err := ad.BuildLaunch(view, o.ProjectDir)
	if err != nil {
		return nil, err
	}
	if spec.WorkDir == "" {
		spec.WorkDir = a.WorkDir
	}
	sessName := o.Cfg.SessionName()

	paneStillThere := false
	if a.Tmux.PaneID != "" {
		if panes, err := o.Tmux.ListPanes(sessName); err == nil {
			for _, p := range panes {
				if p.PaneID == a.Tmux.PaneID {
					paneStillThere = true
				}
			}
		}
	}
	var paneID string
	if paneStillThere {
		if err := o.Tmux.RespawnPane(a.Tmux.PaneID, spec.WorkDir, withEnv(spec, o.ProjectDir, a.Name)); err != nil {
			return nil, fmt.Errorf("respawn pane: %w", err)
		}
		paneID = a.Tmux.PaneID
	} else {
		if _, err := o.EnsureSession(); err != nil {
			return nil, err
		}
		paneID, err = o.Tmux.NewWindow(sessName, a.Name, spec.WorkDir, withEnv(spec, o.ProjectDir, a.Name))
		if err != nil {
			return nil, fmt.Errorf("tmux window: %w", err)
		}
	}
	logPath := filepath.Join(o.AgentDir(a), "output.log")
	_ = o.Tmux.PipePane(paneID, logPath)

	pid := 0
	if panes, err := o.Tmux.ListPanes(sessName); err == nil {
		for _, p := range panes {
			if p.PaneID == paneID {
				pid = p.PanePID
			}
		}
	}
	a.Tmux = domain.TmuxRef{Session: sessName, Window: a.Name, PaneID: paneID}
	a.PID = pid
	a.Status = domain.StatusStarting
	a.ExitCode = nil
	a.LastActivity = time.Now().UTC()
	// A respawned harness is a fresh process with no memory, so re-brief it
	// (identity, roster). But do NOT re-deliver the raw initial task —
	// re-issuing it verbatim makes the agent redo work it may have already
	// done. Instead the briefing gains a restart note pointing it at the
	// task board (for its assignment and current status) and its branch
	// (for prior changes). taskDeliveredKey is kept so the task isn't
	// re-sent.
	if a.Config != nil {
		delete(a.Config, briefedKey)
		delete(a.Config, readyAtKey)
		delete(a.Config, trustAnsweredKey) // a respawned worktree may re-prompt for trust
		a.Config[restartedKey] = "1"
	}
	o.mu.Lock()
	delete(o.stopping, a.ID)
	o.mu.Unlock()
	if err := o.Store.SaveAgent(a); err != nil {
		return nil, err
	}
	return a, nil
}

// RemoveAgent stops an agent (if live), kills its window, and deletes it.
func (o *Orchestrator) RemoveAgent(name string) error {
	a, err := o.Store.GetAgentByName(name)
	if err != nil {
		return err
	}
	if a == nil {
		return fmt.Errorf("no agent named %q", name)
	}
	if a.Status.IsLive() {
		_ = o.StopAgent(name, true)
	}
	// Kill by pane id, never by name: window names can collide across
	// add/remove cycles, and a name-targeted kill could destroy another
	// agent's window (or fail as ambiguous).
	if a.Tmux.PaneID != "" {
		_ = o.Tmux.KillWindowByPane(a.Tmux.PaneID)
	} else if a.Tmux.Window != "" {
		_ = o.Tmux.KillWindow(o.Cfg.SessionName(), a.Tmux.Window)
	}
	// Reparent observed children so the hierarchy never dangles.
	children, _ := o.Store.ListAgents()
	for _, c := range children {
		if c.ParentID == a.ID {
			c.ParentID = ""
			_ = o.Store.SaveAgent(c)
		}
	}
	if err := o.Store.DeleteAgent(a.ID); err != nil {
		return err
	}
	o.emit(domain.EvAgentRemoved, domain.LeadSender, a.Name, map[string]any{"id": a.ID})
	return nil
}

// CleanOrphans kills tmux windows on the managed session that no
// registered agent owns — leftovers from removed agents or interrupted
// starts. The session's base shell window and every agent-owned pane
// (including dead ones kept for their final screen) are never touched.
// Returns the window names it closed.
func (o *Orchestrator) CleanOrphans() ([]string, error) {
	if !o.Tmux.ServerAlive() || !o.Tmux.HasSession(o.Cfg.SessionName()) {
		return nil, nil
	}
	panes, err := o.Tmux.ListPanes(o.Cfg.SessionName())
	if err != nil {
		return nil, err
	}
	agents, err := o.Store.ListAgents()
	if err != nil {
		return nil, err
	}
	owned := map[string]bool{}
	for _, a := range agents {
		if a.Tmux.PaneID != "" {
			owned[a.Tmux.PaneID] = true
		}
	}
	var closed []string
	for _, p := range panes {
		if owned[p.PaneID] {
			continue
		}
		switch p.WindowName {
		case "zsh", "bash", "sh", "fish":
			continue // the session's base shell window anchors the session
		}
		if err := o.Tmux.KillWindowByPane(p.PaneID); err == nil {
			closed = append(closed, p.WindowName)
			o.emit(domain.EvAgentRemoved, domain.LeadSender, p.WindowName, map[string]any{
				"orphan_pane": p.PaneID, "cleaned": true,
			})
		}
	}
	return closed, nil
}

// SetAgentMeta updates an agent's role and/or team at runtime (nil = keep).
// The context roster follows automatically via the config.changed event, so
// selectors like @role:reviewer and @team:reviewers re-cluster live agents
// without restarts.
func (o *Orchestrator) SetAgentMeta(name string, role, team *string) (*domain.Agent, error) {
	a, err := o.Store.GetAgentByName(name)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, fmt.Errorf("no agent named %q", name)
	}
	changed := map[string]any{}
	if role != nil && *role != a.Role {
		changed["role"] = map[string]string{"from": a.Role, "to": *role}
		a.Role = *role
	}
	if team != nil && *team != a.Team {
		changed["team"] = map[string]string{"from": a.Team, "to": *team}
		a.Team = *team
	}
	if len(changed) == 0 {
		return a, nil
	}
	if err := o.Store.SaveAgent(a); err != nil {
		return nil, err
	}
	o.emit(domain.EvConfigChanged, domain.LeadSender, a.Name, changed)
	return a, nil
}

// FocusAgent selects the agent's window (for use before/while attached).
func (o *Orchestrator) FocusAgent(name string) error {
	a, err := o.Store.GetAgentByName(name)
	if err != nil {
		return err
	}
	if a == nil {
		return fmt.Errorf("no agent named %q", name)
	}
	if a.Tmux.PaneID == "" && a.Tmux.Window == "" {
		return fmt.Errorf("agent %q has no terminal (observed sub-agent or never started)", name)
	}
	// Pane ids are unique; window names may collide across add/remove
	// cycles (tmux refuses ambiguous name targets).
	if a.Tmux.PaneID != "" {
		return o.Tmux.SelectWindowByPane(a.Tmux.PaneID)
	}
	return o.Tmux.SelectWindow(o.Cfg.SessionName(), a.Tmux.Window)
}

// setStatus persists a status change and emits the event.
func (o *Orchestrator) setStatus(a *domain.Agent, s domain.AgentStatus, reason string) {
	if a.Status == s {
		return
	}
	old := a.Status
	a.Status = s
	a.LastActivity = time.Now().UTC()
	_ = o.Store.SaveAgent(a)
	o.emit(domain.EvAgentStatusChanged, "clishake", a.Name, map[string]any{
		"from": string(old), "to": string(s), "reason": reason,
	})
}

// paneAlive reports whether the agent's pane exists and is not dead.
func (o *Orchestrator) paneAlive(a *domain.Agent) bool {
	if a.Tmux.PaneID == "" {
		return false
	}
	panes, err := o.Tmux.ListPanes(o.Cfg.SessionName())
	if err != nil {
		return false
	}
	for _, p := range panes {
		if p.PaneID == a.Tmux.PaneID {
			return !p.Dead
		}
	}
	return false
}
