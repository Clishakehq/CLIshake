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

### Reducing in-harness prompts: `--permissions` and `--model`

The profile above is CLIshake's own gate. Separately, you can hand the
harness a **launch-time permission profile** so it re-prompts less *during*
the session — set once at add time, alongside the model:

```bash
clishake agent add builder --adapter claude-code \
  --permissions auto --model sonnet
```

`--permissions` (or the `permissions` agent config) takes one cross-harness
profile — `default | auto | full | plan` — which each adapter maps to its
harness's real flags (validated against the installed binaries):

| profile   | claude-code                      | codex                                                | copilot / generic TUI                 |
| --------- | -------------------------------- | ---------------------------------------------------- | ------------------------------------- |
| `default` | *(nothing — asks as usual)*      | *(nothing)*                                           | *(nothing)*                           |
| `auto`    | `--permission-mode acceptEdits`  | `--sandbox workspace-write --ask-for-approval never` | `--allow-all-tools`                   |
| `full`    | `--dangerously-skip-permissions` | `--dangerously-bypass-approvals-and-sandbox`         | `--allow-all-tools --allow-all-paths` |
| `plan`    | `--permission-mode plan`         | `--sandbox read-only`                                | —                                     |

- **claude-code:** the raw `permission_mode` config still wins as a
  low-level override (it is passed as `--permission-mode <mode>` verbatim).
- **generic TUI / copilot:** flags come from the adapter's `PermissionFlags`
  map; override any entry per agent with a `perm_<profile>` config value
  (e.g. `perm_auto`).
- **`--model`** picks the harness model at the same time — `opus`, `sonnet`,
  `claude-fable-5`, or a codex/copilot model name. It maps to `--model` for
  claude-code and codex, and to the generic TUI's configurable `model_flag`.

**Two things these profiles deliberately do NOT do (important):**

- They never skip the **one-time folder-trust dialog** — that is a separate
  startup gate (auto-answered; see below).
- `full` on claude-code (`--dangerously-skip-permissions`) makes Claude show
  a separate **"Bypass Permissions mode" danger warning** (cursor defaults to
  "No, exit"). CLIshake will **not** auto-accept a danger acknowledgement —
  answer it once from the dashboard, or **prefer `auto`**, which clears
  in-session tool prompts without tripping the danger gate.

### Folder-trust auto-answer

The first time a harness opens a directory it shows a one-time
folder-trust dialog. Because every agent gets its own worktree
(`.clishake/worktrees/<agent>/`), that prompt would otherwise reappear once
*per agent*. The supervisor auto-answers it: when a starting agent's
rendered screen shows a numbered selection dialog whose text mentions
"trust", it sends Enter — the cursor defaults to "Yes, trust", which is safe
for a worktree derived from the lead's own project.

It is deliberately narrow. It fires **only** on a genuine trust dialog (text
contains "trust" *and* a numbered prompt), fires once per start (re-armed on
respawn so a fresh worktree re-answers), and **never** answers the
bypass-permissions danger prompt above. Opt out per agent with
`auto_trust=false`.

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
