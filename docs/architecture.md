# CLIshake architecture

## Component map

```
cmd/clishake                 CLI entry point
internal/cli                 command tree (cobra) + dashboard bootstrap
internal/ui                  interactive dashboard (Bubble Tea)
internal/orchestrator        THE CORE: session, lifecycle, supervision,
                             delivery, reconciliation, workspaces, approvals,
                             team loop, shared skills
internal/adapter             harness adapter contract + registry
internal/adapter/mock        built-in simulated coding agent adapter
internal/adapter/claudecode  Claude Code adapter (send-keys input)
internal/adapter/codex       OpenAI Codex adapter (send-keys input)
internal/mockagent           the mock agent runtime (runs inside panes)
internal/messaging           selector parsing + message routing
internal/tasks               task coordination service
internal/state               SQLite persistence (materialized current state)
internal/events              append-only JSONL audit log + in-process pub/sub
internal/tmux                dedicated-socket tmux client
internal/config              .clishake/config.toml loading/validation
internal/wire                shared line protocols (structured output, inbox envelopes)
internal/domain              core types; imported by everything, imports nothing
```

Dependency rule: `domain` is the shared vocabulary; the orchestrator is the
only package that composes state + tmux + adapters + messaging + tasks.
Nothing vendor-specific exists outside `internal/adapter/*`.

## Two sources of truth, deliberately

| Store | Purpose | Failure mode it covers |
|---|---|---|
| `.clishake/state.db` (SQLite, WAL) | materialized *current* state: agents, tasks, messages, approvals, session | fast queries, concurrent CLI invocations |
| `.clishake/events.jsonl` (append-only JSONL) | *history*: every state change with actor, subject, timestamp, payload | audit, attribution, post-mortem, recovery |

Every mutation goes through the orchestrator, which writes state and appends
an event. Corrupt event lines are skipped (never fatal) when reading.

## Event model

Events are `domain.Event{ID, Type, Timestamp, Actor, Subject, SessionID,
Payload, CorrelationID}`. Types cover the full lifecycle: `agent.created/
started/ready/status_changed/exited/restarted/removed`,
`agent.subagent_discovered`, `message.sent/delivered/failed`,
`task.created/assigned/updated`, `repo.file_changed/branch_changed/
conflict_detected`, `approval.requested/granted/denied`,
`session.created/attached/detached`, `config.changed`.

Actor is always the responsible party: `lead` (the human), an agent name, or
`clishake` (the supervisor itself). This is what makes the log an
attribution record and not just a debug trace.

## Message flow

```
lead types "@builder work 3"
  └─ orchestrator.Send(lead, "@builder", "work 3")
       ├─ messaging.ParseSelector + Resolve (name → role → team precedence)
       ├─ per recipient: persist Message (state.db) …
       ├─ … deliver via the agent's adapter:
       │     InputFile   → append wire.Envelope to .clishake/agents/<id>/inbox.jsonl
       │     InputSendKeys → bracketed-paste into the pane, then confirmed submit
       └─ events: message.sent + message.delivered / message.failed
```

Agents reply through their *output*: the pane is piped
(`tmux pipe-pane`) to `.clishake/agents/<id>/output.log`; the supervisor's
`Poll()` reads new bytes, hands them to the adapter's `ParseOutput`, and maps
structured lines (`##clishake:{json}`) onto orchestration state: status
changes, agent→agent/lead messages (re-entering the router with full
attribution), task progress, sub-agent registration, approval requests.

Messages to the **lead** are persisted as delivered immediately — the human
reads them in the dashboard / `clishake messages`, not in a terminal.

This mirrors the design of Claude Code agent teams: file-based mailboxes
appended by the sender and drained by the recipient, structured control
payloads riding the same channel as freeform chat, and
coordination-by-shared-state (the task board) alongside messaging.

## Deterministic peer delivery

The diagram above is the **lead's** send: the dashboard process owns the
terminals, so it pastes straight into the recipient's pane. An **agent's**
`clishake send` cannot — it runs inside the harness's sandbox (Codex, others),
which can reach the workspace filesystem but not CLIshake's tmux socket. So an
agent's send never touches a pane: `deliver` returns `messaging.ErrQueued` when
`CLISHAKE_AGENT` is set, and the router records the message as `DeliveryPending`
— queued, not failed (no `message.failed` event).

The shared SQLite state DB is the reliable cross-process bridge: it lives inside
the agent's writable workspace, whereas the tmux socket sits outside the sandbox.
The single process that owns the terminals — the supervisor — drains the queue in
`Orchestrator.DeliverQueued` (internal/orchestrator/supervise.go): it retries
every `DeliveryPending` / `DeliveryFailed` message on each tick until it is
delivered, its recipient reaches a terminal status, or it ages out (10 min).
There is no fixed attempt cap, so nothing is dropped while a recipient is merely
busy. This replaced an earlier scheme where each agent pasted directly and a
supervisor relay re-attempted the failures, dropping after three tries — under
which Codex→Claude sends intermittently never arrived.

**150ms delivery pump.** To keep queued peer messages feeling instant, the
dashboard runs a fast 150ms tick (`deliverTick` / `deliverInterval` in
internal/ui/dashboard.go) that calls `DeliverQueued` between the 1s supervision
`Poll()` ticks — a queued message lands in ~1–2s rather than up to a second
later. It stays inside the single Bubble Tea loop (no goroutines or mutexes, so
no double-send races) and is nearly free when the queue is empty (two indexed
reads and out).

**Confirmed submit.** Send-keys delivery — the lead's direct paste and the
supervisor's queued delivery alike — no longer fires Enter and hopes. A bracketed
paste is ingested asynchronously, so an Enter can arrive before the TUI has
finished reading the paste and get dropped, leaving the text unsubmitted in the
composer. `submitComposer` (internal/orchestrator/deliver.go) snapshots the
settled pane, sends Enter, and re-sends it while the pane shows no reaction —
retrying the keypress only, never re-pasting (an empty composer ignores a stray
Enter, whereas a second paste would double the text). A genuinely stuck delivery
is left to the supervisor to redeliver whole.

## The context directory (file-based shared context)

`.clishake/context/` is the session's CLAUDE.md-equivalent — file-based
context every agent can read, but **maintained by CLIshake instead of by
hand** and updated live:

| File | Content | Updated |
|---|---|---|
| `session.md` | session identity + communication protocol | on relevant events |
| `roster.md` | live agent roster: roles, harnesses, status, branches, tasks | on agent/task/session/approval events |
| `tasks.md` | the shared task board | same |
| `notes.md` | append-only shared notes & decisions | `clishake note "..."` (attributed via `CLISHAKE_AGENT`) |

The orchestrator subscribes to its own event log and regenerates the files
whenever agents, tasks, or approvals change (message traffic is excluded —
too chatty, and already queryable). The launch briefing points every agent
at these files, which turns context from a launch-time snapshot into
something agents can re-pull at any moment. `clishake init` also writes
`.clishake/.gitignore` so runtime state (db, events, logs, worktrees,
context) stays out of the project's history while `config.toml` remains
committable.

## Shared skills

`.clishake/skills/` is the canonical place to maintain team capabilities every
agent gets, whatever its harness. Each skill is a subdirectory with a `SKILL.md`
(the Agent Skills format: YAML frontmatter then instructions) or a flat
`<name>.md`. On agent launch — and on demand via `clishake skills sync` — the
orchestrator (internal/orchestrator/skills.go) installs them into a harness's
native skills directory when it has one, via the optional `adapter.SkillHost`
interface (Claude Code's `.claude/skills`), as symlinks to the canonical dir so
edits stay live and a real skill the user placed there is never clobbered.
Harnesses without a native skills system still reach them: every agent's launch
briefing points at `.clishake/skills`. The directory is committable, so a team
shares skills through git.

## Supervision

`Poll()` runs one cycle (the dashboard runs it every second; every CLI
invocation runs it at least once):

1. **Consume output** per agent from its piped log (offset persisted in the
   agent record, so catch-up works across CLIshake restarts and while
   detached).
2. **Check the process**: dead pane (`remain-on-exit` keeps it inspectable,
   with exit status), missing pane, or dead PID. *Pane existence is never
   treated as proof of process health.*
3. **Classify exits**: intentional stop (status `stopped`) vs clean exit
   (`completed`) vs crash (`failed`), with the exit code recorded and an
   `agent.exited` event emitted.
4. **Restart policy**: `on-failure`/`always` with exponential backoff and a
   crash-loop cap (`max_restarts` within `window_sec`, then permanent
   `failed` for human attention). Automatic restarts run in a long-lived
   CLIshake process (the dashboard); one-shot CLI invocations detect and
   record failures, and `clishake agent restart` recovers manually.

The same cycle also does the supervisor's non-lifecycle work: it retries any
deferred first deliveries (briefing, then assigned task, gated behind a
post-ready settle period), drains the peer-delivery queue (`DeliverQueued`,
above), reads each harness's live status, and drives the team loop. The latter
three run only when pane state is trusted, and `DeliverQueued`/`driveLoop`
additionally no-op when `CLISHAKE_AGENT` is set — only the supervisor process,
which owns the terminals, performs them.

**Live status** (`readLiveStatus`, throttled to ~every 3s): the supervisor
already has each agent's screen on the socket, so it captures the pane and — via
the optional `adapter.StatusReporter` interface — records the model and usage the
harness reports onto the agent's Config (`_live_model` / `_usage`) for the
dashboard header. It reads the harness's own status line; it types nothing into
the agent.

**Team loop** (`driveLoop`, internal/orchestrator/loop.go): while a loop is
active, any live agent idle longer than 25s (by `LastActivity`) is re-nudged
back toward the shared goal, rate-limited per agent, so the team keeps pushing
until the lead stops it. The loop's state (`LoopState`) is persisted to
`.clishake/loop.json` rather than the DB, precisely so a separate or sandboxed
process — an agent running `clishake loop stop` — can end it too.

## Reconciliation

On open/reattach, `Reconcile()` compares persisted agents against live tmux
panes on CLIshake's dedicated socket:

- live-status agent, pane missing → `disconnected`
- live-status agent, dead pane → record the exit that happened while detached
- live agent, live pane → refresh PID from tmux
- panes not owned by any agent → reported as orphans (never killed silently)

## Sub-agents and hierarchy

Agents are a tree (`ParentID`), not a flat list. When a harness reports a
sub-agent (mock adapter: `##clishake:{"type":"subagent",...}`), CLIshake
registers it as an **observed** agent (`adapter: "observed"`) under its
parent: visible in the tree, attributed in the log, but with no pane or PID
of its own. Where a harness exposes nothing, that limitation is stated
rather than faked — see [Recovery & limitations](recovery.md).
