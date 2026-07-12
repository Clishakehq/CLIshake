package ask

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// translateTimeout bounds how long a backend CLI gets to produce a plan.
const translateTimeout = 120 * time.Second

// runBackend executes a backend CLI and returns its combined output.
// Tests stub this to avoid shelling out.
var runBackend = func(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("%s", msg)
	}
	return stdout.String(), nil
}

// lookPath resolves a backend binary in PATH. Tests stub this to simulate
// which AI CLIs are installed without touching the real PATH.
var lookPath = exec.LookPath

// backendDef is one candidate AI CLI translate can try, in order.
type backendDef struct {
	name string
	bin  string
	args func(prompt string) []string
}

var backends = []backendDef{
	{name: "claude", bin: "claude", args: func(p string) []string { return []string{"-p", p} }},
	{name: "codex", bin: "codex", args: func(p string) []string { return []string{"exec", p} }},
}

// Translate runs the configured backend CLI to turn the prompt into a Plan.
// Backends are tried in order: `claude -p <prompt>` (if claude is in PATH),
// then `codex exec <prompt>` (if codex is in PATH). If a backend is
// missing, fails to run, or produces output that doesn't parse into a
// usable plan, Translate falls through to the next one. It returns a
// helpful error naming both backends if none of them produced a plan.
func Translate(prompt string) (Plan, string, error) {
	var reasons []string

	for _, be := range backends {
		if _, err := lookPath(be.bin); err != nil {
			reasons = append(reasons, fmt.Sprintf("%s: not found in PATH", be.name))
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), translateTimeout)
		out, err := runBackend(ctx, be.bin, be.args(prompt)...)
		cancel()
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("%s: %v", be.name, err))
			continue
		}

		plan, perr := ExtractPlan(out)
		if perr != nil {
			reasons = append(reasons, fmt.Sprintf("%s: %v", be.name, perr))
			continue
		}
		return plan, be.name, nil
	}

	return Plan{}, "", fmt.Errorf(
		"could not translate request: neither the claude CLI nor the codex CLI produced a usable plan (%s); install the Claude CLI (claude) or the Codex CLI (codex) and make sure it is on PATH",
		strings.Join(reasons, "; "))
}
