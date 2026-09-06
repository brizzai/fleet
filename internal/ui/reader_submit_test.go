package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/review"
)

// readerPress sends one key by name and returns whatever command it produced.
func readerPress(t *testing.T, d *ReaderDialog, name string) tea.Cmd {
	t.Helper()
	next, cmd := d.Update(keyOf(name))
	*d = *next
	return cmd
}

// A stray S followed by a stray Enter must not review anybody's pull request.
//
// This is the whole reason COMMENT leads the verdict cycle: it is the one event
// GitHub refuses without a summary, so the two-keystroke accident dead-ends on
// a stated reason instead of posting.
func TestSubmitRefusesUntilThereIsASummary(t *testing.T) {
	d := newDemoReader(t, 160, 40)

	if cmd := readerPress(t, d, "S"); cmd != nil {
		t.Fatal("opening the submit sheet produced a command — it should only open a sheet")
	}
	if d.mode != modeSubmit {
		t.Fatalf("S did not open the submit sheet (mode = %v)", d.mode)
	}
	if got := d.chosenEvent(); got != github.EventComment {
		t.Fatalf("the sheet opened on %s — it must open on COMMENT, the one verdict "+
			"that cannot be submitted by accident", got)
	}

	if cmd := readerPress(t, d, "enter"); cmd != nil {
		t.Fatal("enter submitted a review with no summary — GitHub requires one for " +
			"COMMENT, so this would have been a 422 after the fact")
	}
	if d.submitting {
		t.Fatal("the sheet believes it is posting when nothing was sent")
	}
	if !strings.Contains(d.submitErr, "summary") {
		t.Errorf("refusal reason %q does not name the missing summary", d.submitErr)
	}
}

// Approve is the review you leave with nothing to say, so it must not demand a
// summary the way the other two do.
func TestSubmitApproveNeedsNoSummary(t *testing.T) {
	d := newDemoReader(t, 160, 40)
	readerPress(t, d, "S")
	readerPress(t, d, "tab") // comment → approve

	if got := d.chosenEvent(); got != github.EventApprove {
		t.Fatalf("tab moved to %s, want APPROVE", got)
	}
	cmd := readerPress(t, d, "enter")
	if cmd == nil {
		t.Fatal("approve with no summary was refused — GitHub asks for a body only " +
			"on COMMENT and REQUEST_CHANGES")
	}
	msg, ok := cmd().(reviewSubmitRequestMsg)
	if !ok {
		t.Fatalf("enter produced %T, want reviewSubmitRequestMsg", cmd())
	}
	if msg.event != github.EventApprove || msg.pr != 283 {
		t.Errorf("submitted %+v, want an APPROVE on #283", msg)
	}
	if !d.submitting {
		t.Error("the sheet is not showing the request in flight; esc would look like it cancelled")
	}
}

// The space bar has now eaten text in three separate places in this reader
// because KeyPressMsg.String() reports the key's NAME. The summary box is a
// text field, so it is the fourth place it could happen.
func TestSubmitSummaryTakesSpaces(t *testing.T) {
	d := newDemoReader(t, 160, 40)
	readerPress(t, d, "S")
	for _, k := range []string{"o", "k", " ", "b", "y", " ", "m", "e"} {
		readerPress(t, d, k)
	}
	if d.submitBody != "ok by me" {
		t.Errorf("summary is %q, want %q — String() returns \"space\", Text returns \" \"",
			d.submitBody, "ok by me")
	}
	if d.submitBlocker() != "" {
		t.Errorf("a typed summary still blocks submit: %q", d.submitBlocker())
	}
}

// Esc sends nothing, and says so: silence after cancelling a review is the one
// case where you need to be told that nothing happened.
func TestSubmitEscSendsNothing(t *testing.T) {
	d := newDemoReader(t, 160, 40)
	readerPress(t, d, "S")
	readerPress(t, d, "y")
	if cmd := readerPress(t, d, "esc"); cmd != nil {
		t.Fatal("esc produced a command")
	}
	if d.mode != modeNormal || d.submitBody != "" {
		t.Errorf("esc left the sheet behind (mode %v, body %q)", d.mode, d.submitBody)
	}
	if !strings.Contains(d.toast, "nothing") {
		t.Errorf("toast %q does not say that nothing was submitted", d.toast)
	}
}

// The reader owns the whole screen, so the sheet must not push it out of shape:
// never more rows than the terminal has, and never a row wider than it is.
//
// Rows are allowed to come out SHORT. lipgloss's compositor trims the trailing
// blanks off any row the overlay does not cover, which is true of the `?` sheet
// today too; a short row leaves unpainted cells the terminal clears anyway,
// while a long one wraps and shifts everything below it.
func TestSubmitSheetKeepsTheScreenExact(t *testing.T) {
	for _, tc := range []struct{ w, h int }{{160, 40}, {124, 30}, {80, 24}} {
		d := newDemoReader(t, tc.w, tc.h)
		readerPress(t, d, "S")
		lines := strings.Split(d.View(), "\n")
		if len(lines) != tc.h {
			t.Errorf("%dx%d: sheet rendered %d rows, want %d", tc.w, tc.h, len(lines), tc.h)
			continue
		}
		for i, l := range lines {
			if got := lipgloss.Width(l); got > tc.w {
				t.Errorf("%dx%d: row %d is %d columns, over the %d available",
					tc.w, tc.h, i, got, tc.w)
				break
			}
		}
	}
}

// A failed submit keeps the sheet, the summary and the reason. The usual causes
// — a rate limit, a line GitHub will not anchor — are retried, and throwing the
// typed summary away would make every retry start from a blank box.
func TestFailedSubmitKeepsTheSheet(t *testing.T) {
	d := newDemoReader(t, 160, 40)
	readerPress(t, d, "S")
	for _, k := range []string{"h", "i"} {
		readerPress(t, d, k)
	}
	readerPress(t, d, "enter")

	d.SubmitDone(errors.New("line must be part of the diff"), 0)
	if d.mode != modeSubmit {
		t.Fatal("a failed submit closed the sheet")
	}
	if d.submitBody != "hi" {
		t.Errorf("summary lost on failure: %q", d.submitBody)
	}
	if !strings.Contains(d.View(), "part of the diff") {
		t.Error("GitHub's own reason is not on screen — it names the thing to fix")
	}
	if d.submitting {
		t.Error("still showing in-flight after the failure landed")
	}
}

// What GitHub receives is decided here: the kind prefix, the side of the diff,
// and the commit the lines were read at.
func TestBuildSubmissionCarriesKindSideAndCommit(t *testing.T) {
	r := github.ReviewRequest{Number: 283, Repo: "brizzai/fleet", HeadSHA: "deadbeef"}
	cs := []review.Comment{
		{File: "internal/ui/app.go", Line: 12, Kind: review.CommentNit, Body: "spelling"},
		{File: "internal/ui/app.go", Line: 40, Kind: review.CommentIssue, Body: "this races"},
	}
	sub := buildSubmission(r, github.EventRequestChanges, "two things", cs)

	if sub.CommitID != "deadbeef" {
		t.Errorf("commit_id = %q — without it GitHub re-anchors every line at the "+
			"current head, which may be code nobody read", sub.CommitID)
	}
	if sub.Event != github.EventRequestChanges || sub.Body != "two things" {
		t.Errorf("verdict or summary lost: %+v", sub)
	}
	if len(sub.Comments) != 2 {
		t.Fatalf("got %d comments, want 2", len(sub.Comments))
	}
	if sub.Comments[0].Body != "nit: spelling" {
		t.Errorf("comment body %q dropped the kind prefix — a nit that arrives "+
			"unlabelled reads as a blocking objection", sub.Comments[0].Body)
	}
	if sub.Comments[1].Body != "this races" {
		t.Errorf("an issue must carry no prefix, got %q", sub.Comments[1].Body)
	}
	for i, c := range sub.Comments {
		if c.Side != "RIGHT" {
			t.Errorf("comment %d anchors to %q, want RIGHT — the reader only ever "+
				"comments on the new side", i, c.Side)
		}
	}
	if sub.Comments[0].Line != 12 {
		t.Errorf("line = %d, want the new-side line 12", sub.Comments[0].Line)
	}
}

// A submitted comment must never be offered for submission again.
func TestSyncDropsSubmittedComments(t *testing.T) {
	h := &Home{reviewQueue: []github.ReviewRequest{{Number: 7, Repo: "brizzai/fleet"}}}
	h.syncReviewComments(7, []review.Comment{
		{File: "a.go", Line: 1, Body: "sent", Sent: true},
		{File: "a.go", Line: 2, Body: "not sent"},
	})
	got := h.reviewComments[7]
	if len(got) != 1 || got[0].body != "not sent" {
		t.Fatalf("pending comments = %+v, want only the unsent one", got)
	}

	h.syncReviewComments(7, []review.Comment{{File: "a.go", Line: 1, Body: "sent", Sent: true}})
	if _, ok := h.reviewComments[7]; ok {
		t.Error("a PR whose every comment was submitted still has a pending entry — " +
			"reopening the reader would offer to post them a second time")
	}
}
