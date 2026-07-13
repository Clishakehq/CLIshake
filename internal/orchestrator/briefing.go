package orchestrator

import (
	"fmt"
	"os"
	"strings"

	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/messaging"
)

// BriefingKey is the launch-config key adapters read to obtain the session
// briefing composed at start time. It is injected into the launch view
// only, never persisted on the agent record.
const BriefingKey = "_briefing"

// CoordinatorRole marks an agent as the session coordinator: its briefing
// gains coordination responsibilities and, unless explicitly overridden,
// it gets a read-and-coordinate permission profile (no file edits, so it
// runs in the project root with full visibility instead of a worktree).
const CoordinatorRole = "coordinator"

// CoordinatorPermissions is the default profile for coordinator agents.
func CoordinatorPermissions() domain.Permissions {
	return domain.Permissions{
		ReadFiles:    true,
		RunCommands:  true,
		UseGit:       true,
		SendMessages: true,
	}
}

// clishakeBin returns the absolute path of the running clishake binary,
// falling back to "clishake" (PATH) if unknown.
func clishakeBin() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		return exe
	}
	return "clishake"
}

// briefing composes the session context injected into a real harness at
// launch: who the agent is, who else is in the session, how messages
// arrive, and how to talk back (by running clishake commands).
func (o *Orchestrator) briefing(a *domain.Agent) string {
	bin := clishakeBin()
	var b strings.Builder

	fmt.Fprintf(&b, "You are %q (role: %s), one member of a multi-agent coding session coordinated by clishake in %s.\n\n",
		a.Name, orDash(a.Role), o.ProjectDir)

	if a.Config[restartedKey] == "1" {
		b.WriteString("YOU WERE JUST RESTARTED — this is a fresh process with NO memory of anything you did before. Do NOT start your task over from scratch. First check your assignment and its current status on the shared task board, and review your prior changes on your branch, before doing anything:\n")
		bin := clishakeBin()
		fmt.Fprintf(&b, "  %s tasks        (your assigned task and whether it is already in progress or completed)\n", bin)
		if a.Branch != "" {
			fmt.Fprintf(&b, "  git -C %s log --oneline -5   (what you already committed on %s)\n", a.WorkDir, a.Branch)
		}
		b.WriteString("Do ONLY what your task asks; if it looks already done, verify and report that rather than redoing or expanding it.\n\n")
	}

	b.WriteString("SESSION CONTEXT\n")
	b.WriteString("- A human team lead supervises this session and is addressed as \"lead\". Messages that begin with \"[clishake message from <sender>]\" are routed through clishake from the lead or a teammate — they are part of this coordinated session, not ordinary standalone user input.\n")

	roster, others := o.rosterLines(a)
	if len(roster) == 0 && others == 0 {
		b.WriteString("- Teammates: none yet — more agents may join; you will be notified.\n")
	} else {
		if a.Team != "" {
			fmt.Fprintf(&b, "- You belong to team %q. Your team right now:\n", a.Team)
		} else {
			b.WriteString("- Teammates in this session right now:\n")
		}
		for _, line := range roster {
			b.WriteString("    " + line + "\n")
		}
		if others > 0 {
			fmt.Fprintf(&b, "- %d agent(s) outside your team are also in this session. Stay focused on your team's work; coordinate with other teams only through the lead unless the lead says otherwise.\n", others)
		}
	}
	if a.Branch != "" {
		fmt.Fprintf(&b, "- Your workspace is a dedicated git worktree (%s) on branch %s. Commit your work there; the lead integrates branches. Do not switch branches or touch other agents' worktrees.\n",
			a.WorkDir, a.Branch)
	} else {
		fmt.Fprintf(&b, "- Your working directory is %s, shared with the session. Coordinate through the lead before large or risky changes.\n", a.WorkDir)
	}
	ctxDir := o.ContextDir()
	fmt.Fprintf(&b, "- LIVE SESSION CONTEXT FILES (clishake keeps these current — re-read them whenever you need fresh context; the roster above is only a launch-time snapshot):\n")
	fmt.Fprintf(&b, "    %s/session.md — session overview & communication protocol\n", ctxDir)
	fmt.Fprintf(&b, "    %s/roster.md  — current agents and their status\n", ctxDir)
	fmt.Fprintf(&b, "    %s/tasks.md   — the shared task board\n", ctxDir)
	fmt.Fprintf(&b, "    %s/notes.md   — shared notes & decisions (append via the note command)\n", ctxDir)

	if skills := o.ListSkills(); len(skills) > 0 {
		fmt.Fprintf(&b, "- SHARED TEAM SKILLS live in %s and apply to every agent regardless of harness. Consult them for how this team does things:\n", o.SkillsDir())
		for _, s := range skills {
			if s.Description != "" {
				fmt.Fprintf(&b, "    %s — %s\n", s.Name, s.Description)
			} else {
				fmt.Fprintf(&b, "    %s\n", s.Name)
			}
		}
	}

	if a.Role == CoordinatorRole {
		b.WriteString("\nYOU ARE THE SESSION COORDINATOR\n")
		b.WriteString("- Your job is coordination, not implementation: do not edit project files or write code unless the lead explicitly asks.\n")
		bin := clishakeBin()
		fmt.Fprintf(&b, "- Break work into tasks and assign them: %s task create --title \"...\" --assign <agent>   ·   %s task assign <task-id> <agent>\n", bin, bin)
		fmt.Fprintf(&b, "- Track progress: %s agents · %s tasks · %s messages — nudge idle or blocked agents by messaging them, and reassign work when someone is stuck.\n", bin, bin, bin)
		fmt.Fprintf(&b, "- Record decisions: %s note \"...\"   Keep the lead informed with concise status summaries to @lead; escalate conflicts, blockers, and approval requests to the lead immediately.\n", bin)
		b.WriteString("- You never approve risky actions yourself — approvals belong to the human lead.\n")
	}

	b.WriteString("\nHOW TO COMMUNICATE\n")
	fmt.Fprintf(&b, "- Message anyone by running this shell command:  %s send @<recipient> \"...\"\n", bin)
	b.WriteString("  Recipients: lead, a teammate's name, a role (@role:<r>), or @all (every agent).\n")
	fmt.Fprintf(&b, "- See teammates and their status:  %s agents      Shared task board:  %s tasks\n", bin, bin)
	fmt.Fprintf(&b, "- Report task progress:  %s task update <task-id> --status in_progress|completed\n", bin)
	b.WriteString("- Every message and action is logged with attribution; the lead sees everything.\n")
	b.WriteString("- When you receive a [clishake message ...], act on it in the context of this session and reply to its sender with the send command above.\n")
	return b.String()
}

// rosterLines describes the other agents this one should know in detail.
// Agents with a team see their own team in full; everyone else is only
// counted (`others`), scoping each team's briefing to its own work while
// staying honest that other agents exist. Teamless agents see everyone.
func (o *Orchestrator) rosterLines(self *domain.Agent) (out []string, others int) {
	agents, err := o.Store.ListAgents()
	if err != nil {
		return nil, 0
	}
	for _, x := range agents {
		if x.ID == self.ID || x.Adapter == "observed" {
			continue
		}
		if self.Team != "" && x.Team != self.Team {
			others++
			continue
		}
		task := ""
		if x.Task != "" {
			task = " — working on: " + x.Task
		}
		out = append(out, fmt.Sprintf("%s (role: %s, harness: %s, status: %s)%s",
			x.Name, orDash(x.Role), x.Adapter, x.Status, task))
	}
	return out, others
}

// notifyPeers sends a short control message to every settled live agent
// except the one given (which may be nil). Agents still starting are
// skipped: their terminal may not be ready for typed input yet, and they
// receive the current roster in their launch briefing anyway.
func (o *Orchestrator) notifyPeers(except *domain.Agent, body string) {
	agents, err := o.Store.ListAgents()
	if err != nil {
		return
	}
	for _, x := range agents {
		if except != nil && x.ID == except.ID {
			continue
		}
		if x.Adapter == "observed" || x.Status == domain.StatusStarting || !x.Status.IsLive() {
			continue
		}
		// Team scoping: news about a team member stays within that team,
		// but teamless agents (coordinators, generalists) hear everything.
		if except != nil && except.Team != "" && x.Team != "" && x.Team != except.Team {
			continue
		}
		_, _ = o.Send("clishake", "@"+x.Name, body, messaging.SendOpts{Type: domain.MsgControl})
	}
}

func orDash(s string) string {
	if s == "" {
		return "unspecified"
	}
	return s
}
