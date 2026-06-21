// Package termkeys controls terminal key-reporting modes that affect how
// modified keys (e.g. Ctrl+K) reach the TUI.
//
// Some terminals — and tmux configs such as gpakosz/.tmux, which enable
// `extended-keys` for iTerm/xterm — report "other" modified keys using the
// CSI-u / xterm modifyOtherKeys encoding instead of the legacy control byte.
// A Ctrl+K then arrives as `ESC[107;5u` (or `ESC[27;5;107~`) rather than the
// 0x0b byte. Bubble Tea v1 cannot decode those sequences, so it silently drops
// the keystroke: the Ctrl+K command palette appears to do nothing. (Ctrl+C and
// Ctrl+Q still work because modifyOtherKeys mode 1 keeps well-known combos in
// their legacy encoding, which is why only Ctrl+K looks broken.)
//
// Asking the terminal for legacy key reporting forces Ctrl+K back to 0x0b, so
// the palette opens reliably. Alt+<digit> slot bindings benefit the same way.
package termkeys

import "io"

// Escape sequences that select legacy vs. enhanced key reporting.
const (
	// disableModifyOtherKeys sets the xterm modifyOtherKeys resource (XTMODKEYS,
	// Ps=4) to mode 0 (off): keys are reported with their legacy encoding.
	disableModifyOtherKeys = "\x1b[>4;0m"
	// resetModifyOtherKeys (Pm omitted) restores the terminal's initial value.
	resetModifyOtherKeys = "\x1b[>4m"
	// pushKittyLegacy pushes flags=0 (no progressive enhancements) onto the Kitty
	// keyboard protocol stack, forcing legacy CSI-u-free key reporting while
	// preserving whatever state the shell/tmux had. popKittyKeyboard removes that
	// entry on exit, restoring the prior state symmetrically. Terminals without
	// the protocol ignore both sequences.
	pushKittyLegacy  = "\x1b[>0u"
	popKittyKeyboard = "\x1b[<u"
)

// disablePreamble forces legacy key reporting across the two enhancement
// mechanisms a Ctrl+K could be intercepted by.
const disablePreamble = disableModifyOtherKeys + pushKittyLegacy

// restorePreamble undoes disablePreamble in reverse: pop our Kitty entry, then
// reset modifyOtherKeys to the terminal's initial value.
const restorePreamble = popKittyKeyboard + resetModifyOtherKeys

// Disable asks the terminal for legacy key reporting so modified keys (Ctrl+K
// in particular) arrive in their legacy encoding instead of as CSI-u sequences
// Bubble Tea v1 can't parse. Terminals that don't support a given mode ignore
// its sequence, so this is safe to call unconditionally.
func Disable(w io.Writer) error {
	_, err := io.WriteString(w, disablePreamble)
	return err
}

// Restore undoes Disable: it pops the Kitty keyboard entry we pushed and resets
// modifyOtherKeys to the terminal's initial value. Call on exit to leave the
// terminal's key-reporting state as we found it.
func Restore(w io.Writer) error {
	_, err := io.WriteString(w, restorePreamble)
	return err
}
