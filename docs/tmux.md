# The tmux model

tmux is foundational to CLIshake, not an integration: it is how agent
processes get real terminals, survive disconnects, and stay inspectable.

## A dedicated server, never yours

CLIshake runs everything on its **own tmux server** via a dedicated socket:

```
tmux -L clishake …        # every clishake tmux command uses -L <socket>
```

Your normal `tmux ls` will never show CLIshake sessions, and CLIshake never
enumerates or touches sessions outside its socket. (This mirrors the
isolated `claude-swarm-<id>` server Claude Code agent teams use.) The socket
name is configurable (`[tmux] socket` in config.toml) — the demo, for
example, uses a private `clishake-demo-<pid>` socket.

## Naming

```
socket:   clishake                     (config: tmux.socket)
session:  clishake-<project-name>      (config: tmux.session_prefix + project.name)
window:   <agent-name>                 one window per agent
pane:     tmux pane id (%N), recorded on the agent record
```

Every agent record stores `{session, window, pane_id}` plus the pane PID, so
any pane can be mapped back to its agent (and vice versa) at any time —
`clishake doctor` audits these mappings.

## Lifecycle mechanics

- **Launch**: `new-window -d -P -F '#{pane_id}' -t <session>: -n <agent> -c
  <workdir> '<command>'` — detached, so the dashboard stays in control.
- **Output capture**: `pipe-pane 'cat >> .clishake/agents/<id>/output.log'`.
  All supervision and attribution reads from this pipe; the pane stays a
  fully interactive terminal.
- **Exit forensics**: `remain-on-exit on` per pane — when the process dies,
  the pane remains with `pane_dead=1` and `pane_dead_status=<exit code>`,
  which is how CLIshake distinguishes clean completion from crashes even if
  it wasn't running at the time.
- **Restart**: `respawn-pane -k` reuses the same window (fresh pipe attached;
  a stale pipe from before the respawn would otherwise silently swallow
  output).
- **Input**: `send-keys -l -- <text>` + `Enter` for send-keys adapters;
  interrupts use adapter-declared keys (`C-c`, `Escape`).

## Attach, detach, focus

```bash
clishake attach              # exec's tmux attach on the clishake socket
clishake agent focus <name>  # selects the agent's window, then attaches
```

Inside the dashboard, `enter`/`f` on a selected agent suspends the UI and
attaches tmux directly. **F12 returns to the dashboard from any agent
pane** (a root-table binding on CLIshake's server — the key never reaches
the agent; the status bar says so), and standard tmux detach (`C-b d`)
works too. Standard tmux navigation works as usual (`C-b n/p` between
agent windows, `C-b z` zoom).

Detaching — or quitting CLIshake entirely — stops nothing: agents live on
the tmux server, keep working, and keep appending output to their piped
logs. The next CLIshake invocation reconciles and catches up (see
[recovery](recovery.md)).

## Health truth table

Pane existence is **not** process health. CLIshake checks, in order:

| pane exists | pane_dead | PID alive | conclusion |
|---|---|---|---|
| no | — | — | exited/killed externally (`disconnected` on reconcile) |
| yes | 1 | — | exited; code from `pane_dead_status` |
| yes | 0 | no | zombie window; treated as exited |
| yes | 0 | yes | running (adapter may still judge it unresponsive) |
