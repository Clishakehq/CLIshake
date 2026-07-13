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

### Optional interfaces

An adapter may implement extra interfaces to opt into richer behavior. The
orchestrator type-asserts for each; absent, it degrades gracefully.

| Interface | Purpose |
|---|---|
| `LaunchBriefer` | briefs at launch (system prompt / preamble) instead of receiving the briefing as the first routed message |
| `SubagentDiscoverer` | exposes sub-agents/teams via durable artifacts (Claude Code's `~/.claude/teams` rosters) |
| `StatusReporter` | `ReadStatus(screen)` → live **model + usage** parsed from the harness status line; empty fields when nothing is reliably shown (never guess). The supervisor reads this every ~3s from the captured pane and surfaces it read-only — no `/usage` keystrokes into the agent |
| `SkillHost` | `NativeSkillsDir()` → where the harness auto-loads skills (Claude Code: `.claude/skills`); clishake installs the shared team skills there (see [Shared skills](#shared-skills)) |

### Model, permissions, and slash passthrough

- **`BuildLaunch`** maps two cross-harness agent settings onto each harness's
  own flags: `model` (`--model`, or the harness's equivalent) and
  `permissions` (`default`/`auto`/`full`/`plan`). See
  [workspace-and-permissions.md](workspace-and-permissions.md) for the mapping.
- **`FormatInput`** (via `adapter.FormatRouted`) passes a slash command from
  the **lead** through verbatim — `@claude /compact`, `/model gpt-5` — so the
  recipient's harness runs it, while ordinary messages keep the
  `[clishake message from <sender>]` attribution prefix. Only the lead may pass
  a command through; a path like `/etc/hosts` is not treated as one.

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
- **Delivery is a bracketed paste, then a confirmed Enter.**
  Per-key injection races some frameworks' input handling and silently
  drops text (observed live with Antigravity), and the Enter pause grows
  with payload size (OpenCode kept a ~1800-char briefing unsubmitted at
  400 ms). A bracketed paste is also ingested asynchronously, so a single
  Enter can be dropped and the message left sitting in the composer
  (reported with Copilot). Delivery therefore **confirms the submit**:
  it snapshots the settled pane, sends Enter, and re-sends Enter if nothing
  on screen changed — retrying the keypress only, never re-pasting (an empty
  composer ignores a stray Enter; a second paste would double the text).
  After readiness, the first delivery also waits out a **settle period**
  (`settle_ms`, default 2500) — composers are often drawn before their input
  handlers attach, and text typed into that gap disappears (observed live
  with Copilot CLI and Antigravity).
- **The session briefing arrives as the first routed message** once the
  agent is ready and settled (audited, attributed to `clishake`), followed
  by the assigned task. Restarted agents are re-briefed automatically — a
  fresh process has no memory.
- `command`, `args`, `ready_marker`, `enter_delay_ms`, and `settle_ms` are
  all overridable per project under `[adapters.<name>]`, so a renamed
  binary or changed UI is a config edit, not a code change. A missing
  binary shows as "harness not installed" in `clishake doctor`.
- Harness notes (validated live): **Antigravity** installs as `agy` (the
  adapter's default); it read the context files and replied via
  `clishake send` after its per-file/command approvals. **Copilot CLI**
  asks folder-trust and command approvals on first run — the folder-trust
  dialog is now **auto-answered** by the supervisor (opt out with
  `auto_trust=false`), and a `--permissions auto` profile maps to
  `--allow-all-tools` so tool approvals don't recur either. **OpenCode**
  needs a configured AI provider (`/connect` inside its TUI) before it can
  respond; delivery works regardless.
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
3. **Typed delivery that actually lands.** Messages are pasted, then
   submitted with a **confirmed** Enter after a delay (`enter_delay_ms`,
   default 400 ms) — interactive TUIs drop an Enter that arrives in the same
   burst as pasted text, so delivery re-sends Enter until the composer reacts
   (keypress only, never re-pasting). Delivery to an agent whose composer has
   not rendered yet (status `starting`) fails honestly instead of being
   silently swallowed; resend once it is ready. Peer messages an agent sends
   from a sandbox are queued and delivered by the supervisor — see
   [architecture.md](architecture.md).

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
`args` (extra CLI args), `enter_delay_ms`, `settle_ms`, `ready_marker`,
`model` (the harness model, also `--model` on `agent add`), `permissions`
(the cross-harness profile, also `--permissions`), `auto_trust` (`false`
to stop the supervisor auto-answering the folder-trust dialog), and
`permission_mode` (claude-code only — a low-level override; pair it with
harness-side tool allowlists, e.g. `args = ["--allowedTools", "Bash"]`, so
agents can run CLIshake commands without a permission prompt). The generic
TUI adapter also takes `model_flag`, `perm_<profile>`, and
`status_model_pattern` / `status_usage_pattern` (regexps that read the live
model/usage from the status line).

## Shared skills

Shared team skills live in `.clishake/skills/` — one set every agent gets,
whatever its harness. Each skill is a subdirectory with a `SKILL.md` (the
Agent Skills format: YAML frontmatter `name`/`description`, then
instructions) or a flat `<name>.md`. clishake installs them into a harness's
native skills directory when it implements `SkillHost` (Claude Code's
`.claude/skills`), as symlinks to the canonical dir that never clobber a
skill the user placed there themselves; and it points every agent at
`.clishake/skills` in the launch briefing, so harnesses without a native
skills system still use them. Manage with `clishake skills` (list) and
`clishake skills sync`; the directory is committable, so a team shares skills
through git.

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
