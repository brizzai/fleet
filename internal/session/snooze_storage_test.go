package session

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *StateDB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestSessionSnoozeRoundTrip guards the hand-maintained positional SQL in
// SaveSession / LoadSessions. Those two column lists must move together: adding
// a column to one and not the other silently scans snoozed_until into the field
// beside it, corrupting every column after it rather than just this one.
func TestSessionSnoozeRoundTrip(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().Truncate(time.Second)
	until := now.Add(4 * time.Hour)

	row := &SessionRow{
		ID: "snz11111-1700000000", Title: "Snoozed", ProjectPath: "/tmp/p",
		Status: "waiting", TmuxSession: "fleet_snz", CreatedAt: now,
		LastAccessed: now, PromptCount: 7, SnoozedUntil: until,
	}
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	got, err := db.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	if !got[0].SnoozedUntil.Equal(until) {
		t.Errorf("SnoozedUntil = %v, want %v", got[0].SnoozedUntil, until)
	}
	// The neighbouring column: a positional mismatch shows up here first.
	if got[0].PromptCount != 7 {
		t.Errorf("PromptCount = %d, want 7 — column list is misaligned", got[0].PromptCount)
	}
}

// TestSessionSnoozeZeroRoundTrip: "not snoozed" must survive as the zero time,
// not 1970. The column is NOT NULL DEFAULT 0, so this is the common case.
func TestSessionSnoozeZeroRoundTrip(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().Truncate(time.Second)

	row := &SessionRow{
		ID: "snz22222-1700000000", Title: "Awake", ProjectPath: "/tmp/p",
		Status: "idle", CreatedAt: now, LastAccessed: now,
	}
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got, _ := db.LoadSessions()
	if !got[0].SnoozedUntil.IsZero() {
		t.Errorf("unset snooze read back as %v, want the zero time", got[0].SnoozedUntil)
	}
}

// TestSetSessionSnooze covers the targeted updater, including that clearing
// with the zero time really writes 0 rather than a 1970 timestamp.
func TestSetSessionSnooze(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().Truncate(time.Second)
	until := now.Add(time.Hour)

	row := &SessionRow{
		ID: "snz33333-1700000000", Title: "T", ProjectPath: "/tmp/p",
		Status: "idle", CreatedAt: now, LastAccessed: now,
	}
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	if err := db.SetSessionSnooze(row.ID, until); err != nil {
		t.Fatalf("SetSessionSnooze: %v", err)
	}
	got, _ := db.LoadSessions()
	if !got[0].SnoozedUntil.Equal(until) {
		t.Fatalf("after set: %v, want %v", got[0].SnoozedUntil, until)
	}

	if err := db.SetSessionSnooze(row.ID, time.Time{}); err != nil {
		t.Fatalf("SetSessionSnooze(clear): %v", err)
	}
	got, _ = db.LoadSessions()
	if !got[0].SnoozedUntil.IsZero() {
		t.Errorf("after clear: %v, want the zero time", got[0].SnoozedUntil)
	}
}

// TestGroupSnoozeRoundTrip covers the umbrella table, which uses the same key
// space as collapsed_groups (origin keys and checkout paths side by side).
func TestGroupSnoozeRoundTrip(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().Truncate(time.Second)
	originUntil := now.Add(time.Hour)
	repoUntil := now.Add(4 * time.Hour)

	if err := db.SetGroupSnooze("origin:github.com/acme/x", originUntil); err != nil {
		t.Fatalf("SetGroupSnooze(origin): %v", err)
	}
	if err := db.SetGroupSnooze("/tmp/checkout", repoUntil); err != nil {
		t.Fatalf("SetGroupSnooze(repo): %v", err)
	}

	got, err := db.LoadSnoozedGroups()
	if err != nil {
		t.Fatalf("LoadSnoozedGroups: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d groups, want 2", len(got))
	}
	if !got["origin:github.com/acme/x"].Equal(originUntil) {
		t.Errorf("origin deadline = %v, want %v", got["origin:github.com/acme/x"], originUntil)
	}
	if !got["/tmp/checkout"].Equal(repoUntil) {
		t.Errorf("repo deadline = %v, want %v", got["/tmp/checkout"], repoUntil)
	}

	// Re-snoozing the same key replaces rather than errors on the PK.
	newer := now.Add(8 * time.Hour)
	if err := db.SetGroupSnooze("/tmp/checkout", newer); err != nil {
		t.Fatalf("SetGroupSnooze(replace): %v", err)
	}
	got, _ = db.LoadSnoozedGroups()
	if !got["/tmp/checkout"].Equal(newer) {
		t.Errorf("after replace: %v, want %v", got["/tmp/checkout"], newer)
	}

	// The zero time deletes the row (absence = not snoozed).
	if err := db.SetGroupSnooze("/tmp/checkout", time.Time{}); err != nil {
		t.Fatalf("SetGroupSnooze(clear): %v", err)
	}
	got, _ = db.LoadSnoozedGroups()
	if _, still := got["/tmp/checkout"]; still {
		t.Error("clearing a group snooze must delete its row")
	}
}

// TestSnoozeMigrationIsIdempotent: migrate() runs on every Open, so reopening an
// existing DB must not fail on the already-present column or table.
func TestSnoozeMigrationIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	until := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := db.SetGroupSnooze("origin:x", until); err != nil {
		t.Fatalf("SetGroupSnooze: %v", err)
	}
	db.Close()

	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen (migration not idempotent): %v", err)
	}
	defer db2.Close()

	got, err := db2.LoadSnoozedGroups()
	if err != nil {
		t.Fatalf("LoadSnoozedGroups after reopen: %v", err)
	}
	if !got["origin:x"].Equal(until) {
		t.Errorf("snooze did not survive reopen: %v", got["origin:x"])
	}
}

// TestFromRowDropsExpiredSnooze: a deadline that lapsed while fleet was closed
// must be gone on the first frame, not linger until the wake sweep runs.
func TestFromRowDropsExpiredSnooze(t *testing.T) {
	now := time.Now()
	live := now.Add(time.Hour)

	expired := FromRow(&SessionRow{
		ID: "a-1", Title: "expired", ProjectPath: "/tmp/p", Status: "idle",
		CreatedAt: now, LastAccessed: now, SnoozedUntil: now.Add(-time.Minute),
	})
	if !expired.SnoozedUntil().IsZero() {
		t.Errorf("expired snooze survived load: %v", expired.SnoozedUntil())
	}

	fresh := FromRow(&SessionRow{
		ID: "a-2", Title: "live", ProjectPath: "/tmp/p", Status: "idle",
		CreatedAt: now, LastAccessed: now, SnoozedUntil: live,
	})
	if !fresh.SnoozedUntil().Equal(live) {
		t.Errorf("live snooze = %v, want %v", fresh.SnoozedUntil(), live)
	}
}
