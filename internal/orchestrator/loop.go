package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/messaging"
)

// LoopState is the clishake team loop: a shared goal the whole team keeps
// working toward. While it is active the supervisor re-engages any agent that
// has gone quiet, so the team keeps pushing on the goal — the team-level
// analogue of a single harness's "/loop" — until the lead stops it.
//
// It lives in .clishake/loop.json (not the DB) so a separate process — an
// agent running `clishake loop stop` from its sandbox — can end the loop too.
type LoopState struct {
	Active    bool      `json:"active"`
	Goal      string    `json:"goal"`
	StartedAt time.Time `json:"started_at"`
}

const (
	// loopIdle is how long an agent must be quiet (no new output) before the
	// loop re-nudges it — long enough that it has finished its turn, not just
	// paused mid-thought.
	loopIdle = 25 * time.Second
	// loopNudgeEvery rate-limits nudges to one agent so the loop encourages
	// rather than spams.
	loopNudgeEvery = 45 * time.Second
)

func (o *Orchestrator) loopPath() string {
	return filepath.Join(ClishakeDir(o.ProjectDir), "loop.json")
}

// Loop returns the current team-loop state (inactive when unset or unreadable).
func (o *Orchestrator) Loop() LoopState {
	b, err := os.ReadFile(o.loopPath())
	if err != nil {
		return LoopState{}
	}
	var ls LoopState
	if json.Unmarshal(b, &ls) != nil {
		return LoopState{}
	}
	return ls
}

func (o *Orchestrator) saveLoop(ls LoopState) error {
	b, err := json.MarshalIndent(ls, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(o.loopPath(), b, 0o644)
}

// SetGoal records the shared team goal and announces it to every live agent,
// without (on its own) starting the loop.
func (o *Orchestrator) SetGoal(goal string) error {
	ls := o.Loop()
	ls.Goal = goal
	if err := o.saveLoop(ls); err != nil {
		return err
	}
	_, _ = o.Broadcast(domain.LeadSender, "◎ Team goal: "+goal)
	return nil
}

// StartLoop sets the goal and activates the team loop, broadcasting the kickoff.
func (o *Orchestrator) StartLoop(goal string) error {
	if err := o.saveLoop(LoopState{Active: true, Goal: goal, StartedAt: time.Now().UTC()}); err != nil {
		return err
	}
	_, _ = o.Broadcast(domain.LeadSender, loopKickoff(goal))
	return nil
}

// StopLoop deactivates the team loop (the goal is kept for reference).
func (o *Orchestrator) StopLoop() error {
	ls := o.Loop()
	if !ls.Active {
		return nil
	}
	ls.Active = false
	if err := o.saveLoop(ls); err != nil {
		return err
	}
	_, _ = o.Broadcast(domain.LeadSender, "⟳ Team loop stopped by the lead — wrap up your current step.")
	return nil
}

// driveLoop is the supervisor half of the team loop: while it is active, any
// live agent that has been quiet longer than loopIdle gets a nudge back toward
// the goal (rate-limited per agent). This runs only in the supervisor process.
func (o *Orchestrator) driveLoop() {
	if os.Getenv("CLISHAKE_AGENT") != "" {
		return
	}
	ls := o.Loop()
	if !ls.Active {
		return
	}
	agents, err := o.Store.ListAgents()
	if err != nil {
		return
	}
	now := time.Now()
	for _, a := range agents {
		if a.Adapter == "observed" || !a.Status.IsLive() || a.Status == domain.StatusStarting {
			continue
		}
		if time.Since(a.LastActivity) < loopIdle {
			continue // still working
		}
		o.mu.Lock()
		if last, ok := o.loopNudged[a.ID]; ok && now.Sub(last) < loopNudgeEvery {
			o.mu.Unlock()
			continue
		}
		o.loopNudged[a.ID] = now
		o.mu.Unlock()
		_, _ = o.Send(domain.LeadSender, "@"+a.Name, loopNudge(ls.Goal), messaging.SendOpts{})
	}
}

func loopKickoff(goal string) string {
	return "⟳ TEAM LOOP started. Shared goal: " + goal +
		". Start working toward it now. When you believe the goal is fully met, message @lead with 'DONE' and a one-line summary; otherwise keep going. The lead ends the loop with `clishake loop stop`."
}

func loopNudge(goal string) string {
	return "⟳ Team loop — you've gone quiet. Keep working toward the goal: " + goal +
		". If it is fully solved, message @lead 'DONE'. If you are blocked, say what you need. Otherwise continue."
}
