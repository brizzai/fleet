package ui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sahilm/fuzzy"

	"github.com/brizzai/fleet/internal/session"
)

// sortRecents orders the recents slice by the rank map (lower rank = more recent).
func sortRecents(recents []scoredItem, rank map[string]int) {
	sort.SliceStable(recents, func(i, j int) bool {
		return rank[recents[i].ID] < rank[recents[j].ID]
	})
}

// PaletteItemKind distinguishes commands from places (repos/worktrees) in the palette.
type PaletteItemKind int

const (
	PaletteKindCommand PaletteItemKind = iota
	PaletteKindRepo
	PaletteKindWorktree
	PaletteKindTicket
)

// commandPaletteMsg is sent when the user selects an item from the palette.
type commandPaletteMsg struct {
	kind PaletteItemKind
	id   string // command ID for commands, repo path for repos/worktrees
}

// PaletteItem represents a single row in the palette — either a command or a place.
type PaletteItem struct {
	Kind     PaletteItemKind
	ID       string // command ID, or repo path for places
	Name     string // display name
	Detail   string // dim right-side detail (branch for places, empty for commands)
	Shortcut string // right-aligned keybinding hint (commands only)
	Haystack string // string used for fuzzy matching

	// Group is a section header this row sits under when nothing is typed
	// (ticket rows only — their Linear state). Empty means no section.
	Group string

	// Priority is the row's Linear priority (1 urgent … 4 low, 0 unset). It is
	// rendered in a column of its own rather than baked into Name for two
	// reasons: a column can be coloured, and a column can be omitted. In the
	// mixed tab it IS omitted, because a lead column no other kind has would
	// push every ticket title out of line with every command and worktree.
	Priority int

	// SessionStatus is the status of the fleet session already working this
	// row, and HasSession says whether there is one at all. They are separate
	// because a zero Status is a real status, and "no session" has to be
	// distinguishable from it — that distinction is the whole point of the
	// badge column for tickets: is this in fleet, or not.
	SessionStatus session.Status
	HasSession    bool
}

// PaletteTab restricts which kinds of items show in the palette.
type PaletteTab int

const (
	PaletteTabAll PaletteTab = iota
	PaletteTabActions
	PaletteTabPlaces
	PaletteTabTickets
)

var paletteTabOrder = []struct {
	Tab   PaletteTab
	Label string
}{
	{PaletteTabAll, "all"},
	{PaletteTabActions, "actions"},
	{PaletteTabPlaces, "repos/worktrees"},
	{PaletteTabTickets, "tickets"},
}

// CommandPaletteDialog shows a fuzzy-filterable list of palette items.
type CommandPaletteDialog struct {
	visible       bool
	width, height int
	items         []PaletteItem // full list
	recent        []string      // recent item IDs (most recent first)
	filtered      []scoredItem  // after fuzzy filter + tab
	cursor        int
	scrollOff     int
	activeTab     PaletteTab
	filterInput   textinput.Model
	sectionCounts map[string]int

	// ticketsLoaded distinguishes an empty ticket tab that is still fetching
	// from one that genuinely has nothing — the difference between "wait" and
	// "you're done", which an empty list alone cannot say.
	ticketsLoaded bool
}

type scoredItem struct {
	PaletteItem
	score          int
	matchedIndexes []int  // rune positions in Haystack — used to highlight matched chars in Name
	recent         bool   // true when this row is sitting in the "recent" section
	section        string // group header this row falls under; "" for none
}

const paletteMaxVisible = 14

// NewCommandPaletteDialog creates a new command palette dialog.
func NewCommandPaletteDialog() *CommandPaletteDialog {
	fi := NewTextInput()
	fi.Placeholder = "search commands, repos, worktrees, tickets..."
	fi.CharLimit = 64
	fi.SetWidth(40)

	return &CommandPaletteDialog{
		filterInput: fi,
	}
}

// Show populates and opens the palette. recent is a list of recently picked
// item IDs (most recent first); rows whose ID appears here float to the top
// when no query is typed.
func (d *CommandPaletteDialog) Show(items []PaletteItem, recent []string) {
	d.visible = true
	d.items = items
	d.recent = recent
	d.ticketsLoaded = false
	d.cursor = 0
	d.scrollOff = 0
	d.activeTab = PaletteTabAll
	d.filterInput.SetValue("")
	d.filterInput.Focus()
	d.rebuildFiltered()
}

// ShowOnTab opens the palette focused on one tab, for a key that means a
// specific thing ("t" is "show me my tickets", not "show me everything").
func (d *CommandPaletteDialog) ShowOnTab(items []PaletteItem, recent []string, tab PaletteTab) {
	d.Show(items, recent)
	d.activeTab = tab
	d.rebuildFiltered()
}

// SetTickets replaces the ticket rows once they arrive.
//
// Tickets are the only palette rows that need a network call, so unlike every
// other kind they cannot be built when the palette opens. Replacing rather than
// appending keeps a second load from doubling the list, and the cursor is
// clamped by rebuildFiltered.
func (d *CommandPaletteDialog) SetTickets(tickets []PaletteItem) {
	if !d.visible {
		return
	}
	kept := d.items[:0:0]
	for _, it := range d.items {
		if it.Kind != PaletteKindTicket {
			kept = append(kept, it)
		}
	}
	d.items = append(kept, tickets...)
	d.ticketsLoaded = true
	d.rebuildFiltered()
}

// TicketsLoaded reports whether a ticket load has completed for this opening,
// so the view can tell "still fetching" from "you have none".
func (d *CommandPaletteDialog) TicketsLoaded() bool { return d.ticketsLoaded }

// itemMatchesTab reports whether an item is included by the active tab.
func itemMatchesTab(it PaletteItem, tab PaletteTab) bool {
	switch tab {
	case PaletteTabActions:
		return it.Kind == PaletteKindCommand
	case PaletteTabPlaces:
		return it.Kind == PaletteKindRepo || it.Kind == PaletteKindWorktree
	case PaletteTabTickets:
		return it.Kind == PaletteKindTicket
	default:
		return true
	}
}

// cycleTab advances to the next/previous tab and rebuilds the filtered list.
func (d *CommandPaletteDialog) cycleTab(delta int) {
	n := len(paletteTabOrder)
	cur := 0
	for i, t := range paletteTabOrder {
		if t.Tab == d.activeTab {
			cur = i
			break
		}
	}
	d.activeTab = paletteTabOrder[((cur+delta)%n+n)%n].Tab
	d.cursor = 0
	d.scrollOff = 0
	d.rebuildFiltered()
}

// Hide closes the palette.
func (d *CommandPaletteDialog) Hide() {
	d.visible = false
	d.filterInput.Blur()
}

// IsVisible returns whether the palette is shown.
func (d *CommandPaletteDialog) IsVisible() bool { return d.visible }

// SetSize updates dimensions.
func (d *CommandPaletteDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

func (d *CommandPaletteDialog) rebuildFiltered() {
	query := strings.TrimSpace(d.filterInput.Value())
	d.filtered = nil

	tabItems := make([]PaletteItem, 0, len(d.items))
	haystacks := make([]string, 0, len(d.items))
	for _, it := range d.items {
		if !itemMatchesTab(it, d.activeTab) {
			continue
		}
		tabItems = append(tabItems, it)
		hay := it.Haystack
		if hay == "" {
			hay = it.Name
		}
		haystacks = append(haystacks, hay)
	}

	if query == "" {
		// Partition into recent (in recent-order) + the rest (in original order).
		recentRank := make(map[string]int, len(d.recent))
		for i, id := range d.recent {
			recentRank[id] = i
		}
		recents := make([]scoredItem, 0, len(d.recent))
		rest := make([]scoredItem, 0, len(tabItems))
		for _, it := range tabItems {
			if _, ok := recentRank[it.ID]; ok {
				recents = append(recents, scoredItem{PaletteItem: it, recent: true})
			} else {
				// Sections only in the tickets tab, and only when nothing is
				// typed. In the mixed tab a state header would sit above a
				// run of commands it does not describe; and grouping a fuzzy
				// result is noise, since matches are already ordered by score
				// and headers would fragment ten rows into six sections.
				sec := ""
				switch {
				case d.activeTab == PaletteTabTickets:
					sec = it.Group
				case it.Kind == PaletteKindTicket:
					// Gated on the KIND, not merely on "not the tickets tab".
					// ticketRightColumn returns the state plus a priority mark,
					// and both are empty for every other kind — so running it
					// unconditionally blanked Detail on repo and worktree rows,
					// which is where their branch name lives. Recent rows took
					// the other branch of this loop and kept theirs, so the same
					// list showed some branches and not others.
					it.Detail = ticketRightColumn(it)
				}
				rest = append(rest, scoredItem{PaletteItem: it, section: sec})
			}
		}
		sortRecents(recents, recentRank)
		d.filtered = append(d.filtered, recents...)
		d.filtered = append(d.filtered, rest...)

		d.sectionCounts = map[string]int{}
		for _, it := range rest {
			if it.section != "" {
				d.sectionCounts[it.section]++
			}
		}
	} else {
		// Filtering drops the headers, so the state has to come back onto the
		// row — otherwise a searched ticket loses the one fact the header was
		// carrying for it.
		d.sectionCounts = nil
		matches := fuzzy.Find(query, haystacks)
		for _, m := range matches {
			it := tabItems[m.Index]
			if it.Group != "" {
				// No headers while filtering, so the state comes back onto the
				// row — with the priority beside it, since the tickets tab's
				// priority column is not rendered here either.
				it.Detail = ticketRightColumn(it)
			}
			d.filtered = append(d.filtered, scoredItem{
				PaletteItem:    it,
				score:          m.Score,
				matchedIndexes: m.MatchedIndexes,
			})
		}
	}

	// Clamp cursor.
	if d.cursor >= len(d.filtered) {
		d.cursor = len(d.filtered) - 1
	}
	if d.cursor < 0 {
		d.cursor = 0
	}
	d.syncScroll()
}

func (d *CommandPaletteDialog) syncScroll() {
	if len(d.filtered) <= paletteMaxVisible {
		d.scrollOff = 0
		return
	}
	if d.cursor < d.scrollOff {
		d.scrollOff = d.cursor
	}
	if d.cursor >= d.scrollOff+paletteMaxVisible {
		d.scrollOff = d.cursor - paletteMaxVisible + 1
	}
}

// Update handles key events.
func (d *CommandPaletteDialog) Update(msg tea.Msg) (*CommandPaletteDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		// Non-key messages (notably tea.PasteMsg for cmd+v) go to the filter input.
		var cmd tea.Cmd
		d.filterInput, cmd = d.filterInput.Update(msg)
		d.rebuildFiltered()
		return d, cmd
	}

	switch keyMsg.String() {
	case "esc", "ctrl+k":
		d.Hide()
		return d, nil
	case "tab":
		d.cycleTab(1)
		return d, nil
	case "shift+tab":
		d.cycleTab(-1)
		return d, nil
	case "up":
		if d.cursor > 0 {
			d.cursor--
			d.syncScroll()
		}
		return d, nil
	case "down":
		if d.cursor < len(d.filtered)-1 {
			d.cursor++
			d.syncScroll()
		}
		return d, nil
	case "enter":
		if d.cursor < 0 || d.cursor >= len(d.filtered) {
			return d, nil
		}
		selected := d.filtered[d.cursor]
		d.Hide()
		return d, func() tea.Msg {
			return commandPaletteMsg{kind: selected.Kind, id: selected.ID}
		}
	}

	// Route all other keys to the text input.
	var cmd tea.Cmd
	d.filterInput, cmd = d.filterInput.Update(msg)
	d.rebuildFiltered()
	return d, cmd
}

// View renders the command palette dialog as a styled box (no fullscreen canvas).
// The caller composites this over the main view via overlay.Composite.
func (d *CommandPaletteDialog) View() string {
	var b strings.Builder

	b.WriteString(TitleStyle.Render("Command Palette"))
	b.WriteString("\n\n")

	// Tab bar.
	b.WriteString(d.renderTabs())
	b.WriteString("\n\n")

	// Search input.
	b.WriteString("  " + d.filterInput.View())
	b.WriteString("\n\n")

	if len(d.filtered) == 0 {
		b.WriteString(DimStyle.Render("  No matches"))
		b.WriteString("\n")
	} else {
		// Scroll indicators.
		if d.scrollOff > 0 {
			b.WriteString(DimStyle.Render(fmt.Sprintf("  ⋮ +%d above", d.scrollOff)))
			b.WriteString("\n")
		}

		end := d.scrollOff + paletteMaxVisible
		if end > len(d.filtered) {
			end = len(d.filtered)
		}

		// Column layout: [prefix 2][badge 4][sep 1][lead L][name N][gap 2][right]
		leadCol := 0
		if d.activeTab == PaletteTabTickets {
			leadCol = paletteLeadWidth
		}
		reserved := 2 + paletteBadgeWidth + 1 + leadCol + 2

		// Measure BOTH columns and give the name whatever the right column
		// genuinely needs left over, rather than capping it at a constant. A
		// fixed cap truncated ticket titles to "Storage opt…" on a wide
		// terminal while the right half sat empty.
		rightCol := 0
		nameCol := 0
		for i := d.scrollOff; i < end; i++ {
			it := d.filtered[i]
			if n := runeLen(it.Name); n > nameCol {
				nameCol = n
			}
			r := runeLen(it.Detail)
			if it.Shortcut != "" {
				r += runeLen(it.Shortcut) + 1
			}
			if r > rightCol {
				rightCol = r
			}
		}
		// Never let the right column take more than half; a single verbose
		// detail must not squeeze every name on screen.
		if half := d.innerContentWidth() / 2; rightCol > half {
			rightCol = half
		}
		if avail := d.innerContentWidth() - reserved - rightCol; nameCol > avail {
			nameCol = avail
		}

		rightBudget := d.innerContentWidth() - reserved - nameCol
		if rightBudget < 0 {
			rightBudget = 0
		}

		prevRecent := false
		prevSection := ""
		if d.scrollOff > 0 {
			// Scrolled into the middle of a section: carry its name forward so
			// the first visible row does not re-print a header it is under.
			prevSection = d.filtered[d.scrollOff-1].section
			prevRecent = d.filtered[d.scrollOff-1].recent
		}
		for i := d.scrollOff; i < end; i++ {
			it := d.filtered[i]
			selected := i == d.cursor

			// Section header: first recent row gets a "recent" label, and the
			// first non-recent row that follows recents gets a blank-line break.
			if it.recent && !prevRecent {
				b.WriteString("  " + DimStyle.Render("recent"))
				b.WriteString("\n")
			} else if !it.recent && prevRecent {
				b.WriteString("\n")
			}
			prevRecent = it.recent

			if it.section != "" && it.section != prevSection {
				if i > d.scrollOff || d.scrollOff == 0 {
					if prevSection != "" {
						b.WriteString("\n") // breathe between groups
					}
				}
				head := it.section
				if n := d.sectionCounts[it.section]; n > 0 {
					head += "  " + fmt.Sprint(n)
				}
				b.WriteString("  " + PaletteSectionStyle.Render(head))
				b.WriteString("\n")
			}
			prevSection = it.section

			prefix := "  "
			if selected {
				prefix = SelectionMarker(true).Render("▸ ")
			}

			// The badge column answers a different question per context, so it
			// is chosen by tab rather than by kind alone. In the tickets tab
			// every row is a ticket, so "tkt" would say nothing and the useful
			// fact is whether the work exists here yet. In the mixed tab a
			// blank reads as a missing badge, not as "not in fleet".
			badge := renderKindBadge(it.Kind)
			if it.Kind == PaletteKindTicket && d.activeTab == PaletteTabTickets {
				badge = renderTicketBadge(it.PaletteItem)
			}

			// Haystack is `Name + " " + Detail` (for places) or just `Name` (commands).
			// Map matched haystack indexes back to the Name and Detail substrings.
			nameRuneLen := runeLen(it.Name)
			detailStart := nameRuneLen + 1
			nameIdx := filterShiftIndexes(it.matchedIndexes, 0, nameRuneLen, 0)
			detailIdx := filterShiftIndexes(it.matchedIndexes, detailStart, detailStart+runeLen(it.Detail), detailStart)

			rawName := truncRunes(it.Name, nameCol)
			namePad := strings.Repeat(" ", nameCol-runeLen(rawName))
			var name string
			if selected {
				name = SelectionBand().Render(rawName + namePad)
			} else {
				name = highlightMatches(rawName, nameIdx) + namePad
			}

			right := it.Shortcut
			highlightRight := false
			if right == "" {
				right = it.Detail
				highlightRight = !selected && len(detailIdx) > 0
			}
			right = truncRunes(right, rightBudget)

			lead := ""
			if leadCol > 0 {
				lead = renderPriorityLead(it.Priority)
			}
			b.WriteString(prefix + badge + " " + lead + name)
			if selected {
				// Carry the fill across the gap and the right column, padded to
				// the row, so the selection is one continuous band.
				b.WriteString(SelectionBandSecondary().Render("  " + pad(right, rightBudget)))
				b.WriteString("\n")
				continue
			}
			b.WriteString("  ")
			if right != "" {
				if highlightRight {
					b.WriteString(highlightMatchesDim(right, detailIdx))
				} else {
					b.WriteString(DimStyle.Render(right))
				}
			}
			b.WriteString("\n")
		}

		below := len(d.filtered) - end
		if below > 0 {
			b.WriteString(DimStyle.Render(fmt.Sprintf("  ⋮ +%d below", below)))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(DimStyle.Render("↑↓ nav  tab: switch tab  enter: select  esc: close"))

	return d.boxed(b.String())
}

// renderTabs renders the tab bar with the active tab highlighted.
func (d *CommandPaletteDialog) renderTabs() string {
	query := strings.TrimSpace(d.filterInput.Value())
	counts := map[PaletteTab]int{}
	for _, tab := range []PaletteTab{PaletteTabAll, PaletteTabActions, PaletteTabPlaces, PaletteTabTickets} {
		haystacks := make([]string, 0, len(d.items))
		for _, it := range d.items {
			if !itemMatchesTab(it, tab) {
				continue
			}
			if query == "" {
				counts[tab]++
				continue
			}
			hay := it.Haystack
			if hay == "" {
				hay = it.Name
			}
			haystacks = append(haystacks, hay)
		}
		if query != "" {
			counts[tab] = len(fuzzy.Find(query, haystacks))
		}
	}

	// The tab bar is a MODE, not focus and not the list cursor: Tab cycles it,
	// typing goes to the input, arrows go to the list. It drew itself as a
	// filled accent chip — the heaviest treatment fleet has, and the same one
	// the selected row uses — which made the least important of the three
	// things lit on screen the loudest, and left no way to tell them apart.
	// ModeOn spends accent and an underline instead of a fill; the fill now
	// belongs to the selected row alone. See docs/design-system.md.
	parts := make([]string, 0, len(paletteTabOrder))
	for _, t := range paletteTabOrder {
		label := fmt.Sprintf("%s %d", t.Label, counts[t.Tab])
		if t.Tab == d.activeTab {
			parts = append(parts, ModeOn().Render(label))
		} else {
			parts = append(parts, ModeOff().Render(label))
		}
	}
	// Three spaces, because the chips' padding used to do the separating.
	return "  " + strings.Join(parts, "   ")
}

const paletteBadgeWidth = 4

// renderKindBadge returns a styled, fixed-width tag that identifies the row's kind.
func renderKindBadge(k PaletteItemKind) string {
	var label string
	var col color.Color
	switch k {
	case PaletteKindRepo:
		label, col = "repo", ColorPurple
	case PaletteKindWorktree:
		label, col = "wkt ", ColorGreen
	case PaletteKindTicket:
		label, col = "tkt ", ColorBlue
	default:
		label, col = "cmd ", ColorTextDim
	}
	return lipgloss.NewStyle().Foreground(col).Render(label)
}

// renderTicketBadge answers "is this already in fleet?" in the badge column.
//
// A ticket row's most useful fact is not that it is a ticket — the whole tab is
// tickets — but whether work on it already exists here, and what that work is
// doing. So the column carries the sidebar's own status vocabulary, and a
// ticket with no worktree is deliberately BLANK rather than dimly marked:
// absence should read as absence at a glance down the column.
func renderTicketBadge(it PaletteItem) string {
	if !it.HasSession {
		return strings.Repeat(" ", paletteBadgeWidth)
	}
	glyph, style := sessionBadgeGlyph(it.SessionStatus)
	return style.Render(pad(glyph, paletteBadgeWidth))
}

// sessionBadgeGlyph maps a session status onto the same dot and colour the
// sidebar uses, so a status means the same thing everywhere in fleet.
func sessionBadgeGlyph(st session.Status) (string, lipgloss.Style) {
	switch st {
	case session.StatusError:
		return "✕", StatusErrorStyle
	case session.StatusWaiting:
		return "◐", StatusWaitingStyle
	case session.StatusRunning, session.StatusStarting:
		return "●", StatusRunningStyle
	case session.StatusFinished:
		return "●", StatusFinishedStyle
	case session.StatusSuspended:
		return "·", StatusSuspendedStyle
	}
	return "○", DimStyle
}

// paletteLeadWidth is the priority column: a three-cell gauge plus its trailing
// separator. Four, not three — the gauge fills every cell it is given, so a
// three-wide column left nothing between it and the identifier and rendered
// "▰▰▰BRZ-1". The old "!!" mark was two glyphs and got its separator for free
// from the padding.
const paletteLeadWidth = 4

// priorityGauge renders a priority as a three-cell bar: filled cells for rank,
// hollow cells for the rest.
//
// A gauge rather than a label, because this list is sorted on priority and a
// sort key you have to READ row by row gives you nothing when you are scanning
// fifty of them. ▰▰▱ ranks below ▰▰▰ at a glance; "P2" only ranks below "P1"
// once you have read both. It is also the shape Linear's own UI uses, so it
// matches where the data came from.
//
// U+25B0/25B1 are Geometric Shapes — the same block as the status dots — and
// crucially they are East-Asian-Neutral, so they are always one column wide.
// The obvious alternatives (■ □ · •) are Ambiguous width, which some terminals
// render double and which would shear this whole column out of alignment.
// Menlo, macOS Terminal's default, covers both; U+23FE and U+2B21 were rejected
// elsewhere in fleet for failing exactly that check.
//
// No priority renders BLANK, not ▱▱▱. "Low" is a choice someone made and "none"
// is the absence of one — different facts, and absence should read as absence
// down the column, the same rule the ticket badge follows.
func priorityGauge(priority int) string {
	switch priority {
	case 1:
		return "▰▰▰"
	case 2:
		return "▰▰▱"
	case 3:
		return "▰▱▱"
	case 4:
		return "▱▱▱"
	}
	return ""
}

// renderPriorityLead styles the gauge for the tickets tab's lead column.
//
// Colour stops after high, and the lower two carry rank by shape alone. Red and
// orange sit in a different column from the status dot's green/blue/amber and
// mean a different kind of urgency, so the two read as separate axes — but
// colouring all four would tint nearly every row in a fifty-row list, and the
// top two would stop standing out, which is the entire reason the list sorts on
// this. Yellow was the obvious third step and is spoken for: it means "waiting"
// in the sidebar, and one screen should not carry two meanings for it.
func renderPriorityLead(priority int) string {
	g := priorityGauge(priority)
	if g == "" {
		return strings.Repeat(" ", paletteLeadWidth)
	}
	switch priority {
	case 1:
		return lipgloss.NewStyle().Foreground(ColorRed).Bold(true).Render(pad(g, paletteLeadWidth))
	case 2:
		return lipgloss.NewStyle().Foreground(ColorOrange).Render(pad(g, paletteLeadWidth))
	}
	return DimStyle.Render(pad(g, paletteLeadWidth))
}

// pad right-pads to a rune width.
func pad(s string, w int) string {
	if n := runeLen(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

func truncRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	if maxRunes == 1 {
		return "…"
	}
	return string(r[:maxRunes-1]) + "…"
}

func runeLen(s string) int { return len([]rune(s)) }

// ticketRightColumn is what a ticket shows on the right anywhere OUTSIDE the
// tickets tab: its state, plus its priority.
//
// Both facts have a dedicated column in the tickets tab — a header for the
// state, a lead column for the priority — and neither of those exists in a
// mixed list, so without this a ticket row loses them entirely.
func ticketRightColumn(it PaletteItem) string {
	out := it.Group
	if mark := plainPriorityMark(it.Priority); mark != "" {
		if out != "" {
			out += "  "
		}
		out += mark
	}
	return out
}

// plainPriorityMark is the gauge without styling or padding, for contexts where
// the mark is embedded in a string that gets truncated and fuzzy-highlighted by
// rune offset — embedded ANSI there would light up the wrong characters.
func plainPriorityMark(priority int) string {
	return priorityGauge(priority)
}

func (d *CommandPaletteDialog) dialogWidth() int {
	w := d.width - 8
	// Wide enough that a ticket title is readable rather than an ellipsis, and
	// still an overlay rather than a takeover.
	if w > 96 {
		w = 96
	}
	if w < 30 {
		w = 30
	}
	return w
}

// innerContentWidth is the writable text width inside the dialog box. In Lip
// Gloss v2, Style.Width sets the TOTAL frame width (border + padding included),
// so subtract both: 2 (rounded border) + 4 (DialogStyle's Padding(1, 2), 2 each
// side). Pre-v2 this only subtracted padding, which left rows 2 cells too wide
// and wrapped the longest ones.
func (d *CommandPaletteDialog) innerContentWidth() int {
	return d.dialogWidth() - 6
}

func (d *CommandPaletteDialog) boxed(content string) string {
	return DialogStyle.Width(d.dialogWidth()).Render(content)
}

// highlightMatches bolds the runes of s at positions listed in matchedIndexes.
// Indexes outside the rune length of s are ignored (the caller is responsible
// for translating haystack offsets into local-substring offsets).
func highlightMatches(s string, matchedIndexes []int) string {
	return highlightWith(s, matchedIndexes, lipgloss.NewStyle().Foreground(ColorText), lipgloss.NewStyle().Foreground(ColorYellow).Bold(true))
}

// highlightMatchesDim is like highlightMatches but uses the dim base style
// (for the right-side Detail column).
func highlightMatchesDim(s string, matchedIndexes []int) string {
	return highlightWith(s, matchedIndexes, DimStyle, lipgloss.NewStyle().Foreground(ColorYellow).Bold(true))
}

func highlightWith(s string, matchedIndexes []int, base, hl lipgloss.Style) string {
	if len(matchedIndexes) == 0 {
		return base.Render(s)
	}
	matched := make(map[int]bool, len(matchedIndexes))
	for _, idx := range matchedIndexes {
		matched[idx] = true
	}
	var b strings.Builder
	for i, r := range []rune(s) {
		if matched[i] {
			b.WriteString(hl.Render(string(r)))
		} else {
			b.WriteString(base.Render(string(r)))
		}
	}
	return b.String()
}

// filterShiftIndexes returns the subset of haystack rune indexes that fall in
// the half-open range [lo, hi), shifted by -shift so they index into a
// substring of the haystack.
func filterShiftIndexes(indexes []int, lo, hi, shift int) []int {
	if len(indexes) == 0 || hi <= lo {
		return nil
	}
	out := make([]int, 0, len(indexes))
	for _, idx := range indexes {
		if idx >= lo && idx < hi {
			out = append(out, idx-shift)
		}
	}
	return out
}
