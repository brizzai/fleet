package git

import (
	"context"
	"errors"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/github"
)

// RepoInfo holds cached git and PR metadata for a repository.
type RepoInfo struct {
	Branch          string
	IsDirty         bool
	IsWorktreeRepo  bool
	OriginKey       string // stable origin identity (see GetOriginKey)
	PR              *github.PR
	LastGitRefresh  time.Time
	LastPRRefresh   time.Time
	PRRateLimitedAt time.Time // last time gh reported a rate-limit error; non-zero = back off
}

// RefreshGitInfo fetches branch, dirty status, and worktree info for a repo.
// Fast operation (<10ms, all local git commands). Runs three git subprocesses:
// a combined rev-parse for branch + worktree status (revParseLayout), the dirty
// check, and the origin lookup — down from five, to ease fork/exec-lock
// contention on the per-cycle, all-repos fan-out.
func RefreshGitInfo(repoPath string) *RepoInfo {
	branch, isWorktree := revParseLayout(repoPath)
	return &RepoInfo{
		Branch:         branch,
		IsDirty:        HasUncommittedChanges(repoPath),
		IsWorktreeRepo: isWorktree,
		OriginKey:      GetOriginKey(repoPath),
		LastGitRefresh: time.Now(),
	}
}

// RefreshPRInfo fetches PR info via gh CLI and updates the RepoInfo.
// Slower operation (~200ms, network call).
// ignorePatterns is the per-repo CI-check ignore list (path.Match globs);
// caller is responsible for loading it (typically via workspace.IgnorePatterns)
// to keep this package free of a workspace-package dependency.
//
// On a rate-limit error, marks PRRateLimitedAt and leaves PR / LastPRRefresh
// untouched — the cached PR (possibly nil) stays visible until the back-off
// expires and the caller retries.
func RefreshPRInfo(info *RepoInfo, repoPath string, ignorePatterns []string) {
	pr, err := github.GetPRForBranch(repoPath, info.Branch, ignorePatterns)
	if errors.Is(err, github.ErrRateLimited) {
		info.PRRateLimitedAt = time.Now()
		debuglog.Logger.Warn("PR fetch rate-limited; backing off", "path", repoPath, "branch", info.Branch)
		return
	}
	// A gh timeout is a transient blip, not "no PR" — preserve the cached PR so
	// a real badge doesn't flicker off mid-refresh (same intent as rate-limit).
	if errors.Is(err, context.DeadlineExceeded) {
		debuglog.Logger.Debug("PR fetch timed out; preserving cached PR", "path", repoPath, "branch", info.Branch)
		return
	}
	if err != nil {
		debuglog.Logger.Debug("RefreshPRInfo failed", "path", repoPath, "branch", info.Branch, "error", err)
	}
	info.PR = pr
	info.LastPRRefresh = time.Now()
	info.PRRateLimitedAt = time.Time{} // clear back-off on any successful round-trip
}
