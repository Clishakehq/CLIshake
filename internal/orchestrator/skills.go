package orchestrator

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/clishakehq/clishake/internal/adapter"
	"github.com/clishakehq/clishake/internal/domain"
)

// Shared skills let the lead maintain ONE set of team capabilities that every
// agent gets, regardless of harness. They live in .clishake/skills/, each skill
// a subdirectory with a SKILL.md (the Agent Skills format: YAML frontmatter
// with name/description, then instructions). clishake installs them into a
// harness's native skills directory when it has one (adapter.SkillHost), and
// tells every agent about .clishake/skills in the launch briefing so harnesses
// without a native system can still use them.

// skillsReadmeText documents the shared-skills directory (written on init).
const skillsReadmeText = `# Shared team skills

Skills placed here are shared with every agent in the session, regardless of
harness. Each skill is either:

  - a subdirectory with a SKILL.md, or
  - a single <name>.md file

SKILL.md uses the Agent Skills format — YAML frontmatter then instructions:

    ---
    name: commit-style
    description: How this team writes commits — use when creating any commit
    ---

    Write imperative subject lines under 72 chars. Explain the why, not the what.

clishake installs these into a harness's native skills directory when it has
one (e.g. Claude Code's .claude/skills), and points every agent here in its
launch briefing. Commit this directory to share skills with your team via git.

Manage from the CLI: 'clishake skills' (list) and 'clishake skills sync'.
`

// SkillsDir is the canonical shared-skills directory for the session.
func (o *Orchestrator) SkillsDir() string {
	return filepath.Join(ClishakeDir(o.ProjectDir), "skills")
}

// Skill is a shared skill discovered under SkillsDir.
type Skill struct {
	Name        string // directory (or file) name
	Description string // from SKILL.md frontmatter, when present
}

// ListSkills returns the shared skills, sorted by name.
func (o *Orchestrator) ListSkills() []Skill {
	entries, err := os.ReadDir(o.SkillsDir())
	if err != nil {
		return nil
	}
	var out []Skill
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "README.md" {
			continue
		}
		s := Skill{Name: name}
		mdPath := filepath.Join(o.SkillsDir(), name, "SKILL.md")
		if !e.IsDir() && strings.HasSuffix(name, ".md") {
			mdPath = filepath.Join(o.SkillsDir(), name)
			s.Name = strings.TrimSuffix(name, ".md")
		}
		s.Description = skillDescription(mdPath)
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// skillDescription reads the "description:" field from a SKILL.md frontmatter
// block (best effort; empty when absent).
func skillDescription(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if d, ok := strings.CutPrefix(strings.TrimSpace(line), "description:"); ok {
			return strings.TrimSpace(strings.Trim(strings.TrimSpace(d), `"'`))
		}
	}
	return ""
}

// SyncSkills installs the shared skills into a's harness native skills
// directory (when the adapter has one), as symlinks so edits to the canonical
// skill stay live. Skills the harness has that clishake doesn't manage (a real
// directory, not one of our symlinks) are left untouched. Returns the number
// installed. A no-op for harnesses without a native skills system — those
// agents reach the skills via the briefing pointer to .clishake/skills.
func (o *Orchestrator) SyncSkills(a *domain.Agent) int {
	ad, ok := o.Registry.Get(a.Adapter)
	if !ok {
		return 0
	}
	host, ok := ad.(adapter.SkillHost)
	if !ok {
		return 0
	}
	nativeRel := host.NativeSkillsDir()
	if nativeRel == "" {
		return 0
	}
	src := o.SkillsDir()
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0 // no shared skills yet
	}
	wd := a.WorkDir
	if wd == "" {
		wd = o.ProjectDir
	}
	dst := filepath.Join(wd, nativeRel)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0
	}
	installed := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || e.Name() == "README.md" {
			continue
		}
		link := filepath.Join(dst, e.Name())
		target := filepath.Join(src, e.Name())
		// Don't clobber a real skill the user placed there themselves; only
		// replace our own (a symlink) or a missing entry.
		if fi, err := os.Lstat(link); err == nil {
			if fi.Mode()&os.ModeSymlink == 0 {
				continue // a real file/dir we didn't create — leave it
			}
			_ = os.Remove(link)
		}
		if err := os.Symlink(target, link); err == nil {
			installed++
		}
	}
	return installed
}
