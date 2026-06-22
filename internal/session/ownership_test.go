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

// TestUpdateHookStatusAdoptsForkDivergence reproduces the fork-of-a-fork bug: a
// `claude --resume <parent> --fork-session` reports the PARENT's id at SessionStart
// and only switches to its own id on the first prompt. The fork transcript copies
// the parent's history verbatim (same OLD first timestamp) and carries no
// logicalParentUuid, so BOTH sessionRotationVerdict signals miss — leaving the fork
// pinned to the parent id, so forking it re-forks the parent. With forkParentID
// known, the fork's own id must be adopted deterministically.
func TestUpdateHookStatusAdoptsForkDivergence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()

	// Parent A: a >10s conversation (so the proximity fallback can't bridge it).
	base := time.Now().Add(-time.Hour).UTC()
	writeTranscript(t, proj, "parent-aaa", base, base.Add(30*time.Minute))
	// Fork B: copies the parent's history verbatim — same OLD first timestamp, no
	// logicalParentUuid — exactly what --fork-session writes. Both rotation signals miss.
	writeTranscript(t, proj, "fork-bbb", base, base.Add(31*time.Minute))

	// Session launched as a fork of parent-aaa (forkParentID is set by Start()).
	s := &Session{ID: "x", ProjectPath: proj, Status: StatusRunning, forkParentID: "parent-aaa"}

	// SessionStart reports the PARENT's id (the fork hasn't diverged yet).
	s.UpdateHookStatus(&HookStatus{Status: "finished", SessionID: "parent-aaa", UpdatedAt: time.Now()}, true)
	if s.ClaudeSessionID != "parent-aaa" {
		t.Fatalf("pre-divergence: ClaudeSessionID=%q, want parent-aaa", s.ClaudeSessionID)
	}

	// First prompt: the fork diverges to its OWN id. Must be adopted (forking this
	// session should now use fork-bbb, not re-fork parent-aaa).
	changed := s.UpdateHookStatus(&HookStatus{Status: "running", SessionID: "fork-bbb", UpdatedAt: time.Now()}, true)
	if !changed {
		t.Errorf("fork divergence hook should register as changed")
	}
	if s.ClaudeSessionID != "fork-bbb" {
		t.Errorf("fork id not adopted: ClaudeSessionID=%q, want fork-bbb (forking it would re-fork the parent)", s.ClaudeSessionID)
	}
	if s.forkParentID != "" {
		t.Errorf("forkParentID should be cleared after divergence, got %q", s.forkParentID)
	}

	// After divergence the fork link is gone, so a genuine nested child is ignored again.
	writeTranscript(t, proj, "nested-ccc", base.Add(40*time.Minute), base.Add(41*time.Minute))
	if changed := s.UpdateHookStatus(&HookStatus{Status: "dead", SessionID: "nested-ccc", UpdatedAt: time.Now()}, true); changed {
		t.Errorf("post-divergence nested child should be ignored")
	}
	if s.ClaudeSessionID != "fork-bbb" {
		t.Errorf("nested child clobbered fork id: %q, want fork-bbb", s.ClaudeSessionID)
	}
}

// Review issue #2a: clearHookState() must clear forkParentID. Restart()/RespawnClaude()
// call clearHookState() without going through Start() (the only place forkParentID is
// set), so an un-diverged fork that survives a restart and resumes the parent id would
// otherwise re-claim owner==forkParent and re-arm the unconditional fork-adoption branch —
// the next foreign hook (incl. a nested child) would then be force-adopted.
func TestClearHookStateClearsForkParent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()

	// Un-diverged fork: launched as a fork of parent-aaa, claimed the parent's id at
	// SessionStart, never diverged (no first prompt yet).
	s := &Session{ID: "x", ProjectPath: proj, Status: StatusRunning, forkParentID: "parent-aaa", ownerSessionID: "parent-aaa"}

	s.clearHookState()
	if s.forkParentID != "" {
		t.Fatalf("clearHookState should clear forkParentID, got %q", s.forkParentID)
	}

	// After a restart the session re-claims the parent id on its next SessionStart
	// (owner was cleared). A nested child firing inside the old pre-divergence window
	// must NOT be force-adopted now that the fork link is gone.
	base := time.Now().Add(-time.Hour).UTC()
	writeTranscript(t, proj, "parent-aaa", base, base.Add(30*time.Minute))
	s.UpdateHookStatus(&HookStatus{Status: "finished", SessionID: "parent-aaa", UpdatedAt: time.Now()}, true)
	if s.ClaudeSessionID != "parent-aaa" {
		t.Fatalf("re-claim parent: ClaudeSessionID=%q, want parent-aaa", s.ClaudeSessionID)
	}

	// Nested child: fresh transcript, no descent from the parent (10min gap, no parent
	// link) → both rotation signals miss → must be rejected, not adopted.
	writeTranscript(t, proj, "nested-ccc", base.Add(40*time.Minute), base.Add(41*time.Minute))
	if changed := s.UpdateHookStatus(&HookStatus{Status: "running", SessionID: "nested-ccc", UpdatedAt: time.Now()}, true); changed {
		t.Errorf("nested child after restart should be ignored, not adopted")
	}
	if s.ClaudeSessionID != "parent-aaa" {
		t.Errorf("nested child clobbered id after restart: %q, want parent-aaa", s.ClaudeSessionID)
	}
}

// TestUpdateHookStatusDefersUndecidedRotation reproduces issue #23: a /clear-style
// rotation fires its SessionStart hook before the new transcript flushes a timestamped
// entry — the file opens with timestamp-less mode/permission-mode/file-history-snapshot
// header entries — so the proximity check is undecidable on that first hook. It must be
// DEFERRED (rejected without neg-caching), so once the transcript flushes its first real
// entry a later hook still adopts it. Before the fix the first attempt neg-cached the
// pair permanently, freezing the in-memory hook on the dead owner session.
func TestUpdateHookStatusDefersUndecidedRotation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()

	base := time.Now().Add(-time.Hour).UTC()
	ownerEnd := base.Add(30 * time.Minute)
	writeTranscript(t, proj, "owner-aaa", base, ownerEnd)
	// Rotated transcript exists but is header-only — no timestamped entry yet.
	writeTranscriptRaw(t, proj, "rot-bbb",
		`{"type":"mode"}`,
		`{"type":"permission-mode"}`,
		`{"type":"file-history-snapshot"}`,
	)

	s := &Session{ID: "x", ProjectPath: proj, Status: StatusRunning}
	s.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "owner-aaa", UpdatedAt: time.Now()}, true)

	// First rotated hook lands before the transcript flushes a timestamp → undecided.
	// Must be deferred: not adopted, and crucially not neg-cached.
	if changed := s.UpdateHookStatus(&HookStatus{Status: "running", SessionID: "rot-bbb", UpdatedAt: time.Now()}, true); changed {
		t.Errorf("undecided rotation should be deferred, not adopted yet")
	}
	if s.ClaudeSessionID != "owner-aaa" {
		t.Errorf("undecided rotation prematurely adopted: %q, want owner-aaa", s.ClaudeSessionID)
	}

	// The transcript now flushes its first real (timestamped) entry, handing off within
	// the proximity window. The deferred id must now be adopted — proving the first,
	// undecidable attempt did NOT permanently neg-cache it.
	writeTranscript(t, proj, "rot-bbb", ownerEnd.Add(-2*time.Millisecond), ownerEnd.Add(10*time.Minute))
	changed := s.UpdateHookStatus(&HookStatus{Status: "running", SessionID: "rot-bbb", UpdatedAt: time.Now()}, true)
	if !changed {
		t.Errorf("rotation should be adopted once the transcript flushes a timestamp")
	}
	if s.ClaudeSessionID != "rot-bbb" {
		t.Errorf("rotation not adopted after flush: %q, want rot-bbb", s.ClaudeSessionID)
	}
}

// TestUpdateHookStatusCapsUndecidedRotation guards the retry cap on rotationUnknown: a
// pair that can never decide (here a permanently missing owner transcript, so
// sessionRotationVerdict's ownerLast is always zero) must not defer forever and rescan
// transcripts on every worker pass. After rotationUndecidedRetryCap consecutive
// undecided cycles it falls back to the neg-cache.
func TestUpdateHookStatusCapsUndecidedRotation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()

	// Owner claims ownership, but its transcript is never written — so the proximity
	// check can't read ownerLast and the (owner, foreign) pair stays undecidable.
	s := &Session{ID: "abcd1234-1700000000", ProjectPath: proj, Status: StatusRunning}
	s.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "owner-aaa", UpdatedAt: time.Now()}, true)

	// Foreign session has a real transcript, but with no readable owner transcript the
	// verdict is rotationUnknown every cycle.
	base := time.Now().Add(-time.Hour).UTC()
	writeTranscript(t, proj, "foreign-bbb", base, base.Add(time.Minute))

	// The first rotationUndecidedRetryCap cycles defer without neg-caching.
	for i := 0; i < rotationUndecidedRetryCap; i++ {
		s.UpdateHookStatus(&HookStatus{Status: "running", SessionID: "foreign-bbb", UpdatedAt: time.Now()}, true)
		if s.rotRejectForeign == "foreign-bbb" {
			t.Fatalf("neg-cached too early at cycle %d (cap=%d)", i+1, rotationUndecidedRetryCap)
		}
	}
	// One more undecided cycle exceeds the cap → neg-cache.
	s.UpdateHookStatus(&HookStatus{Status: "running", SessionID: "foreign-bbb", UpdatedAt: time.Now()}, true)
	if s.rotRejectForeign != "foreign-bbb" {
		t.Errorf("undecided pair past the retry cap should be neg-cached, got rotRejectForeign=%q", s.rotRejectForeign)
	}
	// The owner id is never clobbered by the undecidable foreign session.
	if s.ClaudeSessionID != "owner-aaa" {
		t.Errorf("owner clobbered by undecided foreign: %q, want owner-aaa", s.ClaudeSessionID)
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
