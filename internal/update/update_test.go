package update

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// canReplace's failure must stay wrapped around ErrNotReplaceable: the
// auto-update caller in cmd/fleet distinguishes "this install can never
// self-update" from a real failure by errors.Is, and logs/reports it as a skip.
// Losing the wrap would silently restore the hourly ERROR line in debug.log
// (which the bug-report flow pastes into public issues) for every packaged
// install.
func TestCanReplaceWrapsErrNotReplaceable(t *testing.T) {
	dir := t.TempDir()
	readOnly := filepath.Join(dir, "ro")
	if err := os.Mkdir(readOnly, 0o555); err != nil {
		t.Fatal(err)
	}
	// Some CI images run as root, where a 0555 dir is still writable — the
	// probe would succeed and the assertion below would be vacuous.
	if f, err := os.CreateTemp(readOnly, "writable-check-*"); err == nil {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		t.Skip("running as a user who can write to a 0555 dir (root?)")
	}

	err := canReplace(filepath.Join(readOnly, "fleet"))
	if err == nil {
		t.Fatal("canReplace on a non-writable dir returned nil")
	}
	if !errors.Is(err, ErrNotReplaceable) {
		t.Errorf("errors.Is(err, ErrNotReplaceable) = false, want true; err = %v", err)
	}
}

// The probe must not leave its temp file next to the binary — it runs on every
// update check, including the ones that go on to succeed.
func TestCanReplaceCleansUpProbeFile(t *testing.T) {
	dir := t.TempDir()
	if err := canReplace(filepath.Join(dir, "fleet")); err != nil {
		t.Fatalf("canReplace on a writable dir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("probe left %d file(s) behind: %v", len(entries), entries)
	}
}
