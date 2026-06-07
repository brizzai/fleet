package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTranscript writes a minimal Claude JSONL transcript with the given entry
// timestamps under the HOME-rooted projects dir for projectPath, mirroring how
// Claude Code lays out ~/.claude/projects/<encoded-cwd>/<session>.jsonl.
func writeTranscript(t *testing.T, projectPath, sessionID string, stamps ...time.Time) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".claude", "projects", ClaudeProjectDirName(projectPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, ts := range stamps {
		b.WriteString(`{"type":"user","timestamp":"`)
		b.WriteString(ts.UTC().Format(time.RFC3339Nano))
		b.WriteString(`"}` + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateHookStatusAdoptsRotatedSession reproduces the stuck-running bug: when
// Claude /clear starts a fresh conversation under a new id that hands off within
// milliseconds (no parent link). The new id's hooks must be adopted via the
// timestamp-proximity signal, not dropped as foreign — otherwise the in-memory
// hook freezes at the old session's last event and the resume id goes stale.
func TestUpdateHookStatusAdoptsRotatedSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()

	base := time.Now().Add(-time.Hour).UTC()
	ownerEnd := base.Add(30 * time.Minute)
	// Owner ends; the rotated transcript begins a hair before the owner's last
	// entry (the real handoff is sub-millisecond and slightly out of order).
	writeTranscript(t, proj, "owner-aaa", base, ownerEnd)
	writeTranscript(t, proj, "rot-bbb", ownerEnd.Add(-2*time.Millisecond), ownerEnd.Add(10*time.Minute))

	s := &Session{ID: "x", ProjectPath: proj, Status: StatusRunning}

	// Owner claims ownership.
	s.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "owner-aaa", UpdatedAt: time.Now()}, true)
	if got := s.GetHookStatus(); got != "waiting" {
		t.Fatalf("owner hook not applied: hook=%q", got)
	}

	// Rotated session reports finished — must be adopted (continuation).
	changed := s.UpdateHookStatus(&HookStatus{Status: "finished", SessionID: "rot-bbb", UpdatedAt: time.Now()}, true)
	if !changed {
		t.Errorf("rotated hook should register as changed")
	}
	if got := s.GetHookStatus(); got != "finished" {
		t.Errorf("rotated hook not adopted: hook=%q, want finished", got)
	}
	if s.ClaudeSessionID != "rot-bbb" {
		t.Errorf("resume id not updated to rotated session: %q, want rot-bbb", s.ClaudeSessionID)
	}
}

// TestUpdateHookStatusIgnoresNestedChild guards the other side: a concurrent
// nested child claude (eval harness) starts mid-way through the owner's life and
// the owner keeps writing past it. Its transcript does not continue the owner's,
// so its hooks must still be ignored.
func TestUpdateHookStatusIgnoresNestedChild(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()

	base := time.Now().Add(-time.Hour).UTC()
	writeTranscript(t, proj, "owner-aaa", base, base.Add(time.Hour)) // long-lived owner
	writeTranscript(t, proj, "child-bbb", base.Add(30*time.Minute), base.Add(31*time.Minute))

	s := &Session{ID: "x", ProjectPath: proj, Status: StatusRunning}
	s.UpdateHookStatus(&HookStatus{Status: "running", SessionID: "owner-aaa", UpdatedAt: time.Now()}, true)

	// Child reports dead — must be ignored; owner state preserved.
	if changed := s.UpdateHookStatus(&HookStatus{Status: "dead", SessionID: "child-bbb", UpdatedAt: time.Now()}, true); changed {
		t.Errorf("nested child hook should be ignored (changed=true)")
	}
	if got := s.GetHookStatus(); got != "running" {
		t.Errorf("owner hook clobbered by nested child: hook=%q, want running", got)
	}
	if s.ClaudeSessionID != "owner-aaa" {
		t.Errorf("resume id clobbered by nested child: %q, want owner-aaa", s.ClaudeSessionID)
	}
}

// writeTranscriptRaw writes arbitrary JSONL lines for a session under the
// HOME-rooted projects dir, for tests that need uuid / compact_boundary entries.
func writeTranscriptRaw(t *testing.T, projectPath, sessionID string, lines ...string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".claude", "projects", ClaudeProjectDirName(projectPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateHookStatusAdoptsLinkedRotation covers continue/resume/fork: the new
// transcript opens with a compact_boundary whose logicalParentUuid points at the
// owner's tail uuid, and its first timestamp is OLD (preserved history) — so
// proximity would miss it. The deterministic uuid link must adopt it anyway.
func TestUpdateHookStatusAdoptsLinkedRotation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()

	old := time.Now().Add(-4 * time.Hour).UTC().Format(time.RFC3339Nano)
	// Owner transcript: its tail entry carries uuid "PARENT-TAIL".
	writeTranscriptRaw(t, proj, "owner-link",
		`{"type":"user","timestamp":"`+old+`","uuid":"u1"}`,
		`{"type":"assistant","timestamp":"`+old+`","uuid":"PARENT-TAIL"}`,
	)
	// Rotated transcript: opens with a compact_boundary linking to PARENT-TAIL,
	// first timestamp far in the past (proximity-defeating).
	writeTranscriptRaw(t, proj, "child-link",
		`{"type":"system","subtype":"compact_boundary","timestamp":"`+old+`","logicalParentUuid":"PARENT-TAIL","uuid":"cb1"}`,
		`{"type":"user","timestamp":"`+time.Now().UTC().Format(time.RFC3339Nano)+`","uuid":"u2"}`,
	)

	s := &Session{ID: "x", ProjectPath: proj, Status: StatusRunning}
	s.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "owner-link", UpdatedAt: time.Now()}, true)

	changed := s.UpdateHookStatus(&HookStatus{Status: "finished", SessionID: "child-link", UpdatedAt: time.Now()}, true)
	if !changed || s.GetHookStatus() != "finished" || s.ClaudeSessionID != "child-link" {
		t.Errorf("linked rotation not adopted: changed=%v hook=%q resumeID=%q (want true/finished/child-link)",
			changed, s.GetHookStatus(), s.ClaudeSessionID)
	}
}

// TestUpdateHookStatusIgnoresNestedChildWithCompaction guards the uuid signal's
// precision: a nested child that compacted has a compact_boundary, but its
// logicalParentUuid points at ITS OWN parent, not the owner — so it must NOT be
// adopted.
func TestUpdateHookStatusIgnoresNestedChildWithCompaction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()

	ownerTs := time.Now().Add(-4 * time.Hour).UTC().Format(time.RFC3339Nano)
	childTs := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano) // >10s from owner → proximity also rejects
	writeTranscriptRaw(t, proj, "owner-x",
		`{"type":"user","timestamp":"`+ownerTs+`","uuid":"owner-only-uuid"}`,
	)
	// Child links to a uuid that does NOT exist in the owner transcript, and its
	// first entry is far from the owner's last (so neither signal adopts it).
	writeTranscriptRaw(t, proj, "child-x",
		`{"type":"system","subtype":"compact_boundary","timestamp":"`+childTs+`","logicalParentUuid":"some-other-parent","uuid":"cb1"}`,
	)

	s := &Session{ID: "x", ProjectPath: proj, Status: StatusRunning}
	s.UpdateHookStatus(&HookStatus{Status: "running", SessionID: "owner-x", UpdatedAt: time.Now()}, true)

	if changed := s.UpdateHookStatus(&HookStatus{Status: "dead", SessionID: "child-x", UpdatedAt: time.Now()}, true); changed {
		t.Errorf("nested child with foreign compaction link should be ignored")
	}
	if s.GetHookStatus() != "running" || s.ClaudeSessionID != "owner-x" {
		t.Errorf("owner clobbered: hook=%q resumeID=%q", s.GetHookStatus(), s.ClaudeSessionID)
	}
}
