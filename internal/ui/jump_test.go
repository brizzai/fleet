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

// TestJumpSkipsCollapsedOrigin: Space-jump must NOT dive into a collapsed
// origin. With a waiting session visible in one origin and another waiting
// session hidden under a collapsed origin, jump lands on the visible one and
// leaves the collapsed origin folded.
func TestJumpSkipsCollapsedOrigin(t *testing.T) {
	aMain := session.NewSession("alpha-main", "/tmp/js-a-main")
	aMain.SetStatus(session.StatusIdle)
	aWait := session.NewSession("alpha-wait", "/tmp/js-a-wt")
	aWait.SetStatus(session.StatusWaiting)
	bWait := session.NewSession("beta-wait", "/tmp/js-b")
	bWait.SetStatus(session.StatusWaiting)

	h := &Home{
		sessions:     []*session.Session{aMain, aWait, bWait},
		repoExpanded: map[string]bool{},
		idleFolded:   map[string]bool{},
		pinnedRepos:  map[string]bool{},
	}
	gi := map[string]*git.RepoInfo{
		"/tmp/js-a-main": {OriginKey: "github.com/acme/alpha"},
		"/tmp/js-a-wt":   {OriginKey: "github.com/acme/alpha", IsWorktreeRepo: true},
		"/tmp/js-b":      {OriginKey: "github.com/acme/beta"},
	}
	h.gitInfoCache.Store(&gi)

	// Collapse the beta origin — its waiting session is now hidden.
	betaKey := OriginExpandKey("github.com/acme/beta")
	h.repoExpanded[betaKey] = false
	h.rebuildFlatItems()
	if visibleHasSession(h, bWait.ID) {
		t.Fatal("precondition failed: beta waiting session should be hidden under collapsed origin")
	}

	h.cursor = 0
	h.jumpToNextAttentionSession()

	landed := h.flatItems[h.cursor].Session
	if landed == nil || landed.ID != aWait.ID {
		t.Errorf("jump should land on the visible waiting session (alpha), got %v", landed)
	}
	if IsExpanded(h.repoExpanded, betaKey) {
		t.Error("jump must NOT expand the collapsed beta origin")
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
