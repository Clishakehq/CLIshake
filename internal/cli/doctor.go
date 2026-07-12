package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/clishakehq/clishake/internal/config"
	"github.com/clishakehq/clishake/internal/domain"
	"github.com/clishakehq/clishake/internal/orchestrator"
	"github.com/clishakehq/clishake/internal/state"
	"github.com/clishakehq/clishake/internal/tmux"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose tmux, adapters, config, state, and process health",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			problems := 0
			check := func(ok bool, label, detail string) {
				mark := "✓"
				if !ok {
					mark = "✗"
					problems++
				}
				fmt.Printf("%s %-28s %s\n", mark, label, detail)
			}

			// Config
			cfg, cfgErr := config.Load(dir)
			if cfgErr != nil {
				check(false, "config", cfgErr.Error())
				cfg = config.Default(filepath.Base(dir))
			} else {
				check(true, "config", filepath.Join(config.Dir, config.FileName))
			}

			// Project files
			cdir := orchestrator.ClishakeDir(dir)
			if _, err := os.Stat(cdir); err != nil {
				check(false, "project dir", cdir+" missing (run: clishake init)")
			} else {
				check(true, "project dir", cdir)
			}

			// tmux
			tc := tmux.NewClient(cfg.Tmux.Socket)
			check(tc.Available(), "tmux binary", "required; install tmux ≥ 3.0")
			if tc.Available() {
				sessName := cfg.SessionName()
				if tc.HasSession(sessName) {
					panes, err := tc.ListPanes(sessName)
					check(err == nil, "tmux session", fmt.Sprintf("%s (%d pane(s), socket %q)", sessName, len(panes), cfg.Tmux.Socket))
				} else {
					check(true, "tmux session", sessName+" not running (created on demand)")
				}
			}

			// Adapters
			reg := buildRegistry()
			for _, name := range []string{"mock", "claude-code", "codex", "opencode", "copilot", "antigravity"} {
				ad, ok := reg.Get(name)
				if !ok {
					check(false, "adapter "+name, "not registered")
					continue
				}
				installed, version, _ := ad.Detect()
				if installed {
					check(true, "adapter "+name, version)
				} else {
					// Missing external harness is informational, not an error.
					fmt.Printf("○ %-28s %s\n", "adapter "+name, "harness not installed")
				}
			}

			// State DB + stale process detection
			dbPath := filepath.Join(cdir, "state.db")
			if _, err := os.Stat(dbPath); err != nil {
				fmt.Printf("○ %-28s %s\n", "state db", "not created yet")
			} else {
				st, err := state.Open(dbPath)
				check(err == nil, "state db", dbPath)
				if err == nil {
					agents, aerr := st.ListAgents()
					check(aerr == nil, "agent registry", fmt.Sprintf("%d agent(s)", len(agents)))
					stale := 0
					var panes map[string]bool
					if tc.Available() && tc.HasSession(cfg.SessionName()) {
						panes = map[string]bool{}
						if ps, err := tc.ListPanes(cfg.SessionName()); err == nil {
							for _, p := range ps {
								panes[p.PaneID] = !p.Dead
							}
						}
					}
					for _, a := range agents {
						if a.Status.IsLive() && a.Adapter != "observed" {
							alive := panes != nil && panes[a.Tmux.PaneID]
							if !alive {
								stale++
								fmt.Printf("  ⚠ %s: status %q but no live pane (run clishake status to reconcile)\n", a.Name, a.Status)
							}
						}
					}
					if stale == 0 {
						check(true, "tmux mappings", "no stale agent↔pane mappings")
					} else {
						check(false, "tmux mappings", fmt.Sprintf("%d stale mapping(s)", stale))
					}
					st.Close()
				}
			}

			// Event log
			evPath := filepath.Join(cdir, "events.jsonl")
			if fi, err := os.Stat(evPath); err == nil {
				check(true, "event log", fmt.Sprintf("%s (%d bytes)", evPath, fi.Size()))
			} else {
				fmt.Printf("○ %-28s %s\n", "event log", "not created yet")
			}

			// Permissions sanity
			if cfg.Defaults.Permissions == (domain.Permissions{}) {
				fmt.Printf("○ %-28s %s\n", "default permissions", "all disabled — agents can do nothing; check config")
			}

			if problems > 0 {
				return fmt.Errorf("%d problem(s) found", problems)
			}
			fmt.Println("\nno problems found")
			return nil
		},
	}
}
