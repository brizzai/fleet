package workspace

import (
	"os"
	"path/filepath"
	"strings"
)

// resolveWorktreePath computes the on-disk path for a worktree named `name`
// belonging to the repo at `repoPath`, applying `template`.
//
// An empty template preserves the classic sibling layout:
//
//	repoPath="/code/myrepo", name="feature-login" -> "/code/myrepo-feature-login"
//
// A non-empty template supports the placeholders {{parent}} (absolute parent
// dir of the repo), {{repo}} (repo basename), {{name}} (the worktree name), and
// a leading ~ / ~/ for the home dir. The result is passed through filepath.Abs.
//
//	template="{{parent}}/{{repo}}.worktrees/{{name}}"
//	  -> "/code/myrepo.worktrees/feature-login"
func resolveWorktreePath(repoPath, name, template string) string {
	absRepo, _ := filepath.Abs(repoPath)
	parent := filepath.Dir(absRepo)
	base := filepath.Base(absRepo)

	if strings.TrimSpace(template) == "" {
		return filepath.Join(parent, base+"-"+name)
	}

	p := template
	p = strings.ReplaceAll(p, "{{parent}}", parent)
	p = strings.ReplaceAll(p, "{{repo}}", base)
	p = strings.ReplaceAll(p, "{{name}}", name)
	p = expandTilde(p)

	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// expandTilde replaces a leading ~ / ~/ with the user's home dir. A bare "~"
// becomes the home dir; anything without a leading tilde is returned unchanged.
func expandTilde(p string) string {
	p = strings.TrimSpace(p)
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}
