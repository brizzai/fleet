package ui

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/brizzai/fleet/internal/session"
)

// The startup snapshot runs before the git worker has filled gitInfoCache,
// so worktrees must be counted from disk. An empty cache here is that moment.
func TestSnapshotCountsWorktreesBeforeGitInfoIsCached(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	main := filepath.Join(root, "repo")
	withSession := filepath.Join(root, "repo-a")
	pinnedOnly := filepath.Join(root, "repo-b")
	for _, args := range [][]string{
		{"init", "-q", main},
		{"-C", main, "-c", "user.email=t@e.x", "-c", "user.name=T", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", main, "worktree", "add", "-q", withSession, "-b", "a"},
		{"-C", main, "worktree", "add", "-q", pinnedOnly, "-b", "b"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// macOS TempDir sits behind a /var → /private/var symlink; pins are
	// stored as GetRepoRoot reports them.
	main, withSession, pinnedOnly = session.GetRepoRoot(main), session.GetRepoRoot(withSession), session.GetRepoRoot(pinnedOnly)

	h := &Home{
		sessions:    []*session.Session{{ProjectPath: main}, {ProjectPath: withSession}},
		pinnedRepos: map[string]bool{main: true, withSession: true, pinnedOnly: true},
	}

	got := h.collectSnapshot()
	if got.WorktreeReposTotal != 2 {
		t.Errorf("WorktreeReposTotal = %d, want 2 (one with a session, one pinned only)", got.WorktreeReposTotal)
	}
	if got.ReposTotal != 3 {
		t.Errorf("ReposTotal = %d, want 3 (main + two worktrees)", got.ReposTotal)
	}
}
