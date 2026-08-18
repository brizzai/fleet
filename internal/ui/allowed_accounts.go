package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/charmbracelet/x/ansi"
)

// allowedAccountsSetMsg carries the new policy for one origin.
//
// It names the origin rather than leaving the handler to re-read the cursor,
// which is why this dialog needs none of the focusContextMenuTarget dance the
// account picker does: the write is keyed by an origin the message already
// carries, so there is no cursor left to race.
type allowedAccountsSetMsg struct {
	originKey string
	emails    []string
}

// allowedAccountRow is one account as offered to the user, with the quota
// reading carried for context only — see AllowedAccountsDialog on why a spent or
// logged-out account is still tickable here.
type allowedAccountRow struct {
	email string
	label string
	usage claudeaccount.Usage
}

// AllowedAccountsDialog edits which Claude subscriptions may run under one
// origin (config allowed_accounts, keyed "origin:<key>").
//
// Anchored to its sidebar row like the context menu and the account picker: it
// acts on the origin it hangs from, and the row above it is the only thing that
// says which origin that is.
//
// Every row is tickable, including a logged-out or spent one — unlike the
// account picker, which correctly refuses to move a session onto a dead login.
// The difference is that this is policy, not availability: "client work runs on
// the work subscription" stays true while that subscription is logged out, and a
// dialog that refused to record it would force the user to log in first just to
// write down a rule. The quota cell is shown anyway, because the reason to
// restrict an origin is usually something you can only see in those numbers.
type AllowedAccountsDialog struct {
	visible bool
	width   int
	height  int

	originKey   string
	originLabel string
	rows        []allowedAccountRow
	selected    map[int]bool
	cursor      int

	// anchor mirrors ContextMenuDialog's.
	anchorX     int
	rowY        int
	bottomLimit int
}

func NewAllowedAccountsDialog() *AllowedAccountsDialog { return &AllowedAccountsDialog{} }

// Show opens the editor for one origin. allowed is the origin's stored policy;
// empty means unrestricted, which ticks every row — that is what unrestricted
// *is*, and showing it as "nothing selected" would misreport the current state
// as the one state this dialog refuses to save.
//
// An email in allowed that names no configured account simply has no row to tick.
// Saving therefore drops it, which heals the stale-allowlist state resolveAccount
// refuses new sessions on.
func (d *AllowedAccountsDialog) Show(originKey, originLabel string, rows []allowedAccountRow, allowed []string) {
	d.visible = true
	d.originKey = originKey
	d.originLabel = originLabel
	d.rows = rows
	d.cursor = 0
	d.selected = make(map[int]bool, len(rows))

	if len(allowed) == 0 {
		for i := range rows {
			d.selected[i] = true
		}
		return
	}
	set := make(map[string]bool, len(allowed))
	for _, e := range allowed {
		set[e] = true
	}
	for i, r := range rows {
		d.selected[i] = set[r.email]
	}
}

func (d *AllowedAccountsDialog) Hide()            { d.visible = false }
func (d *AllowedAccountsDialog) IsVisible() bool  { return d.visible }
func (d *AllowedAccountsDialog) SetSize(w, h int) { d.width, d.height = w, h }
func (d *AllowedAccountsDialog) SetAnchor(x, rowY, bottomLimit int) {
	d.anchorX, d.rowY, d.bottomLimit = x, rowY, bottomLimit
}

// selectedCount reports how many rows are ticked.
func (d *AllowedAccountsDialog) selectedCount() int {
	n := 0
	for i := range d.rows {
		if d.selected[i] {
			n++
		}
	}
	return n
}

// emails returns the ticked accounts in row order, which is Store.List order —
// so the config file stays stable and diffs cleanly instead of recording the
// sequence the user happened to click in.
//
// Returns nil when every row is ticked: all-allowed is the absence of a policy,
// not a policy naming everyone. See config.SetAllowedAccounts.
func (d *AllowedAccountsDialog) emails() []string {
	if d.selectedCount() == len(d.rows) {
		return nil
	}
	out := make([]string, 0, len(d.rows))
	for i, r := range d.rows {
		if d.selected[i] {
			out = append(out, r.email)
		}
	}
	return out
}

func (d *AllowedAccountsDialog) Update(msg tea.Msg) (*AllowedAccountsDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}
	switch keyMsg.String() {
	case "esc", "ctrl+c":
		d.Hide()
		return d, nil
	case "up", "k", "shift+tab":
		if d.cursor > 0 {
			d.cursor--
		}
		return d, nil
	case "down", "j", "tab":
		if d.cursor < len(d.rows)-1 {
			d.cursor++
		}
		return d, nil
	case " ", "space":
		if d.cursor >= 0 && d.cursor < len(d.rows) {
			d.selected[d.cursor] = !d.selected[d.cursor]
		}
		return d, nil
	case "a":
		// Ticks everything when anything is unticked, so the one key that undoes
		// a restriction always undoes it — including from the zero-ticked state a
		// stale allowlist opens in, which is otherwise unsavable.
		all := d.selectedCount() == len(d.rows)
		for i := range d.rows {
			d.selected[i] = !all
		}
		return d, nil
	case "enter":
		// Zero ticked is refused rather than saved, the same rule SnoozeDialog
		// applies to an unparseable duration: an empty list already means
		// unrestricted in config, so saving it would silently do the opposite of
		// what ticking nothing looks like it asks for.
		if d.selectedCount() == 0 {
			return d, nil
		}
		originKey, emails := d.originKey, d.emails()
		d.Hide()
		return d, func() tea.Msg {
			return allowedAccountsSetMsg{originKey: originKey, emails: emails}
		}
	}
	return d, nil
}

const (
	// allowedAccountsWidth is fixed so the box doesn't reflow as rows of
	// different lengths take the highlight.
	allowedAccountsWidth = 56
	// allowedAccountsChrome is what DialogStyle spends before any content — see
	// accountPickerChrome for why lipgloss v2 makes this necessary.
	allowedAccountsChrome = 6
	// allowedAccountsFooter must fit the inner width on one line, or it wraps and
	// the box grows a row.
	allowedAccountsFooter = "space toggle • a all • enter save • esc"
)

// View returns the bare box; app.go composites it at Position, matching the
// context menu's overlay pattern.
func (d *AllowedAccountsDialog) View() string {
	if !d.visible {
		return ""
	}

	boxW := allowedAccountsWidth
	if maxW := d.width - 4; boxW > maxW {
		boxW = maxW
	}
	inner := max(boxW-allowedAccountsChrome, 1)

	var b strings.Builder
	b.WriteString(TitleStyle.Render(ansi.Truncate("Accounts for "+d.originLabel, inner, "…")))
	b.WriteString("\n\n")

	for i, r := range d.rows {
		b.WriteString(d.renderRow(i, r, inner))
		b.WriteString("\n")
	}

	// The verdict swaps in place on one line and never wraps, so the box keeps a
	// stable height as the user ticks — a box that grew a row mid-keystroke would
	// move the footer out from under the eye that is reading it.
	b.WriteString("\n")
	b.WriteString(ansi.Truncate(d.verdict(), inner, "…"))
	b.WriteString("\n")
	b.WriteString(DimStyle.Render(ansi.Truncate(allowedAccountsFooter, inner, "…")))

	return DialogStyle.Width(boxW).Render(b.String())
}

// verdict says what enter will do, in the terms the user is choosing in. The
// two ends of the range both need saying out loud: all-ticked stores nothing at
// all, and zero-ticked stores nothing *and* refuses.
func (d *AllowedAccountsDialog) verdict() string {
	n := d.selectedCount()
	switch {
	case n == 0:
		return lipgloss.NewStyle().Foreground(ColorYellow).Render("pick at least one account")
	case n == len(d.rows):
		return DimStyle.Render("all accounts — no restriction")
	default:
		return DimStyle.Render(fmt.Sprintf("%d of %d accounts allowed here", n, len(d.rows)))
	}
}

// renderRow lays one account out across width cells — the dialog's INNER width,
// not the box width.
func (d *AllowedAccountsDialog) renderRow(i int, r allowedAccountRow, width int) string {
	// Logged out outranks the quota figure for the same reason it does in the
	// account picker: a rejected login's last-known percentage presents a dead
	// credential as one with headroom. It does not stop the row being ticked.
	state := ""
	switch {
	case r.usage.LoggedOut:
		state = "✕ logged out"
	case r.usage.Known():
		state = ansi.Strip(renderQuotaCell(r.usage))
	default:
		state = "quota unknown"
	}

	box := DimStyle.Render("○")
	if d.selected[i] {
		box = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("◉")
	}

	// Budget the columns off the raw text and pad before styling — padding a
	// styled string counts the ANSI bytes and the columns come out ragged. Width,
	// never len: the state cell carries multi-byte runes.
	const gutter = 4 // the "▸ " lead cell plus the checkbox and its space
	const gap = 2    // minimum space between the name and its state
	nameW := max(width-gutter-lipgloss.Width(state)-gap, 1)
	name := r.label
	if lipgloss.Width(name) > nameW {
		name = ansi.Truncate(name, nameW, "…")
	}
	pad := max(gap, width-gutter-lipgloss.Width(name)-lipgloss.Width(state))
	row := name + strings.Repeat(" ", pad) + state

	if i == d.cursor {
		return SessionSelectionPrefix.Render("▸ ") + box + " " + selTitle().Render(row)
	}
	return "  " + box + " " + row
}

// Position places the dropdown below its row, flipping above near the footer —
// same rules as ContextMenuDialog.Position.
func (d *AllowedAccountsDialog) Position(boxW, boxH int) (int, int) {
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
