package git

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

// reviewFetchTimeout bounds the fetch. A PR head is one commit on top of an
// object store the repo already has, so this is fast even on a big monorepo —
// but a cold or slow network still must not hang the caller forever.
const reviewFetchTimeout = 3 * time.Minute

// ReviewBranchPrefix names the local branches review worktrees sit on. The
// prefix keeps them recognisable in `git branch` and greppable for cleanup,
// and keeps them out of the way of anything the user named themselves.
const ReviewBranchPrefix = "fleet-review-"

// ReviewWorktreeDir is the folder review worktrees live in, as a suffix on the
// repo's own directory name: a checkout at ~/code/brizzai puts review #5163 at
// ~/code/brizzai-reviews/5163.
//
// A sibling folder rather than one worktree per PR scattered beside your own:
// the reviews are a set, they are disposable as a set, and grouping them on
// disk is what lets the sidebar group them without inventing anything.
const ReviewWorktreeDir = "-reviews"

// ReviewWorktreePath is where review #pr for repoPath lives. It does not
// create anything.
func ReviewWorktreePath(repoPath string, pr int) string {
	absRepo, _ := filepath.Abs(repoPath)
	parent := filepath.Dir(absRepo)
	base := filepath.Base(absRepo)
	return filepath.Join(parent, base+ReviewWorktreeDir, strconv.Itoa(pr))
}

// IsReviewWorktree reports whether path sits inside a repo's reviews folder.
// Used by the sidebar to fold review sessions under one node instead of
// letting each worktree become a group of its own.
func IsReviewWorktree(path string) bool {
	parent := filepath.Base(filepath.Dir(path))
	return strings.HasSuffix(parent, ReviewWorktreeDir)
}

// PrepareReviewWorktree fetches a pull request's head and checks it out into
// the repo's reviews folder, returning the worktree path.
//
// Dependencies are deliberately NOT installed. A review reads; it rarely runs.
// A bare checkout of a large monorepo is ~128MB and a few seconds, while a
// full install is gigabytes and minutes — and paying that on every review to
// serve the rare one that needs to run tests is the wrong default. Installing
// is a second step on that one worktree.
//
// Idempotent: an existing worktree for the same PR is returned as-is, so
// re-opening a review you already started is instant and never disturbs work
// in progress. It is deliberately not refreshed here — picking up the author's
// newer commits is an explicit action, not a side effect of revisiting.
func PrepareReviewWorktree(repoPath string, pr int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), reviewFetchTimeout)
	defer cancel()

	main := GetMainWorktreePath(repoPath)
	if main == "" {
		main = repoPath
	}
	path := ReviewWorktreePath(main, pr)
	branch := ReviewBranchPrefix + strconv.Itoa(pr)

	debuglog.Logger.Info("review worktree: preparing", "repo", main, "pr", pr, "path", path)

	// The existence check comes FIRST, and must: git refuses to fetch into a
	// branch that is checked out in a worktree ("refusing to fetch into
	// branch ... checked out at ..."), so fetching before checking makes
	// re-opening a review you already started fail outright. It is also the
	// right behaviour on its own terms — a second visit should be instant,
	// and picking up commits the author has pushed since is a deliberate
	// refresh, not something that happens silently under a review in progress.
	if existing := worktreePathForBranch(ctx, main, branch); existing != "" {
		debuglog.Logger.Info("review worktree: reusing", "pr", pr, "path", existing)
		return existing, nil
	}

	// Fetch the PR head onto a local branch. --force so a re-fetch after the
	// author force-pushes moves the branch rather than failing; nothing is
	// checked out on it at this point, so nothing can be clobbered.
	ref := fmt.Sprintf("pull/%d/head:%s", pr, branch)
	if out, err := exec.CommandContext(ctx, "git", "-C", main, "fetch", "--force", "origin", ref).CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		debuglog.Logger.Error("review worktree: fetch failed", "pr", pr, "err", msg)
		return "", fmt.Errorf("fetch PR #%d: %s", pr, msg)
	}

	if out, err := exec.CommandContext(ctx, "git", "-C", main, "worktree", "add", path, branch).CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		debuglog.Logger.Error("review worktree: add failed", "pr", pr, "path", path, "err", msg)
		return "", fmt.Errorf("create review worktree: %s", msg)
	}

	debuglog.Logger.Info("review worktree: created", "pr", pr, "path", path)
	return path, nil
}

// worktreePathForBranch returns the worktree already checked out on branch, or
// "" when none is. Reads `git worktree list --porcelain`, whose records are
// blank-line separated with "worktree <path>" and "branch refs/heads/<name>".
func worktreePathForBranch(ctx context.Context, repoPath, branch string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return ""
	}
	want := "branch refs/heads/" + branch
	current := ""
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			current = p
			continue
		}
		if line == want && current != "" {
			return current
		}
	}
	return ""
}
