package orchestrator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/clishakehq/clishake/internal/config"
	"github.com/clishakehq/clishake/internal/domain"
)

// git runs a git command in dir and returns trimmed stdout.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, s)
	}
	return s, nil
}

// isGitRepo reports whether dir is inside a git work tree.
func isGitRepo(dir string) bool {
	out, err := git(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// ensureWorkspace decides and prepares the working directory for an agent.
//
// Default strategy (worktree): each agent with ModifyFiles permission gets a
// dedicated git worktree at .clishake/worktrees/<name> on its own branch
// clishake/<name>. Read-only agents and non-git projects fall back to the
// project root. Strategy "shared" always uses the project root.
func (o *Orchestrator) ensureWorkspace(a *domain.Agent) (workDir, branch string, err error) {
	if o.Cfg.Defaults.Workspace == config.StrategyShared ||
		!a.Permissions.ModifyFiles ||
		!isGitRepo(o.ProjectDir) {
		return o.ProjectDir, "", nil
	}

	branch = "clishake/" + a.Name
	wtDir := filepath.Join(ClishakeDir(o.ProjectDir), "worktrees", a.Name)

	// Reuse an existing worktree at wtDir. A worktree directory contains a
	// `.git` FILE that points back to the main repo, so its presence is the
	// reliable signal — much more robust than string-matching `git worktree
	// list`, whose paths may be spelled differently (e.g. macOS resolves
	// /tmp to /private/tmp, so the naive match missed and wrongly errored
	// after an agent was removed and re-added). `git worktree repair`
	// re-registers a worktree whose bookkeeping drifted.
	if _, statErr := os.Stat(wtDir); statErr == nil {
		if _, gitErr := os.Stat(filepath.Join(wtDir, ".git")); gitErr == nil {
			_, _ = git(o.ProjectDir, "worktree", "repair", wtDir)
			if out, err := git(wtDir, "rev-parse", "--is-inside-work-tree"); err == nil && out == "true" {
				return wtDir, branch, nil
			}
		}
		// Directory exists but is not a git worktree — refuse to clobber
		// it (never silently lose files).
		return "", "", fmt.Errorf("%s exists but is not a git worktree; remove it or use --shared", wtDir)
	}

	// Does the branch already exist (e.g. from a removed agent)?
	branchExists := false
	if _, err := git(o.ProjectDir, "rev-parse", "--verify", "refs/heads/"+branch); err == nil {
		branchExists = true
	}
	if branchExists {
		_, err = git(o.ProjectDir, "worktree", "add", wtDir, branch)
	} else {
		_, err = git(o.ProjectDir, "worktree", "add", "-b", branch, wtDir)
	}
	if err != nil {
		return "", "", err
	}
	o.emit(domain.EvBranchChanged, "clishake", a.Name, map[string]any{
		"branch": branch, "worktree": wtDir,
	})
	return wtDir, branch, nil
}

// ChangedFiles returns the paths modified in an agent's working directory
// (git status --porcelain), relative to that worktree root.
func (o *Orchestrator) ChangedFiles(a *domain.Agent) ([]string, error) {
	dir := a.WorkDir
	if dir == "" {
		dir = o.ProjectDir
	}
	if !isGitRepo(dir) {
		return nil, nil
	}
	out, err := git(dir, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 3 {
			files = append(files, strings.TrimSpace(line[3:]))
		}
	}
	return files, nil
}

// DetectOverlaps finds files modified concurrently by more than one live
// agent and emits a conflict event per overlapping path (once per Poll
// cycle; the TUI/status surfaces them).
func (o *Orchestrator) DetectOverlaps() (map[string][]string, error) {
	agents, err := o.Store.ListAgents()
	if err != nil {
		return nil, err
	}
	byFile := map[string][]string{}
	for _, a := range agents {
		if !a.Status.IsLive() || !a.Permissions.ModifyFiles {
			continue
		}
		files, err := o.ChangedFiles(a)
		if err != nil {
			continue
		}
		for _, f := range files {
			byFile[f] = append(byFile[f], a.Name)
		}
	}
	overlaps := map[string][]string{}
	for f, names := range byFile {
		if len(names) > 1 {
			overlaps[f] = names
			o.emit(domain.EvConflictDetected, "clishake", f, map[string]any{"agents": names})
		}
	}
	return overlaps, nil
}
