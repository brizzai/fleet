package instancelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// flock locks belong to the open file description, so a second Acquire in the
// same process contends exactly like a second fleet process would.
func TestSecondAcquireIsRefusedWithTheHoldersPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "tui.lock")

	first, _, err := Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	second, pid, err := Acquire(path)
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("second Acquire err = %v, want ErrHeld", err)
	}
	if second != nil {
		t.Error("second Acquire returned a lock while the first is held")
	}
	if pid != os.Getpid() {
		t.Errorf("holder pid = %d, want %d", pid, os.Getpid())
	}

	first.Release()
	first.Release() // idempotent

	third, _, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	third.Release()
}

func TestReleaseOnNilLock(t *testing.T) {
	var l *Lock
	l.Release()
}
