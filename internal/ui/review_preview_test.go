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
		"Open",     // state
		"master",   // where it lands
		"94 files", // size
		"+9426",    // and how big
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

// The panel's own bar already reads "Preview · #5266 <title> · finished", so a
// title row here was two rows of stutter before a single fact.
func TestReviewPreviewDoesNotRepeatTheTitle(t *testing.T) {
	if strings.Contains(renderReviewPreview(demoReview(), 90, 30), "Triage runs") {
		t.Error("the title is rendered inside the panel that already names it")
	}
}

// The pane is often 150 columns and the header was six rows stacked at one
// indent. What describes the change reads left; what you are waiting on sits
// right, where the eye finds it without reading the line.
func TestReviewHeaderUsesTwoColumnsWhenItCan(t *testing.T) {
	r := demoReview()
	wide := reviewHeaderLines(r, 150)
	if len(wide) != 3 {
		t.Fatalf("wide header is %d rows, want facts + branches + labels", len(wide))
	}
	if !strings.Contains(wide[0], "hayke102") {
		t.Errorf("row 0 has no author: %q", wide[0])
	}
	if !strings.Contains(wide[0], "checks") {
		t.Error("the state did not join the facts row on a wide pane")
	}

	// Narrow: it stacks rather than truncating, so a small pane loses the
	// layout and not the content.
	narrow := reviewHeaderLines(r, 44)
	if len(narrow) <= len(wide) {
		t.Errorf("narrow header is %d rows, wide is %d — it should stack", len(narrow), len(wide))
	}
	if !strings.Contains(strings.Join(narrow, "\n"), "checks") {
		t.Error("stacking dropped the state instead of moving it")
	}
}

// "into master" is true of nearly every pull request; "into
// aviv/brz-3639-collapse-migrations" means this one is stacked on another.
func TestReviewHeaderShowsBothBranches(t *testing.T) {
	out := strings.Join(reviewHeaderLines(demoReview(), 150), "\n")
	if !strings.Contains(out, "master") || !strings.Contains(out, "brz-3620") {
		t.Errorf("the branch pair is missing:\n%s", out)
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

// The description in the screenshot that prompted this was a wall of raw pipes.
func TestReviewBodyRendersTables(t *testing.T) {
	body := "| Flow | Where it starts | Before |\n" +
		"| --- | --- | --- |\n" +
		"| Using the API while a deploy rolls out | any backend endpoint | new instances take ~21s |\n"
	out := strings.Join(reviewBodyLines(body, 70, 40), "\n")

	if strings.Contains(out, "| --- |") {
		t.Error("the separator row is on screen — it is markup, not content")
	}
	for _, want := range []string{"┌", "├", "└", "│"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q — the table is not drawn", want)
		}
	}
	// Cells WRAP rather than truncate: in a before/after table the tail of the
	// cell is usually the part that mattered.
	if strings.Contains(out, "…") {
		t.Error("a cell was truncated instead of wrapped")
	}
	for _, want := range []string{"deploy", "rolls", "out", "~21s"} {
		if !strings.Contains(out, want) {
			t.Errorf("the table lost %q", want)
		}
	}
}

// A table must not overflow the pane it is drawn into.
func TestReviewTableFitsItsWidth(t *testing.T) {
	body := "| a | b | c | d |\n| --- | --- | --- | --- |\n" +
		"| " + strings.Repeat("long ", 20) + " | x | y | z |\n"
	for _, w := range []int{60, 100, 150} {
		for _, line := range reviewBodyLines(body, w, 60) {
			if got := lipgloss.Width(line); got > w {
				t.Errorf("width %d: a table row is %d columns", w, got)
				break
			}
		}
	}
}

// Fence markers are markup. The block behind them is usually an ASCII diagram,
// so it keeps the full width and is never wrapped.
func TestReviewBodyRendersCodeFences(t *testing.T) {
	body := "before\n\n```text\nbuild -> deploy -> migrate\n```\n\nafter\n"
	out := strings.Join(reviewBodyLines(body, 80, 40), "\n")
	if strings.Contains(out, "```") {
		t.Error("the fence markers are on screen")
	}
	if !strings.Contains(out, "build -> deploy -> migrate") {
		t.Error("the code block's content is missing")
	}
}

// `_` is a word character in code. skip_migrations and is_admin are everywhere
// in a pull request description, and a naive italic rule renders the middle of
// every identifier in italics.
func TestEmphasisDoesNotEatIdentifiers(t *testing.T) {
	// lipgloss folds attributes into ONE escape (3;38;2;r;g;b), so italic is
	// "\x1b[3;" and never a bare "\x1b[3m". "\x1b[38;" is a foreground and must
	// not be mistaken for it.
	got := renderInlineMarkdown("set skip_migrations and is_admin on the_thing")
	if strings.Contains(got, "\x1b[3;") {
		t.Errorf("an identifier was italicised: %q", got)
	}
	for _, want := range []string{"skip_migrations", "is_admin", "the_thing"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q was mangled: %q", want, got)
		}
	}

	// Real emphasis still renders, and its markers go.
	got = renderInlineMarkdown("what an operator does: _nothing_ at all")
	if !strings.Contains(got, "\x1b[3;") {
		t.Errorf("real emphasis was not styled: %q", got)
	}
	if strings.Contains(got, "_nothing_") {
		t.Errorf("emphasis markers are still on screen: %q", got)
	}
}

// Paragraphs get a reading measure; tables and code get the whole pane.
func TestProseIsCappedButTablesAreNot(t *testing.T) {
	prose := strings.Repeat("a readable sentence of prose. ", 30)
	for _, line := range reviewBodyLines(prose, 150, 40) {
		if got := lipgloss.Width(line); got > proseMeasure {
			t.Errorf("a paragraph ran to %d columns, over the %d measure", got, proseMeasure)
			break
		}
	}

	body := "| a | b |\n| --- | --- |\n| " + strings.Repeat("x", 60) + " | " + strings.Repeat("y", 60) + " |\n"
	widest := 0
	for _, line := range reviewBodyLines(body, 150, 40) {
		widest = max(widest, lipgloss.Width(line))
	}
	if widest <= proseMeasure {
		t.Errorf("the table was squeezed to %d columns — it had 150 and is laid "+
			"out rather than read line by line", widest)
	}
}

// Emphasis around a sentence is longer than the reading measure, so it spans a
// line break — and wrapping BEFORE styling puts the opening marker on one line
// and the closing marker on the next, leaving both raw. A real description hits
// this constantly.
func TestEmphasisSurvivesAWrap(t *testing.T) {
	body := "_What an operator has to do differently: nothing, unless a migration " +
		"fails — the deploy then stops before any instance is replaced._"
	out := strings.Join(reviewBodyLines(body, 60, 20), "\n")
	if strings.Contains(out, "_What") || strings.Contains(out, "replaced._") {
		t.Errorf("emphasis markers survived the wrap:\n%s", out)
	}
	if !strings.Contains(out, "operator") || !strings.Contains(out, "replaced") {
		t.Error("wrapping lost the text")
	}
}

// Emphasis around a whole sentence routinely contains bold or code. Emitting
// the span's inner text verbatim left THOSE markers on screen.
func TestNestedInlineMarkupLeavesNoMarkers(t *testing.T) {
	for _, in := range []string{
		"_a **bold** word_",
		"**a `code` span**",
		"_leading `code` here_",
	} {
		got := renderInlineMarkdown(in)
		for _, marker := range []string{"**", "`"} {
			if strings.Contains(got, marker) {
				t.Errorf("%q kept %q: %q", in, marker, got)
			}
		}
	}
}

// A table cell carries inline markdown too — `git push` read as literal
// backticks until the cell renderer ran it as well.
func TestTableCellsRenderInlineMarkdown(t *testing.T) {
	body := "| Where |\n| --- |\n| `git push` to master |\n"
	out := strings.Join(reviewBodyLines(body, 60, 20), "\n")
	if strings.Contains(out, "`") {
		t.Errorf("backticks survived inside a table cell:\n%s", out)
	}
	if !strings.Contains(out, "git push") {
		t.Error("the cell lost its text")
	}
}
