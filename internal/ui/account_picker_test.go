package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/claudeaccount"
)

func pickerRows() []accountPickerRow {
	return []accountPickerRow{
		{email: "here@x.com", label: "here", enabled: false, note: "current"},
		{email: "dead@x.com", label: "dead", enabled: false, note: "logged out",
			usage: claudeaccount.Usage{LoggedOut: true}},
		{email: "good@x.com", label: "good", enabled: true,
			usage: claudeaccount.Usage{FiveHourPct: 12, FetchedAt: time.Now()}},
	}
}

func showPicker(t *testing.T) *AccountPickerDialog {
	t.Helper()
	d := NewAccountPickerDialog()
	d.SetSize(120, 40)
	d.SetAnchor(2, 5, 30)
	d.Show("Move api-work to", pickerRows())
	return d
}

// The two inert rows are the first two, so a naive cursor at 0 would open on
// "current" and make Enter do nothing on the dialog's own default.
func TestPickerOpensOnAPickableRow(t *testing.T) {
	d := showPicker(t)
	if got := d.rows[d.cursor].email; got != "good@x.com" {
		t.Fatalf("cursor opened on %q, want good@x.com", got)
	}
}

// Moving onto a refused credential would trade a busy account for one that
// cannot run at all, so those rows are shown and not selectable.
func TestPickerNavigationSkipsUnpickableRows(t *testing.T) {
	d := showPicker(t)
	for range 5 {
		d, _ = d.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	}
	if got := d.rows[d.cursor].email; got != "good@x.com" {
		t.Fatalf("k walked onto %q; only good@x.com is pickable", got)
	}
}

func TestPickerEnterReturnsTheHighlightedAccount(t *testing.T) {
	d := showPicker(t)
	d, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter produced no command")
	}
	msg, ok := cmd().(accountPickedMsg)
	if !ok {
		t.Fatalf("enter returned %T, want accountPickedMsg", cmd())
	}
	if msg.email != "good@x.com" {
		t.Errorf("picked %q, want good@x.com", msg.email)
	}
	if d.IsVisible() {
		t.Error("picker stayed open after a pick")
	}
}

func TestPickerEscCancelsWithoutPicking(t *testing.T) {
	d := showPicker(t)
	d, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd != nil {
		t.Error("esc produced a command; it must not move anything")
	}
	if d.IsVisible() {
		t.Error("esc left the picker open")
	}
}

// Every row carries a reason, and a session is moved on the strength of them.
// A row rendered without its state is a blind pick.
func TestPickerShowsWhyEachAccountIsWhatItIs(t *testing.T) {
	d := showPicker(t)
	view := d.View()
	for _, want := range []string{"current", "logged out", "12%"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
	// The restart is the surprising half of this action, so it must be stated.
	if !strings.Contains(view, "restart") {
		t.Errorf("view never says it restarts the session:\n%s", view)
	}
}

// The state cell is full of multi-byte runes ("·", "✕"). Budgeting its column
// in bytes would eat three times the space it occupies and truncate the account
// names that are the point of the dialog.
func TestPickerRowsAreOneLineAndFitTheBox(t *testing.T) {
	d := NewAccountPickerDialog()
	d.SetSize(120, 40)
	d.Show("Move x to", []accountPickerRow{
		{email: "a@x.com", label: "an-account-with-a-long-name", enabled: true,
			usage: claudeaccount.Usage{FiveHourPct: 42, FetchedAt: time.Now(),
				FiveHourReset: time.Now().Add(time.Hour)}},
	})
	inner := accountPickerWidth - accountPickerChrome
	row := d.renderRow(0, d.rows[0], inner)
	if strings.Contains(row, "\n") {
		t.Fatalf("row wrapped onto a second line:\n%q", row)
	}
	if w := lipgloss.Width(row); w > inner {
		t.Errorf("row is %d cells wide, content area is %d", w, inner)
	}

	// And the assembled box must not wrap either — lipgloss v2's Width is
	// border-inclusive, so budgeting a line against the box width silently turns
	// every row into two.
	for _, line := range strings.Split(d.View(), "\n") {
		if w := lipgloss.Width(line); w > accountPickerWidth {
			t.Fatalf("rendered line is %d cells, box is %d:\n%s", w, accountPickerWidth, d.View())
		}
	}
	// One more account must cost exactly one more line. Asserted as a delta
	// rather than an absolute height so it tests the thing that matters (nothing
	// wrapped) without encoding DialogStyle's border and padding.
	oneRow := strings.Count(d.View(), "\n")
	d.Show("Move x to", append(d.rows, accountPickerRow{
		email: "b@x.com", label: "another-account-with-a-long-name", enabled: true,
		usage: claudeaccount.Usage{LoggedOut: true},
	}))
	if got := strings.Count(d.View(), "\n") - oneRow; got != 1 {
		t.Errorf("a second account added %d lines, want 1 — a row wrapped:\n%s", got, d.View())
	}
}
