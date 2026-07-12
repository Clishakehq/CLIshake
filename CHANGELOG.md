# Changelog

## v0.1.0 — 2026-07-10

First release. CLIshake — the terminal coordination layer for
collaborating coding agents: one human lead, many coding-agent CLIs, one
tmux-backed, audited session. Built and field-tested against real Claude
Code, OpenAI Codex, OpenCode, GitHub Copilot CLI, and Antigravity CLI
sessions.

### Orchestration core
- Dedicated tmux server (own socket) with one window per agent; output
  piped per agent, exits detected via `remain-on-exit` forensics; all
  window operations target unique pane ids (names are cosmetic).
- SQLite materialized state (`.clishake/state.db`) + append-only JSONL
  audit log (`.clishake/events.jsonl`); disconnect/reconnect reconciles
  persisted agents against live panes and catches up on output produced
  while away.
- Process supervision: crash detection with exit codes, configurable
  restart policy with exponential backoff and crash-loop capping;
  conservative from restricted contexts (an agent's sandboxed CLIshake
  invocation can never mis-mark healthy agents as dead).
- Git worktree-per-agent workspace strategy by default (branch
  `clishake/<agent>`); shared-tree mode opt-in; cross-agent overlapping-
  file detection; non-clobbering guards throughout.

### Harness adapters (5, plus a built-in mock)
- One vendor-neutral contract with honest capability flags; nothing is
  surfaced that the active adapter cannot deliver.
- `mock` (protocol-native, powers tests and the demo), `claude-code`
  (briefing via `--append-system-prompt`; agent teams discovered from
  `~/.claude/teams` rosters), `codex` (briefing as prompt preamble), and
  spec-driven generic TUI adapters for `opencode`, `copilot`, and
  `antigravity` (binary `agy`) — every knob (`command`, `args`,
  `ready_marker`, `enter_delay_ms`, `settle_ms`) config-overridable.
- Delivery hardened against real TUI behavior observed live: bracketed
  paste (per-key injection drops characters), payload-scaled Enter delay,
  post-ready settle gate (composers draw before their input handlers
  attach), prompt-glyph readiness with dialog-cursor veto (trust/approval
  dialogs can never receive typed traffic), rendered-screen re-checks for
  stuck-starting agents.

### Coordination layer
- Structured messaging with attribution and delivery states; selectors
  `@name`, `@role:`, `@team:`, `@task:` (owner + contributors), `@all`;
  task board with a validated state machine; approval gates; append-only
  shared notes.
- Session briefing for every real agent: identity, team-scoped roster,
  workspace rules, and how to reply through the CLIshake CLI
  (`CLISHAKE_PROJECT` + `CLISHAKE_AGENT` env for targeting and
  attribution). Tasks and briefings delivered as routed messages after
  readiness — never as launch arguments (first-run dialogs swallow those).
- Supervisor delivery relay: when an agent in a sandboxed harness (e.g.
  Codex) can't push a message into a peer's terminal itself, the
  supervisor re-delivers it on the sender's behalf. Messages to the lead
  are database-backed and always land.
- An agent's initial `--task` now becomes a real entry on the shared task
  board, owned by that agent — so the board reflects who is working on
  what, and a restarted agent can see its assignment and status there.
- Restart-aware briefing: restarting an agent (`r`) no longer re-issues
  its initial task verbatim (which caused it to redo work). Instead the
  agent is told it was restarted — a fresh process with no memory — and
  pointed at the task board and its branch, with a reminder to do only
  what its task asks.
- Worktree reuse is robust: removing and re-adding an agent of the same
  name reuses its existing worktree instead of failing (the old check
  string-matched git's path, which differs on macOS via /tmp→/private/tmp).
- Dashboard: the terminal preview header spells out the dialog-answer keys
  (`a`/`A`/`d`) when an agent needs input.
- Live-synced context directory (`.clishake/context/`): session.md,
  roster.md, tasks.md regenerated on every relevant event; notes.md via
  `clishake note`.
- Teams as soft scoping: team members are briefed on their own team in
  full, outsiders as a count, with cross-team coordination routed via the
  lead; teamless agents (coordinators) hear everything.
- First-class coordinator role: coordination briefing + read-and-
  coordinate permission profile; validated live running a full
  create→assign→dispatch→collect→report cycle across two teams.
- Natural language: `clishake ask "<intent>"` (and `/ask` in the
  dashboard) translates intent into a whitelisted, always-confirmed plan
  of CLIshake commands via a locally installed AI CLI.

### Dashboard
- Four views (Tab / 1-4): Overview, Focus, Grid (up to six live terminals),
  and Chat (grouped broadcasts, per-agent colors, scrollable, in-view
  filter chips, 500-message history).
- NAV/INPUT mode badge; `@` selector autocomplete (agents, teams, roles,
  open tasks); harness permission dialogs answered in place (`a`/`A`/`d`
  with a "needs input" badge); live terminal previews including final
  screens of exited agents; "while you were away" summary on reattach.
- One-key escape: **F12 returns to the dashboard from inside any agent
  pane** (root-table binding on CLIshake's own tmux server; a branded
  status bar in every pane says so). `x x` on the selected agent removes
  it and closes its window; `/clean` (and `clishake clean`) sweeps orphan
  windows no agent owns.
- Two-tone handshake wordmark (`internal/brand`) on `--help`, `init`, and
  the empty dashboard.

### Tooling
- `clishake doctor` diagnostics; sanitized `logs` (raw TUI bytes only
  behind `--raw`); 16 tested packages including a real-tmux integration
  test; a 14-stage scripted demo (`demo/demo.sh`) covering the full MVP
  acceptance walkthrough.
