package git

import (
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/github"
)

// RepoInfo holds cached git and PR metadata for a repository.
type RepoInfo struct {
	Branch         string
	IsDirty        bool
	IsWorktreeRepo bool
	PR             *github.PR
	LastGitRefresh time.Time
	LastPRRefresh  time.Time
}

// RefreshGitInfo fetches branch, dirty status, and worktree info for a repo.
// Fast operation (<10ms, all local git commands).
func RefreshGitInfo(repoPath string) *RepoInfo {
	return &RepoInfo{
		Branch:         GetBranchName(repoPath),
		IsDirty:        HasUncommittedChanges(repoPath),
		IsWorktreeRepo: IsWorktree(repoPath),
		LastGitRefresh: time.Now(),
	}
}

// RefreshPRInfo fetches PR info via gh CLI and updates the RepoInfo.
// Slower operation (~200ms, network call).
// ignorePatterns is the per-repo CI-check ignore list (path.Match globs);
// caller is responsible for loading it (typically via workspace.IgnorePatterns)
// to keep this package free of a workspace-package dependency.
func RefreshPRInfo(info *RepoInfo, repoPath string, ignorePatterns []string) {
	pr, err := github.GetPRForBranch(repoPath, info.Branch, ignorePatterns)
	if err != nil {
		debuglog.Logger.Debug("RefreshPRInfo failed", "path", repoPath, "branch", info.Branch, "error", err)
	}
	info.PR = pr
	info.LastPRRefresh = time.Now()
}
