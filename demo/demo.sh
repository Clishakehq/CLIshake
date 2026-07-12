#!/usr/bin/env bash
# clishake MVP demonstration.
#
# Walks the full orchestration workflow non-interactively against a sample
# git repository using two mock agents:
#   spawn -> assign task -> attributed work -> agent-to-agent messages ->
#   sub-agent discovery -> broadcast -> disconnect/reconnect -> stop/restart
#   -> failure detection & recovery -> audit log.
#
# Usage:
#   demo/demo.sh [demo-dir]     # default: a fresh temp directory
#   KEEP=1 demo/demo.sh         # keep the demo dir + tmux session afterwards
#
# The demo uses its own tmux socket (clishake-demo-<pid>) so it never touches
# your real tmux server or any other clishake project.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLISHAKE="${CLISHAKE:-$ROOT/clishake}"
if [ ! -x "$CLISHAKE" ]; then
  echo "building clishake…"
  (cd "$ROOT" && go build -o clishake ./cmd/clishake)
fi

DEMO_DIR="${1:-$(mktemp -d)/clishake-demo}"
SOCKET="clishake-demo-$$"
KEEP="${KEEP:-0}"
FAILURES=0

bold()  { printf '\033[1;35m%s\033[0m\n' "$*"; }
step()  { printf '\n'; bold "══ $* ══"; }
run()   { printf '\033[36m$ clishake %s\033[0m\n' "$*"; "$CLISHAKE" "$@"; }

# wait_until <timeout-sec> <description> <grep-pattern> <clishake-args...>
wait_until() {
  local timeout=$1 desc=$2 pattern=$3; shift 3
  local deadline=$((SECONDS + timeout))
  local out
  while (( SECONDS < deadline )); do
    # Capture first, grep after: `cmd | grep -q` would SIGPIPE the writer
    # and read as failure under pipefail.
    out=$("$CLISHAKE" "$@" 2>/dev/null || true)
    if grep -Eq "$pattern" <<<"$out"; then
      printf '  ✓ %s\n' "$desc"
      return 0
    fi
    sleep 1
  done
  printf '  ✗ TIMEOUT: %s (pattern %q not found in `clishake %s`)\n' "$desc" "$pattern" "$*"
  "$CLISHAKE" "$@" || true
  FAILURES=$((FAILURES + 1))
  return 1
}

cleanup() {
  if [ "$KEEP" != "1" ]; then
    (cd "$DEMO_DIR" 2>/dev/null && "$CLISHAKE" stop --kill-session >/dev/null 2>&1) || true
    tmux -L "$SOCKET" kill-server >/dev/null 2>&1 || true
    rm -rf "$DEMO_DIR"
  else
    bold "kept demo dir: $DEMO_DIR (tmux socket $SOCKET)"
    bold "watch agents:  tmux -L $SOCKET attach"
  fi
}
trap cleanup EXIT

step "1. Create a sample git repository"
mkdir -p "$DEMO_DIR"
cd "$DEMO_DIR"
git init -q -b main
echo "# demo project" > README.md
git add README.md && git commit -qm "initial commit"
echo "  project at $DEMO_DIR"

step "2. Initialize clishake and point it at an isolated tmux socket"
run init
sed -i.bak "s/socket = .*/socket = '$SOCKET'/" .clishake/config.toml && rm -f .clishake/config.toml.bak
grep "socket" .clishake/config.toml

step "3. Launch two mock agents: builder and reviewer"
run agent add builder --adapter mock --role builder --task "Build the feature"
run agent add reviewer --adapter mock --role reviewer --task "Review the work"
wait_until 15 "builder is ready" "builder .*ready" agents
wait_until 15 "reviewer is ready" "reviewer .*ready" agents

step "4. Each agent runs in its own tmux window (isolated worktree + branch)"
run agents
tmux -L "$SOCKET" list-windows -t "clishake-clishake-demo" -F '  tmux window: #{window_name} (pane #{pane_id})'

step "5. Create a task and assign it to builder"
TASK_LINE=$("$CLISHAKE" task create --title "Fix authentication regression" --assign builder)
echo "$TASK_LINE"
TASK_ID=$(echo "$TASK_LINE" | awk '{print $2}')
wait_until 10 "builder acknowledged the assignment" "builder .*ack: You are assigned" messages

step "6. Tell builder to do the work (attributed output + simulated progress)"
run send @builder --task "$TASK_ID" "work 2"
wait_until 20 "builder reported completion" "builder .*done: work 2" messages
wait_until 10 "task $TASK_ID marked completed" "$TASK_ID +completed" tasks
run logs builder -n 8

step "7. Builder spawns a sub-agent; it appears under its parent"
run send @builder "spawn helper"
wait_until 20 "sub-agent builder/helper discovered and completed" "builder/helper .*completed" agents
run agents

step "8. Agent-to-agent message through clishake (builder -> reviewer)"
run send @builder "tell reviewer Please inspect the authentication changes"
wait_until 15 "reviewer received and replied to builder" "reviewer .*ack: Please inspect" messages

step "9. Broadcast a status request to all agents"
run broadcast "status?"
wait_until 15 "builder answered the broadcast" "builder .*status:" messages
wait_until 15 "reviewer answered the broadcast" "reviewer .*status:" messages

step "10. Disconnect and reconnect (state survives across processes)"
echo "  (every clishake invocation is a fresh process; agents live in tmux)"
wait_until 10 "reconcile finds builder alive" "builder: alive" status
run status

step "11. Stop and restart an agent"
run agent stop builder
wait_until 10 "builder stopped" "builder .*stopped" agents
run agent restart builder
wait_until 15 "builder ready again after restart" "builder .*(ready|waiting)" agents

step "12. Unexpected failure is detected and recovered"
run send @reviewer "fail!"
wait_until 20 "reviewer failure detected (unexpected exit)" "reviewer .*failed" agents
run agent restart reviewer
wait_until 15 "reviewer recovered" "reviewer .*(ready|waiting)" agents

step "13. The shared event log shows the whole story with attribution"
run events -n 18

step "14. Full message history"
run messages -n 15

if [ "$FAILURES" -eq 0 ]; then
  step "DEMO PASSED — all $((14)) stages verified"
else
  step "DEMO FINISHED WITH $FAILURES FAILED CHECK(S)"
  exit 1
fi
