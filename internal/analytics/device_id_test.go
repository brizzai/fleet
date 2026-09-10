package analytics

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testMachineID = "0123456789abcdef0123456789abcdef"

// withMachineIDPaths points readMachineID at paths for the rest of the test.
func withMachineIDPaths(t *testing.T, paths ...string) {
	t.Helper()
	orig := machineIDPaths
	machineIDPaths = paths
	t.Cleanup(func() { machineIDPaths = orig })
}

func writeFixture(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadMachineIDSkipsUnusableFiles(t *testing.T) {
	dir := t.TempDir()
	valid := writeFixture(t, filepath.Join(dir, "valid"), testMachineID+"\n")
	for _, tc := range []struct {
		name  string
		paths []string
		want  string
	}{
		{"valid", []string{valid}, testMachineID},
		{"missing file falls through", []string{filepath.Join(dir, "absent"), valid}, testMachineID},
		{"first boot falls through", []string{writeFixture(t, filepath.Join(dir, "uninit"), "uninitialized\n"), valid}, testMachineID},
		{"empty, as container images ship it", []string{writeFixture(t, filepath.Join(dir, "empty"), "")}, ""},
		{"not hex", []string{writeFixture(t, filepath.Join(dir, "junk"), strings.Repeat("z", 32))}, ""},
	} {
		withMachineIDPaths(t, tc.paths...)
		if got := readMachineID(); got != tc.want {
			t.Errorf("%s: readMachineID() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A readable machine ID replaces a cached ID from an older build — the
// guessable hostname hash — and what goes out is keyed: never the raw ID, never
// the plain hash every other app would compute.
func TestDeviceIDPrefersMachineIDOverTheCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cache := writeFixture(t, filepath.Join(home, ".config", "fleet", "device_id"), "oldhostnamehash")

	withMachineIDPaths(t, writeFixture(t, filepath.Join(t.TempDir(), "machine-id"), testMachineID+"\n"))
	id := getOrCreateDeviceID()
	if id != machineIDHash(testMachineID) {
		t.Fatalf("device ID = %q, want the machine ID's keyed hash", id)
	}
	if plain := fmt.Sprintf("%x", sha256.Sum256([]byte(testMachineID))); id == plain || strings.Contains(id, testMachineID) {
		t.Errorf("device ID %q is the machine ID or its plain hash", id)
	}
	if data, _ := os.ReadFile(cache); string(data) != id {
		t.Errorf("cache = %q, want it rewritten to %q", data, id)
	}

	// Without a machine ID (macOS, or a Linux box with none readable) the cache
	// stands.
	withMachineIDPaths(t)
	if got := getOrCreateDeviceID(); got != id {
		t.Errorf("without a machine ID, device ID = %q, want the cached %q", got, id)
	}
}
