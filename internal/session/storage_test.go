package session

import (
	"path/filepath"
	"testing"
	"time"
)

func TestNewStorageCreatesDBAndTables(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Verify sessions table exists by loading (should return empty, not error).
	sessions, err := db.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions on fresh DB failed: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions in fresh DB, got %d", len(sessions))
	}
}

func TestSaveAndLoadSessionsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	now := time.Now().Truncate(time.Second)
	row := &SessionRow{
		ID:              "abc12345-1234567890",
		Title:           "Test Session",
		ProjectPath:     "/home/user/project",
		Status:          "running",
		TmuxSession:     "fleet_test-session_abcd1234",
		CreatedAt:       now,
		LastAccessed:    now.Add(5 * time.Minute),
		Acknowledged:    true,
		ClaudeSessionID: "claude-sess-001",
		WorkspaceName:   "feature-branch",
		ManuallyRenamed: true,
		FirstPrompt:     "fix the login bug",
		TitleGenerated:  true,
		PromptCount:     3,
	}

	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	sessions, err := db.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	got := sessions[0]
	if got.ID != row.ID {
		t.Errorf("ID: got %q, want %q", got.ID, row.ID)
	}
	if got.Title != row.Title {
		t.Errorf("Title: got %q, want %q", got.Title, row.Title)
	}
	if got.ProjectPath != row.ProjectPath {
		t.Errorf("ProjectPath: got %q, want %q", got.ProjectPath, row.ProjectPath)
	}
	if got.Status != row.Status {
		t.Errorf("Status: got %q, want %q", got.Status, row.Status)
	}
	if got.TmuxSession != row.TmuxSession {
		t.Errorf("TmuxSession: got %q, want %q", got.TmuxSession, row.TmuxSession)
	}
	if got.CreatedAt.Unix() != row.CreatedAt.Unix() {
		t.Errorf("CreatedAt: got %v, want %v", got.CreatedAt, row.CreatedAt)
	}
	if got.LastAccessed.Unix() != row.LastAccessed.Unix() {
		t.Errorf("LastAccessed: got %v, want %v", got.LastAccessed, row.LastAccessed)
	}
	if got.Acknowledged != row.Acknowledged {
		t.Errorf("Acknowledged: got %v, want %v", got.Acknowledged, row.Acknowledged)
	}
	if got.ClaudeSessionID != row.ClaudeSessionID {
		t.Errorf("ClaudeSessionID: got %q, want %q", got.ClaudeSessionID, row.ClaudeSessionID)
	}
	if got.WorkspaceName != row.WorkspaceName {
		t.Errorf("WorkspaceName: got %q, want %q", got.WorkspaceName, row.WorkspaceName)
	}
	if got.ManuallyRenamed != row.ManuallyRenamed {
		t.Errorf("ManuallyRenamed: got %v, want %v", got.ManuallyRenamed, row.ManuallyRenamed)
	}
	if got.FirstPrompt != row.FirstPrompt {
		t.Errorf("FirstPrompt: got %q, want %q", got.FirstPrompt, row.FirstPrompt)
	}
	if got.TitleGenerated != row.TitleGenerated {
		t.Errorf("TitleGenerated: got %v, want %v", got.TitleGenerated, row.TitleGenerated)
	}
	if got.PromptCount != row.PromptCount {
		t.Errorf("PromptCount: got %d, want %d", got.PromptCount, row.PromptCount)
	}
}

func TestDeleteSession(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	now := time.Now()
	row := &SessionRow{
		ID:          "del-test-001",
		Title:       "To Delete",
		ProjectPath: "/tmp/project",
		Status:      "idle",
		CreatedAt:   now,
	}

	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	// Verify it exists.
	sessions, err := db.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session before delete, got %d", len(sessions))
	}

	// Delete it.
	if err := db.DeleteSession("del-test-001"); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	// Verify it's gone.
	sessions, err = db.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions after delete failed: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions after delete, got %d", len(sessions))
	}
}

func TestDeleteSessionNonExistent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Deleting a non-existent ID should not error.
	if err := db.DeleteSession("does-not-exist"); err != nil {
		t.Errorf("DeleteSession for non-existent ID should not error, got: %v", err)
	}
}

func TestUpdateClaudeSessionID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	now := time.Now()
	row := &SessionRow{
		ID:          "claude-id-test",
		Title:       "Claude ID Test",
		ProjectPath: "/tmp/project",
		Status:      "running",
		CreatedAt:   now,
	}

	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	// Update the Claude session ID.
	if err := db.UpdateClaudeSessionID("claude-id-test", "new-claude-session-id"); err != nil {
		t.Fatalf("UpdateClaudeSessionID failed: %v", err)
	}

	// Reload and verify.
	sessions, err := db.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].ClaudeSessionID != "new-claude-session-id" {
		t.Errorf("ClaudeSessionID: got %q, want %q", sessions[0].ClaudeSessionID, "new-claude-session-id")
	}
}

func TestUpdateStatus(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	now := time.Now()
	row := &SessionRow{
		ID:           "status-test",
		Title:        "Status Test",
		ProjectPath:  "/tmp/project",
		Status:       "idle",
		CreatedAt:    now,
		Acknowledged: true,
	}

	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	t.Run("update to running clears acknowledged", func(t *testing.T) {
		if err := db.UpdateStatus("status-test", "running"); err != nil {
			t.Fatalf("UpdateStatus failed: %v", err)
		}
		sessions, err := db.LoadSessions()
		if err != nil {
			t.Fatalf("LoadSessions failed: %v", err)
		}
		if sessions[0].Status != "running" {
			t.Errorf("Status: got %q, want %q", sessions[0].Status, "running")
		}
		if sessions[0].Acknowledged {
			t.Error("expected Acknowledged to be cleared on running")
		}
	})

	t.Run("update to waiting preserves acknowledged", func(t *testing.T) {
		// First set acknowledged back to true.
		if err := db.SetAcknowledged("status-test", true); err != nil {
			t.Fatalf("SetAcknowledged failed: %v", err)
		}
		if err := db.UpdateStatus("status-test", "waiting"); err != nil {
			t.Fatalf("UpdateStatus failed: %v", err)
		}
		sessions, err := db.LoadSessions()
		if err != nil {
			t.Fatalf("LoadSessions failed: %v", err)
		}
		if sessions[0].Status != "waiting" {
			t.Errorf("Status: got %q, want %q", sessions[0].Status, "waiting")
		}
		if !sessions[0].Acknowledged {
			t.Error("expected Acknowledged to be preserved on non-running status")
		}
	})
}

func TestMultipleSessions(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	now := time.Now()
	rows := []*SessionRow{
		{ID: "s1", Title: "First", ProjectPath: "/p1", Status: "idle", CreatedAt: now},
		{ID: "s2", Title: "Second", ProjectPath: "/p2", Status: "running", CreatedAt: now.Add(time.Second)},
		{ID: "s3", Title: "Third", ProjectPath: "/p3", Status: "waiting", CreatedAt: now.Add(2 * time.Second)},
	}

	for _, r := range rows {
		if err := db.SaveSession(r); err != nil {
			t.Fatalf("SaveSession %q failed: %v", r.ID, err)
		}
	}

	sessions, err := db.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions failed: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
	}

	// Verify ordering by created_at.
	if sessions[0].ID != "s1" || sessions[1].ID != "s2" || sessions[2].ID != "s3" {
		t.Errorf("sessions not ordered by created_at: %v, %v, %v", sessions[0].ID, sessions[1].ID, sessions[2].ID)
	}

	// Delete the middle one.
	if err := db.DeleteSession("s2"); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	sessions, err = db.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions failed: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions after delete, got %d", len(sessions))
	}
	if sessions[0].ID != "s1" || sessions[1].ID != "s3" {
		t.Errorf("unexpected session IDs after delete: %v, %v", sessions[0].ID, sessions[1].ID)
	}
}

func TestSaveSessionUpsert(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	now := time.Now()
	row := &SessionRow{
		ID:          "upsert-test",
		Title:       "Original",
		ProjectPath: "/tmp/project",
		Status:      "idle",
		CreatedAt:   now,
	}

	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	// Update via SaveSession (INSERT OR REPLACE).
	row.Title = "Updated"
	row.Status = "running"
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession (upsert) failed: %v", err)
	}

	sessions, err := db.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session after upsert, got %d", len(sessions))
	}
	if sessions[0].Title != "Updated" {
		t.Errorf("Title: got %q, want %q", sessions[0].Title, "Updated")
	}
	if sessions[0].Status != "running" {
		t.Errorf("Status: got %q, want %q", sessions[0].Status, "running")
	}
}

func TestUpdateTitle(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	now := time.Now()
	row := &SessionRow{
		ID:          "title-test",
		Title:       "Old Title",
		ProjectPath: "/tmp/project",
		Status:      "idle",
		CreatedAt:   now,
	}
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	if err := db.UpdateTitle("title-test", "New Title"); err != nil {
		t.Fatalf("UpdateTitle failed: %v", err)
	}

	sessions, err := db.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions failed: %v", err)
	}
	if sessions[0].Title != "New Title" {
		t.Errorf("Title: got %q, want %q", sessions[0].Title, "New Title")
	}
}

func TestMarkManuallyRenamed(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	now := time.Now()
	row := &SessionRow{
		ID:          "rename-test",
		Title:       "Test",
		ProjectPath: "/tmp/project",
		Status:      "idle",
		CreatedAt:   now,
	}
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	if err := db.MarkManuallyRenamed("rename-test"); err != nil {
		t.Fatalf("MarkManuallyRenamed failed: %v", err)
	}

	sessions, err := db.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions failed: %v", err)
	}
	if !sessions[0].ManuallyRenamed {
		t.Error("expected ManuallyRenamed to be true")
	}
}

func TestUpdatePromptCount(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	now := time.Now()
	row := &SessionRow{
		ID:          "prompt-test",
		Title:       "Test",
		ProjectPath: "/tmp/project",
		Status:      "idle",
		CreatedAt:   now,
	}
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	if err := db.UpdatePromptCount("prompt-test", 5); err != nil {
		t.Fatalf("UpdatePromptCount failed: %v", err)
	}

	sessions, err := db.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions failed: %v", err)
	}
	if sessions[0].PromptCount != 5 {
		t.Errorf("PromptCount: got %d, want %d", sessions[0].PromptCount, 5)
	}
}

func TestMarkTitleGenerated(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	now := time.Now()
	row := &SessionRow{
		ID:          "titlegen-test",
		Title:       "Test",
		ProjectPath: "/tmp/project",
		Status:      "idle",
		CreatedAt:   now,
	}
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	// Mark as generated.
	if err := db.MarkTitleGenerated("titlegen-test"); err != nil {
		t.Fatalf("MarkTitleGenerated failed: %v", err)
	}
	sessions, err := db.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions failed: %v", err)
	}
	if !sessions[0].TitleGenerated {
		t.Error("expected TitleGenerated to be true")
	}
}

func TestBoolToInt(t *testing.T) {
	tests := []struct {
		name string
		in   bool
		want int
	}{
		{"true", true, 1},
		{"false", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := boolToInt(tt.in)
			if got != tt.want {
				t.Errorf("boolToInt(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestMigrationIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Open once to create and migrate.
	db1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open failed: %v", err)
	}
	db1.Close()

	// Open again — should migrate without error (all columns already exist).
	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open failed: %v", err)
	}
	defer db2.Close()

	// Save and load to verify everything works after re-migration.
	now := time.Now()
	row := &SessionRow{
		ID:          "idempotent-test",
		Title:       "Test",
		ProjectPath: "/tmp/project",
		Status:      "idle",
		CreatedAt:   now,
	}
	if err := db2.SaveSession(row); err != nil {
		t.Fatalf("SaveSession after re-migration failed: %v", err)
	}

	sessions, err := db2.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions after re-migration failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("expected 1 session, got %d", len(sessions))
	}
}

func TestSlotBindings(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	now := time.Now()
	saveSession := func(id string) {
		if err := db.SaveSession(&SessionRow{
			ID: id, Title: id, ProjectPath: "/tmp/p",
			Status: "idle", CreatedAt: now,
		}); err != nil {
			t.Fatalf("SaveSession %s: %v", id, err)
		}
	}
	saveSession("sess-a")
	saveSession("sess-b")
	saveSession("sess-c")

	// Initial: no bindings.
	bindings, err := db.LoadSlotBindings()
	if err != nil {
		t.Fatalf("LoadSlotBindings: %v", err)
	}
	if len(bindings) != 0 {
		t.Errorf("expected 0 bindings, got %d", len(bindings))
	}

	// Bind.
	if err := db.BindSlot(1, "sess-a"); err != nil {
		t.Fatalf("BindSlot 1→a: %v", err)
	}
	if err := db.BindSlot(2, "sess-b"); err != nil {
		t.Fatalf("BindSlot 2→b: %v", err)
	}

	bindings, _ = db.LoadSlotBindings()
	if bindings[1] != "sess-a" || bindings[2] != "sess-b" {
		t.Errorf("unexpected bindings: %v", bindings)
	}

	// Rebind slot 1 to a different session — old session clears.
	if err := db.BindSlot(1, "sess-c"); err != nil {
		t.Fatalf("rebind slot 1: %v", err)
	}
	bindings, _ = db.LoadSlotBindings()
	if bindings[1] != "sess-c" {
		t.Errorf("slot 1 should be sess-c, got %q", bindings[1])
	}
	if _, ok := bindings[2]; !ok || bindings[2] != "sess-b" {
		t.Errorf("slot 2 should still be sess-b, got %v", bindings)
	}

	// Move a session to a new slot — old slot clears (uniqueness on session_id).
	if err := db.BindSlot(5, "sess-b"); err != nil {
		t.Fatalf("move sess-b to slot 5: %v", err)
	}
	bindings, _ = db.LoadSlotBindings()
	if _, ok := bindings[2]; ok {
		t.Errorf("slot 2 should be cleared when sess-b moves, got %v", bindings)
	}
	if bindings[5] != "sess-b" {
		t.Errorf("slot 5 should be sess-b, got %q", bindings[5])
	}

	// Unbind.
	if err := db.UnbindSlot(1); err != nil {
		t.Fatalf("UnbindSlot: %v", err)
	}
	bindings, _ = db.LoadSlotBindings()
	if _, ok := bindings[1]; ok {
		t.Errorf("slot 1 should be unbound")
	}

	// Out-of-range slot rejected.
	if err := db.BindSlot(10, "sess-a"); err == nil {
		t.Error("BindSlot(10) should reject out-of-range slot")
	}
	if err := db.BindSlot(-1, "sess-a"); err == nil {
		t.Error("BindSlot(-1) should reject out-of-range slot")
	}

	// FK cascade: deleting a session drops its binding.
	if err := db.BindSlot(3, "sess-a"); err != nil {
		t.Fatalf("BindSlot 3→a: %v", err)
	}
	if err := db.DeleteSession("sess-a"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	bindings, _ = db.LoadSlotBindings()
	if _, ok := bindings[3]; ok {
		t.Errorf("slot 3 should be cleared by FK cascade after sess-a delete, got %v", bindings)
	}

	// Explicit DeleteSlotBindingForSession also works.
	if err := db.BindSlot(4, "sess-c"); err != nil {
		t.Fatalf("BindSlot 4→c: %v", err)
	}
	if err := db.DeleteSlotBindingForSession("sess-c"); err != nil {
		t.Fatalf("DeleteSlotBindingForSession: %v", err)
	}
	bindings, _ = db.LoadSlotBindings()
	if _, ok := bindings[4]; ok {
		t.Errorf("slot 4 should be cleared by explicit delete, got %v", bindings)
	}
}
