package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/brizzai/fleet/internal/github"
)

func demoReview() github.ReviewRequest {
	return github.ReviewRequest{
		Number: 5241, Repo: "brizzai/brizzai", Author: "hayke102",
		Title:        "[BRZ-3620] Triage runs: score how fixable each issue is, before anyone writes a patch",
		BaseRef:      "master",
		HeadRef:      "brz-3620-triage-runs-score-how-fixable-each",
		ChangedFiles: 94, Additions: 9426, Deletions: 574,
		Decision: "REVIEW_REQUIRED", ChecksState: "SUCCESS", ChecksTotal: 50, Comments: 77,
		Labels:    []string{"backend", "enhancement", "frontend", "large-pr"},
		UpdatedAt: time.Now().Add(-48 * time.Hour),
		Body: "<!-- Please fill in the template below.\nDelete anything that does not apply. -->\n\n" +
			"Adds an internal, super-admin-only triage run that ranks one service's open " +
			"issues by how `fixable` they are.\n\n\n\n## What changes for the user\n\nSuper admins get a new page.",
	}
}

// Everything above the description comes from the review queue — one GraphQL
// call fleet has already made — so the preview must never depend on the
// changed-file fetch that used to put "Loading files for #N…" in front of it.
func TestReviewPreviewRendersFromTheQueueAlone(t *testing.T) {
	out := renderReviewPreview(demoReview(), 90, 30)
	for _, want := range []string{
		"Triage runs", // the title
		"Open",        // state
		"master",      // where it lands
		"94 files",    // size
		"+9426",       // and how big
		"review required",
		"50 checks",
		"77 comments",
		"backend",                     // labels
		"super-admin-only triage run", // the author's own words
	} {
		if !strings.Contains(out, want) {
			t.Errorf("preview is missing %q", want)
		}
	}
}

// A repo with a pull request template hands every description a block of
// instructions to the author, which GitHub hides and a naive renderer shows.
func TestReviewPreviewHidesTemplateComments(t *testing.T) {
	out := renderReviewPreview(demoReview(), 90, 30)
	if strings.Contains(out, "Please fill in the template") {
		t.Error("the hidden half of the template is on screen — the first thing " +
			"the user reads would be a form nobody filled in")
	}
	// And the run of blank lines a template leaves behind is collapsed, since
	// vertical space is the scarcest thing in a preview pane.
	if strings.Contains(out, "\n  \n  \n  \n") {
		t.Error("blank lines were not collapsed")
	}
}

// A draft is a different thing from an open pull request and must not read the
// same, and a change with no CI at all must not read as one whose CI passed.
func TestReviewPreviewDistinguishesDraftAndMissingChecks(t *testing.T) {
	r := demoReview()
	r.IsDraft = true
	r.ChecksState, r.ChecksTotal = "", 0
	out := renderReviewPreview(r, 90, 30)
	if !strings.Contains(out, "Draft") {
		t.Error("a draft does not say so")
	}
	if strings.Contains(out, "checks") {
		t.Error("a pull request with no checks claims to have some")
	}
}

// The preview shares the pane with everything else, so it must never render
// wider than it was given.
func TestReviewPreviewRespectsItsWidth(t *testing.T) {
	for _, w := range []int{60, 90, 140} {
		for _, line := range strings.Split(renderReviewPreview(demoReview(), w, 30), "\n") {
			if got := lipgloss.Width(line); got > w {
				t.Errorf("width %d: a row is %d columns: %q", w, got, line)
				break
			}
		}
	}
}

// The body is capped by the space available, or a long description would push
// the panel past the bottom of the screen.
func TestReviewPreviewFitsItsHeight(t *testing.T) {
	r := demoReview()
	r.Body = strings.Repeat("a paragraph of description text that goes on. ", 200)
	got := len(strings.Split(renderReviewPreview(r, 90, 20), "\n"))
	if got > 20 {
		t.Errorf("rendered %d rows into a 20-row pane", got)
	}
}
