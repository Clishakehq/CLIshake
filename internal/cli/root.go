// Package cli implements the clishake command tree.
package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/clishakehq/clishake/internal/adapter"
	adapterclaude "github.com/clishakehq/clishake/internal/adapter/claudecode"
	adaptercodex "github.com/clishakehq/clishake/internal/adapter/codex"
	adaptermock "github.com/clishakehq/clishake/internal/adapter/mock"
	adaptertui "github.com/clishakehq/clishake/internal/adapter/tui"
	"github.com/clishakehq/clishake/internal/brand"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/orchestrator"
	"github.com/clishakehq/clishake/internal/selfupdate"
)

// Version is set at build time via -ldflags.
var Version = "0.1.0"

// buildRegistry registers all built-in adapters. The last three are
// spec-driven generic TUI adapters (internal/adapter/tui): send-keys
// input, prompt-glyph readiness, post-ready briefing. Their binaries,
// args, and readiness markers are overridable per project via
// [adapters.<name>] in config.toml.
func buildRegistry() *adapter.Registry {
	reg := adapter.NewRegistry()
	reg.Register(adaptermock.New())
	reg.Register(adapterclaude.New())
	reg.Register(adaptercodex.New())
	reg.Register(adaptertui.New(adaptertui.Spec{
		Name:    "opencode",
		Command: "opencode",
	}))
	reg.Register(adaptertui.New(adaptertui.Spec{
		Name:    "copilot",
		Command: "copilot",
	}))
	reg.Register(adaptertui.New(adaptertui.Spec{
		Name:    "antigravity",
		Command: "agy", // Antigravity CLI installs as `agy`
	}))
	return reg
}

// open opens the orchestrator for the session project. Resolution order:
// CLISHAKE_PROJECT (set by clishake in every agent's environment, so agents
// can run clishake commands from their worktrees), then the nearest
// ancestor directory containing .clishake/config.toml, then the cwd.
func open() (*orchestrator.Orchestrator, error) {
	if dir := os.Getenv("CLISHAKE_PROJECT"); dir != "" {
		return orchestrator.Open(dir, buildRegistry())
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return orchestrator.Open(findProjectDir(cwd), buildRegistry())
}

// callerSender resolves who is invoking this clishake command. Agent
// processes are launched with CLISHAKE_AGENT set, so messages and task
// updates they make through the CLI are attributed to them; everything else
// is the human lead. This is advisory attribution (an agent process could
// unset the variable), recorded for auditability, not a security boundary.
func callerSender() string {
	if v := os.Getenv("CLISHAKE_AGENT"); v != "" {
		return v
	}
	return domain.LeadSender
}

// findProjectDir walks upward from dir looking for an initialized clishake
// project; returns dir itself when none is found.
func findProjectDir(dir string) string {
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".clishake", "config.toml")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		d = parent
	}
}

// Execute runs the CLI.
func Execute() {
	root := &cobra.Command{
		Use:   "clishake",
		Short: "CLIshake — " + brand.Tagline,
		Long: brand.Banner(Version) + `
CLIshake lets a human team lead launch, observe, coordinate, and control
multiple coding agents working concurrently on the same codebase, even when
those agents use different underlying harnesses (Claude Code, Codex, ...).

Run "clishake" with no arguments inside a project directory to start or
attach to the project's interactive session.`,
		Version: Version,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDashboard()
		},
		// After any command, print a one-line upgrade notice if a newer
		// release is already known from a prior (cached) check. No network
		// call here — it stays instant. The mock-agent subcommand runs
		// inside panes, so it stays silent.
		PersistentPostRun: func(cmd *cobra.Command, args []string) {
			switch cmd.Name() {
			case "mock-agent", "version", "update":
				// mock-agent runs inside panes; version/update already
				// report update status themselves — don't say it twice.
				return
			}
			if os.Getenv("CLISHAKE_AGENT") != "" {
				return
			}
			if n := selfupdate.Notice(Version, selfupdate.CachedLatest()); n != "" {
				fmt.Fprintln(os.Stderr, dimNotice(n))
			}
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(
		newInitCmd(),
		newVersionCmd(),
		newUpdateCmd(),
		newAskCmd(),
		newStatusCmd(),
		newAgentCmd(),
		newAgentsCmd(),
		newSendCmd(),
		newBroadcastCmd(),
		newTaskCmd(),
		newTasksCmd(),
		newLogsCmd(),
		newNoteCmd(),
		newCleanCmd(),
		newEventsCmd(),
		newMessagesCmd(),
		newApprovalsCmd(),
		newAttachCmd(),
		newDoctorCmd(),
		newStopCmd(),
		newMockAgentCmd(), // hidden: runs inside panes
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "clishake: %v\n", err)
		os.Exit(1)
	}
}

// dimNotice styles the upgrade notice so it reads as a footnote, not an error.
func dimNotice(s string) string {
	return "\x1b[2m" + s + "\x1b[0m"
}
