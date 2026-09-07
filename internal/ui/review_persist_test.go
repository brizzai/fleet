package ui

import (
	"path/filepath"
	"testing"

	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/review"
	"github.com/brizzai/fleet/internal/session"
)

func homeWithDB(t *testing.T) *Home {
	t.Helper()
	db, err := session.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Home{
		storage:        db,
		actionLog:      NewActionLog(100),
		toasts:         NewToastStack(),
		reviewRead:     map[reviewKey]map[string]bool{},
		reviewComments: map[reviewKey][]reviewCommentMsg{},
	}
}

// Closing fleet mid-review used to be the same as abandoning it. This is the
// whole feature: what you read and what you wrote come back.
func TestReviewStateSurvivesARestart(t *testing.T) {
	h := homeWithDB(t)
	k := reviewKey{repo: "brizzai/fleet", pr: 283}
	h.reviewQueue = []github.ReviewRequest{{Number: 283, Repo: "brizzai/fleet", HeadSHA: "deadbeef"}}

	h.syncReviewComments(k, []review.Comment{
		{File: "internal/ui/reader.go", Line: 412, Kind: review.CommentNit, Body: "spelling"},
	})
	h.reviewRead[k] = map[string]bool{"a.go": true, "b.go": false}
	h.saveReviewRead(k)

	// A fresh process, same database.
	next := &Home{
		storage:        h.storage,
		reviewRead:     map[reviewKey]map[string]bool{},
		reviewComments: map[reviewKey][]reviewCommentMsg{},
	}
	next.loadReviewState()

	if got := next.reviewRead[k]; !got["a.go"] {
		t.Errorf("read marks lost: %+v", got)
	}
	if next.reviewRead[k]["b.go"] {
		t.Error("a file that was NOT read came back marked read — only true marks " +
			"are stored, so an unmarked file must stay unmarked")
	}

	cs := next.reviewComments[k]
	if len(cs) != 1 {
		t.Fatalf("loaded %d comments, want 1", len(cs))
	}
	if cs[0].kind != review.CommentNit || cs[0].body != "spelling" || cs[0].line != 412 {
		t.Errorf("comment came back changed: %+v", cs[0])
	}
	if cs[0].key != k {
		t.Errorf("comment loaded under %+v, want %+v", cs[0].key, k)
	}

	// And it is offered to the reader on the next open, which is the point.
	if seeded := next.seedComments(k); len(seeded) != 1 || seeded[0].Kind != review.CommentNit {
		t.Errorf("seedComments returned %+v", seeded)
	}
}

// Two repositories both have a PR #12. Before the key carried the repo, they
// shared a map entry — so comments written on one would be offered for posting
// to the other.
func TestSamePRNumberInTwoReposStaysApart(t *testing.T) {
	h := homeWithDB(t)
	fleet := reviewKey{repo: "brizzai/fleet", pr: 12}
	other := reviewKey{repo: "someone/else", pr: 12}

	h.syncReviewComments(fleet, []review.Comment{{File: "a.go", Line: 1, Body: "for fleet"}})
	h.syncReviewComments(other, []review.Comment{{File: "b.go", Line: 2, Body: "for else"}})

	next := &Home{
		storage:        h.storage,
		reviewRead:     map[reviewKey]map[string]bool{},
		reviewComments: map[reviewKey][]reviewCommentMsg{},
	}
	next.loadReviewState()

	if len(next.reviewComments[fleet]) != 1 || next.reviewComments[fleet][0].body != "for fleet" {
		t.Errorf("brizzai/fleet#12 got %+v", next.reviewComments[fleet])
	}
	if len(next.reviewComments[other]) != 1 || next.reviewComments[other][0].body != "for else" {
		t.Errorf("someone/else#12 got %+v", next.reviewComments[other])
	}
}

// A submitted review must not come back as a draft on the next launch and offer
// to post itself a second time.
func TestSubmittingClearsTheStoredComments(t *testing.T) {
	h := homeWithDB(t)
	k := reviewKey{repo: "brizzai/fleet", pr: 283}
	h.syncReviewComments(k, []review.Comment{{File: "a.go", Line: 1, Body: "queued"}})

	h.handleReviewSubmitted(reviewSubmitResultMsg{key: k, event: github.EventComment, count: 1})

	if len(h.reviewComments[k]) != 0 {
		t.Errorf("in-memory queue still holds %d comments", len(h.reviewComments[k]))
	}
	next := &Home{
		storage:        h.storage,
		reviewRead:     map[reviewKey]map[string]bool{},
		reviewComments: map[reviewKey][]reviewCommentMsg{},
	}
	next.loadReviewState()
	if got := next.reviewComments[k]; len(got) != 0 {
		t.Errorf("a submitted comment came back from the database: %+v", got)
	}
}

// The commit a review anchors to is decided by the comments, not by whatever
// the pull request points at now.
func TestReviewAnchorFollowsTheComments(t *testing.T) {
	at := func(shas ...string) []review.Comment {
		var cs []review.Comment
		for _, s := range shas {
			cs = append(cs, review.Comment{File: "a.go", Line: 1, HeadSHA: s})
		}
		return cs
	}

	if got := reviewAnchor(at("aaa", "aaa"), "bbb"); got != "aaa" {
		t.Errorf("anchor = %q, want the commit the comments were written against — "+
			"anchoring at the new head puts old line numbers on new code", got)
	}
	// Mixed: the file list refreshed mid-review and more comments followed.
	// GitHub takes one commit per review, so there is no right answer; the diff
	// on screen is at least what the user was last looking at.
	if got := reviewAnchor(at("aaa", "bbb"), "ccc"); got != "ccc" {
		t.Errorf("mixed anchors gave %q, want the loaded diff's own SHA", got)
	}
	// An approve with nothing queued, and a comment from before anchors were
	// recorded, both fall back rather than sending an empty commit id.
	if got := reviewAnchor(nil, "ccc"); got != "ccc" {
		t.Errorf("empty batch gave %q, want the loaded SHA", got)
	}
	if got := reviewAnchor(at(""), "ccc"); got != "ccc" {
		t.Errorf("an unanchored comment gave %q, want the loaded SHA", got)
	}
}

// The case the stored SHA exists for: write a comment, close fleet, the author
// pushes, come back. The comment must still be about the code it was written
// about.
func TestADraftOutlivesAPushAndKeepsItsAnchor(t *testing.T) {
	h := homeWithDB(t)
	k := reviewKey{repo: "brizzai/fleet", pr: 283}

	// Sitting one: the diff is at aaa.
	h.reviewFiles = map[reviewKey]github.PRFiles{k: {HeadSHA: "aaa"}}
	h.syncReviewComments(k, []review.Comment{
		{File: "internal/ui/reader.go", Line: 412, Body: "this races"},
	})

	// Sitting two, after a restart and a push: the diff refetches at bbb.
	next := &Home{
		storage:        h.storage,
		actionLog:      NewActionLog(100),
		toasts:         NewToastStack(),
		reviewRead:     map[reviewKey]map[string]bool{},
		reviewComments: map[reviewKey][]reviewCommentMsg{},
		reviewFiles:    map[reviewKey]github.PRFiles{k: {HeadSHA: "bbb"}},
	}
	next.loadReviewState()

	seeded := next.seedComments(k)
	if len(seeded) != 1 || seeded[0].HeadSHA != "aaa" {
		t.Fatalf("seeded %+v, want the comment still anchored at aaa", seeded)
	}
	if got := reviewAnchor(seeded, next.reviewFiles[k].HeadSHA); got != "aaa" {
		t.Errorf("submit would anchor at %q — line 412 of aaa is the code this "+
			"comment is about; at bbb it may be something else entirely", got)
	}

	// And a re-save must not quietly restamp it with today's head.
	next.syncReviewComments(k, seeded)
	if got := next.reviewComments[k][0].headSHA; got != "aaa" {
		t.Errorf("re-saving moved the anchor to %q", got)
	}
}

// A comment written in this sitting has no anchor of its own yet, so it takes
// the SHA of the diff it was written against.
func TestAFreshCommentTakesTheLoadedSHA(t *testing.T) {
	h := homeWithDB(t)
	k := reviewKey{repo: "brizzai/fleet", pr: 283}
	h.reviewFiles = map[reviewKey]github.PRFiles{k: {HeadSHA: "aaa"}}

	h.syncReviewComments(k, []review.Comment{{File: "a.go", Line: 1, Body: "new"}})
	if got := h.reviewComments[k][0].headSHA; got != "aaa" {
		t.Errorf("a fresh comment stored anchor %q, want the loaded diff's aaa", got)
	}
}
