package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

// WorkspaceInfo represents a workspace from the provider.
type WorkspaceInfo struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Status string `json:"status,omitempty"`
}

// Provider is the interface for workspace operations.
type Provider interface {
	List(repoPath string) ([]WorkspaceInfo, error)
	Create(repoPath, name, branch, baseBranch string) (*WorkspaceInfo, error)
	Destroy(repoPath, name string) error
	CanCreate() bool
	CanDestroy() bool
	IsCustom() bool
}

// --- GitWorktreeProvider (built-in default) ---

// GitWorktreeProvider uses git worktree commands for workspace management.
type GitWorktreeProvider struct{}

func (g *GitWorktreeProvider) List(repoPath string) ([]WorkspaceInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			debuglog.Logger.Error("git worktree list failed", "repo", repoPath, "err", strings.TrimSpace(string(exitErr.Stderr)))
			return nil, fmt.Errorf("git worktree list: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		debuglog.Logger.Error("git worktree list failed", "repo", repoPath, "err", err)
		return nil, fmt.Errorf("git worktree list: %w", err)
	}

	all := parseWorktreePorcelain(string(out))

	// Filter out the main worktree (where path == repoPath).
	absRepo, _ := filepath.Abs(repoPath)
	var result []WorkspaceInfo
	for _, ws := range all {
		absWS, _ := filepath.Abs(ws.Path)
		if absWS == absRepo {
			continue
		}
		result = append(result, ws)
	}
	return result, nil
}

func (g *GitWorktreeProvider) Create(repoPath, name, branch, baseBranch string) (*WorkspaceInfo, error) {
	path := deriveWorktreePath(repoPath, name)

	debuglog.Logger.Info("git worktree create", "repo", repoPath, "name", name, "branch", branch, "baseBranch", baseBranch, "path", path)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// runAdd runs `git worktree add [extra...] <path> -b <branch> [base]`,
	// retrying without -b if the branch already exists.
	runAdd := func(extra ...string) error {
		head := append([]string{"-C", repoPath, "worktree", "add"}, extra...)
		args := append(append([]string{}, head...), path, "-b", branch)
		if baseBranch != "" {
			args = append(args, baseBranch)
		}
		if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
			debuglog.Logger.Debug("git worktree add with -b failed, retrying without -b", "name", name, "branch", branch, "err", strings.TrimSpace(string(out)))
			// Branch might already exist — retry without -b.
			args2 := append(append([]string{}, head...), path, branch)
			if out2, err2 := exec.CommandContext(ctx, "git", args2...).CombinedOutput(); err2 != nil {
				// Return the more informative error.
				errMsg := strings.TrimSpace(string(out))
				if errMsg == "" {
					errMsg = strings.TrimSpace(string(out2))
				}
				debuglog.Logger.Error("git worktree create failed", "name", name, "branch", branch, "err", errMsg)
				return fmt.Errorf("git worktree add: %s", errMsg)
			}
		}
		return nil
	}

	if usesGitCrypt(ctx, repoPath) {
		// git-crypt (through 0.8.0) locates its key via `git rev-parse --git-dir`,
		// which for a new worktree resolves to .git/worktrees/<id> — a private git
		// dir with no git-crypt key. A normal `worktree add` therefore fails the
		// smudge filter mid-checkout. Work around it: create without checkout, link
		// the shared git-crypt dir into the worktree's private git dir, then check
		// out so the smudge filter can find the key.
		if err := runAdd("--no-checkout"); err != nil {
			return nil, err
		}
		// The worktree dir (and, for a fresh -b, its branch) now exist. If the
		// git-crypt setup or checkout fails below, remove the worktree so the same
		// name can be retried; the branch is intentionally left intact (a retry
		// reuses it via the no-`-b` path).
		if err := linkGitCrypt(ctx, path); err != nil {
			debuglog.Logger.Error("git-crypt worktree link failed", "name", name, "path", path, "err", err)
			removeWorktree(repoPath, path)
			return nil, fmt.Errorf("git-crypt worktree setup: %w", err)
		}
		if out, err := exec.CommandContext(ctx, "git", "-C", path, "checkout").CombinedOutput(); err != nil {
			debuglog.Logger.Error("git-crypt worktree checkout failed", "name", name, "path", path, "err", strings.TrimSpace(string(out)))
			removeWorktree(repoPath, path)
			return nil, fmt.Errorf("git worktree checkout: %s", strings.TrimSpace(string(out)))
		}
	} else if err := runAdd(); err != nil {
		return nil, err
	}

	debuglog.Logger.Info("git worktree created", "name", name, "branch", branch, "path", path)
	return &WorkspaceInfo{
		Name:   name,
		Path:   path,
		Branch: branch,
	}, nil
}

// usesGitCrypt reports whether the repo has an initialized (unlocked) git-crypt
// key in its shared git dir. Only then does the worktree smudge filter need help.
func usesGitCrypt(ctx context.Context, repoPath string) bool {
	out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return false
	}
	common := strings.TrimSpace(string(out))
	if !filepath.IsAbs(common) {
		common = filepath.Join(repoPath, common)
	}
	// git-crypt stores keys as files under <gitdir>/git-crypt/keys/. `git-crypt
	// lock` removes those files but can leave an empty keys/ dir behind, so we
	// require at least one key file present — not merely that the dir exists — to
	// avoid treating a locked (keyless) repo as unlocked, which would link an
	// empty key dir and then fail checkout.
	entries, err := os.ReadDir(filepath.Join(common, "git-crypt", "keys"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			return true
		}
	}
	return false
}

// linkGitCrypt symlinks the shared git-crypt key dir into a worktree's private
// git dir, so git-crypt (which resolves keys via --git-dir) can find the key
// when checking out the worktree.
func linkGitCrypt(ctx context.Context, worktreePath string) error {
	gitDirOut, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		return fmt.Errorf("resolve worktree git dir: %w", err)
	}
	commonOut, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return fmt.Errorf("resolve common git dir: %w", err)
	}
	gitDir := strings.TrimSpace(string(gitDirOut))
	common := strings.TrimSpace(string(commonOut))
	if !filepath.IsAbs(common) {
		common = filepath.Join(worktreePath, common)
	}
	link := filepath.Join(gitDir, "git-crypt")
	_ = os.Remove(link) // clear any stale link before re-creating
	return os.Symlink(filepath.Join(common, "git-crypt"), link)
}

// removeWorktree best-effort removes a partially-created worktree (e.g. after a
// failed git-crypt setup) so its path/name can be reused on retry. It uses a
// fresh context so cleanup still runs even when the caller's context already
// timed out, and leaves the branch intact (a retry reuses it).
func removeWorktree(repoPath, worktreePath string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "remove", "--force", worktreePath).CombinedOutput(); err != nil {
		debuglog.Logger.Warn("git-crypt cleanup: worktree remove failed", "path", worktreePath, "err", strings.TrimSpace(string(out)))
	}
}

func (g *GitWorktreeProvider) Destroy(repoPath, name string) error {
	debuglog.Logger.Info("git worktree destroy", "repo", repoPath, "name", name)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// List ALL worktrees (unfiltered) — repoPath might be a linked worktree
	// itself, so we can't use g.List() which filters out the repoPath entry.
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			debuglog.Logger.Error("git worktree destroy: list failed", "name", name, "err", strings.TrimSpace(string(exitErr.Stderr)))
			return fmt.Errorf("git worktree list: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		debuglog.Logger.Error("git worktree destroy: list failed", "name", name, "err", err)
		return fmt.Errorf("git worktree list: %w", err)
	}
	all := parseWorktreePorcelain(string(out))
	if len(all) == 0 {
		debuglog.Logger.Error("git worktree destroy: not found", "name", name)
		return fmt.Errorf("worktree %q not found", name)
	}

	// First entry is always the main worktree — use it as base for derived path
	// and as the -C target for git worktree remove.
	mainPath, _ := filepath.Abs(all[0].Path)
	absRepo, _ := filepath.Abs(repoPath)
	derivedPath, _ := filepath.Abs(deriveWorktreePath(mainPath, name))

	// Find the worktree to remove. Try multiple matching strategies:
	// 1. Exact name match (ws.Name == name)
	// 2. Derived path match (worktree created by fleet: mainRepo-name)
	// 3. repoPath itself is the worktree (caller resolved GetRepoRoot to worktree path)
	var wtPath string
	for _, ws := range all {
		absWS, _ := filepath.Abs(ws.Path)
		if absWS == mainPath {
			continue // never remove the main worktree
		}
		if ws.Name == name || absWS == derivedPath || absWS == absRepo {
			wtPath = ws.Path
			break
		}
	}
	if wtPath == "" {
		debuglog.Logger.Error("git worktree destroy: not found after search", "name", name, "repo", repoPath)
		return fmt.Errorf("worktree %q not found", name)
	}

	cmd = exec.CommandContext(ctx, "git", "-C", mainPath, "worktree", "remove", "--force", wtPath)
	if rmOut, err := cmd.CombinedOutput(); err != nil {
		debuglog.Logger.Error("git worktree destroy failed", "name", name, "path", wtPath, "err", strings.TrimSpace(string(rmOut)))
		return fmt.Errorf("git worktree remove: %s", strings.TrimSpace(string(rmOut)))
	}
	debuglog.Logger.Info("git worktree destroyed", "name", name, "path", wtPath)
	return nil
}

func (g *GitWorktreeProvider) CanCreate() bool  { return true }
func (g *GitWorktreeProvider) CanDestroy() bool { return true }
func (g *GitWorktreeProvider) IsCustom() bool   { return false }

// --- ShellProvider (from .bc.json) ---

// ShellProvider wraps external shell commands for workspace management.
type ShellProvider struct {
	ListCmd    string
	CreateCmd  string
	DestroyCmd string
}

func (p *ShellProvider) List(repoPath string) ([]WorkspaceInfo, error) {
	if p.ListCmd == "" {
		return nil, fmt.Errorf("list command not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", p.ListCmd)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			debuglog.Logger.Error("shell workspace list failed", "repo", repoPath, "cmd", p.ListCmd, "err", strings.TrimSpace(string(exitErr.Stderr)))
			return nil, fmt.Errorf("list command failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		debuglog.Logger.Error("shell workspace list failed", "repo", repoPath, "cmd", p.ListCmd, "err", err)
		return nil, fmt.Errorf("list command failed: %w", err)
	}

	var workspaces []WorkspaceInfo
	if err := json.Unmarshal(out, &workspaces); err != nil {
		debuglog.Logger.Error("shell workspace list: parse output failed", "repo", repoPath, "err", err)
		return nil, fmt.Errorf("parse list output: %w", err)
	}
	return workspaces, nil
}

func (p *ShellProvider) Create(repoPath, name, branch, baseBranch string) (*WorkspaceInfo, error) {
	if p.CreateCmd == "" {
		return nil, fmt.Errorf("create command not configured")
	}

	debuglog.Logger.Info("shell workspace create", "repo", repoPath, "name", name, "branch", branch)

	cmdStr := strings.ReplaceAll(p.CreateCmd, "{{name}}", name)
	if branch == "" {
		// Strip --branch {{branch}} / -b {{branch}} patterns when branch is empty.
		cmdStr = strings.ReplaceAll(cmdStr, "--branch {{branch}}", "")
		cmdStr = strings.ReplaceAll(cmdStr, "-b {{branch}}", "")
		cmdStr = strings.ReplaceAll(cmdStr, "{{branch}}", "")
	} else {
		cmdStr = strings.ReplaceAll(cmdStr, "{{branch}}", branch)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			debuglog.Logger.Error("shell workspace create failed", "name", name, "branch", branch, "err", strings.TrimSpace(string(exitErr.Stderr)))
			return nil, fmt.Errorf("create command failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		debuglog.Logger.Error("shell workspace create failed", "name", name, "branch", branch, "err", err)
		return nil, fmt.Errorf("create command failed: %w", err)
	}

	// Try parsing output as JSON first.
	var info WorkspaceInfo
	if err := json.Unmarshal(out, &info); err == nil {
		debuglog.Logger.Info("shell workspace created", "name", info.Name, "path", info.Path, "branch", info.Branch)
		return &info, nil
	}

	// Output wasn't JSON — look up the new workspace via list command.
	if p.ListCmd != "" {
		workspaces, listErr := p.List(repoPath)
		if listErr == nil {
			for _, ws := range workspaces {
				if ws.Name == name {
					debuglog.Logger.Info("shell workspace created (found via list)", "name", ws.Name, "path", ws.Path)
					return &ws, nil
				}
			}
		}
	}

	// Fall back to name-only info.
	debuglog.Logger.Info("shell workspace created (name-only fallback)", "name", name, "branch", branch)
	return &WorkspaceInfo{Name: name, Branch: branch}, nil
}

func (p *ShellProvider) Destroy(repoPath, name string) error {
	if p.DestroyCmd == "" {
		return fmt.Errorf("destroy command not configured")
	}

	debuglog.Logger.Info("shell workspace destroy", "repo", repoPath, "name", name)

	cmdStr := strings.ReplaceAll(p.DestroyCmd, "{{name}}", name)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		debuglog.Logger.Error("shell workspace destroy failed", "name", name, "err", strings.TrimSpace(string(out)))
		return fmt.Errorf("destroy command failed: %s", strings.TrimSpace(string(out)))
	}
	debuglog.Logger.Info("shell workspace destroyed", "name", name)
	return nil
}

func (p *ShellProvider) CanCreate() bool  { return p.CreateCmd != "" }
func (p *ShellProvider) CanDestroy() bool { return p.DestroyCmd != "" }
func (p *ShellProvider) IsCustom() bool   { return true }

// --- Helper functions ---

// parseWorktreePorcelain parses git worktree list --porcelain output.
func parseWorktreePorcelain(output string) []WorkspaceInfo {
	var result []WorkspaceInfo
	var current WorkspaceInfo
	hasWorktree := false

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "worktree ") {
			if hasWorktree {
				result = append(result, current)
			}
			path := strings.TrimPrefix(line, "worktree ")
			current = WorkspaceInfo{
				Path: path,
				Name: filepath.Base(path),
			}
			hasWorktree = true
		} else if strings.HasPrefix(line, "branch refs/heads/") {
			current.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		}
	}
	if hasWorktree {
		result = append(result, current)
	}
	return result
}

// SanitizeBranchName converts a branch name to a safe directory name.
// e.g. "feature/login" -> "feature-login"
func SanitizeBranchName(branch string) string {
	s := strings.ReplaceAll(branch, "/", "-")
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "..", "-")
	return s
}

// SanitizeBranchInput normalizes a partial branch-name input for live display:
// space becomes '-', and chars git forbids anywhere in a ref (~ ^ : ? * [ \ `
// and ASCII control chars) are dropped. '/' is kept so users can type
// hierarchical names like "feature/login". Positional rules (leading '-',
// per-component leading '.', trailing '.lock', etc.) are left to
// ValidateBranchName so we don't eat characters mid-type.
func SanitizeBranchInput(s string) string {
	out, _ := SanitizeBranchInputWithCursor(s, 0)
	return out
}

// SanitizeBranchInputWithCursor is SanitizeBranchInput with cursor preservation:
// it returns the sanitized string and the adjusted rune-position cursor so
// callers can keep the user's edit point after live sanitation. Cursor units
// match bubbles/textinput.Position (rune index).
func SanitizeBranchInputWithCursor(s string, cursor int) (string, int) {
	runes := []rune(s)
	newRunes := make([]rune, 0, len(runes))
	newCursor := cursor
	for i, r := range runes {
		keep := true
		out := r
		switch {
		case r == ' ':
			out = '-'
		case r < 0x20 || r == 0x7f:
			keep = false
		case r == '~' || r == '^' || r == ':' || r == '?' || r == '*' || r == '[' || r == '\\' || r == '`':
			keep = false
		}
		if keep {
			newRunes = append(newRunes, out)
		} else if i < cursor {
			newCursor--
		}
	}
	if newCursor < 0 {
		newCursor = 0
	}
	if newCursor > len(newRunes) {
		newCursor = len(newRunes)
	}
	return string(newRunes), newCursor
}

// ValidateBranchName returns a user-friendly error message if branch violates
// git check-ref-format rules that SanitizeBranchInput can't repair live. An
// empty return value means the branch is acceptable.
func ValidateBranchName(branch string) string {
	if branch == "" {
		return "Branch name cannot be empty"
	}
	if branch == "@" {
		return "Branch name cannot be '@'"
	}
	switch branch[0] {
	case '-':
		return "Branch name cannot start with '-'"
	case '/':
		return "Branch name cannot start with '/'"
	}
	if branch[len(branch)-1] == '/' {
		return "Branch name cannot end with '/'"
	}
	if strings.Contains(branch, "..") {
		return "Branch name cannot contain '..'"
	}
	if strings.Contains(branch, "@{") {
		return "Branch name cannot contain '@{'"
	}
	if strings.Contains(branch, "//") {
		return "Branch name cannot contain '//'"
	}
	// Per-component rules (git check-ref-format applies these to each
	// '/'-separated component, not just the whole string).
	for _, comp := range strings.Split(branch, "/") {
		if comp == "" {
			continue
		}
		if comp[0] == '.' {
			return "Branch name cannot have a component starting with '.'"
		}
		if comp[len(comp)-1] == '.' {
			return "Branch name cannot have a component ending with '.'"
		}
		if strings.HasSuffix(comp, ".lock") {
			return "Branch name cannot have a component ending with '.lock'"
		}
	}
	return ""
}

// deriveWorktreePath computes the sibling worktree path.
// e.g. repoPath="/code/myrepo", name="feature-login" -> "/code/myrepo-feature-login"
func deriveWorktreePath(repoPath, name string) string {
	absRepo, _ := filepath.Abs(repoPath)
	parent := filepath.Dir(absRepo)
	base := filepath.Base(absRepo)
	return filepath.Join(parent, base+"-"+name)
}

// DeriveWorktreePathPreview returns a display-friendly relative path preview.
func DeriveWorktreePathPreview(repoPath, name string) string {
	absRepo, _ := filepath.Abs(repoPath)
	base := filepath.Base(absRepo)
	return "../" + base + "-" + name
}
