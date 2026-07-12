# Contributing to CLIshake

Thanks for wanting to help. CLIshake is young and moves fast; this guide
keeps contributions smooth for everyone.

## Development setup

Prerequisites: Go ≥ 1.24, tmux ≥ 3.0, git.

```bash
git clone https://github.com/clishakehq/clishake && cd clishake
make build          # or: go build -o clishake ./cmd/clishake
make test           # unit tests (tmux and harnesses are faked)
make itest          # + integration tests against a real tmux server
make demo           # the full 14-stage orchestration demo, end to end
```

The demo is the fastest way to see the whole system work — it spins up an
isolated tmux server, two mock agents, tasks, messaging, sub-agents,
failure recovery, and tears everything down.

## Before you open a PR

- `make check` must pass: `gofmt` clean, `go vet` clean, all tests green.
- New behavior needs tests. Bug fixes need a test that fails without the
  fix. Pure UI changes in `internal/ui` should factor logic into pure,
  testable helpers (see `dashboard_test.go` for the pattern).
- If you touched anything an agent terminal interacts with (adapters,
  delivery, readiness), run the demo — and if you have a real harness
  installed (Claude Code, Codex, OpenCode, Copilot CLI, Antigravity),
  a quick live smoke test is worth gold. Field testing against real TUIs
  has caught every hard bug in this project's history.
- Keep commits focused; write messages that explain *why*.

## Project principles

These are load-bearing — PRs that violate them will bounce:

1. **Honest capability reporting.** CLIshake never claims a harness
   feature the active adapter cannot deliver. No heuristic guessing of
   agent state from prose output; adapters that can't parse something
   return nothing.
2. **Vendor-neutral core.** Nothing harness-specific outside
   `internal/adapter/*`. The orchestrator talks to the `Adapter`
   interface only.
3. **No silent data loss.** Never clobber files, never kill panes that
   aren't ours, never auto-resolve conflicts quietly.
4. **Explicit, auditable state.** Every state change is an event with an
   actor. If your feature changes state, it emits events.
5. **Terminal-native.** Everything must work in a plain terminal over SSH.

## Adding a harness adapter

Read [docs/adapters.md](docs/adapters.md). Most new TUI harnesses need no
Go code at all — a `tui.Spec` registration plus config overrides. If the
harness exposes durable artifacts (rosters, transcripts), implement the
optional `SubagentDiscoverer`/`LaunchBriefer` interfaces.

## Filing issues

Use the issue templates. For behavior bugs, the goldmine is:
`clishake doctor` output, the relevant slice of `.clishake/events.jsonl`,
and — for anything terminal-related — a screenshot of the pane
(`clishake logs <agent>` or the dashboard). Most historical bugs were
diagnosed from exactly those three artifacts.

## Code of conduct

Be excellent to each other. See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
