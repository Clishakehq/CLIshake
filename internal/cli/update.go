package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/clishakehq/clishake/internal/selfupdate"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the installed version and check for a newer release",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("clishake %s\n", Version)
			latest := selfupdate.Latest(context.Background())
			switch {
			case latest == "":
				// check disabled or unavailable (e.g. private repo) — stay quiet
			case selfupdate.Newer(Version, latest):
				fmt.Printf("→ %s is available. Update with: clishake update\n", latest)
			default:
				fmt.Println("→ you're on the latest release")
			}
			return nil
		},
	}
}

func newUpdateCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update CLIshake to the latest release",
		Long: `Updates clishake in place. If it was installed with 'go install', this
re-runs that to fetch the newest tagged release. Otherwise it prints the
one-line command for how you installed it. Use --check to only look for a
newer version without installing anything.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			latest := selfupdate.Latest(context.Background())
			switch {
			case latest == "":
				// Couldn't reach the release API (offline, checks disabled,
				// or the repo isn't public yet). Don't imply an update exists.
				fmt.Printf("clishake %s — couldn't check for a newer release right now.\n", Version)
				if check {
					return nil
				}
				fmt.Println("Reinstalling the latest tagged release anyway...")
			case !selfupdate.Newer(Version, latest):
				fmt.Printf("clishake %s is already the latest release.\n", Version)
				return nil
			default:
				fmt.Printf("A newer release is available: %s (you have %s)\n\n", latest, Version)
				if check {
					fmt.Println("run `clishake update` (without --check) to install it")
					return nil
				}
			}
			return runUpdate(latest)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only check for a newer version; don't install")
	return cmd
}

// runUpdate updates the binary using the same channel clishake was installed
// with: Homebrew (the clishakehq/homebrew-clishake tap), `go install`, or a source
// clone. It never shells out to brew — brew installs upgrade themselves — it
// just routes each channel to the right one-liner.
func runUpdate(latest string) error {
	ref := "latest"
	if latest != "" {
		ref = latest
	}
	target := "github.com/" + selfupdate.Repo + "/cmd/clishake@" + ref

	// Homebrew install? Those users upgrade with brew — running go install
	// would drop a second, shadowing copy into the Go bin dir.
	if isHomebrewInstall() {
		fmt.Println("Installed via Homebrew — update with:")
		fmt.Println("  brew upgrade clishake")
		return nil
	}

	// go install is the primary non-brew distribution path.
	if _, err := exec.LookPath("go"); err == nil {
		fmt.Printf("Updating via: go install %s\n", target)
		c := exec.Command("go", "install", target)
		c.Stdout, c.Stderr, c.Stdin = os.Stdout, os.Stderr, os.Stdin
		c.Env = os.Environ()
		if err := c.Run(); err != nil {
			return fmt.Errorf("go install failed: %w", err)
		}
		fmt.Println("done — run `clishake version` to confirm")
		fmt.Println("(go install writes to your Go bin dir; make sure it's on your PATH)")
		return nil
	}

	// No Go toolchain available: print the manual paths.
	fmt.Println("No `go` toolchain found. To update, either:")
	fmt.Printf("  • install Go, then run:  go install %s\n", target)
	fmt.Println("  • or from a clone:       git pull && make install")
	return nil
}

// isHomebrewInstall reports whether this binary was installed by Homebrew. It
// resolves symlinks to the real path first, so it works on both Apple Silicon
// (/opt/homebrew) and Intel (/usr/local, where the bin symlink alone is
// indistinguishable from a make install) by matching the Caskroom/Cellar.
func isHomebrewInstall() bool {
	p, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return strings.Contains(p, "/Caskroom/") ||
		strings.Contains(p, "/Cellar/") ||
		strings.Contains(p, "/homebrew/")
}
