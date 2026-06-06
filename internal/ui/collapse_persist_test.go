package ui

import (
	"path/filepath"
	"testing"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
)

func newPersistTestHome(t *testing.T) *Home {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	storage, err := session.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	return NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
}

func collapsedSet(t *testing.T, h *Home) map[string]bool {
	t.Helper()
	keys, err := h.storage.LoadCollapsedGroups()
	if err != nil {
		t.Fatalf("LoadCollapsedGroups: %v", err)
	}
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	return set
}

// setExpanded is the single choke point: every intentful collapse/expand must
// reach storage, and expanding must clear the row again. This guards the
// command-palette "Collapse All" / new-session reveal paths that previously
// wrote the map directly and were lost on restart.
func TestSetExpandedPersists(t *testing.T) {
	h := newPersistTestHome(t)

	originKey := OriginExpandKey("github.com/acme/repo")
	h.setExpanded("/tmp/repo", false)
	h.setExpanded(originKey, false)

	got := collapsedSet(t, h)
	if !got["/tmp/repo"] || !got[originKey] {
		t.Fatalf("expected both keys persisted collapsed, got %v", got)
	}

	// Expanding clears only that row.
	h.setExpanded("/tmp/repo", true)
	got = collapsedSet(t, h)
	if got["/tmp/repo"] {
		t.Errorf("expanded checkout should clear its row, got %v", got)
	}
	if !got[originKey] {
		t.Errorf("origin row should be untouched, got %v", got)
	}
}

// forgetCollapse must drop the origin's persisted row only once its last
// checkout is gone — while a sibling checkout (e.g. a worktree) remains under
// the origin, the origin row has to survive so that checkout still renders
// collapsed.
func TestForgetCollapseDropsOrphanedOrigin(t *testing.T) {
	h := newPersistTestHome(t)

	const origin = "github.com/acme/repo"
	originKey := OriginExpandKey(origin)
	const mainRepo = "/tmp/acme-main"
	const wtRepo = "/tmp/acme-wt"

	gi := map[string]*git.RepoInfo{
		mainRepo: {OriginKey: origin},
		wtRepo:   {OriginKey: origin, IsWorktreeRepo: true},
	}
	h.gitInfoCache.Store(&gi)
	h.pinnedRepos[mainRepo] = true
	h.pinnedRepos[wtRepo] = true

	// Origin and the main checkout are collapsed and persisted.
	h.setExpanded(originKey, false)
	h.setExpanded(mainRepo, false)

	// Forget the main checkout; the worktree still lives under the origin.
	delete(h.pinnedRepos, mainRepo)
	h.forgetCollapse(mainRepo)
	got := collapsedSet(t, h)
	if got[mainRepo] {
		t.Errorf("forgotten checkout row should be cleared, got %v", got)
	}
	if !got[originKey] {
		t.Errorf("origin row must survive while a sibling checkout remains, got %v", got)
	}

	// Forget the worktree too; nothing maps to the origin now, so its row goes.
	delete(h.pinnedRepos, wtRepo)
	h.forgetCollapse(wtRepo)
	got = collapsedSet(t, h)
	if len(got) != 0 {
		t.Errorf("origin row should clear once its last checkout is forgotten, got %v", got)
	}
}
