package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/clishakehq/clishake/internal/domain"
)

// ---------------------------------------------------------------------------
// Load defaults
// ---------------------------------------------------------------------------

func TestLoadDefaultsWhenNoFile(t *testing.T) {
	dir := t.TempDir()

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Defaults.Workspace != StrategyWorktree {
		t.Errorf("Defaults.Workspace = %q, want %q", cfg.Defaults.Workspace, StrategyWorktree)
	}
	if cfg.Defaults.Adapter != "mock" {
		t.Errorf("Defaults.Adapter = %q, want %q", cfg.Defaults.Adapter, "mock")
	}
	if cfg.Tmux.Socket != "clishake" {
		t.Errorf("Tmux.Socket = %q, want %q", cfg.Tmux.Socket, "clishake")
	}
	if cfg.Tmux.SessionPrefix != "clishake-" {
		t.Errorf("Tmux.SessionPrefix = %q, want %q", cfg.Tmux.SessionPrefix, "clishake-")
	}
	if cfg.Project.Name != filepath.Base(dir) {
		t.Errorf("Project.Name = %q, want %q (dir basename)", cfg.Project.Name, filepath.Base(dir))
	}
	if cfg.Defaults.Restart.Mode != "on-failure" {
		t.Errorf("Restart.Mode = %q, want %q", cfg.Defaults.Restart.Mode, "on-failure")
	}
	if cfg.Defaults.Permissions != domain.DefaultPermissions() {
		t.Errorf("Permissions = %+v, want defaults %+v", cfg.Defaults.Permissions, domain.DefaultPermissions())
	}
}

// ---------------------------------------------------------------------------
// Save / Load round-trip
// ---------------------------------------------------------------------------

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()

	cfg := Default(filepath.Base(dir))
	cfg.Tmux.Socket = "my-custom-socket"
	cfg.Defaults.Workspace = StrategyShared
	cfg.Defaults.Restart = RestartPolicy{
		Mode:        "always",
		MaxRestarts: 7,
		WindowSec:   120,
		BackoffSec:  4,
	}
	cfg.Agents = []AgentTemplate{
		{
			Name:    "builder",
			Role:    "builder",
			Adapter: "mock",
			Task:    "build things",
			Permissions: &domain.Permissions{
				ReadFiles:   true,
				ModifyFiles: true,
			},
			Config: map[string]string{"foo": "bar"},
		},
		{Name: "reviewer", Role: "reviewer", Adapter: "mock"},
	}

	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The config file should exist where expected.
	if _, err := os.Stat(filepath.Join(dir, Dir, FileName)); err != nil {
		t.Fatalf("expected config file to exist: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.Tmux.Socket != "my-custom-socket" {
		t.Errorf("Tmux.Socket = %q, want %q", loaded.Tmux.Socket, "my-custom-socket")
	}
	if loaded.Defaults.Workspace != StrategyShared {
		t.Errorf("Defaults.Workspace = %q, want %q", loaded.Defaults.Workspace, StrategyShared)
	}
	if loaded.Defaults.Restart != cfg.Defaults.Restart {
		t.Errorf("Restart = %+v, want %+v", loaded.Defaults.Restart, cfg.Defaults.Restart)
	}
	if len(loaded.Agents) != 2 {
		t.Fatalf("got %d agent templates, want 2", len(loaded.Agents))
	}
	if loaded.Agents[0].Name != "builder" || loaded.Agents[0].Config["foo"] != "bar" {
		t.Errorf("Agents[0] = %+v, want name=builder config[foo]=bar", loaded.Agents[0])
	}
	if loaded.Agents[0].Permissions == nil || !loaded.Agents[0].Permissions.ModifyFiles {
		t.Errorf("Agents[0].Permissions = %+v, want ModifyFiles=true", loaded.Agents[0].Permissions)
	}
	if loaded.Agents[1].Name != "reviewer" {
		t.Errorf("Agents[1].Name = %q, want %q", loaded.Agents[1].Name, "reviewer")
	}
}

// ---------------------------------------------------------------------------
// Malformed / invalid configs
// ---------------------------------------------------------------------------

func TestLoadMalformedTOMLErrors(t *testing.T) {
	dir := t.TempDir()
	mustMkConfigDir(t, dir)
	mustWriteConfig(t, dir, "this is not valid = = toml [[[")

	if _, err := Load(dir); err == nil {
		t.Fatal("Load() error = nil, want error for malformed TOML")
	}
}

func TestLoadInvalidWorkspaceErrors(t *testing.T) {
	dir := t.TempDir()
	mustMkConfigDir(t, dir)
	mustWriteConfig(t, dir, `
[defaults]
workspace = "not-a-real-strategy"
`)

	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load() error = nil, want error for invalid workspace strategy")
	}
}

func TestLoadDuplicateAgentTemplateNamesErrors(t *testing.T) {
	dir := t.TempDir()
	mustMkConfigDir(t, dir)
	mustWriteConfig(t, dir, `
[[agents]]
name = "builder"
role = "builder"

[[agents]]
name = "builder"
role = "duplicate"
`)

	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load() error = nil, want error for duplicate agent template name")
	}
}

func TestValidateInvalidRestartModeErrors(t *testing.T) {
	cfg := Default("proj")
	cfg.Defaults.Restart.Mode = "sometimes"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error for invalid restart mode")
	}
}

func TestValidateEmptyAgentTemplateNameErrors(t *testing.T) {
	cfg := Default("proj")
	cfg.Agents = []AgentTemplate{{Name: "", Role: "x"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error for empty agent template name")
	}
}

func TestValidateAcceptsEmptyWorkspaceAndRestartMode(t *testing.T) {
	cfg := Default("proj")
	cfg.Defaults.Workspace = ""
	cfg.Defaults.Restart.Mode = ""
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil for empty (unset) fields", err)
	}
}

// ---------------------------------------------------------------------------
// SessionName sanitization
// ---------------------------------------------------------------------------

func TestSessionNameSanitization(t *testing.T) {
	cases := []struct {
		project string
		want    string
	}{
		{"My Proj.2", "clishake-My-Proj-2"},
		{"simple", "clishake-simple"},
		{"a/b\\c:d", "clishake-a-b-c-d"},
		{"", "clishake-project"},
	}
	for _, c := range cases {
		cfg := Default(c.project)
		if got := cfg.SessionName(); got != c.want {
			t.Errorf("SessionName() for project %q = %q, want %q", c.project, got, c.want)
		}
	}
}

func TestSessionNameCustomPrefix(t *testing.T) {
	cfg := Default("proj")
	cfg.Tmux.SessionPrefix = "custom-"
	if got := cfg.SessionName(); got != "custom-proj" {
		t.Errorf("SessionName() = %q, want %q", got, "custom-proj")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mustMkConfigDir(t *testing.T, projectDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(projectDir, Dir), 0o755); err != nil {
		t.Fatalf("mkdir .clishake: %v", err)
	}
}

func mustWriteConfig(t *testing.T, projectDir, contents string) {
	t.Helper()
	path := filepath.Join(projectDir, Dir, FileName)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
