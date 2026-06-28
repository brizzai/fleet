package ui

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTranslateFocusKey(t *testing.T) {
	tests := []struct {
		name        string
		msg         tea.KeyMsg
		wantSends   []focusKeySend
		wantUnfocus bool
	}{
		{"esc exits focus mode", tea.KeyMsg{Type: tea.KeyEsc}, nil, true},

		// Plain keys.
		{"enter", tea.KeyMsg{Type: tea.KeyEnter}, []focusKeySend{{val: "Enter"}}, false},
		{"backspace", tea.KeyMsg{Type: tea.KeyBackspace}, []focusKeySend{{val: "BSpace"}}, false},
		{"tab", tea.KeyMsg{Type: tea.KeyTab}, []focusKeySend{{val: "Tab"}}, false},
		{"shift+tab -> BTab", tea.KeyMsg{Type: tea.KeyShiftTab}, []focusKeySend{{val: "BTab"}}, false},
		{"left", tea.KeyMsg{Type: tea.KeyLeft}, []focusKeySend{{val: "Left"}}, false},
		{"ctrl+left -> C-Left", tea.KeyMsg{Type: tea.KeyCtrlLeft}, []focusKeySend{{val: "C-Left"}}, false},
		{"ctrl+right -> C-Right", tea.KeyMsg{Type: tea.KeyCtrlRight}, []focusKeySend{{val: "C-Right"}}, false},
		{"ctrl+up -> C-Up", tea.KeyMsg{Type: tea.KeyCtrlUp}, []focusKeySend{{val: "C-Up"}}, false},
		{"ctrl+down -> C-Down", tea.KeyMsg{Type: tea.KeyCtrlDown}, []focusKeySend{{val: "C-Down"}}, false},
		{"ctrl+w stays C-w", tea.KeyMsg{Type: tea.KeyCtrlW}, []focusKeySend{{val: "C-w"}}, false},
		{"delete -> DC", tea.KeyMsg{Type: tea.KeyDelete}, []focusKeySend{{val: "DC"}}, false},
		{"runes are literal", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")}, []focusKeySend{{literal: true, val: "hi"}}, false},

		// Alt/Meta-modified keys keep the modifier.
		{"alt+backspace -> M-BSpace", tea.KeyMsg{Type: tea.KeyBackspace, Alt: true}, []focusKeySend{{val: "M-BSpace"}}, false},
		{"alt+delete -> M-DC", tea.KeyMsg{Type: tea.KeyDelete, Alt: true}, []focusKeySend{{val: "M-DC"}}, false},
		{"alt+b word-back -> M-b", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b"), Alt: true}, []focusKeySend{{val: "M-b"}}, false},
		{"alt+f word-fwd -> M-f", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f"), Alt: true}, []focusKeySend{{val: "M-f"}}, false},
		{"alt+d kill-word-fwd -> M-d", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d"), Alt: true}, []focusKeySend{{val: "M-d"}}, false},
		{"alt+left -> M-Left", tea.KeyMsg{Type: tea.KeyLeft, Alt: true}, []focusKeySend{{val: "M-Left"}}, false},
		{"alt+right -> M-Right", tea.KeyMsg{Type: tea.KeyRight, Alt: true}, []focusKeySend{{val: "M-Right"}}, false},
		{"alt+up -> M-Up", tea.KeyMsg{Type: tea.KeyUp, Alt: true}, []focusKeySend{{val: "M-Up"}}, false},
		{"alt+down -> M-Down", tea.KeyMsg{Type: tea.KeyDown, Alt: true}, []focusKeySend{{val: "M-Down"}}, false},
		{"alt+enter -> M-Enter", tea.KeyMsg{Type: tea.KeyEnter, Alt: true}, []focusKeySend{{val: "M-Enter"}}, false},

		// Alt + non-letter rune can't use "M-<rune>" safely → ESC then literal.
		{"alt+semicolon -> ESC then literal", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(";"), Alt: true}, []focusKeySend{{val: "Escape"}, {literal: true, val: ";"}}, false},
		{"alt+digit -> ESC then literal", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true}, []focusKeySend{{val: "Escape"}, {literal: true, val: "1"}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSends, gotUnfocus := translateFocusKey(tt.msg)
			if gotUnfocus != tt.wantUnfocus {
				t.Errorf("unfocus = %v, want %v", gotUnfocus, tt.wantUnfocus)
			}
			if !reflect.DeepEqual(gotSends, tt.wantSends) {
				t.Errorf("sends = %+v, want %+v", gotSends, tt.wantSends)
			}
		})
	}
}

// TestTranslateFocusKey_ModifierNotDropped is the regression guard for the bug
// this code path had: Alt-modified keys were switched on by Type alone, so e.g.
// Option+Backspace was forwarded to tmux as a plain BSpace and word-wise line
// editing didn't work in the focused pane.
func TestTranslateFocusKey_ModifierNotDropped(t *testing.T) {
	cases := []struct {
		name  string
		plain tea.KeyMsg
		alt   tea.KeyMsg
	}{
		{"backspace", tea.KeyMsg{Type: tea.KeyBackspace}, tea.KeyMsg{Type: tea.KeyBackspace, Alt: true}},
		{"delete", tea.KeyMsg{Type: tea.KeyDelete}, tea.KeyMsg{Type: tea.KeyDelete, Alt: true}},
		{"left", tea.KeyMsg{Type: tea.KeyLeft}, tea.KeyMsg{Type: tea.KeyLeft, Alt: true}},
		{"rune b", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b"), Alt: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plain, _ := translateFocusKey(c.plain)
			alt, _ := translateFocusKey(c.alt)
			if reflect.DeepEqual(plain, alt) {
				t.Fatalf("Alt+%s produced the same send as plain %s (%+v); the modifier was dropped", c.name, c.name, plain)
			}
		})
	}
}

// TestTranslateFocusKey_AltFallbackEmitsEscape checks the catch-all path:
// an Alt-modified key with no explicit mapping is sent as Escape followed by
// the bare key (Alt+X == ESC then X at the terminal level).
func TestTranslateFocusKey_AltFallbackEmitsEscape(t *testing.T) {
	// KeyHome has no Alt-specific case, so it goes through the fallback.
	sends, unfocus := translateFocusKey(tea.KeyMsg{Type: tea.KeyHome, Alt: true})
	if unfocus {
		t.Fatal("unexpected unfocus")
	}
	want := []focusKeySend{{val: "Escape"}, {val: "Home"}}
	if !reflect.DeepEqual(sends, want) {
		t.Errorf("sends = %+v, want %+v", sends, want)
	}
}
