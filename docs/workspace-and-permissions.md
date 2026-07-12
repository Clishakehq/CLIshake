# Workspaces, Git strategy, permissions, and approvals

## The concurrency problem

Multiple agents editing one working tree will eventually overwrite each
other silently — the worst possible failure mode. CLIshake's answer is
isolation by default, integration on purpose.

## Default strategy: worktree-per-agent

When the project is a Git repository and an agent has the `modify_files`
permission, `agent start` gives it:

- a dedicated **worktree** at `.clishake/worktrees/<agent>/`
- a dedicated **branch** `clishake/<agent>` (reused across restarts)

Read-only agents (reviewers) run in the project root, seeing the real code
but expected not to write — pair this with `modify_files = false`.

The human lead integrates finished branches (`git merge clishake/builder`,
or a PR flow). CLIshake records branch ownership on the agent and task
records, and `DetectOverlaps` flags files modified by more than one live
agent (`repo.conflict_detected` events) even across worktrees.

**Trade-offs, briefly:** worktrees cost disk and a mental hop ("my change is
on the agent's branch, not main"), and cross-agent integration is explicit
rather than automatic. In exchange: no lost writes, clean attribution
per branch, trivial rollback (delete the branch), and crash isolation. The
alternatives — advisory locking (unenforceable across arbitrary CLIs),
patch handoffs (heavyweight), a merge queue (overkill for an MVP) — all
fail the "no silent data loss" bar or the "small enough to be reliable" bar.

### Shared mode (opt-in)

```toml
[defaults]
workspace = "shared"   # everyone in the project root
```

For solo-ish usage or non-Git projects. CLIshake still detects overlapping
modified files but **cannot prevent** concurrent edits in this mode, and
says so — it will never pretend otherwise.

### Safeguards

- A directory already existing at a worktree path that is *not* a registered
  worktree aborts the start (never clobbered).
- Agent removal never deletes worktrees or branches — cleanup of work
  products is always an explicit human action.
- `.clishake/` state (db, events, logs, worktrees) is gitignored.

## Permissions

Each agent carries a permission profile (defaults from
`[defaults.permissions]`, overridable per agent):

```
read_files   modify_files   run_commands   network_access
use_git      commit_changes merge_changes  delete_files
modify_config spawn_subagents send_messages access_secrets
outside_project
```

**Honesty note (important):** CLIshake enforces the permissions that pass
through its own hands — `send_messages` gates the message bus,
`modify_files` gates worktree assignment, and approval gates gate
clishake-mediated actions. For permissions inside a third-party harness
(e.g. whether Claude Code itself runs a shell command), CLIshake **cannot
reach into the harness process**; enforcement there belongs to the harness's
own permission system (claude-code adapter exposes `permission_mode` for
exactly this). CLIshake records intent, surfaces activity, and audits — it
does not claim to sandbox arbitrary CLIs, because it doesn't.

## Teams: scoping who sees whom

Assign agents to teams (`--team` at add time, or live via
`clishake agent set <name> --team reviewers`) and visibility scopes with
them:

- A team member's **briefing** lists its own team in full; agents outside
  the team appear only as a count, with a standing norm: coordinate
  cross-team through the lead.
- **Roster-update notifications** (agent joined/stopped) stay within the
  affected agent's team. Teamless agents — coordinators, generalists —
  hear everything from every team, on purpose.
- Address a whole team with `@team:<name>`; a task's participants with
  `@task:<task-id>` (owner + contributors, resolved at send time).

Honesty note: this is briefing/bus-level scoping, not enforcement. All
harnesses run as the same OS user in the same project — a determined agent
can still read the shared context files or the repo. Team scoping steers
agents (which, in practice, they follow closely — they only know what
their briefing tells them); it does not build walls.

## The coordinator role

`--role coordinator` makes an agent the session coordinator, first-class:

- Its briefing adds coordination responsibilities: break work into tasks
  and assign them, track `agents`/`tasks`/`messages`, nudge idle or blocked
  agents, record decisions with `clishake note`, report summaries to the
  lead, escalate conflicts and approvals. It is told NOT to implement.
- Unless you pass explicit permissions, it gets the **coordinator profile**:
  read files, run commands, use git, send messages — but no file
  modifications, so it runs in the project root with full visibility
  instead of a worktree.
- Approvals stay with the human lead; a coordinator cannot decide them.

```bash
clishake agent add coordinator --adapter claude-code --role coordinator \
  --task "Coordinate the team: triage the backlog, assign tasks, keep me posted"
```

The human remains team lead: the coordinator is one more (auditable,
attributed) participant, not a replacement control plane.

## Approvals

Agents (via adapters with structured output) can request approval:

```
##clishake:{"type":"approval","action":"merge","reason":"...","risk":"high"}
```

The agent flips to `awaiting_approval`, the request appears in
`clishake approvals` and the dashboard, and the lead decides:

```bash
clishake approvals            # ID, agent, action, risk, reason
clishake approvals grant ap_1a2b3c4d
clishake approvals deny  ap_1a2b3c4d
```

The decision is evented (`approval.granted`/`denied`), messaged back to the
requesting agent, and the agent resumes. Configure which action classes
*should* seek approval in `[approval] require_for` — adapters/agents honor
this by convention; see the honesty note above.
