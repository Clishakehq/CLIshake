# Recovery, troubleshooting, and known limitations

## What survives what

| You do | Agents | Orchestration state |
|---|---|---|
| quit the dashboard / close your terminal | keep running (tmux) | intact (state.db + events.jsonl) |
| detach tmux | keep running | intact |
| reboot / tmux server dies | die | intact; agents marked `disconnected` on next start, restartable |
| `clishake stop` | stopped deliberately | intact |
| `clishake stop --kill-session` | stopped + session gone | intact |

Persisted: session identity, project path, the full agent registry
(config, permissions, tmux refs, PIDs, branches, restart counts, output
offsets), tasks, messages, approvals, parent/child relations, and the entire
event history.

## Reattach flow

Every `clishake` / `clishake status` invocation:

1. Detects existing project state in `.clishake/`.
2. Inspects the CLIshake tmux socket for the project session.
3. **Reconciles** each stored agent against live panes:
   `alive` (PID refreshed) · `exited while detached` (dead pane's exit code
   recorded, status becomes completed/failed) · `disconnected` (pane gone).
4. Reports orphan panes it does not own; never kills them.
5. Catches up on agent output produced while detached (per-agent byte
   offsets into the piped logs are persisted), so messages/status/task
   reports emitted in the gap are processed, not lost.
6. Never duplicates an already-running agent: `agent start` refuses when the
   agent is live, and `agent add` refuses duplicate names.

Recovery is deliberately conservative: `disconnected` agents are *not*
auto-relaunched; `clishake agent restart <name>` (or `r` in the dashboard)
does it explicitly.

## Crash handling

Unexpected exits (non-zero, not lead-initiated) mark the agent `failed`,
record the exit code, and — under `restart.mode = "on-failure"` — schedule
an automatic respawn with exponential backoff, capped by
`max_restarts`/`window_sec` (crash-loop protection; after the cap the agent
stays `failed` for a human). Automatic respawns execute in a long-lived
CLIshake process (the dashboard). One-shot CLI invocations still *detect*
and record every failure; recovery there is the explicit restart command.

## Troubleshooting

Start with:

```bash
clishake doctor
```

It checks: tmux availability, per-adapter harness presence/version, config
validity, `.clishake/` layout, state DB health, and stale agent↔pane
mappings.

| Symptom | Likely cause / fix |
|---|---|
| `tmux not found in PATH` | install tmux ≥ 3.0 |
| agent stuck in `starting` | the harness is likely waiting on a first-run dialog (folder trust, login, MCP auth) — the dashboard badges it "⚠ needs input"; answer with a/A/d, or `clishake agent focus <name>` to answer manually; `clishake status` flags agents starting for >1 min |
| agent `disconnected` after reboot | expected — `clishake agent restart <name>` |
| messages `failed` delivery | recipient not live, still `starting` (TUI composer not up yet — resend when ready), or an observed sub-agent (no terminal) — check `clishake agents` |
| `clishake logs` looked garbled / terminal went weird | fixed: `logs` now shows the rendered screen (live agents) or a control-sequence-stripped log; only `--raw` prints untouched TUI bytes |
| `…exists but is not a registered git worktree` | leftover directory at `.clishake/worktrees/<name>` — remove it or switch to shared mode |
| two CLIshake sessions fighting | one project = one session; the state DB serializes concurrent CLI calls, but run one dashboard at a time |
| watching agents directly | `tmux -L clishake attach` (read-only: `attach -r`) |

## Known limitations (MVP, stated plainly)

- **Real-harness insight is process-level.** For Claude Code/Codex, CLIshake
  truthfully tracks launch/readiness/exit/restart and delivers typed input,
  but does not parse their interactive output into statuses or messages.
  Claude Code agent TEAMS are discovered from its on-disk rosters
  (~/.claude/teams/*/config.json) and shown as sub-agents in the tree —
  members are matched by working directory, which is precise under the
  default worktree strategy and ambiguous in shared mode. (Outbound communication works fine: the
  launch briefing teaches agents to message and report through the CLIshake
  CLI, with attribution via `CLISHAKE_AGENT`.)
- **Supervision from restricted contexts is deliberately conservative.**
  When CLIshake is invoked somewhere the managed tmux server is unreachable
  (e.g. by an agent inside its harness sandbox), that invocation skips
  process-exit checks entirely rather than mis-diagnosing healthy agents as
  dead. Real exit detection happens from the lead's own invocations and the
  dashboard.
- **Attribution of agent-run CLI commands is advisory.** `CLISHAKE_AGENT`
  is plain environment; agents run as your user and could unset it. It's an
  audit signal, not a security boundary.
- **Agent-to-agent delivery from a sandboxed harness is relayed, not
  direct.** Some harnesses (e.g. Codex) run the shell commands their agent
  issues inside a sandbox that blocks access to the tmux server. When such
  an agent runs `clishake send <peer>`, it cannot push into the peer's
  terminal itself — the message is recorded but its own delivery fails.
  The supervisor (the dashboard, or any lead-run `clishake`, which has full
  tmux access) then **re-delivers it on the sender's behalf** on its next
  poll. Messages to the lead never hit this — they are database-backed and
  always succeed. Practical implication: agent-to-agent pushes land
  promptly while a dashboard is open; otherwise on the next lead
  invocation. Routing coordination through the lead or the shared task
  board is always immediate.
- **Permissions inside third-party harnesses are not enforceable by
  CLIshake** — see the honesty note in
  [workspaces & permissions](workspace-and-permissions.md).
- **No sandboxing.** Agents run as your user. Worktrees isolate *edits*, not
  processes.
- **Automatic crash-restarts need a running CLIshake process** (dashboard).
- **Sub-agent discovery requires the harness to say something** (structured
  output). Manual registration fallback:
  `clishake agent add <parent>-<child> --no-start` is a stopgap, honest
  about being one.
- **Conflict detection is advisory** — overlapping-file events, not merge
  resolution. Integration of agent branches is a human step.
- **Adapters are compiled in**; `.clishake/adapters/` dynamic loading is
  schema-scaffolded but not implemented.
- Single machine, single project per session; no cloud control plane, no
  browser UI (by design for the MVP).
