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
	// emits the flags-omitted form (\x1b[>u), equivalent to an explicit 0.
	if got, want := b.String(), "\x1b[>4;0m\x1b[>u"; got != want {
		t.Errorf("Disable wrote %q, want %q", got, want)
	}
}

func TestRestoreUndoesDisableSymmetrically(t *testing.T) {
	var b strings.Builder
	if err := Restore(&b); err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	// Pop the Kitty entry we pushed + reset modifyOtherKeys to default;
	// ansi.PopKittyKeyboard(1) emits the explicit count form (\x1b[<1u).
	if got, want := b.String(), "\x1b[<1u\x1b[>4m"; got != want {
		t.Errorf("Restore wrote %q, want %q", got, want)
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
