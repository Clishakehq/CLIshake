package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/clishakehq/clishake/internal/brand"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/events"
	"github.com/clishakehq/clishake/internal/messaging"
	"github.com/clishakehq/clishake/internal/mockagent"
	"github.com/clishakehq/clishake/internal/orchestrator"
)

func newInitCmd() *cobra.Command {
	var noOpen bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize .clishake/ and open the dashboard",
		Long: `Creates .clishake/ in the current directory and drops you into the
interactive dashboard — add agents with /add, message them with @, watch
them work. Use --no-open to only write the files (for scripts and CI).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			created, err := orchestrator.InitProject(dir)
			if err != nil {
				return err
			}
			// Non-interactive (piped/CI) or --no-open: just report and exit.
			if noOpen || !isatty.IsTerminal(os.Stdout.Fd()) {
				if created {
					fmt.Println(brand.ColorBanner(Version))
					fmt.Println("initialized .clishake/ (config.toml, agents/, adapters/, logs/, worktrees/, context/)")
					fmt.Println("next: `clishake` to open the dashboard, or `clishake agent add <name>`")
				} else {
					fmt.Println(".clishake/ already initialized")
				}
				return nil
			}
			// Interactive: land the user in the dashboard, like the main
			// command does. This is the intended first-run experience.
			return runDashboard()
		},
	}
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "initialize only; do not open the dashboard")
	return cmd
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show session, agents, and task summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			report, err := o.Reconcile()
			if err != nil {
				return err
			}
			o.Poll() // consume any output produced while detached
			fmt.Printf("session   %s\nproject   %s\ntmux      %s (socket %q, alive=%v)\n",
				o.Session.ID, o.ProjectDir, o.Cfg.SessionName(), o.Tmux.Socket(),
				o.Tmux.HasSession(o.Cfg.SessionName()))
			fmt.Println("\nreconcile:")
			if len(report) == 0 {
				fmt.Println("  (no agents)")
			}
			for _, r := range report {
				fmt.Println("  " + r)
			}
			fmt.Println()
			if err := printAgents(o); err != nil {
				return err
			}
			tasksList, err := o.Tasks.List()
			if err != nil {
				return err
			}
			open := 0
			for _, t := range tasksList {
				if t.Status != domain.TaskCompleted && t.Status != domain.TaskCancelled {
					open++
				}
			}
			fmt.Printf("\ntasks: %d total, %d open (clishake tasks)\n", len(tasksList), open)
			pend, err := o.Store.ListApprovals(domain.ApprovalPending)
			if err == nil && len(pend) > 0 {
				fmt.Printf("⚠ %d approval(s) pending (clishake approvals)\n", len(pend))
			}
			return nil
		},
	}
}

// printAgents renders the agent hierarchy as a tree.
func printAgents(o *orchestrator.Orchestrator) error {
	agents, err := o.Store.ListAgents()
	if err != nil {
		return err
	}
	if len(agents) == 0 {
		fmt.Println("agents: none (clishake agent add <name> --adapter mock)")
		return nil
	}
	children := map[string][]*domain.Agent{}
	var roots []*domain.Agent
	byID := map[string]*domain.Agent{}
	for _, a := range agents {
		byID[a.ID] = a
	}
	for _, a := range agents {
		if a.ParentID != "" && byID[a.ParentID] != nil {
			children[a.ParentID] = append(children[a.ParentID], a)
		} else {
			roots = append(roots, a)
		}
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT\tROLE\tADAPTER\tSTATUS\tBRANCH\tTASK")
	var walk func(a *domain.Agent, depth int)
	walk = func(a *domain.Agent, depth int) {
		indent := strings.Repeat("  ", depth)
		mark := ""
		if depth > 0 {
			mark = "└ "
		}
		task := a.Task
		if len(task) > 40 {
			task = task[:40] + "…"
		}
		fmt.Fprintf(w, "%s%s%s\t%s\t%s\t%s\t%s\t%s\n",
			indent, mark, a.Name, a.Role, a.Adapter, statusBadge(a.Status), a.Branch, task)
		kids := children[a.ID]
		sort.Slice(kids, func(i, j int) bool { return kids[i].CreatedAt.Before(kids[j].CreatedAt) })
		for _, c := range kids {
			walk(c, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	return w.Flush()
}

func statusBadge(s domain.AgentStatus) string {
	switch s {
	case domain.StatusRunning, domain.StatusReady:
		return "● " + string(s)
	case domain.StatusFailed:
		return "✗ " + string(s)
	case domain.StatusCompleted:
		return "✓ " + string(s)
	case domain.StatusAwaitingApproval, domain.StatusBlocked:
		return "⚠ " + string(s)
	default:
		return "○ " + string(s)
	}
}

func newAgentsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "List agents (tree of parents and sub-agents)",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			o.Poll()
			return printAgents(o)
		},
	}
}

func newAgentCmd() *cobra.Command {
	agentCmd := &cobra.Command{Use: "agent", Short: "Manage individual agents"}

	var role, adapterName, task, team, model, permissions string
	var noStart bool
	addCmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Register (and by default start) a new agent",
		Long: "Register (and by default start) a new agent.\n\n" +
			"<name> is how you address the agent (@name) and names its git branch, so " +
			"it must be " + domain.AgentNameHint + ". Names are matched case-insensitively; " +
			"lead, all, team and role are reserved.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			switch permissions {
			case "", "default", "auto", "full", "plan":
			default:
				return fmt.Errorf("--permissions must be one of default|auto|full|plan (got %q)", permissions)
			}
			var cfg map[string]string
			if model != "" || permissions != "" {
				cfg = map[string]string{}
				if model != "" {
					cfg["model"] = model
				}
				if permissions != "" {
					cfg["permissions"] = permissions
				}
			}
			a, err := o.AddAgent(orchestrator.AgentSpec{
				Name: args[0], Role: role, Adapter: adapterName, Task: task, Team: team, Config: cfg,
			})
			if err != nil {
				return err
			}
			fmt.Printf("registered agent %s (id %s, adapter %s)\n", a.Name, a.ID, a.Adapter)
			if noStart {
				return nil
			}
			if _, err := o.StartAgent(a.Name); err != nil {
				return fmt.Errorf("registered but failed to start: %w", err)
			}
			fmt.Printf("started %s in tmux window %q (clishake attach to watch)\n", a.Name, a.Name)
			return nil
		},
	}
	addCmd.Flags().StringVar(&role, "role", "", "agent role (e.g. backend, reviewer)")
	addCmd.Flags().StringVar(&adapterName, "adapter", "", "harness adapter (default from config)")
	addCmd.Flags().StringVar(&model, "model", "", "harness model to launch with (e.g. opus, sonnet, claude-fable-5)")
	addCmd.Flags().StringVar(&permissions, "permissions", "", "permission profile: default | auto | full | plan (fewer approval prompts)")
	addCmd.Flags().StringVar(&task, "task", "", "initial task description")
	addCmd.Flags().StringVar(&team, "team", "", "team name")
	addCmd.Flags().BoolVar(&noStart, "no-start", false, "register without starting")

	startCmd := &cobra.Command{
		Use: "start <name>", Short: "Start a registered agent", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			a, err := o.StartAgent(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("started %s (pane %s, pid %d)\n", a.Name, a.Tmux.PaneID, a.PID)
			return nil
		},
	}
	var force bool
	stopCmd := &cobra.Command{
		Use: "stop <name>", Short: "Stop an agent (graceful interrupt first)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			if err := o.StopAgent(args[0], !force); err != nil {
				return err
			}
			fmt.Printf("stopped %s\n", args[0])
			return nil
		},
	}
	stopCmd.Flags().BoolVar(&force, "force", false, "kill immediately without graceful interrupt")

	restartCmd := &cobra.Command{
		Use: "restart <name>", Short: "Restart an agent in its pane", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			a, err := o.RestartAgent(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("restarted %s (restart #%d)\n", a.Name, a.RestartCount)
			return nil
		},
	}
	removeCmd := &cobra.Command{
		Use: "remove <name>", Short: "Stop and deregister an agent", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			if err := o.RemoveAgent(args[0]); err != nil {
				return err
			}
			fmt.Printf("removed %s\n", args[0])
			return nil
		},
	}
	var setRole, setTeam string
	setCmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Change an agent's role/team at runtime (re-clusters selectors)",
		Long: `Reassigns an agent's role and/or team while it runs. Group selectors
re-cluster immediately: @role:reviewer and @team:reviewers address whoever
holds that role/team right now. Example:

  clishake agent set opencode --team reviewers
  clishake send @team:reviewers "start the review pass"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("role") && !cmd.Flags().Changed("team") {
				return fmt.Errorf("nothing to change: pass --role and/or --team")
			}
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			var rolePtr, teamPtr *string
			if cmd.Flags().Changed("role") {
				rolePtr = &setRole
			}
			if cmd.Flags().Changed("team") {
				teamPtr = &setTeam
			}
			a, err := o.SetAgentMeta(args[0], rolePtr, teamPtr)
			if err != nil {
				return err
			}
			fmt.Printf("%s: role=%s team=%s\n", a.Name, orDash(a.Role), orDash(a.Team))
			return nil
		},
	}
	setCmd.Flags().StringVar(&setRole, "role", "", "new role")
	setCmd.Flags().StringVar(&setTeam, "team", "", "new team (empty string clears it)")

	focusCmd := &cobra.Command{
		Use: "focus <name>", Short: "Select the agent's tmux window and attach", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			if err := o.FocusAgent(args[0]); err != nil {
				o.Close()
				return err
			}
			argv := o.AttachArgs()
			o.Close()
			return execReplace(argv)
		},
	}
	agentCmd.AddCommand(addCmd, startCmd, stopCmd, restartCmd, removeCmd, setCmd, focusCmd)
	return agentCmd
}

func newSendCmd() *cobra.Command {
	var taskID, replyTo string
	cmd := &cobra.Command{
		Use:   "send <@selector> <message...>",
		Short: "Send a message to an agent, role, team, or @all",
		Long: `Selectors: @name, @role:<role>, @team:<team>, @all. A bare @word resolves
name first, then role, then team. Examples:
  clishake send @claude "Investigate the failing API tests"
  clishake send @role:reviewer "Inspect the authentication changes"`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			o.Poll()
			msgs, err := o.Send(callerSender(), args[0], strings.Join(args[1:], " "),
				messaging.SendOpts{TaskID: taskID, ReplyTo: replyTo})
			if err != nil {
				return err
			}
			for _, m := range msgs {
				fmt.Printf("→ %-12s %s (%s)\n", m.Recipient, m.ID, m.Delivery)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&taskID, "task", "", "related task ID")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "message ID being replied to")
	return cmd
}

func newBroadcastCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "broadcast <message...>",
		Short: "Send a message to all live agents",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			msgs, err := o.Broadcast(callerSender(), strings.Join(args, " "))
			if err != nil {
				return err
			}
			fmt.Printf("broadcast to %d agent(s)\n", len(msgs))
			for _, m := range msgs {
				fmt.Printf("→ %-12s %s (%s)\n", m.Recipient, m.ID, m.Delivery)
			}
			return nil
		},
	}
}

func newTasksCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tasks",
		Short: "List tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			list, err := o.Tasks.List()
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Println("no tasks (clishake task create --title ...)")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTATUS\tPRI\tOWNER\tTITLE")
			for _, t := range list {
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", t.ID, t.Status, t.Priority, t.Owner, t.Title)
			}
			return w.Flush()
		},
	}
}

func newTaskCmd() *cobra.Command {
	taskCmd := &cobra.Command{Use: "task", Short: "Manage tasks"}

	var title, desc, assign string
	var priority int
	var deps []string
	createCmd := &cobra.Command{
		Use: "create", Short: "Create a task",
		RunE: func(cmd *cobra.Command, args []string) error {
			if title == "" {
				return fmt.Errorf("--title is required")
			}
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			t, err := o.Tasks.Create(callerSender(), title, desc, assign, priority, deps)
			if err != nil {
				return err
			}
			fmt.Printf("created %s (%s)\n", t.ID, t.Status)
			if assign != "" {
				notifyOwner(o, t)
			}
			return nil
		},
	}
	createCmd.Flags().StringVar(&title, "title", "", "task title (required)")
	createCmd.Flags().StringVar(&desc, "description", "", "task description")
	createCmd.Flags().StringVar(&assign, "assign", "", "owner agent name")
	createCmd.Flags().IntVar(&priority, "priority", 0, "priority (higher = more urgent)")
	createCmd.Flags().StringSliceVar(&deps, "depends-on", nil, "task IDs this depends on")

	assignCmd := &cobra.Command{
		Use: "assign <task-id> <agent-name>", Short: "Assign a task to an agent", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			if a, err := o.Store.GetAgentByName(args[1]); err != nil {
				return err
			} else if a == nil {
				return fmt.Errorf("no agent named %q", args[1])
			}
			t, err := o.Tasks.Assign(callerSender(), args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Printf("assigned %s to %s\n", t.ID, t.Owner)
			notifyOwner(o, t)
			return nil
		},
	}
	var status, summary string
	updateCmd := &cobra.Command{
		Use: "update <task-id>", Short: "Update task status", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if status == "" {
				return fmt.Errorf("--status is required")
			}
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			t, err := o.Tasks.SetStatus(callerSender(), args[0], domain.TaskStatus(status), summary)
			if err != nil {
				return err
			}
			fmt.Printf("%s → %s\n", t.ID, t.Status)
			return nil
		},
	}
	updateCmd.Flags().StringVar(&status, "status", "", "new status (backlog|assigned|in_progress|blocked|in_review|completed|cancelled)")
	updateCmd.Flags().StringVar(&summary, "summary", "", "completion summary")

	taskCmd.AddCommand(createCmd, assignCmd, updateCmd)
	return taskCmd
}

// notifyOwner tells the owning agent about its assignment via the bus.
func notifyOwner(o *orchestrator.Orchestrator, t *domain.Task) {
	if t.Owner == "" {
		return
	}
	body := fmt.Sprintf("You are assigned task %s: %s", t.ID, t.Title)
	if t.Description != "" {
		body += " — " + t.Description
	}
	if _, err := o.Send(domain.LeadSender, "@"+t.Owner, body,
		messaging.SendOpts{Type: domain.MsgTask, TaskID: t.ID}); err != nil {
		fmt.Fprintf(os.Stderr, "note: task saved but owner not notified: %v\n", err)
	}
}

func newCleanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clean",
		Short: "Close orphan tmux windows no agent owns (leftovers of removed agents)",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			closed, err := o.CleanOrphans()
			if err != nil {
				return err
			}
			if len(closed) == 0 {
				fmt.Println("no orphan windows to clean")
				return nil
			}
			fmt.Printf("closed %d orphan window(s): %s\n", len(closed), strings.Join(closed, ", "))
			return nil
		},
	}
}

func newLoopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "loop <task...> | stop | status",
		Short: "Start/stop the team loop: keep every agent working toward a shared goal",
		Long: `The team loop is the clishake-level analogue of a harness "/loop": it sets a
shared goal, tells every agent to work toward it, and the supervisor re-engages
any agent that goes idle until the loop is stopped.

  clishake loop Ship the auth refactor with tests green
  clishake loop status
  clishake loop stop     # any agent that finishes the goal can end the loop`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			switch {
			case args[0] == "stop":
				if err := o.StopLoop(); err != nil {
					return err
				}
				fmt.Println("team loop stopped")
			case args[0] == "status":
				ls := o.Loop()
				if ls.Active {
					fmt.Printf("loop active — goal: %s\n", ls.Goal)
				} else if ls.Goal != "" {
					fmt.Printf("loop stopped — last goal: %s\n", ls.Goal)
				} else {
					fmt.Println("no team loop")
				}
			default:
				if err := o.StartLoop(strings.Join(args, " ")); err != nil {
					return err
				}
				fmt.Println("team loop started — agents will keep working until `clishake loop stop`")
			}
			return nil
		},
	}
}

func newNoteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "note <text...>",
		Short: "Append a shared note/decision to .clishake/context/notes.md",
		Long: `Notes are the session's shared memory: decisions, conventions, findings.
They live in .clishake/context/notes.md, are attributed to whoever wrote
them (the lead, or an agent via its CLISHAKE_AGENT identity), and every
agent is briefed to consult them.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			if err := o.AddNote(callerSender(), strings.Join(args, " ")); err != nil {
				return err
			}
			fmt.Println("noted → .clishake/context/notes.md")
			return nil
		},
	}
}

func newLogsCmd() *cobra.Command {
	var lines int
	var raw bool
	cmd := &cobra.Command{
		Use:   "logs <agent-name>",
		Short: "Show an agent's terminal output (rendered screen for live agents)",
		Long: `For a live agent, shows the rendered content of its terminal (what you
would see attached), which is the readable view for TUI harnesses like
Claude Code or Codex. For stopped agents, shows its captured output log
with terminal control sequences stripped.

--raw prints the untouched pipe log. WARNING: raw TUI output contains
terminal control sequences (mouse tracking, alternate screen, ...) that
will reprogram your terminal; pipe it to a file or use cat -v.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			a, err := o.Store.GetAgentByName(args[0])
			if err != nil {
				return err
			}
			if a == nil {
				return fmt.Errorf("no agent named %q", args[0])
			}
			if raw {
				b, err := os.ReadFile(o.AgentDir(a) + "/output.log")
				if err != nil {
					return fmt.Errorf("no output captured yet for %s", args[0])
				}
				_, err = os.Stdout.Write(b)
				return err
			}
			var text string
			if a.Tmux.PaneID != "" && a.Status.IsLive() {
				if captured, err := o.Tmux.CapturePane(a.Tmux.PaneID, lines); err == nil {
					text = strings.TrimRight(captured, "\n")
				}
			}
			if text == "" {
				b, err := os.ReadFile(o.AgentDir(a) + "/output.log")
				if err != nil {
					return fmt.Errorf("no output captured yet for %s", args[0])
				}
				text = StripANSI(string(b))
			}
			ls := strings.Split(text, "\n")
			if lines > 0 && len(ls) > lines {
				ls = ls[len(ls)-lines:]
			}
			for _, l := range ls {
				fmt.Printf("[%s] %s\n", a.Name, l)
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 50, "number of trailing lines")
	cmd.Flags().BoolVar(&raw, "raw", false, "print the raw pipe log (contains terminal control sequences)")
	return cmd
}

func newEventsCmd() *cobra.Command {
	var n int
	var follow bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Show the shared activity log",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			path := orchestrator.ClishakeDir(dir) + "/events.jsonl"
			evs, err := events.Tail(path, n)
			if err != nil {
				return err
			}
			printEv := func(ev domain.Event) {
				if jsonOut {
					b, _ := json.Marshal(ev)
					fmt.Println(string(b))
					return
				}
				payload := ""
				if len(ev.Payload) > 0 {
					b, _ := json.Marshal(ev.Payload)
					payload = string(b)
					if len(payload) > 100 {
						payload = payload[:100] + "…"
					}
				}
				fmt.Printf("%s  %-24s %-12s %-16s %s\n",
					ev.Timestamp.Local().Format("15:04:05"), ev.Type, ev.Actor, ev.Subject, payload)
			}
			for _, ev := range evs {
				printEv(ev)
			}
			if !follow {
				return nil
			}
			seen := len(evs)
			for {
				time.Sleep(700 * time.Millisecond)
				all, _, err := events.ReadAll(path)
				if err != nil {
					return err
				}
				for ; seen < len(all); seen++ {
					printEv(all[seen])
				}
			}
		},
	}
	cmd.Flags().IntVarP(&n, "lines", "n", 40, "number of trailing events (0 = all)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream new events")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "raw JSONL output")
	return cmd
}

func newMessagesCmd() *cobra.Command {
	var n int
	var with string
	cmd := &cobra.Command{
		Use:   "messages",
		Short: "Show message history",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			o.Poll()
			var msgs []*domain.Message
			if with != "" {
				msgs, err = o.Store.ListMessagesWith(with, n)
			} else {
				msgs, err = o.Store.ListMessages(n)
			}
			if err != nil {
				return err
			}
			for _, m := range msgs {
				mark := "→"
				if m.Delivery == domain.DeliveryFailed {
					mark = "✗"
				}
				fmt.Printf("%s  %-10s %s %-10s  %s\n",
					m.CreatedAt.Local().Format("15:04:05"), m.Sender, mark, m.Recipient, m.Body)
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&n, "lines", "n", 40, "number of messages")
	cmd.Flags().StringVar(&with, "with", "", "only messages sent by or to this agent")
	return cmd
}

func newApprovalsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approvals",
		Short: "List and decide approval requests",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			o.Poll()
			list, err := o.Store.ListApprovals("")
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Println("no approval requests")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTATE\tAGENT\tRISK\tACTION\tREASON")
			for _, ap := range list {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					ap.ID, ap.State, ap.AgentName, ap.Risk, ap.Action, ap.Reason)
			}
			return w.Flush()
		},
	}
	decide := func(grant bool) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			ap, err := o.Decide(args[0], grant)
			if err != nil {
				return err
			}
			fmt.Printf("%s: %s (%s by lead)\n", ap.ID, ap.Action, ap.State)
			return nil
		}
	}
	cmd.AddCommand(
		&cobra.Command{Use: "grant <id>", Short: "Approve a request", Args: cobra.ExactArgs(1), RunE: decide(true)},
		&cobra.Command{Use: "deny <id>", Short: "Deny a request", Args: cobra.ExactArgs(1), RunE: decide(false)},
	)
	return cmd
}

func newAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach",
		Short: "Attach to the project's tmux session (agent terminals)",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			if _, err := o.EnsureSession(); err != nil {
				o.Close()
				return err
			}
			argv := o.AttachArgs()
			o.Close()
			return execReplace(argv)
		},
	}
}

func newStopCmd() *cobra.Command {
	var killSession bool
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop all live agents (and optionally the tmux session)",
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()
			agents, err := o.Store.ListAgents()
			if err != nil {
				return err
			}
			for _, a := range agents {
				if a.Status.IsLive() && a.Adapter != "observed" {
					if err := o.StopAgent(a.Name, true); err != nil {
						fmt.Fprintf(os.Stderr, "stop %s: %v\n", a.Name, err)
					} else {
						fmt.Printf("stopped %s\n", a.Name)
					}
				}
			}
			if killSession {
				if err := o.Tmux.KillSession(o.Cfg.SessionName()); err == nil {
					fmt.Printf("killed tmux session %s\n", o.Cfg.SessionName())
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&killSession, "kill-session", false, "also kill the managed tmux session")
	return cmd
}

// newMockAgentCmd is the hidden in-pane entry point for the mock harness.
func newMockAgentCmd() *cobra.Command {
	var name, role, agentDir string
	cmd := &cobra.Command{
		Use:    "mock-agent",
		Hidden: true,
		Short:  "Run the built-in mock coding agent (internal; launched in tmux panes)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" || agentDir == "" {
				return fmt.Errorf("--name and --agent-dir are required")
			}
			wd, _ := os.Getwd()
			code := mockagent.Run(mockagent.Options{
				Name: name, Role: role, AgentDir: agentDir, ProjectDir: wd,
			})
			os.Exit(code)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "agent name")
	cmd.Flags().StringVar(&role, "role", "", "agent role")
	cmd.Flags().StringVar(&agentDir, "agent-dir", "", "agent runtime directory")
	return cmd
}

// execReplace replaces the current process with argv (for tmux attach).
func execReplace(argv []string) error {
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return sysExec(bin, argv, os.Environ())
}

// orDash renders empty strings as a dash for display.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
