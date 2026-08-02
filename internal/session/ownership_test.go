package session

import (
	"os"
	"os/exec"
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
		if s.rotRejects["foreign-bbb"] != nil {
			t.Fatalf("neg-cached too early at cycle %d (cap=%d)", i+1, rotationUndecidedRetryCap)
		}
	}
	// One more undecided cycle exceeds the cap → neg-cache.
	s.UpdateHookStatus(&HookStatus{Status: "running", SessionID: "foreign-bbb", UpdatedAt: time.Now()}, true)
	if s.rotRejects["foreign-bbb"] == nil {
		t.Errorf("undecided pair past the retry cap should be neg-cached, got rotRejects=%v", s.rotRejects)
	}
	// The owner id is never clobbered by the undecidable foreign session.
	if s.ClaudeSessionID != "owner-aaa" {
		t.Errorf("owner clobbered by undecided foreign: %q, want owner-aaa", s.ClaudeSessionID)
	}
}

// otherLivePID returns the pid of a live process that is not the test's own.
// Stands in for a nested agent running beside the owner.
func otherLivePID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn long-lived process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd.Process.Pid
}

// reapedPID returns the pid of a process that has run and been waited on, so it is
// reliably gone. Stands in for an agent whose conversation ended.
func reapedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/usr/bin/true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn throwaway process: %v", err)
	}
	return cmd.Process.Pid
}

// negCachedSession returns a session whose live-bbb hook has been rejected against
// owner-aaa and neg-cached — the frozen state issue #226 was reported in — with the
// recovery clock wound back so the next worker-path hook rechecks immediately.
// ownerPID/livePID are the agent processes recorded for each conversation (0 = a
// status file older than the field, which forces the transcript fallback); equal
// pids are an in-process rotation. ownerLast/liveLast set where each conversation
// last wrote.
func negCachedSession(t *testing.T, ownerPID, livePID int, ownerLast, liveLast time.Time) (*Session, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()

	// No parent link, and the live session's FIRST entry sits hours before the
	// owner's last — a handoff gap way past rotationHandoffWindow — so
	// sessionRotationVerdict rejects the pair however close their last writes are.
	writeTranscript(t, proj, "owner-aaa", ownerLast.Add(-3*time.Hour), ownerLast)
	writeTranscript(t, proj, "live-bbb", ownerLast.Add(-2*time.Hour), liveLast)

	s := &Session{ID: "x", ProjectPath: proj, Status: StatusRunning}
	s.UpdateHookStatus(&HookStatus{Status: "running", SessionID: "owner-aaa", UpdatedAt: time.Now(), AgentPID: ownerPID}, true)
	s.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "live-bbb", UpdatedAt: time.Now(), AgentPID: livePID}, true)
	if s.rotRejects["live-bbb"] == nil {
		t.Fatalf("setup: foreign id not neg-cached (rotRejects=%v)", s.rotRejects)
	}
	// negCacheRotation stamps the recovery clock; rewind it so the next hook is due.
	s.mu.Lock()
	s.rotRejects["live-bbb"].checkedAt = time.Time{}
	s.mu.Unlock()
	return s, proj
}

// TestUpdateHookStatusRecoversWhenOwnerProcessGone covers issue #226: a rotation we
// could not prove gets neg-cached, and nothing ever releases the owner — an
// in-process rotation fires no SessionEnd for the id it abandons. Every hook from
// the live conversation is then dropped, freezing the status and the id that
// fork/restart resume. A dead owner process ends the argument.
func TestUpdateHookStatusRecoversWhenOwnerProcessGone(t *testing.T) {
	now := time.Now().UTC()
	s, _ := negCachedSession(t, reapedPID(t), os.Getpid(), now.Add(-2*time.Hour), now.Add(-time.Minute))

	changed := s.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "live-bbb", UpdatedAt: time.Now(), AgentPID: os.Getpid()}, true)
	if !changed {
		t.Errorf("dead owner process: hook should register as changed")
	}
	if got := s.GetHookStatus(); got != "waiting" {
		t.Errorf("dead owner process: hook not adopted: %q, want waiting", got)
	}
	if s.ClaudeSessionID != "live-bbb" {
		t.Errorf("dead owner process: resume id still stale: %q, want live-bbb", s.ClaudeSessionID)
	}
}

// TestUpdateHookStatusRecoversInProcessRotation is the shape issue #226 was
// actually reported in, and the one a pure liveness check gets exactly wrong:
// `/clear` starts a fresh conversation INSIDE the running agent, so the owner's
// process is still very much alive. What identifies it is that the new id is
// reported by that same process.
func TestUpdateHookStatusRecoversInProcessRotation(t *testing.T) {
	now := time.Now().UTC()
	agent := otherLivePID(t)
	// Same pid on both sides: one process, two conversation ids.
	s, _ := negCachedSession(t, agent, agent, now.Add(-time.Hour), now.Add(-time.Minute))

	changed := s.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "live-bbb", UpdatedAt: time.Now(), AgentPID: agent}, true)
	if !changed {
		t.Errorf("in-process rotation: hook should register as changed")
	}
	if s.ClaudeSessionID != "live-bbb" {
		t.Errorf("in-process rotation stayed frozen: %q, want live-bbb", s.ClaudeSessionID)
	}
}

// TestResolveLaunchIDHealsInProcessRotation is the same case on the launch path —
// the one that decides what `f`, `F` and `r` actually open.
func TestResolveLaunchIDHealsInProcessRotation(t *testing.T) {
	now := time.Now().UTC()
	agent := otherLivePID(t)
	s, _ := negCachedSession(t, agent, agent, now.Add(-time.Hour), now.Add(-time.Minute))

	launchID, stale := s.ResolveLaunchID()
	if launchID != "live-bbb" || stale != "owner-aaa" {
		t.Errorf("ResolveLaunchID() = (%q, %q), want (live-bbb, owner-aaa)", launchID, stale)
	}
}

// TestUpdateHookStatusKeepsOwnerWhileProcessAlive is the case transcripts cannot
// decide and this one can: a nested agent that inherited FLEET_INSTANCE_ID writes
// for hours while its parent, blocked on the tool call that spawned it, writes
// nothing. Transcript-wise that is indistinguishable from a dead owner — the owner
// here has been silent for two hours. The owner's process being alive is what says
// otherwise, and the rejection must hold.
func TestUpdateHookStatusKeepsOwnerWhileProcessAlive(t *testing.T) {
	now := time.Now().UTC()
	s, _ := negCachedSession(t, os.Getpid(), otherLivePID(t), now.Add(-2*time.Hour), now.Add(-time.Minute))

	if changed := s.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "live-bbb", UpdatedAt: time.Now(), AgentPID: otherLivePID(t)}, true); changed {
		t.Errorf("owner process alive: nested agent hook should stay rejected")
	}
	if s.ClaudeSessionID != "owner-aaa" {
		t.Errorf("owner process alive: resume id clobbered: %q, want owner-aaa", s.ClaudeSessionID)
	}
}

// TestUpdateHookStatusFallsBackToTranscriptWithoutPID covers a status file written
// by a handler older than StatusFile.AgentPID: with no pid there is nothing to ask,
// so owner silence has to carry the decision.
func TestUpdateHookStatusFallsBackToTranscriptWithoutPID(t *testing.T) {
	now := time.Now().UTC()
	s, _ := negCachedSession(t, 0, 0, now.Add(-2*time.Hour), now.Add(-time.Minute))

	if changed := s.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "live-bbb", UpdatedAt: time.Now()}, true); !changed {
		t.Errorf("no pid + owner silent 2h: fallback should adopt")
	}
	if s.ClaudeSessionID != "live-bbb" {
		t.Errorf("no pid: resume id still stale: %q, want live-bbb", s.ClaudeSessionID)
	}
}

// TestUpdateHookStatusFallbackHoldsForRecentOwner is the fallback's other side: an
// owner that wrote moments ago is not abandoned, whatever else is reporting.
func TestUpdateHookStatusFallbackHoldsForRecentOwner(t *testing.T) {
	now := time.Now().UTC()
	s, _ := negCachedSession(t, 0, 0, now.Add(-2*time.Minute), now.Add(-time.Minute))

	if changed := s.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "live-bbb", UpdatedAt: time.Now()}, true); changed {
		t.Errorf("owner wrote 2m ago: fallback should hold the rejection")
	}
	if s.ClaudeSessionID != "owner-aaa" {
		t.Errorf("recent owner: resume id clobbered: %q, want owner-aaa", s.ClaudeSessionID)
	}
}

// TestUpdateHookStatusRotationShortlyAfterClearRecovers is the shape the first
// attempt at this got wrong: `/clear`, a few minutes of work, then a pause. A
// predicate asking how far the NEW conversation ran past the owner tops out at
// those few minutes and never grows, freezing the session for good — and a short
// piece of work after a /clear is the common shape.
func TestUpdateHookStatusRotationShortlyAfterClearRecovers(t *testing.T) {
	now := time.Now().UTC()
	// Owner cleared an hour ago; the new conversation ran six minutes and stopped.
	s, _ := negCachedSession(t, 0, 0, now.Add(-time.Hour), now.Add(-54*time.Minute))

	if changed := s.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "live-bbb", UpdatedAt: time.Now()}, true); !changed {
		t.Errorf("short post-/clear session should still recover")
	}
	if s.ClaudeSessionID != "live-bbb" {
		t.Errorf("short post-/clear session stayed frozen: %q, want live-bbb", s.ClaudeSessionID)
	}
}

// TestUpdateHookStatusDeadOwnerRecoveryStaysOffUIPath keeps the recovery off the
// Bubble Tea Update loop: the UI path passes resolveRotation=false and must defer
// to the worker rather than touch the disk.
func TestUpdateHookStatusDeadOwnerRecoveryStaysOffUIPath(t *testing.T) {
	now := time.Now().UTC()
	s, _ := negCachedSession(t, reapedPID(t), os.Getpid(), now.Add(-2*time.Hour), now.Add(-time.Minute))

	if changed := s.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "live-bbb", UpdatedAt: time.Now(), AgentPID: os.Getpid()}, false); changed {
		t.Errorf("UI path must not adopt (it must not read transcripts)")
	}
	if s.ClaudeSessionID != "owner-aaa" {
		t.Errorf("UI path adopted anyway: %q, want owner-aaa", s.ClaudeSessionID)
	}
	// The worker path, same instant, recovers — proving the UI path only deferred.
	if changed := s.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "live-bbb", UpdatedAt: time.Now(), AgentPID: os.Getpid()}, true); !changed {
		t.Errorf("worker path should recover after the UI path deferred")
	}
}

// TestNegCacheKeyedByForeignID pins the eviction fix: an eval harness produces a
// stream of foreign ids, and a single-slot cache let each new one evict the last —
// so the rotated id fell out of the cache, paid a full transcript verdict on every
// cycle, and had its recovery clock reset before it could ever fire.
func TestNegCacheKeyedByForeignID(t *testing.T) {
	now := time.Now().UTC()
	s, proj := negCachedSession(t, os.Getpid(), otherLivePID(t), now.Add(-2*time.Hour), now.Add(-time.Minute))

	// A second foreign id reports and is rejected in its turn.
	writeTranscript(t, proj, "child-ccc", now.Add(-3*time.Hour), now.Add(-2*time.Hour))
	s.UpdateHookStatus(&HookStatus{Status: "running", SessionID: "child-ccc", UpdatedAt: time.Now()}, true)

	if s.rotRejects["live-bbb"] == nil {
		t.Error("second foreign id evicted the first rejection")
	}
	if s.rotRejects["child-ccc"] == nil {
		t.Error("second foreign id was not cached")
	}
}

// TestDeadOwnerRecheckThrottled pins the throttle: the fallback walks two
// transcripts, so the recovery check must not run on every ~500ms worker cycle.
func TestDeadOwnerRecheckThrottled(t *testing.T) {
	s := &Session{ID: "x", rotRejects: map[string]*rotReject{"f": {owner: "o"}}}
	if !s.dueForDeadOwnerRecheck("f") {
		t.Fatal("first check should be due")
	}
	if s.dueForDeadOwnerRecheck("f") {
		t.Error("second check within the interval should be throttled")
	}
	if s.dueForDeadOwnerRecheck("unknown-id") {
		t.Error("an id with no rejection recorded is never due")
	}
	s.mu.Lock()
	s.rotRejects["f"].checkedAt = time.Now().Add(-deadOwnerRecheckInterval - time.Second)
	s.mu.Unlock()
	if !s.dueForDeadOwnerRecheck("f") {
		t.Error("check should be due again after the interval")
	}
}

// TestResolveLaunchIDHealsFrozenID covers the launch half of #226. The worker's
// recovery runs on a one-minute clock, so a key pressed inside that window would
// otherwise fork or restart into a conversation nobody is in.
func TestResolveLaunchIDHealsFrozenID(t *testing.T) {
	now := time.Now().UTC()
	s, _ := negCachedSession(t, reapedPID(t), os.Getpid(), now.Add(-2*time.Hour), now.Add(-time.Minute))

	launchID, stale := s.ResolveLaunchID()
	if launchID != "live-bbb" || stale != "owner-aaa" {
		t.Errorf("ResolveLaunchID() = (%q, %q), want (live-bbb, owner-aaa)", launchID, stale)
	}
}

// TestResolveLaunchIDKeepsOwnerWhileProcessAlive: a live owner means the rejected id
// is a nested agent, and launching must not follow it.
func TestResolveLaunchIDKeepsOwnerWhileProcessAlive(t *testing.T) {
	now := time.Now().UTC()
	s, _ := negCachedSession(t, os.Getpid(), otherLivePID(t), now.Add(-2*time.Hour), now.Add(-time.Minute))

	launchID, stale := s.ResolveLaunchID()
	if launchID != "owner-aaa" || stale != "" {
		t.Errorf("ResolveLaunchID() = (%q, %q), want (owner-aaa, \"\")", launchID, stale)
	}
}

// TestResolveLaunchIDKeepsHealthyID guards the common path: with no standing
// rejection the launch id is simply the stored one, and nothing is reported healed.
func TestResolveLaunchIDKeepsHealthyID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()
	s := &Session{ID: "x", ProjectPath: proj, Status: StatusRunning}
	s.UpdateHookStatus(&HookStatus{Status: "running", SessionID: "owner-aaa", UpdatedAt: time.Now()}, true)

	launchID, stale := s.ResolveLaunchID()
	if launchID != "owner-aaa" || stale != "" {
		t.Errorf("ResolveLaunchID() = (%q, %q), want (owner-aaa, \"\")", launchID, stale)
	}
}

// TestAdoptResolvedLaunchIDBeforeClear is the ordering restart depends on:
// clearHookState() drops the rejection state the heal is derived from, so a
// restart that cleared first would resume the frozen id every time.
func TestAdoptResolvedLaunchIDBeforeClear(t *testing.T) {
	now := time.Now().UTC()
	s, _ := negCachedSession(t, reapedPID(t), os.Getpid(), now.Add(-2*time.Hour), now.Add(-time.Minute))

	s.adoptResolvedLaunchID("test")
	if s.ClaudeSessionID != "live-bbb" {
		t.Fatalf("resume id not healed before clear: %q, want live-bbb", s.ClaudeSessionID)
	}
	// After clearing there is nothing left to heal from — the evidence is gone.
	s.clearHookState()
	if s.rotRejects != nil {
		t.Errorf("clearHookState left rejections behind: %v", s.rotRejects)
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
	// Two live processes: the owner, and a child reporting under its own pid. That
	// pairing is what keeps this rejected on the second delivery, where the recovery
	// check lives — the owner's transcript has been silent for four hours, so silence
	// alone would read as abandonment.
	ownerPID, childPID := os.Getpid(), otherLivePID(t)
	s.UpdateHookStatus(&HookStatus{Status: "running", SessionID: "owner-x", UpdatedAt: time.Now(), AgentPID: ownerPID}, true)

	if changed := s.UpdateHookStatus(&HookStatus{Status: "dead", SessionID: "child-x", UpdatedAt: time.Now(), AgentPID: childPID}, true); changed {
		t.Errorf("nested child with foreign compaction link should be ignored")
	}
	// Again, now through the neg-cached branch where the recovery check lives.
	s.mu.Lock()
	s.rotRejects["child-x"].checkedAt = time.Time{}
	s.mu.Unlock()
	if changed := s.UpdateHookStatus(&HookStatus{Status: "dead", SessionID: "child-x", UpdatedAt: time.Now(), AgentPID: childPID}, true); changed {
		t.Errorf("nested child should stay ignored while the owner process is alive")
	}
	if s.GetHookStatus() != "running" || s.ClaudeSessionID != "owner-x" {
		t.Errorf("owner clobbered: hook=%q resumeID=%q", s.GetHookStatus(), s.ClaudeSessionID)
	}
}
