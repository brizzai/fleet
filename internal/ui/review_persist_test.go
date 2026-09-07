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
