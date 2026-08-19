package linear

import (
	"os"
	"path/filepath"
	"strings"
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

// TestPriorStateWriteIsNotReportedAsThisRunsMove separates the record from the
// report.
//
// res.StateMoved is what cmd/fleet/worktree.go prints as "Moved %s to its team's
// started state", and what ticketStatusLine shows in the TUI. Copying a prior
// run's MovedTo into it made a re-materialization claim a write to someone's
// board that never happened on that run — a false statement about a mutation,
// which is the one thing this feature must never make. The record must still
// travel, or the exactly-once guard forgets what it knew.
//
// Asserted against the source because the property IS about the source: only
// the branch that performs the mutation may assign the reported field, and
// Materialize cannot be driven here without a live API.
func TestPriorStateWriteIsNotReportedAsThisRunsMove(t *testing.T) {
	src, err := os.ReadFile("materialize.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	const marker = `case hadPrior && prior.StateWrite == "done":`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("the carry-forward branch is gone — this guard is stale, %s not found", marker)
	}
	rest := body[i+len(marker):]
	j := strings.Index(rest, "case o.MoveState:")
	if j < 0 {
		t.Fatal("could not find the end of the carry-forward branch")
	}
	branch := stripLineComments(rest[:j])

	if strings.Contains(branch, "res.StateMoved") {
		t.Error("the carry-forward branch assigns res.StateMoved, which the caller " +
			"prints as \"Moved ... to its team's started state\". This run performed no " +
			"mutation; only the branch that calls MoveToStarted may set it.")
	}
	if !strings.Contains(branch, "m.MovedTo") {
		t.Error("the carry-forward branch must still carry MovedTo into the new record, " +
			"or the exactly-once guard loses what it knew")
	}
}

// stripLineComments removes // comments so a source guard scans code, not prose.
// The branch above is documented with a comment that names the very field it
// must not assign, and without this the guard would fail on its own rationale.
func stripLineComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
