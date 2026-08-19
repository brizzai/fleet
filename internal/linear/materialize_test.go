package linear

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStateWriteIsExactlyOnce pins the durable half of the exactly-once claim.
//
// CLAUDE.md said "meta.json records state_write so it stays exactly-once", but
// nothing ever read meta.json before mutating — the only guard was inFlight,
// which is an in-process concurrency lock and says nothing about a retry, a
// second fleet process, or a rerun after a crash between the mutation and the
// write.
//
// The mutation is the one write fleet makes to someone's board, and re-asserting
// "started" after a human has moved the issue on is the worst thing it could do.
func TestStateWriteIsExactlyOnce(t *testing.T) {
	dir := t.TempDir()

	if _, ok := readMeta(dir); ok {
		t.Fatal("empty dir must report no record")
	}

	writeMeta(dir, meta{Identifier: "BRZ-1", StateWrite: "done", MovedTo: "In Progress"})
	got, ok := readMeta(dir)
	if !ok {
		t.Fatal("a written record must read back")
	}
	if got.StateWrite != "done" || got.MovedTo != "In Progress" {
		t.Fatalf("record did not round-trip: %+v", got)
	}

	// An unreadable record must re-arm the write, never disable it: treating
	// corruption as "already done" would silently kill the mutation forever the
	// first time a truncated write landed.
	if err := os.WriteFile(filepath.Join(dir, metaFile), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readMeta(dir); ok {
		t.Error("a corrupt record must report no record, so the write re-arms")
	}
}

// TestStateWriteSkippedRecordDoesNotBlock keeps the guard narrow. Only "done"
// means the issue was moved; "skipped" and "failed" must both leave a later
// attempt free to run, or a single failed mutation would latch off for good.
func TestStateWriteSkippedRecordDoesNotBlock(t *testing.T) {
	for _, state := range []string{"skipped", "failed", ""} {
		dir := t.TempDir()
		writeMeta(dir, meta{Identifier: "BRZ-1", StateWrite: state})
		m, ok := readMeta(dir)
		if !ok {
			t.Fatalf("%q: record should read back", state)
		}
		if m.StateWrite == "done" {
			t.Errorf("%q must not read as done", state)
		}
	}
}
