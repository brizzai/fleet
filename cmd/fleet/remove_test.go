package main

import (
	"path/filepath"
	"testing"

	"github.com/brizzai/fleet/internal/session"
)

// Creating a session pins its repo, so `fleet remove` has to unpin when it took
// the last one — a shell has no `d`-on-the-empty-header gesture to clear it
// with, and a create/remove loop would otherwise pile up phantom sidebar rows.
func TestLastSessionInRepo(t *testing.T) {
	repoOf := func(path string) string { return filepath.Dir(path) }

	worktree := &session.SessionRow{ID: "a", ProjectPath: "/code/api-fix/x"}
	sibling := &session.SessionRow{ID: "b", ProjectPath: "/code/api-fix/y"}
	elsewhere := &session.SessionRow{ID: "c", ProjectPath: "/code/other/z"}

	rows := []*session.SessionRow{worktree, elsewhere}
	if !lastSessionInRepo(rows, worktree, "/code/api-fix", repoOf) {
		t.Error("a session alone in its repo should be the last one")
	}

	rows = []*session.SessionRow{worktree, sibling, elsewhere}
	if lastSessionInRepo(rows, worktree, "/code/api-fix", repoOf) {
		t.Error("a sibling session in the same repo must keep the pin")
	}
}
