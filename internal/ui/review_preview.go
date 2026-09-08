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

	for _, l := range wrapTo(inner, r.Title) {
		put(TitleStyle.Render(l))
	}
	put("")

	// State and where it lands. The branch pair is the one fact that says what
	// this change is against, and it is missing from every other fleet surface.
	head := reviewStatePill(r)
	if r.BaseRef != "" && r.HeadRef != "" {
		head += "   " + DiffNumStyle.Render(r.BaseRef) + DimStyle.Render(" ← ") +
			DiffNumStyle.Render(truncFront(r.HeadRef, max(inner-len(r.BaseRef)-16, 12)))
	}
	put(ansi.Truncate(head, inner, "…"))

	put(DimStyle.Render(ansi.Truncate(reviewSizeLine(r), inner, "…")))
	if line := reviewStateLine(r); line != "" {
		put(ansi.Truncate(line, inner, "…"))
	}
	if labels := reviewLabelRow(r.Labels, inner); labels != "" {
		put(labels)
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

// reviewStatePill is open/draft, in the sidebar's own vocabulary — the same
// glyph means the same thing here as on the row you came from.
func reviewStatePill(r github.ReviewRequest) string {
	if r.IsDraft {
		return SelectionPill(false).Render(" ◌ Draft ")
	}
	return PROpenStyle.Render("◉ Open")
}

// reviewSizeLine is who, when, and how big.
func reviewSizeLine(r github.ReviewRequest) string {
	parts := []string{r.Author}
	if age := reviewAge(r.UpdatedAt); age != "" {
		parts = append(parts, age)
	}
	if r.ChangedFiles > 0 {
		parts = append(parts, plural(r.ChangedFiles, "file"))
	}
	parts = append(parts, fmt.Sprintf("+%d −%d", r.Additions, r.Deletions))
	return strings.Join(parts, " · ")
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
		chip := SelectionPill(false).Render(" " + l + " ")
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

// reviewBodyLines renders the description: light markdown, wrapped, capped.
//
// Hand-rolled rather than a markdown library, matching the release-notes
// dialog: the subset that actually appears in a pull request body is headings,
// bullets and inline code, and a dependency that renders all of CommonMark into
// a preview pane is a dependency for the parts nobody writes here.
func reviewBodyLines(body string, width, budget int) []string {
	body = htmlComment.ReplaceAllString(body, "")
	body = strings.ReplaceAll(body, "\r\n", "\n")

	var out []string
	blank := false
	for _, raw := range strings.Split(body, "\n") {
		if len(out) >= budget {
			break
		}
		line := strings.TrimRight(raw, " \t")
		if strings.TrimSpace(line) == "" {
			// One blank line between paragraphs, never the three a template
			// leaves behind — vertical space is the scarcest thing here.
			if !blank && len(out) > 0 {
				out = append(out, "")
				blank = true
			}
			continue
		}
		blank = false

		if h := strings.TrimLeft(line, "#"); h != line {
			out = append(out, TitleStyle.Render(ansi.Truncate(strings.TrimSpace(h), width, "…")))
			continue
		}
		// Wrapped BEFORE styling: renderInlineMarkdown returns fully styled
		// text, and wrapping that counts the ANSI bytes as columns.
		for _, w := range wrapTo(width, line) {
			if len(out) >= budget {
				break
			}
			out = append(out, renderInlineMarkdown(w))
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}
