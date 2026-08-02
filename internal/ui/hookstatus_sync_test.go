package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/hooks"
	"github.com/brizzai/fleet/internal/session"
)

// TestSyncHookStatusesPropagatesAgentPID guards the seam between the hook watcher
// and the ownership gate.
//
// syncHookStatuses is the ONLY production caller of UpdateHookStatus, and it
// rebuilds the HookStatus field by field. Every ownership test injects AgentPID
// straight into UpdateHookStatus, so dropping it here stayed green everywhere
// while production ran with both sides of the comparison at 0 —
// conversationSucceeds answers "unknown", the neg-cached rotation is never
// released, and the session's hook sits frozen while the file on disk moves on.
// Asserting on the reconstructed struct is the point: this test must fail if a
// field is dropped in transit, not if the gate's logic changes.
func TestSyncHookStatusesPropagatesAgentPID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const instanceID = "inst-agentpid"
	const conversationID = "sess-aaa"
	agentPID := os.Getpid()

	// Land the file before the watcher exists so loadExisting picks it up on
	// Start, rather than racing an fsnotify delivery.
	if err := hooks.WriteStatusFile(hooks.GetHooksDir(), instanceID, &hooks.StatusFile{
		Status:    "running",
		SessionID: conversationID,
		Event:     "UserPromptSubmit",
		Timestamp: time.Now().Unix(),
		AgentPID:  agentPID,
	}); err != nil {
		t.Fatalf("write status file: %v", err)
	}

	w, err := hooks.NewHookWatcher()
	if err != nil {
		t.Fatalf("new hook watcher: %v", err)
	}
	go w.Start()
	t.Cleanup(w.Stop)

	var hs *hooks.HookStatus
	for range 200 {
		if hs = w.GetStatus(instanceID); hs != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if hs == nil {
		t.Fatal("hook watcher never loaded the status file")
	}
	if hs.AgentPID != agentPID {
		t.Fatalf("watcher lost AgentPID: got %d, want %d", hs.AgentPID, agentPID)
	}

	db, err := session.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	h := &Home{hookWatcher: w, storage: db}
	s := &session.Session{ID: instanceID, ProjectPath: t.TempDir()}

	h.syncHookStatuses([]*session.Session{s}, true)

	snap := s.SnapshotData("")
	if snap.OwnerSessionID != conversationID {
		t.Fatalf("ownership not claimed: owner = %q, want %q", snap.OwnerSessionID, conversationID)
	}
	if snap.OwnerPID != agentPID {
		t.Fatalf("syncHookStatuses dropped AgentPID: ownerPID = %d, want %d", snap.OwnerPID, agentPID)
	}
}
