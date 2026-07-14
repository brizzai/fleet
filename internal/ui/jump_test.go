package ui

import (
	tea "charm.land/bubbletea/v2"

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

// TestJumpEntersCollapsedCheckout: a collapsed CHECKOUT (branch) under an
// expanded origin is NOT muted — jump reaches its waiting session and expands
// just that checkout to reveal it.
func TestJumpEntersCollapsedCheckout(t *testing.T) {
	main := session.NewSession("main", "/tmp/jc-main")
	main.SetStatus(session.StatusIdle)
	wt := session.NewSession("wt-wait", "/tmp/jc-wt")
	wt.SetStatus(session.StatusWaiting)

	h := &Home{
		sessions:     []*session.Session{main, wt},
		repoExpanded: map[string]bool{},
		pinnedRepos:  map[string]bool{},
	}
	gi := map[string]*git.RepoInfo{
		"/tmp/jc-main": {OriginKey: "github.com/acme/repo"},
		"/tmp/jc-wt":   {OriginKey: "github.com/acme/repo", IsWorktreeRepo: true},
	}
	h.gitInfoCache.Store(&gi)

	// Collapse only the worktree CHECKOUT; the origin stays expanded.
	h.repoExpanded["/tmp/jc-wt"] = false
	h.rebuildFlatItems()
	if visibleHasSession(h, wt.ID) {
		t.Fatal("precondition failed: waiting session should be hidden under collapsed checkout")
	}
	if !IsExpanded(h.repoExpanded, OriginExpandKey("github.com/acme/repo")) {
		t.Fatal("precondition failed: origin should be expanded")
	}

	h.cursor = 0
	h.jumpToNextAttentionSession()

	if !IsExpanded(h.repoExpanded, "/tmp/jc-wt") {
		t.Error("jump should expand the collapsed checkout under an expanded origin")
	}
	if h.cursor < 0 || h.cursor >= len(h.flatItems) || h.flatItems[h.cursor].Session == nil ||
		h.flatItems[h.cursor].Session.ID != wt.ID {
		t.Errorf("jump should land on the waiting session inside the now-expanded checkout")
	}
}

// TestJumpFromHeaderAnchorsAfterIt: with the cursor on an origin header, jump
// must continue from that header (landing in its group) rather than restarting
// at the first candidate and wrapping to an earlier origin.
func TestJumpFromHeaderAnchorsAfterIt(t *testing.T) {
	aWait := session.NewSession("alpha-wait", "/tmp/jh-alpha")
	aWait.SetStatus(session.StatusWaiting)
	bWait := session.NewSession("beta-wait", "/tmp/jh-beta")
	bWait.SetStatus(session.StatusWaiting)

	h := &Home{
		sessions:     []*session.Session{aWait, bWait},
		repoExpanded: map[string]bool{},
		pinnedRepos:  map[string]bool{},
	}
	gi := map[string]*git.RepoInfo{
		"/tmp/jh-alpha": {OriginKey: "github.com/acme/alpha"},
		"/tmp/jh-beta":  {OriginKey: "github.com/acme/beta"},
	}
	h.gitInfoCache.Store(&gi)
	h.rebuildFlatItems()

	// Park the cursor on the beta origin header (it sorts after alpha).
	betaHeader := -1
	for i, it := range h.flatItems {
		if it.IsOriginHeader && it.OriginKey == "github.com/acme/beta" {
			betaHeader = i
			break
		}
	}
	if betaHeader < 0 {
		t.Fatal("beta origin header not found in flatItems")
	}
	h.cursor = betaHeader

	h.jumpToNextAttentionSession()

	landed := h.flatItems[h.cursor].Session
	if landed == nil || landed.ID != bWait.ID {
		t.Errorf("jump from beta header should land on beta's waiting session, not wrap to alpha; got %v", landed)
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

// headerJumpHome builds a two-origin tree:
//
//	origin alpha
//	  /tmp/hj-a-main   a1, a2
//	  /tmp/hj-a-wt     a3        (worktree — sorts after the main clone)
//	origin beta
//	  /tmp/hj-b        b1
func headerJumpHome() (*Home, map[string]*session.Session) {
	mk := func(name, path string) *session.Session {
		s := session.NewSession(name, path)
		s.SetStatus(session.StatusIdle)
		return s
	}
	byName := map[string]*session.Session{
		"a1": mk("a1", "/tmp/hj-a-main"),
		"a2": mk("a2", "/tmp/hj-a-main"),
		"a3": mk("a3", "/tmp/hj-a-wt"),
		"b1": mk("b1", "/tmp/hj-b"),
	}
	h := &Home{
		sessions:     []*session.Session{byName["a1"], byName["a2"], byName["a3"], byName["b1"]},
		repoExpanded: map[string]bool{},
		pinnedRepos:  map[string]bool{},
	}
	gi := map[string]*git.RepoInfo{
		"/tmp/hj-a-main": {OriginKey: "github.com/acme/alpha"},
		"/tmp/hj-a-wt":   {OriginKey: "github.com/acme/alpha", IsWorktreeRepo: true},
		"/tmp/hj-b":      {OriginKey: "github.com/acme/beta"},
	}
	h.gitInfoCache.Store(&gi)
	h.rebuildFlatItems()
	return h, byName
}

func idxOfSession(t *testing.T, h *Home, id string) int {
	t.Helper()
	for i, it := range h.flatItems {
		if it.Session != nil && it.Session.ID == id {
			return i
		}
	}
	t.Fatalf("session %s not in flatItems", id)
	return -1
}

func idxOfCheckout(t *testing.T, h *Home, repoPath string) int {
	t.Helper()
	for i, it := range h.flatItems {
		if it.IsCheckoutHeader && it.RepoPath == repoPath {
			return i
		}
	}
	t.Fatalf("checkout header %s not in flatItems", repoPath)
	return -1
}

func idxOfOrigin(t *testing.T, h *Home, originKey string) int {
	t.Helper()
	for i, it := range h.flatItems {
		if it.IsOriginHeader && it.OriginKey == originKey {
			return i
		}
	}
	t.Fatalf("origin header %s not in flatItems", originKey)
	return -1
}

// TestHeaderJumpUpClimbsOutOfGroup: from a session row, shift+↑ surfaces that
// session's OWN checkout header first, then keeps climbing header by header, and
// finally clamps to the top of the list instead of stalling.
func TestHeaderJumpUpClimbsOutOfGroup(t *testing.T) {
	h, s := headerJumpHome()

	h.cursor = idxOfSession(t, h, s["a3"].ID) // inside the alpha worktree
	want := []int{
		idxOfCheckout(t, h, "/tmp/hj-a-wt"),   // own checkout header
		idxOfCheckout(t, h, "/tmp/hj-a-main"), // previous checkout header
		idxOfOrigin(t, h, "github.com/acme/alpha"),
		FirstSelectableItem(h.flatItems), // no header above → clamp to top
	}
	for step, w := range want {
		h.jumpToHeader(-1)
		if h.cursor != w {
			t.Fatalf("shift+↑ step %d: cursor = %d, want %d", step+1, h.cursor, w)
		}
	}

	// Already at the top: another press must not move or wrap.
	before := h.cursor
	h.jumpToHeader(-1)
	if h.cursor != before {
		t.Errorf("shift+↑ at the top moved the cursor to %d (want %d — no wrap)", h.cursor, before)
	}
}

// TestHeaderJumpDownSkipsSessionsAndClampsToBottom: shift+↓ steps over session
// rows to the next header — crossing the origin gap without landing on the
// spacer — and clamps to the last row once no header remains.
func TestHeaderJumpDownSkipsSessionsAndClampsToBottom(t *testing.T) {
	h, s := headerJumpHome()

	h.cursor = idxOfSession(t, h, s["a1"].ID) // first session under alpha/main
	want := []int{
		idxOfCheckout(t, h, "/tmp/hj-a-wt"), // skips sibling session a2
		idxOfOrigin(t, h, "github.com/acme/beta"),
		idxOfCheckout(t, h, "/tmp/hj-b"),
		LastSelectableItem(h.flatItems), // no header below → clamp to bottom
	}
	for step, w := range want {
		h.jumpToHeader(1)
		if h.cursor != w {
			t.Fatalf("shift+↓ step %d: cursor = %d, want %d", step+1, h.cursor, w)
		}
		if h.flatItems[h.cursor].IsSpacer {
			t.Fatalf("shift+↓ step %d landed on a spacer row (%d)", step+1, h.cursor)
		}
	}

	before := h.cursor
	h.jumpToHeader(1)
	if h.cursor != before {
		t.Errorf("shift+↓ at the bottom moved the cursor to %d (want %d — no wrap)", h.cursor, before)
	}
}

// TestHeaderJumpStopsAtCollapsedGroupHeader: a collapsed origin still emits its
// header row, so it stays a jump target even though its sessions are hidden.
// This is what lets shift+↓ traverse a fully-folded tree.
func TestHeaderJumpStopsAtCollapsedGroupHeader(t *testing.T) {
	h, s := headerJumpHome()

	betaKey := OriginExpandKey("github.com/acme/beta")
	h.repoExpanded[betaKey] = false
	h.rebuildFlatItems()
	if visibleHasSession(h, s["b1"].ID) {
		t.Fatal("precondition failed: beta's session should be hidden under the collapsed origin")
	}

	h.cursor = idxOfSession(t, h, s["a3"].ID)
	h.jumpToHeader(1)

	if want := idxOfOrigin(t, h, "github.com/acme/beta"); h.cursor != want {
		t.Errorf("shift+↓ should stop on the collapsed beta origin header (%d), got %d", want, h.cursor)
	}
	if IsExpanded(h.repoExpanded, betaKey) {
		t.Error("shift+↓ must not expand the group it lands on")
	}
}

// TestHeaderJumpKeyBinding drives the real handleKey switch with an actual
// shift+down / shift+up key event. Guards the binding *string*: this codebase
// matches modifiers via msg.String(), so if Bubble Tea ever stringified a
// shifted arrow as anything other than "shift+down"/"shift+up", the case labels
// would silently stop matching and the key would dead-press.
func TestHeaderJumpKeyBinding(t *testing.T) {
	down := tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}
	if got := down.String(); got != "shift+down" {
		t.Fatalf("shift+down stringifies as %q — the case label in handleKey no longer matches", got)
	}
	up := tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift}
	if got := up.String(); got != "shift+up" {
		t.Fatalf("shift+up stringifies as %q — the case label in handleKey no longer matches", got)
	}

	// A fully-wired Home (bare &Home{} has no dialogs, and routeToModal runs
	// ahead of the key switch), populated with the same two-origin tree.
	h := newPersistTestHome(t)
	seed, s := headerJumpHome()
	h.sessions = seed.sessions
	h.gitInfoCache.Store(seed.gitInfoCache.Load())
	h.rebuildFlatItems()
	h.cursor = idxOfSession(t, h, s["a1"].ID)

	if _, _ = h.handleKey(down); h.cursor != idxOfCheckout(t, h, "/tmp/hj-a-wt") {
		t.Errorf("shift+down through handleKey: cursor = %d, want the next checkout header", h.cursor)
	}
	if _, _ = h.handleKey(up); h.cursor != idxOfCheckout(t, h, "/tmp/hj-a-main") {
		t.Errorf("shift+up through handleKey: cursor = %d, want the previous checkout header", h.cursor)
	}
}
