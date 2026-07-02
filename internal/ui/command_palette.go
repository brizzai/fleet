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
}

// PaletteTab restricts which kinds of items show in the palette.
type PaletteTab int

const (
	PaletteTabAll PaletteTab = iota
	PaletteTabActions
	PaletteTabPlaces
)

var paletteTabOrder = []struct {
	Tab   PaletteTab
	Label string
}{
	{PaletteTabAll, "all"},
	{PaletteTabActions, "actions"},
	{PaletteTabPlaces, "repos/worktrees"},
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
}

type scoredItem struct {
	PaletteItem
	score          int
	matchedIndexes []int // rune positions in Haystack — used to highlight matched chars in Name
	recent         bool  // true when this row is sitting in the "recent" section
}

const paletteMaxVisible = 14

// NewCommandPaletteDialog creates a new command palette dialog.
func NewCommandPaletteDialog() *CommandPaletteDialog {
	fi := textinput.New()
	fi.Placeholder = "search commands, repos, worktrees..."
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
	d.cursor = 0
	d.scrollOff = 0
	d.activeTab = PaletteTabAll
	d.filterInput.SetValue("")
	d.filterInput.Focus()
	d.rebuildFiltered()
}

// itemMatchesTab reports whether an item is included by the active tab.
func itemMatchesTab(it PaletteItem, tab PaletteTab) bool {
	switch tab {
	case PaletteTabActions:
		return it.Kind == PaletteKindCommand
	case PaletteTabPlaces:
		return it.Kind == PaletteKindRepo || it.Kind == PaletteKindWorktree
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
				rest = append(rest, scoredItem{PaletteItem: it})
			}
		}
		sortRecents(recents, recentRank)
		d.filtered = append(d.filtered, recents...)
		d.filtered = append(d.filtered, rest...)
	} else {
		matches := fuzzy.Find(query, haystacks)
		for _, m := range matches {
			d.filtered = append(d.filtered, scoredItem{
				PaletteItem:    tabItems[m.Index],
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
	b.WriteString("  " + DimStyle.Render(">") + " " + d.filterInput.View())
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

		// Column layout: [prefix 2][badge 4][sep 1][name N][gap 2][right]
		const reserved = 2 + paletteBadgeWidth + 1 + 2
		const maxNameCap = 22

		nameCol := 0
		for i := d.scrollOff; i < end; i++ {
			n := runeLen(d.filtered[i].Name)
			if n > nameCol {
				nameCol = n
			}
		}
		if nameCol > maxNameCap {
			nameCol = maxNameCap
		}

		rightBudget := d.innerContentWidth() - reserved - nameCol
		if rightBudget < 0 {
			rightBudget = 0
		}

		prevRecent := false
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

			prefix := "  "
			if selected {
				prefix = SessionSelectionPrefix.Render("▸ ")
			}

			badge := renderKindBadge(it.Kind)

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
				name = SessionTitleSelStyle.Render(rawName + namePad)
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

			b.WriteString(prefix + badge + " " + name + "  ")
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
	for _, tab := range []PaletteTab{PaletteTabAll, PaletteTabActions, PaletteTabPlaces} {
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

	activeStyle := lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent).Bold(true).Padding(0, 1)
	inactiveStyle := lipgloss.NewStyle().Foreground(ColorTextDim).Padding(0, 1)

	parts := make([]string, 0, len(paletteTabOrder))
	for _, t := range paletteTabOrder {
		label := fmt.Sprintf("%s %d", t.Label, counts[t.Tab])
		if t.Tab == d.activeTab {
			parts = append(parts, activeStyle.Render(label))
		} else {
			parts = append(parts, inactiveStyle.Render(label))
		}
	}
	return "  " + strings.Join(parts, " ")
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
	default:
		label, col = "cmd ", ColorTextDim
	}
	return lipgloss.NewStyle().Foreground(col).Render(label)
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

func (d *CommandPaletteDialog) dialogWidth() int {
	w := d.width - 4
	if w > 64 {
		w = 64
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
