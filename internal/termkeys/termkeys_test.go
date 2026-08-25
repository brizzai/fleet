package termkeys

import (
	"strings"
	"testing"
)

func TestDisableWritesLegacyKeyReporting(t *testing.T) {
	var b strings.Builder
	if err := Disable(&b); err != nil {
		t.Fatalf("Disable returned error: %v", err)
	}
	// modifyOtherKeys off + push Kitty flags=0 (legacy); ansi.DisableKittyKeyboard
	// emits the flags-omitted form (\x1b[>u), equivalent to an explicit 0. Then
	// save + clear alternate-screen scroll (1007), so the terminal stops turning
	// the wheel into arrow keys while fleet is on the alternate screen.
	if got, want := b.String(), "\x1b[>4;0m\x1b[>u\x1b[?1007s\x1b[?1007l"; got != want {
		t.Errorf("Disable wrote %q, want %q", got, want)
	}
}

func TestRestoreUndoesDisableSymmetrically(t *testing.T) {
	var b strings.Builder
	if err := Restore(&b); err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	// Restore 1007 to whatever Disable saved, then pop the Kitty entry we pushed
	// + reset modifyOtherKeys to default; ansi.PopKittyKeyboard(1) emits the
	// explicit count form (\x1b[<1u).
	if got, want := b.String(), "\x1b[?1007r\x1b[<1u\x1b[>4m"; got != want {
		t.Errorf("Restore wrote %q, want %q", got, want)
	}
}

// TestRestoreNeverForcesAltScreenScrollOn pins the save/restore pairing rather
// than the bytes: Disable clears mode 1007, and the obvious "undo" is a DECSET
// on the way out — which silently turns the mode ON for every user who started
// with it off, since the terminal's prior value is unknowable from here. XTSAVE
// (1007s) / XTRESTORE (1007r) is the only form that leaves both kinds of user
// as they were, so a DECSET appearing in Restore is a regression, not a cleanup.
func TestRestoreNeverForcesAltScreenScrollOn(t *testing.T) {
	var b strings.Builder
	if err := Restore(&b); err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	if strings.Contains(b.String(), "\x1b[?1007h") {
		t.Errorf("Restore wrote a DECSET for 1007 (%q); it must restore the saved "+
			"value with \\x1b[?1007r, not force the mode on", b.String())
	}
	if !strings.Contains(b.String(), restoreAltScreenScroll) {
		t.Errorf("Restore wrote %q, missing the 1007 XTRESTORE — Disable saved a "+
			"value that nothing puts back", b.String())
	}
}

// errWriter always fails, to exercise the error path.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errFailed }

var errFailed = &writeError{}

type writeError struct{}

func (*writeError) Error() string { return "write failed" }

func TestDisablePropagatesWriteError(t *testing.T) {
	if err := Disable(errWriter{}); err == nil {
		t.Error("expected error from failing writer, got nil")
	}
}

func TestRestorePropagatesWriteError(t *testing.T) {
	if err := Restore(errWriter{}); err == nil {
		t.Error("expected error from failing writer, got nil")
	}
}

func TestReassertTurnsMouseReportingOff(t *testing.T) {
	var b strings.Builder
	if err := Reassert(&b); err != nil {
		t.Fatalf("Reassert returned error: %v", err)
	}
	// Clear alternate-screen scroll (no save, see below), then reset the four
	// mouse-reporting modes a tmux client may have left on. No key-reporting
	// sequence: see TestReassertLeavesKeyReportingToBubbleTea.
	if got, want := b.String(), "\x1b[?1007l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l"; got != want {
		t.Errorf("Reassert wrote %q, want %q", got, want)
	}
}

// TestReassertRepeatsNothingStateful is the reason Reassert exists as its own
// function rather than a second call to Disable. Two of Disable's sequences
// carry state across calls: XTSAVE (1007s) has a single save slot, so a repeat
// would overwrite the user's original value with the already-cleared one and
// Restore would hand back the wrong mode on exit; DisableKittyKeyboard pushes
// onto a stack Restore pops exactly once, so a repeat leaks an entry per attach.
// Reassert runs once per attach, so either would compound all session long.
func TestReassertRepeatsNothingStateful(t *testing.T) {
	var b strings.Builder
	if err := Reassert(&b); err != nil {
		t.Fatalf("Reassert returned error: %v", err)
	}
	if strings.Contains(b.String(), saveAltScreenScroll) {
		t.Errorf("Reassert wrote the 1007 XTSAVE (%q); it would overwrite the value "+
			"Disable saved with the cleared one, so Restore leaves the terminal wrong", b.String())
	}
	// ansi.DisableKittyKeyboard emits the flags-omitted push form \x1b[>u.
	if strings.Contains(b.String(), "\x1b[>u") {
		t.Errorf("Reassert pushed a Kitty keyboard entry (%q); Restore pops exactly "+
			"one, so every attach past the first leaks an entry", b.String())
	}
}

func TestReassertPropagatesWriteError(t *testing.T) {
	if err := Reassert(errWriter{}); err == nil {
		t.Error("expected error from failing writer, got nil")
	}
}

// TestReassertLeavesKeyReportingToBubbleTea pins the omission, which is easy to
// read as a bug and "fix" back in. Reassert cannot restore legacy key reporting:
// tea.Exec calls RestoreTerminal as soon as attachCmd.Run returns (v2.0.7
// exec.go:125), reaching cursedRenderer.start() with a non-nil lastView, which
// unconditionally writes SetModifyOtherKeys2 at cursed_renderer.go:136. Anything
// this wrote would be overwritten microseconds later — dead bytes plus a comment
// the terminal contradicts.
func TestReassertLeavesKeyReportingToBubbleTea(t *testing.T) {
	var b strings.Builder
	if err := Reassert(&b); err != nil {
		t.Fatalf("Reassert returned error: %v", err)
	}
	if strings.Contains(b.String(), disableModifyOtherKeys) {
		t.Errorf("Reassert wrote modifyOtherKeys-off (%q); Bubble Tea overwrites it with "+
			"mode 2 microseconds later, so it asserts a state the terminal will not be in", b.String())
	}
}
