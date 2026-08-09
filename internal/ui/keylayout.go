package ui

import (
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/keylayout"
)

// normalizeKey rewrites a keypress from a non-Latin keyboard layout into the
// US-QWERTY key at the same physical position, so a Hebrew 'ח' reaches the
// command switches as "j". Anything it doesn't recognise is returned untouched,
// and ASCII never matches (see internal/keylayout), so a Latin layout is a
// no-op.
//
// Call it only where a key means a command. It must never reach a text input or
// the tmux forwarders — a Hebrew user renaming a session, filtering, or typing
// into the drawer's shell has to get Hebrew back. The call sites are chosen so
// that is structurally true rather than merely intended: handleKey applies it
// below every text-owning branch, and routeToModal applies it only to dialogs
// that hold no textinput at all (TestNormalizedDialogsHoldNoTextInput). The two
// branches that match above the remap — the drawer's chrome and the launchpad —
// own no text of their own and forward nothing they matched.
//
// Both Code and Text are rewritten because upstream Key.String() returns Text
// verbatim whenever it is non-empty, and String() is what every switch here
// matches on.
func normalizeKey(msg tea.KeyPressMsg) tea.KeyPressMsg {
	// Ctrl/Alt chords already arrive with a Latin letter on every layout — the
	// modifier is carried separately from the character — so remapping one would
	// only corrupt it. Shift and the lock modifiers are fine to pass through.
	if msg.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModSuper|tea.ModHyper) != 0 {
		return msg
	}
	// Named keys ("enter", "down") carry multi-rune Text, and so does a ligature
	// key; a single rune is the only thing a layout table can speak about.
	r, size := utf8.DecodeRuneInString(msg.Text)
	if size == 0 || size != len(msg.Text) {
		return msg
	}
	us, ok := keylayout.ToUS(r)
	if !ok {
		return msg
	}
	msg.Code = us
	msg.Text = string(us)
	return msg
}
