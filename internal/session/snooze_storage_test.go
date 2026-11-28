package session

import (
	"database/sql"
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
// TestFromRowKeepsLapsedSnooze: a deadline that lapsed while fleet was closed
// must survive the load. The UI's startup reconciliation is what clears it —
// in storage, and flipping an idle session to finished as it goes. FromRow
// used to drop it in memory only, which left the row holding a dead deadline;
// with the wake now carrying a side effect, that would re-fire the reminder on
// every launch. A lapsed deadline still never reads as snoozed.
func TestFromRowKeepsLapsedSnooze(t *testing.T) {
	now := time.Now()
	lapsed := now.Add(-time.Minute)
	live := now.Add(time.Hour)

	expired := FromRow(&SessionRow{
		ID: "a-1", Title: "expired", ProjectPath: "/tmp/p", Status: "idle",
		CreatedAt: now, LastAccessed: now, SnoozedUntil: lapsed,
	})
	if !expired.SnoozedUntil().Equal(lapsed) {
		t.Errorf("lapsed snooze = %v, want it kept as %v for the startup sweep", expired.SnoozedUntil(), lapsed)
	}
	if expired.IsSnoozed(now) {
		t.Error("a lapsed deadline must not read as snoozed")
	}

	fresh := FromRow(&SessionRow{
		ID: "a-2", Title: "live", ProjectPath: "/tmp/p", Status: "idle",
		CreatedAt: now, LastAccessed: now, SnoozedUntil: live,
	})
	if !fresh.SnoozedUntil().Equal(live) {
		t.Errorf("live snooze = %v, want %v", fresh.SnoozedUntil(), live)
	}
}

// TestSnoozeForeverRoundTrip: "until I wake it" is stored as -1, not as a
// far-future timestamp. -1 is what an OLDER fleet reads as not snoozed; a
// year-9999 timestamp would instead render a
// seven-digit countdown on every row it touched after a downgrade. In memory it
// is the SnoozeForever sentinel, so every `until.After(now)` check keeps
// treating it as live without knowing it exists.
func TestSnoozeForeverRoundTrip(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().Truncate(time.Second)

	row := &SessionRow{
		ID: "snzff111-1700000000", Title: "Parked", ProjectPath: "/tmp/p",
		Status: "idle", CreatedAt: now, LastAccessed: now, SnoozedUntil: SnoozeForever,
	}
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	var raw int64
	if err := db.db.QueryRow("SELECT snoozed_until FROM sessions WHERE id = ?", row.ID).Scan(&raw); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if raw != -1 {
		t.Errorf("stored snoozed_until = %d, want -1", raw)
	}
	got, _ := db.LoadSessions()
	if !IsSnoozeForever(got[0].SnoozedUntil) {
		t.Errorf("loaded snooze = %v, want SnoozeForever", got[0].SnoozedUntil)
	}

	// The targeted updater takes the same path.
	if err := db.SetSessionSnooze(row.ID, time.Time{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := db.SetSessionSnooze(row.ID, SnoozeForever); err != nil {
		t.Fatalf("SetSessionSnooze(forever): %v", err)
	}
	got, _ = db.LoadSessions()
	if !IsSnoozeForever(got[0].SnoozedUntil) {
		t.Errorf("after SetSessionSnooze: %v, want SnoozeForever", got[0].SnoozedUntil)
	}

	if err := db.SetGroupSnooze("origin:x", SnoozeForever); err != nil {
		t.Fatalf("SetGroupSnooze(forever): %v", err)
	}
	if err := db.db.QueryRow("SELECT snoozed_until FROM snoozed_groups WHERE group_key = ?", "origin:x").Scan(&raw); err != nil {
		t.Fatalf("raw group read: %v", err)
	}
	if raw != -1 {
		t.Errorf("stored group snoozed_until = %d, want -1", raw)
	}
	groups, _ := db.LoadSnoozedGroups()
	if !IsSnoozeForever(groups["origin:x"]) {
		t.Errorf("loaded group snooze = %v, want SnoozeForever", groups["origin:x"])
	}
}

// TestGroupSnoozedAtRecorded: a group parked "until I wake it" suspends only
// the sessions that were already there and untouched when it was snoozed, so
// the moment of the snooze has to survive a restart.
func TestGroupSnoozedAtRecorded(t *testing.T) {
	db := openTestDB(t)
	before := time.Now().Truncate(time.Second)
	if err := db.SetGroupSnooze("/tmp/checkout", SnoozeForever); err != nil {
		t.Fatalf("SetGroupSnooze: %v", err)
	}
	_, at, err := db.LoadSnoozedGroupsAt()
	if err != nil {
		t.Fatalf("LoadSnoozedGroupsAt: %v", err)
	}
	got := at["/tmp/checkout"]
	if got.Before(before) || got.After(time.Now()) {
		t.Errorf("snoozed_at = %v, want the time of the write (>= %v)", got, before)
	}
}

// TestGroupSnoozedAtMigration: an existing snoozed_groups table predates the
// snoozed_at column, so Open must add it rather than fail on the first write.
func TestGroupSnoozedAtMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := old.Exec(`CREATE TABLE snoozed_groups (group_key TEXT PRIMARY KEY, snoozed_until INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create old table: %v", err)
	}
	if _, err := old.Exec(`INSERT INTO snoozed_groups VALUES ('origin:x', ?)`, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	old.Close()

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open on old schema: %v", err)
	}
	defer db.Close()
	if err := db.SetGroupSnooze("/tmp/new", SnoozeForever); err != nil {
		t.Fatalf("SetGroupSnooze after migration: %v", err)
	}
	_, at, err := db.LoadSnoozedGroupsAt()
	if err != nil {
		t.Fatalf("LoadSnoozedGroupsAt: %v", err)
	}
	if !at["origin:x"].IsZero() {
		t.Errorf("pre-migration row snoozed_at = %v, want zero (unknown)", at["origin:x"])
	}
	if at["/tmp/new"].IsZero() {
		t.Error("new row has no snoozed_at")
	}
}

// TestLoadGroupSnooze: the park sweep re-reads one group before suspending its
// members, so the single-row read must agree with the bulk load and report a
// woken group as absent.
func TestLoadGroupSnooze(t *testing.T) {
	db := openTestDB(t)
	at := time.Unix(1_700_000_000, 0)
	if err := db.SetGroupSnoozeAt("/tmp/checkout", SnoozeForever, at); err != nil {
		t.Fatalf("SetGroupSnoozeAt: %v", err)
	}
	until, gotAt, ok, err := db.LoadGroupSnooze("/tmp/checkout")
	if err != nil || !ok {
		t.Fatalf("LoadGroupSnooze: ok=%v err=%v", ok, err)
	}
	if !IsSnoozeForever(until) || !gotAt.Equal(at) {
		t.Errorf("got until=%v at=%v, want forever at %v", until, gotAt, at)
	}
	if err := db.SetGroupSnooze("/tmp/checkout", time.Time{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, _, ok, err := db.LoadGroupSnooze("/tmp/checkout"); ok || err != nil {
		t.Errorf("after clear: ok=%v err=%v, want absent", ok, err)
	}
}

// TestClearIndefiniteSessionSnoozeKeepsATimedOne: the CLI clears an "until I
// wake it" snooze on relaunch from a row it read earlier. If the TUI set a
// timed snooze in between, that deadline must survive.
func TestClearIndefiniteSessionSnoozeKeepsATimedOne(t *testing.T) {
	db := openTestDB(t)
	row := &SessionRow{ID: "abc-1", Title: "t", ProjectPath: "/tmp/p", Status: "idle", SnoozedUntil: SnoozeForever}
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := db.ClearIndefiniteSessionSnooze(row.ID); err != nil {
		t.Fatalf("clear forever: %v", err)
	}
	if until, _, _, _ := db.LoadSessionSnooze(row.ID); !until.IsZero() {
		t.Errorf("indefinite snooze not cleared: %v", until)
	}

	timed := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := db.SetSessionSnooze(row.ID, timed); err != nil {
		t.Fatalf("SetSessionSnooze: %v", err)
	}
	if err := db.ClearIndefiniteSessionSnooze(row.ID); err != nil {
		t.Fatalf("clear timed: %v", err)
	}
	if until, _, _, _ := db.LoadSessionSnooze(row.ID); !until.Equal(timed) {
		t.Errorf("timed snooze = %v, want %v kept", until, timed)
	}
}
