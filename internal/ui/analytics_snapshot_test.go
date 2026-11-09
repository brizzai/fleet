package ui

import (
	"testing"

	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
)

func TestSnapshotCountsPinnedWorktreesWithoutSessions(t *testing.T) {
	main, withSession, pinnedOnly := t.TempDir(), t.TempDir(), t.TempDir()
	h := &Home{
		sessions:    []*session.Session{{ProjectPath: main}, {ProjectPath: withSession}},
		pinnedRepos: map[string]bool{main: true, withSession: true, pinnedOnly: true},
	}
	h.writeGitInfo(func(m map[string]*git.RepoInfo) bool {
		m[main] = &git.RepoInfo{}
		m[withSession] = &git.RepoInfo{IsWorktreeRepo: true}
		m[pinnedOnly] = &git.RepoInfo{IsWorktreeRepo: true}
		return true
	})

	if got := h.collectSnapshot().WorktreeReposTotal; got != 2 {
		t.Fatalf("WorktreeReposTotal = %d, want 2 (one with a session, one pinned only)", got)
	}
}
