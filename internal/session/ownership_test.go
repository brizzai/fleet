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
// Claude rotates its session id mid-life (compaction/clear/continue), the new
// transcript continues the old one within milliseconds. The new id's hooks must
// be adopted, not dropped as foreign — otherwise the in-memory hook freezes at
// the old session's last event and the resume id goes stale.
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
	s.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "owner-aaa", UpdatedAt: time.Now()})
	if got := s.GetHookStatus(); got != "waiting" {
		t.Fatalf("owner hook not applied: hook=%q", got)
	}

	// Rotated session reports finished — must be adopted (continuation).
	changed := s.UpdateHookStatus(&HookStatus{Status: "finished", SessionID: "rot-bbb", UpdatedAt: time.Now()})
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
	s.UpdateHookStatus(&HookStatus{Status: "running", SessionID: "owner-aaa", UpdatedAt: time.Now()})

	// Child reports dead — must be ignored; owner state preserved.
	if changed := s.UpdateHookStatus(&HookStatus{Status: "dead", SessionID: "child-bbb", UpdatedAt: time.Now()}); changed {
		t.Errorf("nested child hook should be ignored (changed=true)")
	}
	if got := s.GetHookStatus(); got != "running" {
		t.Errorf("owner hook clobbered by nested child: hook=%q, want running", got)
	}
	if s.ClaudeSessionID != "owner-aaa" {
		t.Errorf("resume id clobbered by nested child: %q, want owner-aaa", s.ClaudeSessionID)
	}
}
