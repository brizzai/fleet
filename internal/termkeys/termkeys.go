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
//
// It also turns off xterm's alternate-screen scroll (DECSET 1007), which is a
// key-reporting mode in the same sense: while the alternate screen is up and
// mouse reporting is off, a terminal with 1007 on *synthesizes arrow keys from
// the wheel*. fleet asks for no mouse reporting (see Home.chrome) and stays in
// the alternate screen, which is exactly that combination — so without this a
// trackpad scroll would arrive as a burst of Up/Down KeyPressMsg: walking the
// sidebar cursor and forking a preview capture per tick, and, with the drawer
// focused, walking the shell's readline history (arrows are not intercepted by
// handleTypingKey, so they reach forwardKeyToPane).
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

// Alternate-screen scroll (DECSET 1007) is saved and reset rather than blindly
// re-enabled on exit: the terminal's prior value is unknowable from here, so a
// DECSET in Restore would be wrong for anyone who started with it off. XTSAVE
// (Ps=s) / XTRESTORE (Ps=r) are the paired form xterm defines for exactly this.
// Literals for the same reason disableModifyOtherKeys is one — x/ansi carries no
// constant for 1007.
const (
	saveAltScreenScroll    = "\x1b[?1007s"
	disableAltScreenScroll = "\x1b[?1007l"
	restoreAltScreenScroll = "\x1b[?1007r"
)

// disablePreamble forces legacy key reporting across the two enhancement
// mechanisms a Ctrl+K could be intercepted by: modifyOtherKeys off, plus a
// flags=0 entry pushed onto the Kitty keyboard protocol stack
// (ansi.DisableKittyKeyboard) so CSI-u reporting is suppressed while preserving
// whatever state the shell/tmux had. It then saves and clears alternate-screen
// scroll, so the wheel stops being turned into arrow keys. Terminals without a
// mode ignore its bytes.
const disablePreamble = disableModifyOtherKeys + ansi.DisableKittyKeyboard +
	saveAltScreenScroll + disableAltScreenScroll

// disableMouseReporting turns off every mouse-reporting mode a tmux client can
// leave switched on in our terminal. fleet sets `mouse on` per session
// (tmux.Session.ApplyStatusBar), so an attach puts the outer terminal into
// DECSET 1000/1002/1003 + SGR 1006; the modes are reset in that same broad
// sweep because which ones tmux enabled is not knowable from here, and a reset
// for a mode that was never set is a no-op.
const disableMouseReporting = ansi.ResetModeMouseNormal + ansi.ResetModeMouseButtonEvent +
	ansi.ResetModeMouseAnyEvent + ansi.ResetModeMouseExtSgr

// Reassert re-applies what Disable asked for, for callers returning from a
// full-screen attach that handed the terminal to tmux and got it back dirty:
// fleet's attach ends by killing the tmux client (Ctrl+Q, see
// internal/tmux/pty.go), so tmux never runs its own cleanup and whatever it
// enabled stays on. It also turns mouse reporting off, which Disable never had
// to: Bubble Tea owns that mode via Home.chrome, and owns it only on the way in
// — its renderer emits a mouse-mode change only when the mode *differs* from
// the last view's, and fleet's is MouseModeNone on every frame including the
// first one after an attach. So nothing else will ever take reporting back off,
// and the next scroll is a MouseWheelMsg burst costing a full View() apiece.
//
// It is deliberately not a second call to Disable, because two of Disable's
// four sequences carry state and are not idempotent: XTSAVE (1007s) would
// overwrite the saved original with the already-cleared value, so Restore would
// hand the user back the wrong mode on exit, and ansi.DisableKittyKeyboard
// pushes onto the Kitty keyboard stack, which Restore pops exactly once — every
// extra push would leak an entry. What is left is the two sequences that are
// plain idempotent sets, plus the mouse reset.
func Reassert(w io.Writer) error {
	_, err := io.WriteString(w, disableModifyOtherKeys+disableAltScreenScroll+disableMouseReporting)
	return err
}

// Disable asks the terminal for legacy key reporting so modified keys (Ctrl+K
// in particular) arrive in their legacy encoding instead of as CSI-u sequences
// Bubble Tea v1 can't parse. Terminals that don't support a given mode ignore
// its sequence, so this is safe to call unconditionally.
func Disable(w io.Writer) error {
	_, err := io.WriteString(w, disablePreamble)
	return err
}

// Restore undoes Disable in reverse: it restores the saved alternate-screen
// scroll value, pops the Kitty keyboard entry we pushed (ansi.PopKittyKeyboard,
// a func so it can't be a package const) and resets modifyOtherKeys to the
// terminal's initial value. Call on exit to leave the terminal's key-reporting
// state as we found it.
func Restore(w io.Writer) error {
	_, err := io.WriteString(w, restoreAltScreenScroll+ansi.PopKittyKeyboard(1)+ansi.ResetModifyOtherKeys)
	return err
}
