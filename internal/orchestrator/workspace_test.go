package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/clishakehq/clishake/internal/adapter"
	adaptermock "github.com/clishakehq/clishake/internal/adapter/mock"
	"github.com/clishakehq/clishake/internal/domain"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.invalid"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

// TestEnsureWorkspaceReuseAfterReAdd covers the real bug: an agent's
// worktree, left behind after the agent was removed, must be REUSED (not
// rejected) when an agent of the same name is added again — even when the
// project path is spelled differently than git's own (macOS /tmp vs
// /private/tmp).
func TestEnsureWorkspaceReuseAfterReAdd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitInit(t, dir)

	reg := adapter.NewRegistry()
	reg.Register(adaptermock.New())
	o, err := Open(dir, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Close)

	a := &domain.Agent{Name: "claude", Permissions: domain.Permissions{ModifyFiles: true}}
	wt1, br1, err := o.ensureWorkspace(a)
	if err != nil {
		t.Fatalf("first ensureWorkspace: %v", err)
	}
	if br1 != "clishake/claude" || wt1 == "" {
		t.Fatalf("unexpected worktree/branch: %q %q", wt1, br1)
	}
	if _, err := os.Stat(filepath.Join(wt1, ".git")); err != nil {
		t.Fatalf("worktree .git missing: %v", err)
	}

	// Simulate remove + re-add: the worktree dir persists. Re-adding must
	// reuse it, not error.
	wt2, br2, err := o.ensureWorkspace(a)
	if err != nil {
		t.Fatalf("reuse after re-add errored: %v", err)
	}
	if wt2 != wt1 || br2 != br1 {
		t.Fatalf("reuse returned different workspace: %q %q vs %q %q", wt2, br2, wt1, br1)
	}

	// A non-worktree directory at the path is still refused.
	blocker := &domain.Agent{Name: "blocker", Permissions: domain.Permissions{ModifyFiles: true}}
	bdir := filepath.Join(ClishakeDir(dir), "worktrees", "blocker")
	if err := os.MkdirAll(bdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.ensureWorkspace(blocker); err == nil {
		t.Fatal("a non-worktree directory should be refused, not clobbered")
	}
}
