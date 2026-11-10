package session

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestEveryConnectionGetsThePragmas pins the root cause of the SQLITE_BUSY
// errors seen from a single fleet instance: busy_timeout and foreign_keys are
// per-connection, so setting them with db.Exec configured only the first pooled
// connection. Every connection the pool hands out must carry both.
func TestEveryConnectionGetsThePragmas(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	// Hold stateDBMaxConns connections at once, so the pool is forced to open
	// that many distinct ones rather than reusing the first.
	held := make([]*sql.Conn, 0, stateDBMaxConns)
	for i := 0; i < stateDBMaxConns; i++ {
		c, err := db.db.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn %d: %v", i, err)
		}
		held = append(held, c)
	}
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()

	for i, c := range held {
		var timeout, fk int
		var mode string
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
			t.Fatalf("conn %d busy_timeout: %v", i, err)
		}
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("conn %d foreign_keys: %v", i, err)
		}
		if err := c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
			t.Fatalf("conn %d journal_mode: %v", i, err)
		}
		if timeout != 5000 {
			t.Errorf("conn %d: busy_timeout = %d, want 5000 — a connection without a busy handler fails with SQLITE_BUSY the instant another writer holds the lock", i, timeout)
		}
		if fk != 1 {
			t.Errorf("conn %d: foreign_keys = %d, want 1 — ON DELETE CASCADE is not enforced on this connection", i, fk)
		}
		if mode != "wal" {
			t.Errorf("conn %d: journal_mode = %q, want wal", i, mode)
		}
	}
}

// TestConcurrentWritersNeverSeeBusy reproduces the git worker's shape — several
// goroutines upserting PR cache rows at once — and requires every write to land.
// Before the DSN fix this failed most writes with SQLITE_BUSY.
func TestConcurrentWritersNeverSeeBusy(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// A second handle on the same file stands in for a second fleet process
	// (the CLI, or another TUI): its writes contend through the file lock, not
	// the shared pool.
	other, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open second handle: %v", err)
	}
	defer other.Close()

	const writers, perWriter = 8, 50
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		target := db
		if w%2 == 1 {
			target = other
		}
		wg.Add(1)
		go func(w int, s *StateDB) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				err := s.SavePRCacheRow(&PRCacheRow{
					RepoPath:      fmt.Sprintf("/repo/%d", (w*perWriter+i)%10),
					Branch:        fmt.Sprintf("b%d", i),
					LastPRRefresh: time.Now(),
				})
				if err != nil {
					errs <- err
				}
			}
		}(w, target)
	}
	wg.Wait()
	close(errs)

	failed := 0
	var first error
	for err := range errs {
		if first == nil {
			first = err
		}
		failed++
	}
	if failed > 0 {
		t.Fatalf("%d of %d concurrent writes failed; first: %v", failed, writers*perWriter, first)
	}
}

// A path SQLite's URI parser would misread (`?` starts a query, `#` a
// fragment, `%` an escape) must still open the file it names.
func TestOpenPathWithURIMetacharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "we?ird #dir 100%")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	db.Close()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("database not created at the requested path: %v", err)
	}
}
