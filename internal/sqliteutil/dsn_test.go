package sqliteutil

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// A path SQLite's URI parser would misread (`?` starts a query, `#` a
// fragment, `%` an escape) must still open the file it names.
func TestDSNOpensPathWithURIMetacharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "we?ird #dir 100%")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "x.db")
	db, err := sql.Open("sqlite", DSN(path, []string{"busy_timeout(5000)"}, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t(x)"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("database not created at the requested path: %v", err)
	}
}

func TestDSNCarriesPragmasAndTxlock(t *testing.T) {
	db, err := sql.Open("sqlite", DSN(filepath.Join(t.TempDir(), "y.db"), []string{"busy_timeout(1234)", "foreign_keys(1)"}, "immediate"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	var bt, fk int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&bt); err != nil || bt != 1234 {
		t.Errorf("busy_timeout = %d (%v), want 1234", bt, err)
	}
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys = %d (%v), want 1", fk, err)
	}
}
