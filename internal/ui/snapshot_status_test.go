package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// deadPID is near int32 max, so syscall.Kill(deadPID, 0) reliably reports ESRCH
// (no such process) — a stable stand-in for a Claude process that has exited.
const deadPID = 2147483646

func writeClaudeSessionFile(t *testing.T, dir, name string, v map[string]any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0644); err != nil {
		t.Fatal(err)
	}
}

// A session can leave a stale dead-process file (keyed by pid) alongside the live
// one; the live process's status is the truth even if its stamp is older.
func TestFindClaudeSessionStatus_PrefersLiveOverStaleDead(t *testing.T) {
	dir := t.TempDir()
	const sid = "8ef74b39-3dcf-49f8-afcb-db794dd47369"
	livePID := os.Getpid()

	writeClaudeSessionFile(t, dir, "dead.json", map[string]any{
		"pid": deadPID, "sessionId": sid, "status": "idle",
		"statusUpdatedAt": 2000, "name": "old", "version": "2.1.201",
	})
	writeClaudeSessionFile(t, dir, "live.json", map[string]any{
		"pid": livePID, "sessionId": sid, "status": "busy",
		"statusUpdatedAt": 1000, "name": "cur", "version": "2.1.201",
	})
	writeClaudeSessionFile(t, dir, "other.json", map[string]any{
		"pid": livePID, "sessionId": "different-uuid", "status": "busy",
		"statusUpdatedAt": 9999,
	})

	got := findClaudeSessionStatusIn(dir, sid)
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if got.PID != livePID {
		t.Errorf("live pid %d should beat dead pid despite older stamp, got pid %d", livePID, got.PID)
	}
	if got.Status != "busy" {
		t.Errorf("status = %q, want busy (from the live process)", got.Status)
	}
}

// With no live process, the newest statusUpdatedAt wins.
func TestFindClaudeSessionStatus_TiebreakNewestAmongDead(t *testing.T) {
	dir := t.TempDir()
	const sid = "abc"
	writeClaudeSessionFile(t, dir, "a.json", map[string]any{"pid": deadPID, "sessionId": sid, "status": "idle", "statusUpdatedAt": 1000})
	writeClaudeSessionFile(t, dir, "b.json", map[string]any{"pid": deadPID - 1, "sessionId": sid, "status": "idle", "statusUpdatedAt": 2000})

	got := findClaudeSessionStatusIn(dir, sid)
	if got == nil || got.StatusUpdatedAt != 2000 {
		t.Fatalf("expected newest dead match (stamp 2000), got %+v", got)
	}
}

func TestFindClaudeSessionStatus_NoMatch(t *testing.T) {
	dir := t.TempDir()
	writeClaudeSessionFile(t, dir, "a.json", map[string]any{"pid": 1, "sessionId": "x", "status": "idle"})

	if got := findClaudeSessionStatusIn(dir, "nope"); got != nil {
		t.Errorf("unmatched sessionId should yield nil, got %+v", got)
	}
	if got := findClaudeSessionStatusIn(dir, ""); got != nil {
		t.Errorf("empty sessionId should yield nil, got %+v", got)
	}
}

func TestBuildClaudeStatusBlock(t *testing.T) {
	now := time.Now()
	updated := now.Add(-90 * time.Second)
	css := &claudeSessionStatus{
		PID:             os.Getpid(),
		Status:          "busy",
		StatusUpdatedAt: updated.UnixMilli(),
		Name:            "my-session",
		Version:         "2.1.201",
	}

	m := buildClaudeStatusBlock(css, now)
	if m["status"] != "busy" {
		t.Errorf("status = %v, want busy", m["status"])
	}
	if m["pid_alive"] != true {
		t.Errorf("pid_alive = %v, want true (the test process is alive)", m["pid_alive"])
	}
	if m["file"] != "claude_session_status.json" {
		t.Errorf("file = %v", m["file"])
	}
	if m["name"] != "my-session" || m["version"] != "2.1.201" {
		t.Errorf("name/version = %v / %v", m["name"], m["version"])
	}
	if _, ok := m["status_updated_at"].(string); !ok {
		t.Errorf("status_updated_at missing or wrong type: %v", m["status_updated_at"])
	}

	if buildClaudeStatusBlock(nil, now) != nil {
		t.Error("nil input should yield a nil block")
	}
}
