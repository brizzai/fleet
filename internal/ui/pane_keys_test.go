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
