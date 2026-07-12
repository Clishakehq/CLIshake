package claudecode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/clishakehq/clishake/internal/domain"
)

// writeTeam creates a fixture roster in the fake home directory.
func writeTeam(t *testing.T, home, team, content string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "teams", team)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverSubagentsFromTeamRoster(t *testing.T) {
	home := t.TempDir()
	orig := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	defer func() { userHomeDir = orig }()

	// The roster shape Claude Code writes for agent teams.
	writeTeam(t, home, "session-a672a58c", `{
	  "name": "session-a672a58c",
	  "leadAgentId": "team-lead@session-a672a58c",
	  "members": [
	    {"agentId":"team-lead@session-a672a58c","name":"team-lead","agentType":"team-lead","cwd":"/proj/wt/claude"},
	    {"agentId":"scout@session-a672a58c","name":"scout","agentType":"general-purpose","cwd":"/proj/wt/claude"},
	    {"agentId":"tester@session-a672a58c","name":"tester","agentType":"code-reviewer","cwd":"/proj/wt/claude"}
	  ]
	}`)
	// A team for a DIFFERENT directory must not match.
	writeTeam(t, home, "session-other111", `{
	  "members": [
	    {"name":"team-lead","agentType":"team-lead","cwd":"/elsewhere"},
	    {"name":"ghost","agentType":"general-purpose","cwd":"/elsewhere"}
	  ]
	}`)
	// Corrupt rosters are skipped, never guessed.
	writeTeam(t, home, "session-corrupt1", `{not json`)

	a := &domain.Agent{Name: "claude", WorkDir: "/proj/wt/claude"}
	infos := New().DiscoverSubagents(a)
	if len(infos) != 2 {
		t.Fatalf("DiscoverSubagents = %d members, want 2 (lead excluded): %+v", len(infos), infos)
	}
	names := map[string]string{}
	for _, i := range infos {
		names[i.Name] = i.Role
		if i.Status != domain.StatusRunning {
			t.Errorf("%s status = %s, want running", i.Name, i.Status)
		}
	}
	if names["scout"] != "general-purpose" || names["tester"] != "code-reviewer" {
		t.Fatalf("wrong members/roles: %v", names)
	}
}

func TestDiscoverSubagentsNoTeamsDir(t *testing.T) {
	home := t.TempDir() // no .claude/teams at all
	orig := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	defer func() { userHomeDir = orig }()

	if got := New().DiscoverSubagents(&domain.Agent{Name: "c", WorkDir: "/proj"}); got != nil {
		t.Fatalf("expected nil without a teams dir, got %v", got)
	}
	if got := New().DiscoverSubagents(&domain.Agent{Name: "c"}); got != nil {
		t.Fatalf("expected nil for empty workdir, got %v", got)
	}
}
