package ui

import (
	"testing"

	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
)

func visibleHasSession(h *Home, id string) bool {
	for _, it := range h.flatItems {
		if !it.IsRepoHeader && it.Session != nil && it.Session.ID == id {
			return true
		}
	}
	return false
}

// TestJumpExpandsCollapsedOrigin: a waiting session hidden under a collapsed
// origin group must be revealed (origin expanded) and selected when the user
// presses Space (jump to next attention session).
func TestJumpExpandsCollapsedOrigin(t *testing.T) {
	idle := session.NewSession("main work", "/tmp/jt-main")
	idle.SetStatus(session.StatusIdle)
	waiting := session.NewSession("needs you", "/tmp/jt-wt")
	waiting.SetStatus(session.StatusWaiting)

	h := &Home{
		sessions:     []*session.Session{idle, waiting},
		repoExpanded: map[string]bool{},
		idleFolded:   map[string]bool{},
		pinnedRepos:  map[string]bool{},
	}
	gi := map[string]*git.RepoInfo{
		"/tmp/jt-main": {OriginKey: "github.com/acme/repo"},
		"/tmp/jt-wt":   {OriginKey: "github.com/acme/repo", IsWorktreeRepo: true},
	}
	h.gitInfoCache.Store(&gi)

	// Collapse the shared origin group and rebuild — the waiting row is hidden.
	originKey := OriginExpandKey("github.com/acme/repo")
	h.repoExpanded[originKey] = false
	h.rebuildFlatItems()
	if visibleHasSession(h, waiting.ID) {
		t.Fatal("precondition failed: waiting session should be hidden under collapsed origin")
	}

	// Land the cursor on the (collapsed) origin header, then jump.
	h.cursor = 0
	h.jumpToNextAttentionSession()

	if !IsExpanded(h.repoExpanded, originKey) {
		t.Error("jump did not expand the collapsed origin")
	}
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		t.Fatalf("cursor out of range after jump: %d (items=%d)", h.cursor, len(h.flatItems))
	}
	landed := h.flatItems[h.cursor].Session
	if landed == nil || landed.ID != waiting.ID {
		t.Errorf("jump did not land on the waiting session inside the collapsed origin")
	}
}

// TestSlotJumpExpandsCollapsedOrigin: jumping to a slot-bound session must
// also expand a collapsed origin group, not just the checkout.
func TestSlotJumpExpandsCollapsedOrigin(t *testing.T) {
	other := session.NewSession("elsewhere", "/tmp/sj-main")
	other.SetStatus(session.StatusIdle)
	bound := session.NewSession("bound one", "/tmp/sj-wt")
	bound.SetStatus(session.StatusIdle)

	h := &Home{
		sessions:        []*session.Session{other, bound},
		repoExpanded:    map[string]bool{},
		idleFolded:      map[string]bool{},
		pinnedRepos:     map[string]bool{},
		slotBindings:    map[int]string{3: bound.ID},
		lastSlotTapSlot: -1,
	}
	h.rebuildSessionMap()
	gi := map[string]*git.RepoInfo{
		"/tmp/sj-main": {OriginKey: "github.com/acme/repo"},
		"/tmp/sj-wt":   {OriginKey: "github.com/acme/repo", IsWorktreeRepo: true},
	}
	h.gitInfoCache.Store(&gi)

	originKey := OriginExpandKey("github.com/acme/repo")
	h.repoExpanded[originKey] = false
	h.rebuildFlatItems()
	if visibleHasSession(h, bound.ID) {
		t.Fatal("precondition failed: bound session should be hidden under collapsed origin")
	}

	h.jumpToSlot(3)

	if !IsExpanded(h.repoExpanded, originKey) {
		t.Error("slot jump did not expand the collapsed origin")
	}
	if h.cursor < 0 || h.cursor >= len(h.flatItems) || h.flatItems[h.cursor].Session == nil ||
		h.flatItems[h.cursor].Session.ID != bound.ID {
		t.Errorf("slot jump did not land on the bound session inside the collapsed origin")
	}
}
