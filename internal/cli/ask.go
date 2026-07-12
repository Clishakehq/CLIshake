package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/clishakehq/clishake/internal/ask"
	"github.com/clishakehq/clishake/internal/orchestrator"
)

// newAskCmd builds `clishake ask "<what you want>" [--yes] [--dry-run]`.
func newAskCmd() *cobra.Command {
	var yes, dryRun bool
	cmd := &cobra.Command{
		Use:   `ask "<what you want>" [--yes] [--dry-run]`,
		Short: "Translate a natural-language request into CLIshake commands",
		Long: `ask uses a locally installed AI CLI (claude or codex) to translate the
team lead's plain-English intent into one or more clishake commands, drawn
from a fixed whitelist of command shapes. It always prints the proposed
plan — the commands it would run and why — before doing anything, and asks
for confirmation unless --yes is given. --dry-run prints the plan and stops
without executing it.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := open()
			if err != nil {
				return err
			}
			defer o.Close()

			sc, err := buildAskContext(o)
			if err != nil {
				return err
			}

			query := strings.Join(args, " ")
			prompt := ask.BuildPrompt(sc, query)

			plan, backend, err := ask.Translate(prompt)
			if err != nil {
				return err
			}
			if err := ask.Validate(plan); err != nil {
				return fmt.Errorf("model produced an invalid plan: %w", err)
			}

			printPlan(plan, backend)

			if dryRun {
				return nil
			}

			if !yes {
				ok, err := confirmPlan(len(plan.Commands))
				if err != nil {
					return err
				}
				if !ok {
					fmt.Println("aborted; no commands run")
					return nil
				}
			}

			return runPlan(plan)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "run the plan without confirmation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan and stop without running it")
	return cmd
}

// buildAskContext gathers a compact snapshot of session state to give the
// translation model.
func buildAskContext(o *orchestrator.Orchestrator) (ask.SessionContext, error) {
	agents, err := o.Store.ListAgents()
	if err != nil {
		return ask.SessionContext{}, err
	}
	taskList, err := o.Tasks.List()
	if err != nil {
		return ask.SessionContext{}, err
	}
	msgs, err := o.Store.ListMessages(10)
	if err != nil {
		return ask.SessionContext{}, err
	}
	return ask.BuildContext(o.ProjectDir, agents, taskList, msgs, o.Registry.Names()), nil
}

// printPlan shows the explanation and the numbered commands as they would
// run, followed by which backend produced them.
func printPlan(plan ask.Plan, backend string) {
	if plan.Explanation != "" {
		fmt.Println(plan.Explanation)
		fmt.Println()
	}
	for i, argv := range plan.Commands {
		fmt.Printf("  %d. clishake %s\n", i+1, renderArgv(argv))
	}
	fmt.Printf("\n(translated by %s)\n", backend)
}

// renderArgv shows an argv the way it will actually be executed: tokens
// containing whitespace are quoted so the user can see the tokenization.
func renderArgv(argv []string) string {
	out := make([]string, len(argv))
	for i, tok := range argv {
		if strings.ContainsAny(tok, " \t\"") {
			out[i] = fmt.Sprintf("%q", tok)
		} else {
			out[i] = tok
		}
	}
	return strings.Join(out, " ")
}

// confirmPlan prompts on stderr and reads a yes/no answer from stdin.
func confirmPlan(n int) (bool, error) {
	fmt.Fprintf(os.Stderr, "run these %d command(s)? [y/N] ", n)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// runPlan executes each command in the plan sequentially by re-invoking the
// clishake executable, streaming its output live. It stops at the first
// failure and reports which step failed.
func runPlan(plan ask.Plan) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve clishake executable: %w", err)
	}
	for i, argv := range plan.Commands {
		step := exec.Command(self, argv...)
		step.Stdout = os.Stdout
		step.Stderr = os.Stderr
		step.Stdin = os.Stdin
		step.Env = os.Environ()
		if err := step.Run(); err != nil {
			return fmt.Errorf("step %d (clishake %s) failed: %w", i+1, renderArgv(argv), err)
		}
		fmt.Printf("✓ step %d: clishake %s\n", i+1, renderArgv(argv))
	}
	return nil
}
