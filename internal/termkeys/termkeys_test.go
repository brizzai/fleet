package termkeys

import (
	"strings"
	"testing"
)

func TestDisableWritesModifyOtherKeysOff(t *testing.T) {
	var b strings.Builder
	if err := Disable(&b); err != nil {
		t.Fatalf("Disable returned error: %v", err)
	}
	if got, want := b.String(), "\x1b[>4;0m\x1b[<u"; got != want {
		t.Errorf("Disable wrote %q, want %q", got, want)
	}
}

func TestRestoreWritesModifyOtherKeysReset(t *testing.T) {
	var b strings.Builder
	if err := Restore(&b); err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	if got, want := b.String(), "\x1b[>4m"; got != want {
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
