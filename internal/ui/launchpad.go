package ui

import (
	"fmt"
	"strings"

	"github.com/brizzai/fleet/internal/discovery"
	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/lipgloss"
)

// Launchpad is the first-run / empty-fleet experience. Instead of an empty
// sidebar that asks the user to type a path from memory, it lists the repos
// they've recently used in Claude Code and lets them resume the exact
// conversation they left — "continue, but better": now inside fleet's
// parallel switchboard.
//
// It only renders when the fleet has no sessions and no pinned repos. The
// moment a session exists, the normal sidebar takes over and the launchpad
// never shows again.
type Launchpad struct {
	items    []discovery.Recent
	cursor   int
	selected map[int]bool // multi-select: item indices checked for launch
	loading  bool
}

// NewLaunchpad returns a launchpad in its pre-scan loading state.
func NewLaunchpad() *Launchpad {
	return &Launchpad{loading: true, selected: make(map[int]bool)}
}

// SetItems installs the discovery results and leaves the loading state. Items
// are clustered by origin (preserving recency order) so the render groups
// worktrees of one repo under a single origin header — and the cursor index
// lines up with the rendered order.
func (l *Launchpad) SetItems(items []discovery.Recent) {
	l.items = groupByOrigin(items)
	// Pre-check everything: onboarding's goal is to add the whole working set,
	// so the default is "add all" and the user just unchecks what they don't want.
	l.selected = make(map[int]bool, len(l.items))
	for i := range l.items {
		l.selected[i] = true
	}
	l.loading = false
	if l.cursor >= len(l.items) {
		l.cursor = 0
	}
}

// groupByOrigin clusters items that share an OriginKey, keeping each origin at
// the position of its most-recent member (input is already recency-sorted).
func groupByOrigin(items []discovery.Recent) []discovery.Recent {
	var order []string
	groups := make(map[string][]discovery.Recent)
	for _, it := range items {
		if _, ok := groups[it.OriginKey]; !ok {
			order = append(order, it.OriginKey)
		}
		groups[it.OriginKey] = append(groups[it.OriginKey], it)
	}
	out := make([]discovery.Recent, 0, len(items))
	for _, k := range order {
		out = append(out, groups[k]...)
	}
	return out
}

// HasItems reports whether discovery surfaced at least one repo.
func (l *Launchpad) HasItems() bool { return len(l.items) > 0 }

// Move walks the cursor by delta, clamped to the list bounds.
func (l *Launchpad) Move(delta int) {
	if len(l.items) == 0 {
		return
	}
	l.cursor += delta
	if l.cursor < 0 {
		l.cursor = 0
	}
	if l.cursor >= len(l.items) {
		l.cursor = len(l.items) - 1
	}
}

// Toggle flips the multi-select checkbox on the item under the cursor.
func (l *Launchpad) Toggle() {
	if l.cursor < 0 || l.cursor >= len(l.items) {
		return
	}
	if l.selected[l.cursor] {
		delete(l.selected, l.cursor)
	} else {
		l.selected[l.cursor] = true
	}
}

// ToggleAll selects every item, or clears the selection if all are already on.
func (l *Launchpad) ToggleAll() {
	if len(l.selected) == len(l.items) {
		l.selected = make(map[int]bool)
		return
	}
	l.selected = make(map[int]bool, len(l.items))
	for i := range l.items {
		l.selected[i] = true
	}
}

// SelectedCount reports how many items are checked.
func (l *Launchpad) SelectedCount() int { return len(l.selected) }

// LaunchSet returns the items to start: every checked item, or — when nothing
// is checked — just the row under the cursor. Empty only when the list is.
func (l *Launchpad) LaunchSet() []discovery.Recent {
	if len(l.selected) > 0 {
		out := make([]discovery.Recent, 0, len(l.selected))
		for i, it := range l.items {
			if l.selected[i] {
				out = append(out, it)
			}
		}
		return out
	}
	if l.cursor >= 0 && l.cursor < len(l.items) {
		return []discovery.Recent{l.items[l.cursor]}
	}
	return nil
}

// View renders the launchpad centered in the viewport.
func (l *Launchpad) View(width, height int) string {
	cw := width - 8
	if cw > 60 {
		cw = 60
	}
	if cw < 38 {
		cw = 38
	}
	// DialogStyle adds a 1×2 padding, so the usable text column is cw-4.
	rowW := cw - 4

	var b strings.Builder
	b.WriteString(TitleStyle.Render("⬡  Welcome to fleet"))
	b.WriteString("\n")

	if l.loading {
		b.WriteString(DimStyle.Render("Scanning your Claude Code history…"))
		return place(width, height, DialogStyle.Width(cw).Render(b.String()))
	}

	b.WriteString(DimStyle.Render("Add the repos & worktrees you regularly work in."))
	b.WriteString("\n\n")

	// Window the list to the available height, keeping the cursor in view.
	// Items take 2 rows each (branch + title); origin headers and group
	// separators add a little more, so budget conservatively at 3 rows/item.
	budget := height - 9
	winSize := budget / 3
	if winSize < 1 {
		winSize = 1
	}
	// Reserve a row for the "+N more" note when the list doesn't fully fit.
	if len(l.items) > winSize {
		winSize--
		if winSize < 1 {
			winSize = 1
		}
	}
	// Scroll so the cursor sits inside [start, start+winSize).
	start := 0
	if l.cursor >= winSize {
		start = l.cursor - winSize + 1
	}
	if start > len(l.items)-winSize {
		start = len(l.items) - winSize
	}
	if start < 0 {
		start = 0
	}
	end := start + winSize
	if end > len(l.items) {
		end = len(l.items)
	}
	shown := l.items[start:end]
	hidden := len(l.items) - len(shown)

	originCounts := make(map[string]int)
	for _, it := range shown {
		originCounts[it.OriginKey]++
	}

	prevOrigin := ""
	for i, it := range shown {
		idx := start + i // absolute index for cursor + selection lookup
		if it.OriginKey != prevOrigin {
			if i > 0 {
				b.WriteString("\n") // blank row before a new origin group
			}
			b.WriteString(renderLaunchpadOrigin(it.OriginKey, originCounts[it.OriginKey]))
			b.WriteString("\n")
			prevOrigin = it.OriginKey
		}
		b.WriteString(l.renderItem(it, idx == l.cursor, l.selected[idx], rowW))
		b.WriteString("\n")
	}
	if hidden > 0 {
		b.WriteString("\n")
		b.WriteString(DimStyle.Render(fmt.Sprintf("   +%d more in your history", hidden)))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(l.footer(rowW))

	return place(width, height, DialogStyle.Width(cw).Render(b.String()))
}

// renderLaunchpadOrigin draws a group header in fleet's sidebar idiom:
// "▾ <origin> <count>" — accent label, dim chevron + count.
func renderLaunchpadOrigin(originKey string, count int) string {
	chevron := DimStyle.Render("▾")
	name := RepoHeaderStyle.Render(labelForOrigin(originKey))
	return fmt.Sprintf("%s %s %s", chevron, name, DimStyle.Render(fmt.Sprintf("%d", count)))
}

// renderItem draws one resumable checkout as two rows on a fixed grid so every
// column lines up: "  <cursor> <box> <branch> … <time>" then an idle session
// row "│ · <prompt>". cursor at col 2, checkbox col 4, branch col 6.
func (l *Launchpad) renderItem(it discovery.Recent, isCursor, isChecked bool, w int) string {
	cursorMark := " "
	if isCursor {
		cursorMark = SessionSelectionPrefix.Render("❯")
	}
	box := DimStyle.Render("○")
	if isChecked {
		box = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("◉")
	}

	branch := shortBranch(it.Branch)
	bs := BranchStyle.Italic(it.IsWorktree)
	if isCursor {
		bs = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Italic(it.IsWorktree)
	}

	ago := formatTimeAgo(it.LastUsed)
	left := "  " + cursorMark + " " + box + " " + bs.Render(branch)
	gap := w - lipgloss.Width(left) - len(ago)
	if gap < 1 {
		gap = 1
	}
	line1 := left + strings.Repeat(" ", gap) + DimStyle.Render(ago)

	// Idle session row — the prompt, drawn like the sidebar's not-running row.
	title := truncate(it.Title, w-8)
	line2 := "    " + DimStyle.Render("│") + " " + StatusSymbol(session.StatusIdle) + " " + DimStyle.Render(title)
	return line1 + "\n" + line2
}

// shortBranch trims a branch to its last path segment and caps its width,
// matching how the sidebar renders checkout branches.
func shortBranch(branch string) string {
	if branch == "" {
		return "(detached)"
	}
	if i := strings.LastIndex(branch, "/"); i != -1 {
		branch = branch[i+1:]
	}
	if len(branch) > 24 {
		branch = branch[:23] + "…"
	}
	return branch
}

// footer renders a prominent "⏎ continue" call-to-action button over a row of
// dim secondary hints, both centered in the panel.
func (l *Launchpad) footer(w int) string {
	n := l.SelectedCount()
	cta := "Continue"
	if n > 0 {
		cta = fmt.Sprintf("Add %d & continue", n)
	}
	button := SessionTitleSelStyle.Render("  ⏎  " + cta + "  ")

	key := func(k string) string { return HelpKeyStyle.Render(k) }
	dim := func(s string) string { return DimStyle.Render(s) }
	all := "all"
	if n > 0 && n == len(l.items) {
		all = "none"
	}
	hints := key("space") + dim(" toggle   ") +
		key("A") + dim(" "+all+"   ") +
		key("n") + dim(" path   ") +
		key("?") + dim(" help")

	return centerWithin(button, w) + "\n\n" + centerWithin(hints, w)
}

// place centers a rendered box in the viewport.
func place(width, height int, box string) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}
