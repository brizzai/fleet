package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Claude cycles its permission/plan mode on Shift+Tab; sending a plain Tab
// from the split view or drawer made that key do nothing.
func TestShiftTabReachesPaneAsBTab(t *testing.T) {
	if got := paneKeyName(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}); got != "BTab" {
		t.Errorf("shift+tab = %q, want BTab", got)
	}
	if got := paneKeyName(tea.KeyPressMsg{Code: tea.KeyTab}); got != "Tab" {
		t.Errorf("tab = %q, want Tab", got)
	}
	if got := paneKeyName(tea.KeyPressMsg{Code: 'a', Text: "a"}); got != "" {
		t.Errorf("text key = %q, want empty", got)
	}
}

// Ctrl+←/→ is word navigation in Claude's input box and in readline; sending
// a bare arrow moved one character instead.
func TestModifiedArrowsKeepTheirModifiers(t *testing.T) {
	cases := map[string]tea.KeyPressMsg{
		"C-Left":   {Code: tea.KeyLeft, Mod: tea.ModCtrl},
		"C-Right":  {Code: tea.KeyRight, Mod: tea.ModCtrl},
		"M-Left":   {Code: tea.KeyLeft, Mod: tea.ModAlt},
		"C-S-Home": {Code: tea.KeyHome, Mod: tea.ModCtrl | tea.ModShift},
		"Left":     {Code: tea.KeyLeft},
	}
	for want, msg := range cases {
		if got := paneKeyName(msg); got != want {
			t.Errorf("%v = %q, want %q", msg, got, want)
		}
	}
}
