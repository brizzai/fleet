package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/reviewjob"
	"github.com/brizzai/fleet/internal/selfreview"
	"github.com/brizzai/fleet/internal/session"
)

// The review row sits first under its checkout header, only when that checkout
// is expanded and has a review, and carries the checkout's snooze.
func TestInsertReviewRowsFirstUnderExpandedCheckout(t *testing.T) {
	a := session.NewSession("a", "/r/a")
	items := []SidebarItem{
		{IsRepoHeader: true, IsOriginHeader: true, OriginKey: "o", Expanded: true},
		{IsRepoHeader: true, IsCheckoutHeader: true, OriginKey: "o", RepoPath: "/r/a", Expanded: true},
		{OriginKey: "o", RepoPath: "/r/a", Session: a},
		{IsRepoHeader: true, IsCheckoutHeader: true, OriginKey: "o", RepoPath: "/r/b", Expanded: false},
		{IsRepoHeader: true, IsCheckoutHeader: true, OriginKey: "o", RepoPath: "/r/c", Expanded: true},
	}
	badge := func(repo string) (reviewBadge, bool) {
		b := reviewBadge{Status: ReviewBadgeReady, Counts: selfreview.Counts{Decisions: 2, Unanswered: 2}}
		return b, repo != "/r/c"
	}
	out := insertReviewRows(items, badge, func(origin, repo string) snoozeResult {
		return snoozeResult{Muted: repo == "/r/a"}
	})
	if len(out) != len(items)+1 {
		t.Fatalf("rows = %d, want %d (one review row: /r/b is collapsed, /r/c has none)", len(out), len(items)+1)
	}
	if r := out[2]; !r.IsReview || r.RepoPath != "/r/a" || r.OriginKey != "o" || r.Session != nil || !r.Snooze.Muted {
		t.Errorf("row under /r/a = %+v", r)
	}
	if out[3].Session != a {
		t.Error("the review row must come before the checkout's sessions")
	}
	if got := insertReviewRows(items, nil, nil); len(got) != len(items) {
		t.Error("no badge source must leave the rows alone")
	}
}

// newReviewRowHome is a Home with one checkout holding two sessions.
func newReviewRowHome(t *testing.T) (*Home, *session.Session, *session.Session, string) {
	t.Helper()
	storage, err := session.Open(filepath.Join(t.TempDir(), "rr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.Close() })
	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	h.width, h.height = 120, 40
	a := session.NewSession("alpha", "/tmp/rr-wt")
	b := session.NewSession("beta", "/tmp/rr-wt")
	a.SetStatus(session.StatusIdle)
	b.SetStatus(session.StatusIdle)
	root := session.GetRepoRoot(a.ProjectPath)
	h.sessions = []*session.Session{a, b}
	h.rebuildSessionMap()
	h.setGitInfo(map[string]*git.RepoInfo{root: {Branch: "feat-x", IsWorktreeRepo: true}})
	h.rebuildFlatItems()
	return h, a, b, root
}

func (h *Home) indexOfSession(id string) int {
	return contextMenuTarget{sessionID: id}.find(h.flatItems)
}

// A review row appearing above the cursor on a timer must not move the
// highlight onto a different session.
func TestReviewRowAppearingKeepsTheCursorByIdentity(t *testing.T) {
	h, _, b, root := newReviewRowHome(t)
	h.cursor = h.indexOfSession(b.ID)
	before := h.cursor

	h.handleReviewMsg(reviewSummariesMsg{m: map[string]reviewjob.Summary{
		root: {RunID: "r1", Status: session.ReviewReady, Counts: selfreview.Counts{Decisions: 4, Unanswered: 4}, Model: "opus"},
	}})
	ri := contextMenuTarget{reviewRepo: root}.find(h.flatItems)
	if ri < 0 || !h.flatItems[ri].IsReview || h.flatItems[ri-1].RepoPath != root || !h.flatItems[ri-1].IsCheckoutHeader {
		t.Fatalf("review row not first under its header: %d", ri)
	}
	if h.cursor != before+1 || h.flatItems[h.cursor].Session != b {
		t.Errorf("cursor moved off beta: %d (was %d)", h.cursor, before)
	}

	// On the row itself, identity survives the row's own text changing.
	h.cursor = ri
	if got := h.targetForCursor(); got != (contextMenuTarget{reviewRepo: root}) {
		t.Errorf("target = %+v", got)
	}
	h.handleReviewMsg(reviewStaleMsg{runID: "r1", stale: true})
	if h.cursor != ri || !h.flatItems[h.cursor].IsReview || h.flatItems[h.cursor].Review.Status != ReviewBadgeStale {
		t.Errorf("cursor/row after stale: %d %+v", h.cursor, h.flatItems[h.cursor])
	}

	// Approved: the row goes and the cursor stays in range.
	h.handleReviewMsg(reviewSummariesMsg{m: map[string]reviewjob.Summary{root: {RunID: "r1", Status: session.ReviewApproved}}})
	if (contextMenuTarget{reviewRepo: root}).find(h.flatItems) >= 0 {
		t.Error("an approved review must lose its row")
	}
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		t.Errorf("cursor out of range: %d", h.cursor)
	}
}

// Space treats an unopened review with decisions like a waiting session, and
// an opened one not at all.
func TestSpaceJumpReachesAReviewThatNeedsYou(t *testing.T) {
	h, a, _, root := newReviewRowHome(t)
	h.cursor = h.indexOfSession(a.ID)
	h.handleReviewMsg(reviewSummariesMsg{m: map[string]reviewjob.Summary{
		root: {RunID: "r1", Status: session.ReviewReady, Counts: selfreview.Counts{Decisions: 4, Unanswered: 4}},
	}})
	h.jumpToNextAttentionSession()
	if it := h.flatItems[h.cursor]; !it.IsReview {
		t.Fatalf("Space landed on %+v, want the review row", it)
	}

	h.handleReviewMsg(reviewSummariesMsg{m: map[string]reviewjob.Summary{
		root: {RunID: "r1", Status: session.ReviewReady, Counts: selfreview.Counts{Decisions: 4, Unanswered: 4}, Seen: true},
	}})
	h.cursor = h.indexOfSession(a.ID)
	h.jumpToNextAttentionSession()
	if it := h.flatItems[h.cursor]; it.IsReview {
		t.Error("an opened review must leave the rotation")
	}

	// A snoozed checkout mutes its review row too.
	h.handleReviewMsg(reviewSummariesMsg{m: map[string]reviewjob.Summary{
		root: {RunID: "r1", Status: session.ReviewReady, Counts: selfreview.Counts{Decisions: 4, Unanswered: 4}},
	}})
	if h.groupSnooze == nil {
		h.groupSnooze = map[string]time.Time{}
	}
	h.groupSnooze[root] = time.Now().Add(time.Hour)
	h.rebuildFlatItems()
	h.cursor = h.indexOfSession(a.ID)
	h.jumpToNextAttentionSession()
	if it := h.flatItems[h.cursor]; it.IsReview {
		t.Error("a snoozed checkout's review must stay out of the rotation")
	}
}

// The review row's keys: ⏎ opens, tab does nothing, d asks before discarding,
// `.` offers open / regenerate / discard, and the bar names them.
func TestReviewRowKeys(t *testing.T) {
	h, _, _, root := newReviewRowHome(t)
	h.handleReviewMsg(reviewSummariesMsg{m: map[string]reviewjob.Summary{
		root: {RunID: "r1", Status: session.ReviewReady, Counts: selfreview.Counts{Decisions: 4, Unanswered: 4}},
	}})
	h.cursor = contextMenuTarget{reviewRepo: root}.find(h.flatItems)
	if h.cursorBarContext() != BarContextReview {
		t.Fatalf("bar context = %v", h.cursorBarContext())
	}
	if h.selectedSession() != nil {
		t.Error("a review row is not a session")
	}
	if h.resolveCurrentRepo() != root {
		t.Error("a review row's repo is its checkout (w / a / p act on it)")
	}
	if _, cmd := h.handleKey(tea.KeyPressMsg{Code: tea.KeyTab}); cmd != nil {
		t.Error("tab on a review row must do nothing")
	}
	if cmd := h.activateCursorRow(); cmd == nil {
		t.Error("⏎ on a ready review row must load it")
	}
	_, items := h.buildContextMenuItems()
	if got := ids(items); len(got) != 3 || got[0] != "review" || got[1] != "review_regenerate" || got[2] != "review_discard" {
		t.Errorf("menu = %v", got)
	}
	h.handleKey(key("d"))
	if !h.confirmDialog.IsVisible() {
		t.Fatal("d on a review row must confirm")
	}

	// While preparing, discard and regenerate are dimmed with the reason.
	h.confirmDialog.Hide()
	h.reviewPreparing[root] = true
	h.rebuildFlatItems()
	h.cursor = contextMenuTarget{reviewRepo: root}.find(h.flatItems)
	_, items = h.buildContextMenuItems()
	if it, _ := findItem(items, "review_discard"); it.Enabled || it.Note == "" {
		t.Errorf("discard while preparing = %+v", it)
	}
	if it, _ := findItem(items, "review_regenerate"); it.Enabled || it.Note != "already reviewing" {
		t.Errorf("regenerate while preparing = %+v", it)
	}
}

// `v` works on a main checkout's header now, not only on worktree sessions.
func TestReviewKeyOnACheckoutHeader(t *testing.T) {
	h, _, _, root := newReviewRowHome(t)
	h.setGitInfo(map[string]*git.RepoInfo{root: {Branch: "main"}})
	for i, it := range h.flatItems {
		if it.IsCheckoutHeader && it.RepoPath == root {
			h.cursor = i
		}
	}
	if r, why := h.reviewCheckoutAtCursor(); r != root || why != "" || h.reviewBlocker(r) != "" {
		t.Fatalf("checkout header → %q %q %q", r, why, h.reviewBlocker(r))
	}
	_, items := h.buildContextMenuItems()
	if it, ok := findItem(items, "review"); !ok || !it.Enabled {
		t.Errorf("checkout menu review row = %+v (present %v)", it, ok)
	}
}

// A narrow sidebar sheds words whole — the short form, then "Review" — and
// the row never overflows, selected or not.
func TestReviewRowFitsNarrowSidebars(t *testing.T) {
	ApplyPalette(PaletteFleetPink)
	for _, b := range []reviewBadge{
		{Status: ReviewBadgeStale, Counts: selfreview.Counts{Decisions: 9, Unanswered: 9}},
		{Status: ReviewBadgeSent, Working: true},
		{Status: ReviewBadgeSent},
		{Status: ReviewBadgeReady, Counts: selfreview.Counts{NotReviewed: 12}},
	} {
		for _, w := range []int{20, 24, 30, 34, 50} {
			for _, sel := range []bool{false, true} {
				got := renderReviewRow(SidebarItem{IsReview: true, Review: b}, w, sel)
				if lw := lipgloss.Width(got); lw > w {
					t.Errorf("%+v w=%d sel=%v: %d wide: %q", b, w, sel, lw, ansi.Strip(got))
				}
				if strings.Contains(ansi.Strip(got), "…") && w >= 30 {
					t.Errorf("%+v w=%d sel=%v cut a word: %q", b, w, sel, ansi.Strip(got))
				}
			}
		}
	}
	ready := reviewBadge{Status: ReviewBadgeReady, Counts: selfreview.Counts{Decisions: 9, Unanswered: 9}}
	if got := ansi.Strip(renderReviewRow(SidebarItem{IsReview: true, Review: ready}, 34, false)); !strings.Contains(got, "Review · 9 to decide") {
		t.Errorf("34 ready = %q", got)
	}
}
