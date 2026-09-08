package ui

import (
	"fmt"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/github"
)

// renderReviewPreview draws a pull request the way its own page opens: what it
// is, who wants what from you, and then the author's own description.
//
// Every line of it comes from the review queue, which is one GraphQL call fleet
// has already made — so this paints the instant the row exists. The changed-file
// list used to live here and is a SECOND fetch, seconds long, which is what put
// "Loading files for #N…" in front of a question the queue could already answer.
// That list moved into the reader, where the tree and the tour are the things
// that answer "where do I start".
func renderReviewPreview(r github.ReviewRequest, width, height int) string {
	inner := max(width-4, 20)
	var out []string
	put := func(s string) { out = append(out, "  "+s) }

	// No title row. The panel's own bar already reads
	// "Preview · #5266 [BRZ-3641] Run migrations… · finished", so repeating it
	// here was two rows of stutter before a single fact.
	for _, l := range reviewHeaderLines(r, inner) {
		put(l)
	}
	put(lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("─", inner)))

	// The description, which is the whole reason a pull request page opens on
	// Conversation: it is the author telling you what they were trying to do,
	// and no amount of reading the diff recovers it.
	body := reviewBodyLines(r.Body, inner, max(height-len(out)-1, 1))
	if len(body) == 0 {
		put(DimStyle.Render("No description."))
	}
	for _, l := range body {
		put(l)
	}
	return strings.Join(out, "\n")
}

// reviewHeaderLines is everything above the description: what the change is,
// and what it is waiting on.
//
// Two columns where the pane allows it. The facts that describe the change read
// left to right; the things you are waiting on sit right, where the eye finds
// them without reading the line — and on a review queue that second group is
// the one you scan. They stack when the width cannot hold both, so a narrow
// pane loses the layout rather than the content.
func reviewHeaderLines(r github.ReviewRequest, width int) []string {
	left, right := reviewFactsLine(r), reviewStateLine(r)

	var out []string
	switch {
	case right == "":
		out = append(out, ansi.Truncate(left, width, "…"))
	case lipgloss.Width(left)+lipgloss.Width(right)+4 <= width:
		gap := width - lipgloss.Width(left) - lipgloss.Width(right)
		out = append(out, left+strings.Repeat(" ", gap)+right)
	default:
		out = append(out, ansi.Truncate(left, width, "…"), ansi.Truncate(right, width, "…"))
	}
	if line := reviewBranchLine(r, width); line != "" {
		out = append(out, line)
	}
	if labels := reviewLabelRow(r.Labels, width); labels != "" {
		out = append(out, labels)
	}
	return out
}

// reviewFactsLine is the change itself: its state, who wrote it, how old it is
// and how big.
//
// The insertion and deletion counts are coloured because they are the one pair
// of numbers everybody scans, and because a bare "+946 −39" in body text reads
// as prose rather than as a measurement.
func reviewFactsLine(r github.ReviewRequest) string {
	dot := DimStyle.Render(" · ")
	parts := []string{
		reviewStatePill(r),
		lipgloss.NewStyle().Foreground(ColorText).Render(r.Author),
	}
	if age := reviewAge(r.UpdatedAt); age != "" {
		parts = append(parts, DimStyle.Render(age))
	}
	if r.ChangedFiles > 0 {
		parts = append(parts, DimStyle.Render(plural(r.ChangedFiles, "file")))
	}
	parts = append(parts,
		PROpenStyle.Render(fmt.Sprintf("+%d", r.Additions))+" "+
			PRFailStyle.Render(fmt.Sprintf("−%d", r.Deletions)))
	return strings.Join(parts, dot)
}

// reviewBranchLine is where the change lands and what it lands from.
//
// Worth its own row because the base is occasionally news: "into master" is
// true of nearly every pull request, and "into aviv/brz-3639-collapse-migrations"
// means this one is stacked on another and merges after it. Rendered in the
// body colour rather than the gutter's, which was too quiet to read.
func reviewBranchLine(r github.ReviewRequest, width int) string {
	if r.BaseRef == "" || r.HeadRef == "" {
		return ""
	}
	ref := lipgloss.NewStyle().Foreground(ColorText)
	// The head is cut from the LEFT, like every other name in this app: the
	// tail of a branch is what distinguishes it from its siblings.
	budget := max(width-lipgloss.Width(r.BaseRef)-3, 10)
	return ref.Render(r.BaseRef) + DimStyle.Render(" ← ") +
		ref.Render(truncFront(r.HeadRef, budget))
}

// reviewStatePill is open/draft, in the sidebar's own vocabulary — the same
// glyph means the same thing here as on the row you came from.
//
// A draft carries no fill. The design system spends a background on the cursor
// and the caret, and a state indicator that fills competes with both for the
// scarcest signal on the screen; weight and colour carry it instead.
func reviewStatePill(r github.ReviewRequest) string {
	if r.IsDraft {
		return lipgloss.NewStyle().Foreground(ColorTextDim).Bold(true).Render("◌ Draft")
	}
	return lipgloss.NewStyle().Foreground(ColorGreen).Bold(true).Render("◉ Open")
}

// reviewStateLine is what is being asked of you and whether the change is green.
//
// Checks are reported as GitHub's rollup rather than a bare tick, and an absent
// rollup says "no checks" rather than nothing: a pull request with no CI at all
// must not read the same as one whose CI passed.
func reviewStateLine(r github.ReviewRequest) string {
	var parts []string
	switch r.Decision {
	case "APPROVED":
		parts = append(parts, PROpenStyle.Render("✓ approved"))
	case "CHANGES_REQUESTED":
		parts = append(parts, PRFailStyle.Render("↩ changes requested"))
	case "REVIEW_REQUIRED":
		parts = append(parts, StatusWaitingStyle.Render("◐ review required"))
	}
	switch {
	case r.ChecksState == "SUCCESS":
		parts = append(parts, PROpenStyle.Render("✓ "+plural(r.ChecksTotal, "check")))
	case r.ChecksState == "FAILURE" || r.ChecksState == "ERROR":
		parts = append(parts, PRFailStyle.Render("✕ "+plural(r.ChecksTotal, "check")+" failing"))
	case r.ChecksState == "PENDING":
		parts = append(parts, StatusWaitingStyle.Render("◐ "+plural(r.ChecksTotal, "check")+" running"))
	case r.ChecksState != "":
		parts = append(parts, DimStyle.Render(r.ChecksState))
	}
	if r.Comments > 0 {
		parts = append(parts, DimStyle.Render(plural(r.Comments, "comment")))
	}
	return strings.Join(parts, DimStyle.Render(" · "))
}

// reviewLabelRow renders labels as a row of muted chips, dropping the ones that
// do not fit rather than wrapping into a second row — a label list is context,
// and context does not get to push the description off the panel.
func reviewLabelRow(labels []string, width int) string {
	if len(labels) == 0 {
		return ""
	}
	var out string
	for _, l := range labels {
		chip := ReviewLabelStyle.Render(" " + l + " ")
		if lipgloss.Width(out)+lipgloss.Width(chip)+1 > width {
			break
		}
		if out != "" {
			out += " "
		}
		out += chip
	}
	return out
}

// htmlComment strips the invisible half of a pull request template.
//
// A repo with a template hands every description a block of instructions to the
// author wrapped in <!-- -->, which GitHub hides and a naive renderer shows —
// so without this the first screen of most descriptions is a form nobody filled
// in.
var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
