package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/clishakehq/clishake/internal/adapter"
	"github.com/clishakehq/clishake/internal/domain"
)

// Claude Code agent teams leave durable artifacts on disk (documented from
// the roster files Claude Code writes for agent teams):
//
//	~/.claude/teams/<team>/config.json   — roster: members with name,
//	                                       agentType, cwd, tmuxPaneId
//
// where <team> is "session-" + the first 8 chars of the lead session id.
// A team belongs to one of OUR agents when a member's cwd is the agent's
// working directory — under the default worktree strategy every clishake
// agent has a unique cwd, which makes the match precise. (In shared mode
// several agents share the project root; discovery may then attribute a
// team to the wrong same-directory agent — documented caveat.)

// userHomeDir is swappable in tests.
var userHomeDir = os.UserHomeDir

type teamConfig struct {
	Name    string       `json:"name"`
	Members []teamMember `json:"members"`
}

type teamMember struct {
	AgentID   string `json:"agentId"`
	Name      string `json:"name"`
	AgentType string `json:"agentType"`
	Cwd       string `json:"cwd"`
}

// DiscoverSubagents implements adapter.SubagentDiscoverer: it scans the
// Claude Code teams directory for rosters whose members run in the
// agent's working directory and reports every non-lead member as a live
// sub-agent. Unreadable or unparseable rosters are skipped, never guessed.
func (*A) DiscoverSubagents(a *domain.Agent) []adapter.SubagentInfo {
	if a.WorkDir == "" {
		return nil
	}
	home, err := userHomeDir()
	if err != nil || home == "" {
		return nil
	}
	base := filepath.Join(home, ".claude", "teams")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []adapter.SubagentInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(base, e.Name(), "config.json"))
		if err != nil {
			continue
		}
		var tc teamConfig
		if json.Unmarshal(b, &tc) != nil {
			continue
		}
		owns := false
		for _, mem := range tc.Members {
			if mem.Cwd == a.WorkDir {
				owns = true
				break
			}
		}
		if !owns {
			continue
		}
		for _, mem := range tc.Members {
			if mem.AgentType == "team-lead" || mem.Name == "team-lead" || mem.Name == "" {
				continue // the lead IS our managed agent
			}
			out = append(out, adapter.SubagentInfo{
				Name:   mem.Name,
				Role:   mem.AgentType,
				Status: domain.StatusRunning,
			})
		}
	}
	return out
}
