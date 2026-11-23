package ui

import (
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/reviewjob"
	"github.com/brizzai/fleet/internal/selfreview"
	"github.com/brizzai/fleet/internal/session"
)

func newReviewWiringHome() *Home {
	return &Home{
		reviewView:       &ReviewView{},
		reviewSummaries:  map[string]reviewjob.Summary{},
		reviewPreparing:  map[string]bool{},
		reviewLocalFail:  map[string]string{},
		reviewStale:      map[string]bool{},
		reviewTargetPick: map[string]string{},
	}
}

// The `v` key, the context-menu rows and the palette command share one guard,
// and each reason it gives has to be true: a cache that hasn't warmed is not a
// missing repo. A main checkout is reviewable as much as a worktree.
func TestReviewBlockerNamesTheRealReason(t *testing.T) {
	h := newReviewWiringHome()
	repo := "/r/wt"
	for _, tc := range []struct {
		info *git.RepoInfo
		want string
	}{
		{nil, "git status loading"},
		{&git.RepoInfo{}, "no git repo"},
		{&git.RepoInfo{Branch: "main"}, ""},
		{&git.RepoInfo{Branch: "feat", IsWorktreeRepo: true}, ""},
	} {
		m := map[string]*git.RepoInfo{}
		if tc.info != nil {
			m[repo] = tc.info
		}
		h.setGitInfo(m)
		if got := h.reviewBlocker(repo); got != tc.want {
			t.Errorf("info %+v: blocker %q, want %q", tc.info, got, tc.want)
		}
	}
	if got := h.reviewBlocker(""); got == "" {
		t.Error("no checkout must be refused")
	}
	if _, why := reviewCheckoutOf(SidebarItem{IsOriginHeader: true}); why == "" {
		t.Error("an origin header names no single checkout")
	}
	if root, _ := reviewCheckoutOf(SidebarItem{IsReview: true, RepoPath: repo}); root != repo {
		t.Errorf("review row → %q", root)
	}
}

// Local knowledge beats the stored summary: a prepare this TUI started reads
// preparing on the keypress, a failure that stored no run still reads failed,
// and a run CheckStale found stale says so.
func TestReviewBadgePrecedence(t *testing.T) {
	h := newReviewWiringHome()
	h.reviewSummaries["/a"] = reviewjob.Summary{RunID: "r1", Status: session.ReviewReady, Counts: selfreview.Counts{Decisions: 6, Unanswered: 6}, Seen: true}

	if b := h.reviewBadgeFor("/a"); b.Status != ReviewBadgeReady || b.Counts.Unanswered != 6 || !b.Seen {
		t.Errorf("ready summary → %+v", b)
	}
	h.reviewStale["r1"] = true
	if b := h.reviewBadgeFor("/a"); reviewRowText(b) != "Review · ⚠ code changed" {
		t.Errorf("stale run → %q", reviewRowText(b))
	}
	h.reviewLocalFail["/a"] = "not a git checkout"
	if b := h.reviewBadgeFor("/a"); b.Status != ReviewBadgeFailed {
		t.Errorf("local failure → %+v, want failed", b)
	}
	if p := h.reviewPreviewForCheckout("/a"); p.Reason != "not a git checkout" {
		t.Errorf("preview reason %q", p.Reason)
	}
	h.reviewPreparing["/a"] = true
	if b := h.reviewBadgeFor("/a"); reviewRowText(b) != "Review · preparing…" {
		t.Errorf("in-flight prepare → %q", reviewRowText(b))
	}

	h.reviewSummaries["/b"] = reviewjob.Summary{Status: session.ReviewApproved, Counts: selfreview.Counts{Decisions: 3, Unanswered: 3}}
	if _, shown := h.reviewRowBadge("/b"); shown {
		t.Error("approved → the row must go")
	}
	if _, shown := h.reviewRowBadge("/none"); shown {
		t.Error("no run → no row")
	}
}

func TestReviewDataForMapsStatus(t *testing.T) {
	row := &session.ReviewRunRow{ID: "r", Round: 2, HeadSHA: "0123456789abcdef", Model: "sonnet", ElapsedMS: 74000}
	rec := &reviewjob.Record{Row: row, Status: session.ReviewReady}
	rec.Snapshot.Dirty = true

	d := reviewDataFor("/wt", "feat", rec, nil, false)
	if d.Freshness != ReviewFresh || d.Snapshot != "0123456 + uncommitted" || d.Round != 2 || d.Status != "" ||
		d.Checkout != "/wt" || d.CheckoutLabel != "feat" {
		t.Errorf("ready → %+v", d)
	}
	if d := reviewDataFor("/wt", "feat", rec, nil, true); d.Freshness != ReviewStale {
		t.Errorf("stale → %q", d.Freshness)
	}
	rec.Status = session.ReviewSent
	if d := reviewDataFor("/wt", "feat", rec, nil, false); d.Status != reviewStatusSent {
		t.Errorf("sent → status %q", d.Status)
	}
	rec.Status, row.Error = session.ReviewFailed, "boom"
	if d := reviewDataFor("/wt", "feat", rec, nil, true); d.Freshness != ReviewFailed || d.FailReason != "boom" {
		t.Errorf("failed → %+v (a failed run must not read as stale)", d)
	}
}

func TestReviewFileStatusMatchesTheEngine(t *testing.T) {
	for letter, want := range map[string]string{"A": "added", "D": "removed", "R": "renamed", "M": "modified", "T": "changed"} {
		if got := reviewFileStatus(letter); got != want {
			t.Errorf("%s → %q, want %q", letter, got, want)
		}
	}
}

// An answer counted before its Cmd exists is what makes a send dispatched after
// it wait for the write; the counter must be taken on the Update goroutine.
func TestReviewAnswerIsCountedBeforeItsCmdRuns(t *testing.T) {
	h := newReviewWiringHome()
	cmd, ok := h.handleReviewMsg(reviewAnswerMsg{RunID: "r", Answer: selfreview.Answer{StopID: "s1", Kind: selfreview.AnswerFine}})
	if !ok || cmd == nil {
		t.Fatal("answer not handled")
	}
	done := make(chan struct{})
	go func() { h.reviewWrites.Wait(); close(done) }()
	time.Sleep(20 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("the write was not counted before its Cmd ran")
	default:
	}
	h.reviewWrites.Done() // stand in for the Cmd (no store in this test)
	<-done
}
