package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The review row: a checkout's self-review as a first-class sidebar row, the
// first child under its checkout header. A review belongs to the checkout —
// several sessions can work one worktree, and the change under review is the
// tree's — so it is not a suffix on any one session's row.
//
//	▾ feat-x  #412
//	    ✎ Review · 4 to decide
//	    ● ✻ api
//	    ○ ✻ api tests
//
// Shown while a run exists or is preparing; an approved review has nothing
// left to say, so its row disappears (approving is how you clear it). Bold
// while it needs you (ready, decisions waiting, not opened yet), dim after.
// Never coloured: the status dot owns status colour, and this row has none.
// Its count is selfreview.Counts.ToDecide — the same words the CLI and the
// Review view's header print.

// reviewRowShown reports whether a checkout's review gets a row.
func reviewRowShown(b reviewBadge) bool {
	return b.Status != "" && b.Status != ReviewBadgeApproved
}

// reviewNeedsYou is the review row's "needs you" class — the one Space
// rotates through alongside waiting sessions: a ready run with decisions the
// human hasn't opened yet. Opening it (MarkSeen) is what takes it out.
func reviewNeedsYou(b reviewBadge) bool {
	return (b.Status == ReviewBadgeReady || b.Status == ReviewBadgeStale) && !b.Seen && b.Counts.Unanswered > 0
}

// reviewRowState is the row's text after "Review · ", in full and in the
// short form a narrow sidebar takes:
//
//	preparing…
//	4 to decide
//	ready to send             (every decision answered, something to change)
//	nothing to change         (every decision answered ok or later)
//	2 files not reviewed      (no decisions, but files nobody read)
//	nothing needs you
//	⚠ code changed            (the tree moved since the review)
//	sent · agent working      (until the agent finishes)
//	press v for round 2
//	failed
func reviewRowState(b reviewBadge) (full, short string) {
	c := b.Counts
	switch b.Status {
	case ReviewBadgePreparing:
		return "preparing…", "preparing…"
	case ReviewBadgeFailed:
		return "failed", "failed"
	case ReviewBadgeSent:
		if b.Working {
			return "sent · agent working", "sent · working"
		}
		return "press v for round 2", "v for round 2"
	case ReviewBadgeStale:
		return "⚠ code changed", "⚠ changed"
	case ReviewBadgeReady:
		switch {
		case c.Unanswered > 0:
			t := c.ToDecide()
			return t, t
		case c.Decisions > 0 && c.Acting() > 0:
			return "ready to send", "ready to send"
		case c.Decisions > 0:
			return "nothing to change", "all ok"
		case c.NotReviewed > 0:
			return c.NotReviewedLabel(), fmt.Sprintf("%d not reviewed", c.NotReviewed)
		}
		return "nothing needs you", "all clear"
	}
	return "", ""
}

// reviewRowText is the row's text after the glyph: "Review · 4 to decide".
func reviewRowText(b reviewBadge) string {
	full, _ := reviewRowState(b)
	if full == "" {
		return ""
	}
	return "Review · " + full
}

// renderReviewRow renders a review row at the session rows' indent.
func renderReviewRow(item SidebarItem, width int, selected bool) string {
	// The row costs its text plus 6 cells ("    ✎ "), 7 selected ("   " +
	// " ✎ " … " "). When that doesn't fit it takes the short form, then
	// drops "Review", rather than cut a word in half.
	maxLen := max(width-6, 10)
	if selected {
		maxLen = max(width-7, 10)
	}
	full, short := reviewRowState(item.Review)
	text := "Review · " + full
	if ansi.StringWidth(text) > maxLen {
		text = "Review · " + short
	}
	if ansi.StringWidth(text) > maxLen {
		text = short
	}
	text = rvClip(text, maxLen)
	if selected {
		return "   " + selTitle().Render(" "+ReviewGlyph+" "+text+" ")
	}
	style := DimStyle
	if reviewNeedsYou(item.Review) && !item.Snooze.Muted {
		style = reviewBoldStyle()
	}
	return "    " + style.Render(ReviewGlyph+" "+text)
}

// insertReviewRows puts each checkout's review row first under its header,
// above the sessions, where the checkout is expanded and badgeOf says it has
// one. A collapsed checkout hides it like any child; a filtered-out checkout
// has no header to hang it on. snoozeOf stamps the checkout's snooze, so a
// snoozed checkout's review row is muted (dim, out of Space) like its sessions.
func insertReviewRows(items []SidebarItem, badgeOf func(repo string) (reviewBadge, bool),
	snoozeOf func(origin, repo string) snoozeResult) []SidebarItem {
	if badgeOf == nil {
		return items
	}
	var out []SidebarItem
	for i, it := range items {
		if out != nil {
			out = append(out, it)
		}
		if !it.IsCheckoutHeader || !it.Expanded {
			continue
		}
		b, ok := badgeOf(it.RepoPath)
		if !ok {
			continue
		}
		if out == nil {
			out = make([]SidebarItem, 0, len(items)+4)
			out = append(out, items[:i+1]...)
		}
		row := SidebarItem{IsReview: true, OriginKey: it.OriginKey, RepoPath: it.RepoPath, Review: b}
		if snoozeOf != nil {
			row.Snooze = snoozeOf(it.OriginKey, it.RepoPath)
		}
		out = append(out, row)
	}
	if out == nil {
		return items
	}
	return out
}

// reviewRowKeys is the set of checkouts with a review row in items — compared
// across rebuilds to tell when rows came or went under the cursor.
func reviewRowKeys(items []SidebarItem) string {
	var b strings.Builder
	for _, it := range items {
		if it.IsReview {
			b.WriteString(it.RepoPath)
			b.WriteByte(0)
		}
	}
	return b.String()
}

// renderReviewRowPreview is the preview pane for a review row: what the run
// says and what the keys do, in place of a session's pane.
func renderReviewRowPreview(label string, b reviewBadge, sessions int, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	wrap := func(s string) []string {
		if s == "" {
			return nil
		}
		return rvWrap(s, max(width-4, 10))
	}
	var lines []string
	add := func(style lipgloss.Style, s string) {
		for _, l := range wrap(s) {
			lines = append(lines, "  "+style.Render(strings.TrimRight(l, " ")))
		}
	}
	blank := func() { lines = append(lines, "") }
	text := lipgloss.NewStyle().Foreground(ColorText)

	// The panel title already names the review and its checkout.
	_ = label
	blank()
	c := b.Counts
	switch b.Status {
	case ReviewBadgePreparing:
		add(text, "A reviewer is reading the changes in this checkout. It usually takes a minute or two.")
	case ReviewBadgeFailed:
		add(ErrorStyle, "Review failed. "+reviewFailText(b.Reason))
	default:
		if b.Verdict != "" {
			add(reviewBoldStyle(), b.Verdict)
			blank()
		}
		switch {
		case b.Status == ReviewBadgeSent && b.Working:
			add(text, "Your answers were sent. The agent is working on them.")
			add(DimStyle, "When it finishes, press v to review its changes (round 2).")
		case b.Status == ReviewBadgeSent:
			add(text, "Your answers were sent and the agent has stopped.")
			add(DimStyle, "Press v to review its changes (round 2).")
		case c.Unanswered > 0:
			add(text, capFirst(c.ToDecide())+" · "+c.LinesNeedYou()+".")
			add(DimStyle, c.SafeToSkipLabel()+".")
		case c.Decisions > 0:
			add(text, "Every decision is answered. Open it to send your answers or approve.")
		case c.NotReviewed > 0:
			add(text, "No decisions, but "+c.NotReviewedLabel()+". Open it to read them.")
		default:
			add(text, "Nothing needs you. Open it to look, then approve.")
		}
		if b.Status == ReviewBadgeStale {
			blank()
			add(reviewWarnStyle(), "⚠ The code changed after this review. Open it and press r to refresh.")
		}
	}
	blank()
	keys := []string{HelpKeyStyle.Render("⏎") + " " + HelpDescStyle.Render("open")}
	if b.Status == ReviewBadgeFailed {
		keys = []string{HelpKeyStyle.Render("v") + " " + HelpDescStyle.Render("try again")}
	}
	if b.Status == ReviewBadgeSent && !b.Working {
		keys = append(keys, HelpKeyStyle.Render("v")+" "+HelpDescStyle.Render("round 2"))
	}
	keys = append(keys, HelpKeyStyle.Render(".")+" "+HelpDescStyle.Render("more"),
		HelpKeyStyle.Render("d")+" "+HelpDescStyle.Render("discard"))
	lines = append(lines, "  "+strings.Join(keys, "   "))
	if meta := strings.TrimSpace(b.Model + " " + rvElapsed(b.Elapsed)); meta != "" && b.Status != ReviewBadgePreparing && b.Status != ReviewBadgeFailed {
		blank()
		add(DimStyle, "reviewed by "+meta+" · "+rvPlural(sessions, "session", "sessions")+" in this checkout")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}
