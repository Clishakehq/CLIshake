# Harness adapters

An adapter is the seam between CLIshake's vendor-neutral core and one
specific coding-agent CLI. Adapters never touch tmux; they describe *what to
launch* and *how to interpret output*, and the orchestrator owns terminals.

## The contract (`internal/adapter.Adapter`)

| Method | Responsibility |
|---|---|
| `Name()` | stable registry key (`mock`, `claude-code`, `codex`, …) |
| `Capabilities()` | honest feature list (see flags below) |
| `Detect()` | is the harness installed? which version? |
| `ValidateConfig(cfg)` | check adapter-specific agent config before launch |
| `BuildLaunch(agent, projectDir)` | argv + env + workdir to run in the pane |
| `InputMode()` | `send-keys` (typed into the pane) or `file` (inbox JSONL) |
| `FormatInput(agent, msg)` | render one message for delivery |
| `ParseOutput(agent, chunk)` | extract structured events; **never guess** |
| `DetectReady(agent, chunk)` | did startup finish? |
| `CheckHealth(agent, alive, outputAge)` | ok / unresponsive / unknown |
| `InterruptKeys()` | graceful-interrupt keys (e.g. `C-c`), empty = kill only |

### Capability flags

```
structured_input   structured_output   subagents         agent_teams
task_events        tool_events         runtime_reconfiguration
graceful_interrupt session_resume      working_directory_override
permission_controls
```

**Rule: CLIshake never surfaces a feature unless the active adapter declares
the capability or a documented fallback exists.** The universal input
fallback is send-keys typing; there is no output fallback — an adapter that
cannot parse output simply yields no structured events, and supervision falls
back to process-level signals only.

## Built-in adapters

| | mock | claude-code | codex | opencode / copilot / antigravity |
|---|---|---|---|---|
| input | `file` (inbox JSONL) | `send-keys` | `send-keys` | `send-keys` |
| structured output | yes (`##clishake:` lines) | no (honest: none claimed) | no | no |
| sub-agents | yes (simulated) | teams discovered from ~/.claude/teams rosters | no | no |
| readiness | `status: ready` line | composer prompt (`❯`) rendered | composer prompt (`›`) rendered | prompt-glyph heuristic + `ready_marker` |
| briefing | n/a (protocol-native) | `--append-system-prompt` | initial-prompt preamble | first routed message after ready |
| interrupt | `C-c` | `Escape` | `C-c` | `Escape` |

### The generic TUI adapter (opencode, copilot, antigravity)

OpenCode, GitHub Copilot CLI, and Antigravity CLI share one spec-driven
implementation (`internal/adapter/tui`): launch the binary in a pane, type
instructions in, supervise at the process level. Because their UI details
vary by version (and none exposes a verified system-prompt flag):

- **Readiness** uses a prompt-glyph heuristic — a rendered line starting
  with `❯`, `›`, `>`, or `┃` that is *not* a numbered menu cursor — and a
  **selection dialog anywhere on screen vetoes readiness outright**:
  dialogs (folder trust, tool approval) overlay the screen while the
  composer glyph is still visible, and input delivered then lands in the
  dialog (observed live with Copilot CLI). If a harness version uses a
  different composer, set a positive marker in config:
  `[adapters.opencode.options] ready_marker = "Ask anything"`. Until
  readiness fires the agent stays `starting` (flagged in `status` with an
  attach hint) and deliveries fail honestly instead of being swallowed.
- **Delivery is a bracketed paste, then Enter after a scaled pause.**
  Per-key injection races some frameworks' input handling and silently
  drops text (observed live with Antigravity), and the Enter pause grows
  with payload size (OpenCode kept a ~1800-char briefing unsubmitted at
  400 ms). After readiness, the first delivery also waits out a **settle
  period** (`settle_ms`, default 2500) — composers are often drawn before
  their input handlers attach, and text typed into that gap disappears
  (observed live with Copilot CLI and Antigravity).
- **The session briefing arrives as the first routed message** once the
  agent is ready and settled (audited, attributed to `clishake`), followed
  by the assigned task. Restarted agents are re-briefed automatically — a
  fresh process has no memory.
- `command`, `args`, `ready_marker`, `enter_delay_ms`, and `settle_ms` are
  all overridable per project under `[adapters.<name>]`, so a renamed
  binary or changed UI is a config edit, not a code change. A missing
  binary shows as "harness not installed" in `clishake doctor`.
- Harness notes (validated live 2026-07-10): **Antigravity** installs as
  `agy` (the adapter's default); it read the context files and replied via
  `clishake send` after its per-file/command approvals. **Copilot CLI**
  asks folder-trust and command approvals on first run — answer once from
  the dashboard (a/A/d keys; pick the "remember/allow list" options) and
  the loop runs hands-free afterwards. **OpenCode** needs a configured AI
  provider (`/connect` inside its TUI) before it can respond; delivery
  works regardless.
- Caveat: send-keys harnesses must be full-screen TUIs (raw-mode input).
  Plain line-based REPLs cap input lines at the tty's canonical limit
  (~1024 chars) and will drop the briefing.

The Claude Code and Codex adapters launch the real interactive CLIs and
deliver instructions by typing into the pane. Three mechanisms make real
harnesses first-class session members:

1. **Session briefing.** At launch the orchestrator composes a briefing —
   the agent's identity/role, the current roster, the workspace rules, and
   how to communicate — and injects it (system prompt for Claude Code,
   prompt preamble for Codex). Roster changes are pushed to settled agents
   as `[clishake message from clishake]` updates.
2. **The CLIshake CLI as the reply channel.** Briefed agents message
   teammates and the lead, and update tasks, by running CLIshake commands
   from their own shell. `CLISHAKE_PROJECT` (set in every agent's
   environment) makes those commands target the session project from any
   directory, and `CLISHAKE_AGENT` attributes them to the agent —
   advisory attribution for auditability, not a security boundary.
3. **Typed delivery that actually lands.** Messages are typed literally,
   then submitted with Enter after a delay (`enter_delay_ms`, default
   400 ms) — interactive TUIs drop an Enter that arrives in the same burst
   as pasted text. Delivery to an agent whose composer has not rendered yet
   (status `starting`) fails honestly instead of being silently swallowed;
   resend once it is ready.

Supervision beyond that is process-level (alive/exited/restart), which is
exactly what CLIshake can truthfully provide today — with one structured
exception: **Claude Code agent teams are discovered from its on-disk
rosters** (`~/.claude/teams/*/config.json`). Members are matched to the
clishake agent by
working directory (precise under the default worktree strategy; ambiguous
in shared mode), registered as observed sub-agents in the tree, and marked
completed when they leave the roster. Reading its mailbox *contents*
remains future work, not a claimed feature.

Per-agent adapter settings go in the agent's `config` map or the
project-level `[adapters.<name>]` section: `command` (executable override),
`args` (extra CLI args), `enter_delay_ms`, `permission_mode` (claude-code
only — pair it with harness-side tool allowlists, e.g.
`args = ["--allowedTools", "Bash"]`, so agents can run CLIshake commands
without a permission prompt).

## The wire protocol (`internal/wire`)

Any harness (or a thin wrapper script around one) can opt into structured
integration by emitting marker lines on stdout:

```
##clishake:{"type":"status","status":"running"}
##clishake:{"type":"message","to":"reviewer","text":"please inspect"}
##clishake:{"type":"task","task_id":"task_ab12","status":"completed"}
##clishake:{"type":"subagent","name":"helper","role":"tests","status":"running"}
##clishake:{"type":"approval","action":"merge","reason":"...","risk":"high"}
##clishake:{"type":"log","text":"notable but unstructured"}
```

and by reading inbox envelopes (one JSON object per line) from
`.clishake/agents/<id>/inbox.jsonl`:

```json
{"from":"lead","text":"work 3","timestamp":"2026-07-09T12:00:00Z",
 "msg_id":"msg_1a2b3c4d","type":"message","task_id":"task_ab12"}
```

The envelope schema intentionally mirrors the mailbox format used by Claude
Code agent teams (append by sender; recipient tracks its own read offset).

## Adding a new harness adapter

1. Create `internal/adapter/<name>/<name>.go` implementing `adapter.Adapter`.
   Start from `internal/adapter/codex/codex.go` (simplest real one).
2. Declare **only** the capabilities you actually implement.
3. `ParseOutput` must return nothing for lines it cannot parse — never
   heuristically guess status from prose.
4. Register it in `internal/cli/root.go` (`buildRegistry`).
5. Add tests: capability list, `BuildLaunch` command shape, `FormatInput`
   round-trip, `ParseOutput` on a mixed chunk, `DetectReady`.
6. `clishake doctor` will automatically report its Detect() result.

A dynamic plugin loader (`.clishake/adapters/`) is scaffolded in the config
schema but not yet implemented; adapters are compiled in today. See
[known limitations](recovery.md#known-limitations).

## The mock agent

`clishake mock-agent --name N --role R --agent-dir DIR` (hidden subcommand)
runs the built-in simulated coding agent used by the demo and tests. Command
grammar (first word of any message it receives):

| command | behavior |
|---|---|
| `work [n]` | n steps of simulated work → `done:` reply, task completion if the message carried a task |
| `status?` | replies with its current status |
| `tell <agent> <text>` | sends a message to another agent through CLIshake |
| `spawn [name]` | reports a simulated sub-agent (running → completed) |
| `complete` | completes task + exits 0 |
| `fail!` | exits 1 (crash simulation) |
| `stop` | exits 0 after reporting `stopped` |
| anything else | `ack:` reply (never acks an ack — loop guard) |
