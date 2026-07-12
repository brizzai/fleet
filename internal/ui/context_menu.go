package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// contextMenuMsg is sent when the user picks an entry from the context menu.
// The id is a dispatchCommand action id, so the menu adds no handler logic of
// its own — it's a view over the commands that already exist.
type contextMenuMsg struct{ id string }

// ContextMenuItem is one row of the context menu.
//
// Enabled=false rows still render (dimmed, with Note explaining why) so the menu
// teaches what exists on this row type. They are never landed on by j/k and their
// Key does nothing.
type ContextMenuItem struct {
	ID       string // dispatchCommand action id
	Label    string
	Shortcut string // display only, e.g. "d", "⏎"
	Key      string // key that fires it from inside the menu ("" = none)
	Enabled  bool
	Note     string // dim suffix on disabled rows, e.g. "Claude only"
}

// ContextMenuDialog is a dropdown anchored to the sidebar row under the cursor.
//
// It follows the command-palette pattern, not the full-screen-dialog one: View
// returns the bare box and the caller composites it with overlayAt. Unlike the
// palette it does not dim the backdrop — the box is small and sits right next to
// the row it acts on, so dimming the whole app would read as a modal takeover.
type ContextMenuDialog struct {
	visible   bool
	title     string
	items     []ContextMenuItem
	cursor    int
	scrollOff int

	anchorX     int // desired left edge
	rowY        int // screen row of the sidebar cursor; menu opens just below it
	bottomLimit int // first screen row the menu must not reach (top of the footer)

	width, height int // terminal size, for clamping
}

// contextMenuStyle is DialogStyle without the vertical padding — a dropdown
// should hug its rows rather than float in a tall box.
var contextMenuStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(ColorAccent).
	Padding(0, 1)

func NewContextMenuDialog() *ContextMenuDialog { return &ContextMenuDialog{} }

// SetAnchor records where the menu should hang: anchorX is the desired left
// edge, rowY the screen row of the sidebar cursor, and bottomLimit the first row
// the menu must stay clear of (the top of the footer).
//
// Call it on open and again on resize. While the menu is open every key is routed
// to it, so the sidebar cursor cannot move — a resize is the only thing that can
// invalidate the anchor. It re-clamps the scroll, since bottomLimit decides how
// many rows fit.
func (d *ContextMenuDialog) SetAnchor(anchorX, rowY, bottomLimit int) {
	d.anchorX, d.rowY, d.bottomLimit = anchorX, rowY, bottomLimit
	d.syncScroll()
}

// Show opens the menu. Call SetAnchor first — the initial scroll clamp needs the
// bottom limit. The cursor lands on the first enabled item, never on a dimmed one.
func (d *ContextMenuDialog) Show(title string, items []ContextMenuItem) {
	d.visible = true
	d.title = title
	d.items = items
	d.scrollOff = 0
	d.cursor = 0
	if !d.enabledAt(d.cursor) {
		d.cursor = d.nextEnabled(0, 1)
	}
	d.syncScroll()
}

func (d *ContextMenuDialog) Hide()           { d.visible = false }
func (d *ContextMenuDialog) IsVisible() bool { return d.visible }

func (d *ContextMenuDialog) SetSize(w, h int) { d.width, d.height = w, h }

func (d *ContextMenuDialog) enabledAt(i int) bool {
	return i >= 0 && i < len(d.items) && d.items[i].Enabled
}

// nextEnabled walks from i in direction step to the next enabled row. It does not
// wrap — the ends are hard stops, so holding j can't cycle you back to the top.
// Returns the original index when nothing enabled lies ahead.
func (d *ContextMenuDialog) nextEnabled(i, step int) int {
	for j := i + step; j >= 0 && j < len(d.items); j += step {
		if d.items[j].Enabled {
			return j
		}
	}
	if d.enabledAt(i) {
		return i
	}
	// Nothing ahead and the start row is itself disabled (Show's first-enabled
	// scan): fall back to the first enabled row anywhere, else stay put.
	for j := range d.items {
		if d.items[j].Enabled {
			return j
		}
	}
	return i
}

// contextMenuChrome is every non-item row View can draw: 2 border rows, the title,
// and — once the list overflows — a "⋮" indicator above AND below. Budgeting the
// worst case unconditionally costs one wasted row on an unscrolled menu; getting
// it wrong lets a scrolled box overrun the footer.
const contextMenuChrome = 5

// maxVisible is how many item rows fit between the menu's top and the footer.
// Keeps the dropdown on screen on a short terminal; the overflow scrolls.
func (d *ContextMenuDialog) maxVisible() int {
	return min(max(d.bottomLimit-contextMenuChrome, 1), len(d.items))
}

func (d *ContextMenuDialog) syncScroll() {
	maxVis := d.maxVisible()
	if len(d.items) <= maxVis {
		d.scrollOff = 0
		return
	}
	if d.cursor < d.scrollOff {
		d.scrollOff = d.cursor
	}
	if d.cursor >= d.scrollOff+maxVis {
		d.scrollOff = d.cursor - maxVis + 1
	}
}

// Update handles menu keys. Nav (j/k) skips disabled rows; enter fires the row
// under the cursor; any other key is matched against the items' own shortcuts, so
// the menu doubles as a cheatsheet that trains you onto the real keybinding.
// Unmatched keys are swallowed — they never fall through to handleKey.
func (d *ContextMenuDialog) Update(msg tea.Msg) (*ContextMenuDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	switch keyMsg.String() {
	case "esc", ".", "ctrl+c":
		d.Hide()
		return d, nil
	case "up", "k":
		d.cursor = d.nextEnabled(d.cursor, -1)
		d.syncScroll()
		return d, nil
	case "down", "j":
		d.cursor = d.nextEnabled(d.cursor, 1)
		d.syncScroll()
		return d, nil
	case "enter":
		if !d.enabledAt(d.cursor) {
			return d, nil
		}
		return d, d.fire(d.items[d.cursor].ID)
	}

	// Shortcut passthrough: the row's own key runs it from inside the menu.
	for _, it := range d.items {
		if it.Enabled && it.Key != "" && it.Key == keyMsg.String() {
			return d, d.fire(it.ID)
		}
	}
	return d, nil
}

func (d *ContextMenuDialog) fire(id string) tea.Cmd {
	d.Hide()
	return func() tea.Msg { return contextMenuMsg{id: id} }
}

// Position returns the top-left cell for a menu of the given rendered size. The
// menu prefers to hang below its row and flips above when that would cross the
// footer, then clamps to the screen.
func (d *ContextMenuDialog) Position(menuW, menuH int) (int, int) {
	x := d.anchorX
	if x+menuW > d.width {
		x = d.width - menuW
	}
	if x < 0 {
		x = 0
	}

	y := d.rowY + 1
	if y+menuH > d.bottomLimit {
		y = d.rowY - menuH // flip above the row
	}
	if y < 0 {
		y = 0
	}
	return x, y
}

// View renders the menu box. Returns "" when hidden.
func (d *ContextMenuDialog) View() string {
	if !d.visible || len(d.items) == 0 {
		return ""
	}

	// Column widths: the label column also has to hold the disabled note, or a
	// "Claude only" row would push past the box and wrap.
	labelW, keyW := 0, 0
	for _, it := range d.items {
		labelW = max(labelW, runeLen(d.labelOf(it)))
		keyW = max(keyW, runeLen(it.Shortcut))
	}

	end := min(d.scrollOff+d.maxVisible(), len(d.items))

	// Joined, not appended with a trailing "\n" — a trailing newline would leave a
	// blank row hanging above the box's bottom border.
	lines := []string{DimStyle.Render(truncRunes(d.title, labelW+keyW+2))}
	if d.scrollOff > 0 {
		lines = append(lines, DimStyle.Render("  ⋮"))
	}
	for i := d.scrollOff; i < end; i++ {
		lines = append(lines, d.renderRow(i, labelW, keyW))
	}
	if end < len(d.items) {
		lines = append(lines, DimStyle.Render("  ⋮"))
	}

	return contextMenuStyle.Render(strings.Join(lines, "\n"))
}

// labelOf is the label as it appears in the label column — the note is part of
// it, so it counts toward the column width.
func (d *ContextMenuDialog) labelOf(it ContextMenuItem) string {
	if !it.Enabled && it.Note != "" {
		return it.Label + " — " + it.Note
	}
	return it.Label
}

func (d *ContextMenuDialog) renderRow(i, labelW, keyW int) string {
	it := d.items[i]
	label := d.labelOf(it)
	pad := strings.Repeat(" ", max(0, labelW-runeLen(label)))
	keyPad := strings.Repeat(" ", max(0, keyW-runeLen(it.Shortcut)))

	// A disabled row is dim end-to-end, so it reads as unavailable at a glance
	// rather than as "an option I could pick".
	if !it.Enabled {
		return DimStyle.Render("  " + label + pad + "  " + keyPad + it.Shortcut)
	}
	if i == d.cursor {
		return SessionSelectionPrefix.Render("▸ ") +
			SessionTitleSelStyle.Render(label+pad) +
			"  " + DimStyle.Render(keyPad+it.Shortcut)
	}
	return "  " + label + pad + "  " + DimStyle.Render(keyPad+it.Shortcut)
}
