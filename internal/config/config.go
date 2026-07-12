// Package config loads and validates the project-level configuration at
// .clishake/config.toml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"github.com/clishakehq/clishake/internal/domain"
)

// Dir is the project-relative clishake directory name.
const Dir = ".clishake"

// FileName is the config file name inside Dir.
const FileName = "config.toml"

// WorkspaceStrategy selects how concurrent agents share the repository.
type WorkspaceStrategy string

const (
	// StrategyWorktree gives each editing agent a dedicated git worktree
	// and branch under .clishake/worktrees/<agent>. Safest default.
	StrategyWorktree WorkspaceStrategy = "worktree"
	// StrategyShared runs every agent in the project root. Opt-in only;
	// clishake detects overlapping file changes but cannot prevent them.
	StrategyShared WorkspaceStrategy = "shared"
)

// Config is the root of .clishake/config.toml.
type Config struct {
	Project  ProjectConfig            `toml:"project"`
	Tmux     TmuxConfig               `toml:"tmux"`
	Defaults DefaultsConfig           `toml:"defaults"`
	Logging  LoggingConfig            `toml:"logging"`
	Adapters map[string]AdapterConfig `toml:"adapters"`
	Agents   []AgentTemplate          `toml:"agents"`
	Approval ApprovalConfig           `toml:"approval"`
}

// ProjectConfig holds project identity settings.
type ProjectConfig struct {
	Name string `toml:"name"` // used in tmux session name; defaults to directory basename
}

// TmuxConfig controls the managed tmux server.
type TmuxConfig struct {
	// Socket is the dedicated tmux server socket name (tmux -L). Keeping
	// clishake on its own server guarantees zero interference with the
	// user's normal tmux sessions.
	Socket string `toml:"socket"`
	// SessionPrefix prefixes the tmux session name: "<prefix><project>".
	SessionPrefix string `toml:"session_prefix"`
}

// DefaultsConfig holds cross-agent defaults.
type DefaultsConfig struct {
	Workspace   WorkspaceStrategy  `toml:"workspace"`   // worktree | shared
	Adapter     string             `toml:"adapter"`     // default adapter for new agents
	Restart     RestartPolicy      `toml:"restart"`     // default restart policy
	Permissions domain.Permissions `toml:"permissions"` // default permission profile
}

// RestartPolicy controls supervisor restart behavior.
type RestartPolicy struct {
	// Mode: "never" | "on-failure" | "always"
	Mode string `toml:"mode"`
	// MaxRestarts within Window before the agent is marked failed
	// permanently (crash-loop protection).
	MaxRestarts int `toml:"max_restarts"`
	// WindowSec is the crash-loop measurement window in seconds.
	WindowSec int `toml:"window_sec"`
	// BackoffSec is the base delay before a restart; doubles per attempt.
	BackoffSec int `toml:"backoff_sec"`
}

// LoggingConfig controls log output.
type LoggingConfig struct {
	// Level: "debug" | "info" | "warn" | "error"
	Level string `toml:"level"`
}

// AdapterConfig configures one adapter entry.
type AdapterConfig struct {
	// Enabled toggles the adapter without deleting its config.
	Enabled *bool `toml:"enabled"`
	// Command overrides the harness executable path.
	Command string `toml:"command"`
	// Args are extra arguments appended to the launch command.
	Args []string `toml:"args"`
	// Options are adapter-specific string settings.
	Options map[string]string `toml:"options"`
}

// AgentTemplate predefines an agent that can be spawned by name.
type AgentTemplate struct {
	Name        string              `toml:"name"`
	Role        string              `toml:"role"`
	Adapter     string              `toml:"adapter"`
	Task        string              `toml:"task"`
	Permissions *domain.Permissions `toml:"permissions"`
	Config      map[string]string   `toml:"config"`
}

// ApprovalConfig controls the approval gate.
type ApprovalConfig struct {
	// RequireFor lists actions that need lead approval, e.g.
	// ["merge", "delete", "network"].
	RequireFor []string `toml:"require_for"`
}

// Default returns the built-in configuration.
func Default(projectName string) Config {
	return Config{
		Project: ProjectConfig{Name: projectName},
		Tmux: TmuxConfig{
			Socket:        "clishake",
			SessionPrefix: "clishake-",
		},
		Defaults: DefaultsConfig{
			Workspace: StrategyWorktree,
			Adapter:   "mock",
			Restart: RestartPolicy{
				Mode:        "on-failure",
				MaxRestarts: 3,
				WindowSec:   300,
				BackoffSec:  2,
			},
			Permissions: domain.DefaultPermissions(),
		},
		Logging:  LoggingConfig{Level: "info"},
		Adapters: map[string]AdapterConfig{},
	}
}

// Load reads the config for projectDir. Missing file returns Default with
// no error (zero-config startup); a malformed file is an error.
func Load(projectDir string) (Config, error) {
	name := filepath.Base(projectDir)
	path := filepath.Join(projectDir, Dir, FileName)
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(name), nil
		}
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	cfg := Default(name)
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes cfg to projectDir/.clishake/config.toml.
func Save(projectDir string, cfg Config) error {
	if err := os.MkdirAll(filepath.Join(projectDir, Dir), 0o755); err != nil {
		return err
	}
	b, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(filepath.Join(projectDir, Dir, FileName), b, 0o644)
}

// Validate checks semantic constraints.
func (c Config) Validate() error {
	switch c.Defaults.Workspace {
	case StrategyWorktree, StrategyShared, "":
	default:
		return fmt.Errorf("defaults.workspace: unknown strategy %q (want %q or %q)",
			c.Defaults.Workspace, StrategyWorktree, StrategyShared)
	}
	switch c.Defaults.Restart.Mode {
	case "never", "on-failure", "always", "":
	default:
		return fmt.Errorf("defaults.restart.mode: unknown mode %q (want never|on-failure|always)",
			c.Defaults.Restart.Mode)
	}
	seen := map[string]bool{}
	for _, t := range c.Agents {
		if t.Name == "" {
			return errors.New("agents: template with empty name")
		}
		if seen[t.Name] {
			return fmt.Errorf("agents: duplicate template name %q", t.Name)
		}
		seen[t.Name] = true
	}
	return nil
}

// SessionName returns the tmux session name for this project.
func (c Config) SessionName() string {
	return c.Tmux.SessionPrefix + sanitize(c.Project.Name)
}

// sanitize makes a string safe for tmux session names (no dots/colons).
func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "project"
	}
	return string(out)
}
