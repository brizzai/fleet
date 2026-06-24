// Package termkeys controls terminal key-reporting modes that affect how
// modified keys (e.g. Ctrl+K) reach the TUI.
//
// Some terminals — and tmux configs such as gpakosz/.tmux, which enable
// `extended-keys` for iTerm/xterm — report "other" modified keys using the
// CSI-u / xterm modifyOtherKeys encoding instead of the legacy control byte.
// A Ctrl+K then arrives as `ESC[107;5u` (or `ESC[27;5;107~`) rather than the
// 0x0b byte. Bubble Tea v1 cannot decode those sequences, so it silently drops
// the keystroke: the Ctrl+K command palette appears to do nothing. Ctrl+Q still
// detaches under these modes only because the PTY layer explicitly decodes its
// CSI-u / modifyOtherKeys forms (see ctrlQSequences in internal/tmux/pty.go);
// Ctrl+K has no such handling, which is why only it looked broken.
//
// Asking the terminal for legacy key reporting forces Ctrl+K back to 0x0b, so
// the palette opens reliably. Alt+<digit> slot bindings benefit the same way.
package termkeys

import (
	"io"

	"github.com/charmbracelet/x/ansi"
)

// disableModifyOtherKeys sets the xterm modifyOtherKeys resource (XTMODKEYS,
// Ps=4) to mode 0 (off): keys are reported with their legacy encoding. x/ansi
// only exposes this as the deprecated ansi.DisableModifyOtherKeys (its
// suggested replacement, ResetModifyOtherKeys, resets to the terminal default
// instead of forcing off), so we keep the literal here.
const disableModifyOtherKeys = "\x1b[>4;0m"

// disablePreamble forces legacy key reporting across the two enhancement
// mechanisms a Ctrl+K could be intercepted by: modifyOtherKeys off, plus a
// flags=0 entry pushed onto the Kitty keyboard protocol stack
// (ansi.DisableKittyKeyboard) so CSI-u reporting is suppressed while preserving
// whatever state the shell/tmux had. Terminals without a mode ignore its bytes.
const disablePreamble = disableModifyOtherKeys + ansi.DisableKittyKeyboard

// Disable asks the terminal for legacy key reporting so modified keys (Ctrl+K
// in particular) arrive in their legacy encoding instead of as CSI-u sequences
// Bubble Tea v1 can't parse. Terminals that don't support a given mode ignore
// its sequence, so this is safe to call unconditionally.
func Disable(w io.Writer) error {
	_, err := io.WriteString(w, disablePreamble)
	return err
}

// Restore undoes Disable in reverse: it pops the Kitty keyboard entry we pushed
// (ansi.PopKittyKeyboard, a func so it can't be a package const) and resets
// modifyOtherKeys to the terminal's initial value. Call on exit to leave the
// terminal's key-reporting state as we found it.
func Restore(w io.Writer) error {
	_, err := io.WriteString(w, ansi.PopKittyKeyboard(1)+ansi.ResetModifyOtherKeys)
	return err
}
