package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// Placeholders a worktree_path template may carry.
const (
	repoPlaceholder = "{{repo}}"
	namePlaceholder = "{{name}}"
)

// DefaultWorktreePathTemplate is where a worktree lands when nothing is
// configured: a sibling of the repo named <repo>-<name>. It is the layout
// fleet has always produced, so an unset setting changes nothing.
const DefaultWorktreePathTemplate = "../" + repoPlaceholder + "-" + namePlaceholder

// globalWorktreePath holds config.json's worktree_path. It is package state,
// set once at startup (SetGlobalWorktreePath), because every provider is built
// by ResolveProvider from a repo path alone and the workspace package must not
// import config. A repo's own .fleet.json / .fleet.local.json overrides it.
var globalWorktreePath atomic.Value // string

// SetGlobalWorktreePath records the user-wide worktree_path template from
// config.json. Empty restores the default sibling layout.
func SetGlobalWorktreePath(tmpl string) {
	globalWorktreePath.Store(strings.TrimSpace(tmpl))
}

func globalWorktreePathTemplate() string {
	v, _ := globalWorktreePath.Load().(string)
	return v
}

// WorktreePathTemplate returns the template that governs where new worktrees
// of repoPath go: the repo's own config first, then config.json, then the
// default. It reads the repo's config files, so callers that render on every
// frame should resolve it once and keep it.
func WorktreePathTemplate(repoPath string) string {
	if t := strings.TrimSpace(loadMergedRepoConfig(repoPath).WorktreePath); t != "" {
		return t
	}
	if t := globalWorktreePathTemplate(); t != "" {
		return t
	}
	return DefaultWorktreePathTemplate
}

// ResolveWorktreePath turns a template into the absolute path a worktree named
// name (already sanitized) gets for the repo at repoPath.
//
// The template may use {{repo}} (the repo folder's base name) and {{name}}.
// A template without {{name}} names a directory to hold worktrees; the folder
// inside it is {{name}} when the directory already carries {{repo}}, and
// {{repo}}-{{name}} when it doesn't, so a single shared directory such as
// ~/worktrees can't collide two repos' branches of the same name. A leading ~
// is the home directory, and a relative template is resolved against the repo
// root (so "../{{repo}}.worktrees" puts them beside the repo).
//
// A path inside the repo is refused: the worktree would show up as an untracked
// directory in the main checkout's git status, and a recursive tool (grep,
// the agent's own search) would walk every worktree from the main checkout.
func ResolveWorktreePath(repoPath, tmpl, name string) (string, error) {
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("resolve repo path: %w", err)
	}
	if strings.TrimSpace(tmpl) == "" {
		tmpl = DefaultWorktreePathTemplate
	}
	if name == "" {
		return "", fmt.Errorf("worktree name is empty")
	}

	tmpl = completeTemplate(strings.TrimSpace(tmpl))
	repo := filepath.Base(absRepo)
	p := strings.ReplaceAll(tmpl, repoPlaceholder, repo)
	p = strings.ReplaceAll(p, namePlaceholder, name)

	p, err = expandHome(p)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(absRepo, p)
	}
	p = filepath.Clean(p)

	if p == absRepo || strings.HasPrefix(p, absRepo+string(filepath.Separator)) {
		return "", fmt.Errorf("worktree_path %q puts the worktree inside the repo (%s); point it outside, e.g. \"../{{repo}}.worktrees\"", tmpl, p)
	}
	return p, nil
}

// completeTemplate makes a directory-only template (no {{name}}) name a folder
// inside that directory.
func completeTemplate(tmpl string) string {
	if strings.Contains(tmpl, namePlaceholder) {
		return tmpl
	}
	folder := repoPlaceholder + "-" + namePlaceholder
	if strings.Contains(tmpl, repoPlaceholder) {
		folder = namePlaceholder
	}
	return strings.TrimRight(tmpl, `/\`) + "/" + folder
}

func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand ~ in worktree_path: %w", err)
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}

// WorktreePathPreview renders the resolved path for the creation dialogs:
// relative ("../fleet-foo") when it sits beside the repo, ~-abbreviated
// otherwise, and the error text when the template is unusable so the problem
// shows before Enter rather than after.
func WorktreePathPreview(repoPath, tmpl, name string) string {
	p, err := ResolveWorktreePath(repoPath, tmpl, name)
	if err != nil {
		return "✕ " + err.Error()
	}
	absRepo, _ := filepath.Abs(repoPath)
	if rel, rerr := filepath.Rel(filepath.Dir(absRepo), p); rerr == nil && !strings.HasPrefix(rel, "..") {
		return "../" + filepath.ToSlash(rel)
	}
	if home, herr := os.UserHomeDir(); herr == nil && strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// ProviderPathTemplate returns the template a provider places worktrees by,
// without touching the disk, so a dialog can render a preview every frame.
// Only the built-in git provider has one; a custom shell provider decides its
// own paths, and the default stands in for it as before.
func ProviderPathTemplate(p Provider) string {
	if g, ok := p.(*GitWorktreeProvider); ok && g.PathTemplate != "" {
		return g.PathTemplate
	}
	return DefaultWorktreePathTemplate
}
