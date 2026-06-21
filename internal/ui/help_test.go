package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// firstBindingDesc and lastBindingDesc are stable rows at the top and bottom of
// the help sheet, used to detect what the current window shows.
const (
	firstBindingDesc = "Move down"
	lastBindingDesc  = "Detach from session"
)

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "j":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}
	case "G":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestHelpOverlayFitsTerminalHeightWhenSmall(t *testing.T) {
	h := NewHelpOverlay()
	h.Show()
	h.SetSize(80, 18)

	// The dialog box (pre-Place) must not exceed the terminal height, or
	// lipgloss.Place would clip it.
	lines := h.bodyLines()
	if len(lines) <= h.visibleRows() {
		t.Fatalf("test precondition failed: expected sheet to overflow height 18 (%d rows, %d visible)", len(lines), h.visibleRows())
	}

	view := h.View()
	if got := lipgloss.Height(view); got > 18 {
		t.Errorf("View height = %d, want <= 18 (overflow/clip)", got)
	}
	if !strings.Contains(view, "scroll") {
		t.Error("expected scroll hint in footer when content overflows")
	}
}

func TestHelpOverlayScrollsToReachLastBinding(t *testing.T) {
	h := NewHelpOverlay()
	h.Show()
	h.SetSize(80, 18)

	if strings.Contains(h.View(), lastBindingDesc) {
		t.Fatalf("last binding %q should be below the fold at scroll=0", lastBindingDesc)
	}

	// Jump to the bottom.
	h, _ = h.Update(keyMsg("G"))
	if !strings.Contains(h.View(), lastBindingDesc) {
		t.Errorf("after End, expected %q to be visible", lastBindingDesc)
	}
}

func TestHelpOverlayShowsEverythingWhenTall(t *testing.T) {
	h := NewHelpOverlay()
	h.Show()
	h.SetSize(80, 200)

	view := h.View()
	if !strings.Contains(view, firstBindingDesc) || !strings.Contains(view, lastBindingDesc) {
		t.Error("tall terminal should render the full sheet (first and last bindings)")
	}
	if !strings.Contains(view, "Press any key to close") {
		t.Error("non-scrolling sheet should show the 'press any key' footer")
	}
}

func TestHelpOverlayScrollKeysDoNotCloseButOthersDo(t *testing.T) {
	h := NewHelpOverlay()
	h.Show()
	h.SetSize(80, 18)

	// A scroll key keeps it open.
	h, _ = h.Update(keyMsg("j"))
	if !h.IsVisible() {
		t.Error("scroll key 'j' should not close the overlay")
	}

	// A non-scroll key closes it.
	h, _ = h.Update(keyMsg("esc"))
	if h.IsVisible() {
		t.Error("esc should close the overlay")
	}
}

// TestHelpOverlayNeverOverflows sweeps a range of terminal sizes and asserts
// the rendered sheet never exceeds the terminal height (which would clip rows).
// Guards against rows wrapping internally (e.g. a key label wider than its
// column) and throwing off the height budget.
func TestHelpOverlayNeverOverflows(t *testing.T) {
	h := NewHelpOverlay()
	h.Show()
	for w := 40; w <= 160; w += 8 {
		for ht := 9; ht <= 60; ht++ {
			h.SetSize(w, ht)
			h.scroll = 0
			if got := lipgloss.Height(h.View()); got > ht {
				t.Errorf("size %dx%d: rendered height %d exceeds terminal height", w, ht, got)
			}
		}
	}
}

func TestHelpOverlayAnyKeyClosesWhenItFits(t *testing.T) {
	h := NewHelpOverlay()
	h.Show()
	h.SetSize(80, 200)

	h, _ = h.Update(keyMsg("j"))
	if h.IsVisible() {
		t.Error("when the sheet fits, any key (incl. 'j') should close it")
	}
}
