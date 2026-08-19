package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/charmbracelet/x/ansi"
)

// accountPickedMsg carries the account a session should move to.
type accountPickedMsg struct{ email string }

// accountPickerRow is one account as offered to the user, with the quota reading
// that is the whole reason they opened this dialog.
type accountPickerRow struct {
	email   string
	label   string
	usage   claudeaccount.Usage
	enabled bool
	note    string // why a dimmed row can't be picked
}

// AccountPickerDialog moves one session to a different Claude subscription.
//
// Anchored to its sidebar row like the context menu and the snooze dropdown
// rather than centered: it acts on the row it hangs from, and a full-screen
// takeover would misrepresent a change that touches exactly one session.
//
// It shows each account's 5-hour usage, not the weekly figure the header
// readout shows. The header is deliberately quiet because it is always on
// screen; here the user is choosing between accounts *because* one is spent, so
// the fast-moving number is the one that answers the question.
type AccountPickerDialog struct {
	visible bool
	width   int
	height  int

	title  string
	rows   []accountPickerRow
	cursor int

	// anchor mirrors ContextMenuDialog's.
	anchorX     int
	rowY        int
	bottomLimit int
}

func NewAccountPickerDialog() *AccountPickerDialog { return &AccountPickerDialog{} }

// Show opens the picker. The cursor lands on the first pickable row, never on a
// dimmed one — the current account and any rejected token are both shown but
// inert, so the list explains itself without offering a no-op.
func (d *AccountPickerDialog) Show(title string, rows []accountPickerRow) {
	d.visible = true
	d.title = title
	d.rows = rows
	d.cursor = 0
	if !d.enabledAt(d.cursor) {
		d.cursor = d.nextEnabled(0, 1)
	}
}

func (d *AccountPickerDialog) Hide()            { d.visible = false }
func (d *AccountPickerDialog) IsVisible() bool  { return d.visible }
func (d *AccountPickerDialog) SetSize(w, h int) { d.width, d.height = w, h }
func (d *AccountPickerDialog) SetAnchor(x, rowY, bottomLimit int) {
	d.anchorX, d.rowY, d.bottomLimit = x, rowY, bottomLimit
}

func (d *AccountPickerDialog) enabledAt(i int) bool {
	return i >= 0 && i < len(d.rows) && d.rows[i].enabled
}

// nextEnabled walks to the next pickable row without wrapping, so holding j
// can't cycle back to the top. Same contract as ContextMenuDialog's.
func (d *AccountPickerDialog) nextEnabled(i, step int) int {
	for j := i + step; j >= 0 && j < len(d.rows); j += step {
		if d.rows[j].enabled {
			return j
		}
	}
	if d.enabledAt(i) {
		return i
	}
	for j := range d.rows {
		if d.rows[j].enabled {
			return j
		}
	}
	return i
}

func (d *AccountPickerDialog) Update(msg tea.Msg) (*AccountPickerDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}
	switch keyMsg.String() {
	case "esc", "ctrl+c":
		d.Hide()
		return d, nil
	case "up", "k", "shift+tab":
		d.cursor = d.nextEnabled(d.cursor, -1)
		return d, nil
	case "down", "j", "tab":
		d.cursor = d.nextEnabled(d.cursor, 1)
		return d, nil
	case "enter":
		if !d.enabledAt(d.cursor) {
			return d, nil
		}
		email := d.rows[d.cursor].email
		d.Hide()
		return d, func() tea.Msg { return accountPickedMsg{email: email} }
	}
	return d, nil
}

const (
	// accountPickerWidth is fixed so the box doesn't reflow as rows of different
	// lengths take the highlight.
	accountPickerWidth = 52
	// accountPickerChrome is what DialogStyle spends before any content: two
	// border columns and two of padding a side. lipgloss v2's Width is
	// border-INCLUSIVE, so a line budgeted against the box width overflows the
	// content area and wraps — which turns every row into two and pushes the
	// footer off the bottom.
	accountPickerChrome = 6
	// accountPickerFooter names the surprising half of the action. Must fit the
	// inner width on one line, or it wraps and the box grows a row.
	accountPickerFooter = "↑↓ pick • enter move+restart • esc"
)

// View returns the bare box; app.go composites it at Position, matching the
// context menu's overlay pattern.
func (d *AccountPickerDialog) View() string {
	if !d.visible {
		return ""
	}

	boxW := accountPickerWidth
	if maxW := d.width - 4; boxW > maxW {
		boxW = maxW
	}
	inner := max(boxW-accountPickerChrome, 1)

	var b strings.Builder
	b.WriteString(TitleStyle.Render(ansi.Truncate(d.title, inner, "…")))
	b.WriteString("\n\n")

	for i, r := range d.rows {
		b.WriteString(d.renderRow(i, r, inner))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	// Says plainly that this restarts, because it does and there is no second
	// confirm. The env var is baked into the tmux session at launch, so a move
	// that didn't relaunch would change the label and nothing else — exactly the
	// lie this feature exists to remove.
	b.WriteString(DimStyle.Render(ansi.Truncate(accountPickerFooter, inner, "…")))

	return DialogStyle.Width(boxW).Render(b.String())
}

// renderRow lays one account out across width cells — the dialog's INNER width,
// not the box width.
func (d *AccountPickerDialog) renderRow(i int, r accountPickerRow, width int) string {
	// The state cell answers "why would I pick this one", so it takes priority
	// over the quota numbers: a rejected token's last-known percentage would
	// present a dead credential as one with headroom.
	state := ""
	switch {
	case r.note != "":
		state = r.note
	case r.usage.Known():
		state = ansi.Strip(renderQuotaCell(r.usage))
	default:
		state = "quota unknown"
	}

	// Budget the columns off the raw text and pad before styling — padding a
	// styled string counts the ANSI bytes and the columns come out ragged.
	//
	// Width, never len: the state cell carries "·" and "✕", three bytes each, so
	// a byte count would over-reserve and squeeze the name to a third of its
	// column.
	const gutter = 2 // the "▸ " / "  " lead cell every row carries
	const gap = 2    // minimum space between the name and its state
	nameW := max(width-gutter-lipgloss.Width(state)-gap, 1)
	name := r.label
	if lipgloss.Width(name) > nameW {
		name = ansi.Truncate(name, nameW, "…")
	}
	pad := max(gap, width-gutter-lipgloss.Width(name)-lipgloss.Width(state))
	row := name + strings.Repeat(" ", pad) + state

	if !r.enabled {
		return "  " + DimStyle.Render(row)
	}
	if i == d.cursor {
		return SelectionMarker(true).Render("▸ ") + selTitle().Render(row)
	}
	return "  " + row
}

// Position places the dropdown below its row, flipping above near the footer —
// same rules as ContextMenuDialog.Position.
func (d *AccountPickerDialog) Position(boxW, boxH int) (int, int) {
	x := d.anchorX
	if x+boxW > d.width {
		x = d.width - boxW
	}
	if x < 0 {
		x = 0
	}
	y := d.rowY + 1
	if y+boxH > d.bottomLimit {
		y = d.rowY - boxH
	}
	if y < 0 {
		y = 0
	}
	return x, y
}
