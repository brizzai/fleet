package ui

import (
	"testing"

	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
)

// cursorOnOriginHeader positions the cursor on the first origin header, failing
// the test if none is present.
func cursorOnOriginHeader(t *testing.T, h *Home) {
	t.Helper()
	h.rebuildFlatItems()
	for i, item := range h.flatItems {
		if item.IsOriginHeader {
			h.cursor = i
			return
		}
	}
	t.Fatal("no origin header found in flat items")
}

// With the cursor on an origin header (RepoPath empty), a new worktree must
// still resolve a base repo — the origin's main clone — instead of "". This is
// the fix for "create worktrees while the origin is selected".
func TestResolveWorktreeBaseRepoOnOriginHeader(t *testing.T) {
	h := newPersistTestHome(t)

	const origin = "github.com/acme/repo"
	const mainRepo = "/tmp/acme-main"
	const wtRepo = "/tmp/acme-wt"

	gi := map[string]*git.RepoInfo{
		mainRepo: {OriginKey: origin},
		wtRepo:   {OriginKey: origin, IsWorktreeRepo: true},
	}
	h.gitInfoCache.Store(&gi)

	// Both checkouts known to the sidebar: main via a session, worktree via pin.
	h.sessions = []*session.Session{session.NewSession("s1", mainRepo)}
	h.pinnedRepos[wtRepo] = true

	cursorOnOriginHeader(t, h)

	if got := h.resolveWorktreeBaseRepo(); got != mainRepo {
		t.Errorf("resolveWorktreeBaseRepo() on origin header = %q, want main clone %q", got, mainRepo)
	}
}

// originBaseRepo prefers the main clone, but falls back to a worktree checkout
// when the main clone isn't tracked (GetMainWorktreePath later normalizes it).
func TestOriginBaseRepoFallsBackToWorktree(t *testing.T) {
	h := newPersistTestHome(t)

	const origin = "github.com/acme/repo"
	const wtRepo = "/tmp/acme-wt"

	gi := map[string]*git.RepoInfo{
		wtRepo: {OriginKey: origin, IsWorktreeRepo: true},
	}
	h.gitInfoCache.Store(&gi)
	h.pinnedRepos[wtRepo] = true

	if got := h.originBaseRepo(origin); got != wtRepo {
		t.Errorf("originBaseRepo() = %q, want worktree fallback %q", got, wtRepo)
	}
	if got := h.originBaseRepo("github.com/acme/unknown"); got != "" {
		t.Errorf("originBaseRepo(unknown) = %q, want empty", got)
	}
}

// A cursor on a session (not an origin header) must keep delegating to
// resolveCurrentRepo — the origin path is additive, not a replacement.
func TestResolveWorktreeBaseRepoOnSession(t *testing.T) {
	h := newPersistTestHome(t)

	const origin = "github.com/acme/repo"
	const mainRepo = "/tmp/acme-main"

	gi := map[string]*git.RepoInfo{mainRepo: {OriginKey: origin}}
	h.gitInfoCache.Store(&gi)
	h.sessions = []*session.Session{session.NewSession("s1", mainRepo)}

	h.rebuildFlatItems()
	found := false
	for i, item := range h.flatItems {
		if item.Session != nil {
			h.cursor = i
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no session row found in flat items")
	}

	// session.GetRepoRoot shells out to git; for a non-repo temp path it returns
	// the path unchanged, so the base equals mainRepo here.
	if got := h.resolveWorktreeBaseRepo(); got != mainRepo {
		t.Errorf("resolveWorktreeBaseRepo() on session = %q, want %q", got, mainRepo)
	}
}
