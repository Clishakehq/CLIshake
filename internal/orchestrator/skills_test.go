package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/clishakehq/clishake/internal/adapter"
	adapterclaude "github.com/clishakehq/clishake/internal/adapter/claudecode"
)

func writeSkill(t *testing.T, o *Orchestrator, name, desc string) string {
	t.Helper()
	dir := filepath.Join(o.SkillsDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + desc + "\n---\nInstructions.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSharedSkills_ListAndSync(t *testing.T) {
	dir := t.TempDir()
	reg := adapter.NewRegistry()
	reg.Register(adapterclaude.New())
	o, err := Open(dir, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Close)

	skDir := writeSkill(t, o, "commit-style", "How this team writes commits")
	// README.md must be ignored, not treated as a skill.
	_ = os.WriteFile(filepath.Join(o.SkillsDir(), "README.md"), []byte("readme"), 0o644)

	skills := o.ListSkills()
	if len(skills) != 1 || skills[0].Name != "commit-style" {
		t.Fatalf("ListSkills = %+v", skills)
	}
	if skills[0].Description != "How this team writes commits" {
		t.Errorf("description = %q", skills[0].Description)
	}

	a, err := o.AddAgent(AgentSpec{Name: "cl", Adapter: "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	a.WorkDir = dir
	_ = o.Store.SaveAgent(a)

	if n := o.SyncSkills(a); n != 1 {
		t.Fatalf("SyncSkills installed %d, want 1", n)
	}
	link := filepath.Join(dir, ".claude", "skills", "commit-style")
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("skill not installed: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("installed skill should be a symlink to the canonical dir")
	}
	if tgt, _ := os.Readlink(link); tgt != skDir {
		t.Errorf("symlink → %q, want %q", tgt, skDir)
	}
}

// SyncSkills must never clobber a real skill the user placed in the harness's
// own skills directory (only replace clishake's own symlinks).
func TestSharedSkills_DoesNotClobberUserSkill(t *testing.T) {
	dir := t.TempDir()
	reg := adapter.NewRegistry()
	reg.Register(adapterclaude.New())
	o, err := Open(dir, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Close)

	writeSkill(t, o, "manual", "shared version")

	// The user already has a real (non-symlink) skill named "manual".
	userSkill := filepath.Join(dir, ".claude", "skills", "manual")
	if err := os.MkdirAll(userSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(userSkill, "SKILL.md")
	_ = os.WriteFile(marker, []byte("USER OWNED"), 0o644)

	a, _ := o.AddAgent(AgentSpec{Name: "cl", Adapter: "claude-code"})
	a.WorkDir = dir
	_ = o.Store.SaveAgent(a)
	o.SyncSkills(a)

	// The user's real directory must be untouched (not replaced by a symlink).
	fi, err := os.Lstat(userSkill)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("user skill was clobbered (mode=%v err=%v)", fi.Mode(), err)
	}
	b, _ := os.ReadFile(marker)
	if string(b) != "USER OWNED" {
		t.Errorf("user skill content changed: %q", b)
	}
}
