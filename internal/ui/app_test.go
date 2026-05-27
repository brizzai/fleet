package ui

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
)

func TestHomeInitializes(t *testing.T) {
	// Create temp dir for in-memory-like SQLite DB.
	tmpDir, err := os.MkdirTemp("", "fleet-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	storage, err := session.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer storage.Close()

	cfg := &config.Config{
		TickIntervalSec: 2,
	}

	// Should not panic.
	home := NewHome(storage, cfg, "test", analytics.Identity{})
	if home == nil {
		t.Fatal("NewHome returned nil")
		return
	}

	// Set minimal dimensions for rendering.
	home.width = 120
	home.height = 40

	// View() should not panic and should return non-empty output.
	output := home.View()
	if output == "" {
		t.Error("View() returned empty string")
	}
}

// TestViewGitInfoCacheRace guards against the "concurrent map read and map write"
// fatal that happens if View() reads h.gitInfoCache while the status worker writes
// it. Run with `go test -race` — pre-fix this trips the race detector reliably.
func TestViewGitInfoCacheRace(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fleet-race-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	storage, err := session.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer storage.Close()

	home := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	home.width = 120
	home.height = 40

	// Seed a repo-header flatItem so RenderSidebar hits the gitInfo[item.RepoPath]
	// read path at sidebar.go:183.
	const repo = "/tmp/fleet-race-repo"
	home.flatItems = []SidebarItem{{IsRepoHeader: true, RepoPath: repo, Expanded: false, SessionCount: 0}}

	const iterations = 500
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			home.workerMu.Lock()
			home.gitInfoCache[repo] = &git.RepoInfo{Branch: "main"}
			home.workerMu.Unlock()
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = home.View()
		}
	}()

	wg.Wait()
}
