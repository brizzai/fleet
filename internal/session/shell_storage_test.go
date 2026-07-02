package session

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSaveAndLoadShellsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	now := time.Now().Truncate(time.Second)
	row := &ShellRow{
		ID:        "sh001234-1234567890",
		Name:      "dev",
		RepoPath:  "/home/user/project",
		Command:   "npm run dev",
		TmuxName:  "fleetsh_dev_abcd1234",
		CreatedAt: now,
	}
	if err := db.SaveShell(row); err != nil {
		t.Fatalf("SaveShell failed: %v", err)
	}

	shells, err := db.LoadShells()
	if err != nil {
		t.Fatalf("LoadShells failed: %v", err)
	}
	if len(shells) != 1 {
		t.Fatalf("expected 1 shell, got %d", len(shells))
	}
	got := shells[0]
	if got.ID != row.ID || got.Name != row.Name || got.RepoPath != row.RepoPath ||
		got.Command != row.Command || got.TmuxName != row.TmuxName {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, row)
	}
	if got.CreatedAt.Unix() != row.CreatedAt.Unix() {
		t.Errorf("CreatedAt: got %v, want %v", got.CreatedAt, row.CreatedAt)
	}
}

func TestUpdateAndDeleteShell(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	row := &ShellRow{ID: "sh1", Name: "shell", RepoPath: "/r", CreatedAt: time.Now()}
	if err := db.SaveShell(row); err != nil {
		t.Fatalf("SaveShell: %v", err)
	}

	if err := db.UpdateShellTmuxName("sh1", "fleetsh_logs_ffff0000"); err != nil {
		t.Fatalf("UpdateShellTmuxName: %v", err)
	}
	shells, err := db.LoadShells()
	if err != nil {
		t.Fatalf("LoadShells after update: %v", err)
	}
	if len(shells) != 1 || shells[0].Name != "shell" || shells[0].TmuxName != "fleetsh_logs_ffff0000" {
		t.Fatalf("update did not persist: %+v", shells)
	}

	if err := db.DeleteShell("sh1"); err != nil {
		t.Fatalf("DeleteShell: %v", err)
	}
	shells, err = db.LoadShells()
	if err != nil {
		t.Fatalf("LoadShells after delete: %v", err)
	}
	if len(shells) != 0 {
		t.Errorf("expected 0 shells after delete, got %d", len(shells))
	}
}
