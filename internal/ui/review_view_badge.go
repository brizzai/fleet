package ui

import (
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/selfreview"
)

// ---------------------------------------------------------------------------
// Main-screen hooks: the review row's state and the preview's verdict line.
// Both take a small value the wiring fills; the zero value renders nothing, so
// a checkout without a review looks exactly as it did before.

// ReviewGlyph marks a review. U+270E LOWER RIGHT PENCIL: Dingbats,
// East-Asian-Neutral (one column in go-runewidth and x/ansi), and present in
// Menlo — checked against the font's cmap, the check that rejected U+23FE and
// U+2B21 elsewhere.
const ReviewGlyph = "✎"

// Review row states, as the sidebar shows them.
const (
	ReviewBadgePreparing = "preparing"
	ReviewBadgeReady     = "ready"
	ReviewBadgeFailed    = "failed"
	ReviewBadgeStale     = "stale"
	ReviewBadgeSent      = "sent"
	ReviewBadgeApproved  = "approved"
)

// reviewBadge is a checkout's review state. Zero value: no review.
type reviewBadge struct {
	Status string // one of the ReviewBadge* constants, or "" for none
	// Counts is selfreview.CountsOf the run and its answers — the one
	// definition the CLI and the Review view read too.
	Counts  selfreview.Counts
	Seen    bool // the review view has been opened since the run finished
	Round   int
	Model   string
	Elapsed time.Duration
	Verdict string // the run's one-liner, when ready
	Reason  string // why it failed
	// Working: the answers were sent and the agent is working on them, so
	// the next round would review half-done work.
	Working bool
}

// reviewPreview is what the preview pane says about a session's review.
type reviewPreview struct {
	Status  string // a ReviewBadge* constant, or "" for none
	Verdict string // the run's one-liner, when ready
	Reason  string // why it failed
}

// renderReviewPreviewLine is the dim line under the preview's prompt strip:
// `✎ <verdict>  v review`, `✎ preparing review…`, or `✎ review failed: …`.
// Empty for the zero value.
func renderReviewPreviewLine(p reviewPreview, width int) string {
	if width <= 0 {
		return ""
	}
	// One column of right margin, so the hint doesn't butt into the panel's
	// border the way nothing else in the preview does.
	if width > 20 {
		width--
	}
	var text, hint string
	switch p.Status {
	case ReviewBadgePreparing:
		text = ReviewGlyph + " preparing review…"
	case ReviewBadgeFailed:
		text = ReviewGlyph + " review failed"
		if p.Reason != "" {
			text += ": " + p.Reason
		}
	case ReviewBadgeReady, ReviewBadgeStale, ReviewBadgeSent:
		if p.Verdict == "" {
			return ""
		}
		text = ReviewGlyph + " " + p.Verdict
		hint = "v review"
	default:
		return ""
	}
	text = "  " + text
	if hint != "" {
		hint = "  " + hint
		if lipgloss.Width(text)+lipgloss.Width(hint) > width {
			text = ansi.Truncate(text, max(width-lipgloss.Width(hint), 1), "…")
		}
	}
	line := text + hint
	if lipgloss.Width(line) > width {
		line = ansi.Truncate(line, width, "…")
	}
	return DimStyle.Render(line)
}
