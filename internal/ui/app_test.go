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

	// Seed a checkout-header flatItem so RenderSidebar hits the
	// gitInfo[item.RepoPath] read path inside the checkout renderer.
	const repo = "/tmp/fleet-race-repo"
	home.flatItems = []SidebarItem{{IsRepoHeader: true, IsCheckoutHeader: true, RepoPath: repo, Expanded: false, SessionCount: 0}}

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

// TestRefreshAllGitAndPR_AllReposPopulated proves that the bootstrap
// fan-out fills gitInfoCache for every repo in the input set. We can't shell
// out to a real git in the unit test, but RefreshGitInfo gracefully returns a
// non-nil RepoInfo even for non-git paths — so a tmp dir is a sufficient
// witness that the goroutine ran and the cache write happened.
func TestRefreshAllGitAndPR_AllReposPopulated(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fleet-bootstrap-*")
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

	// Three throwaway repo paths — they don't need to be real git repos for
	// the cache-write contract; RefreshGitInfo returns a non-nil RepoInfo
	// either way.
	repos := []string{
		filepath.Join(tmpDir, "r1"),
		filepath.Join(tmpDir, "r2"),
		filepath.Join(tmpDir, "r3"),
	}

	home.refreshAllGitAndPR(repos, 4, 0)

	home.workerMu.Lock()
	defer home.workerMu.Unlock()
	for _, r := range repos {
		if _, ok := home.gitInfoCache[r]; !ok {
			t.Errorf("gitInfoCache missing entry for %q after bootstrap", r)
		}
	}
}

// TestBootstrapRepoSet_UnionOfSessionsAndPinned proves the bootstrap covers
// both sources — a pinned repo with no live sessions still gets its origin
// resolved on the first paint.
func TestBootstrapRepoSet_UnionOfSessionsAndPinned(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fleet-bootset-*")
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
	home.sessions = []*session.Session{
		session.NewSession("s1", "/tmp/repoA"),
		session.NewSession("s2", "/tmp/repoA/subdir"),
	}
	home.pinnedRepos["/tmp/repoB"] = true
	home.pinnedRepos["/tmp/repoA"] = true // overlap with session repo — must not double-count

	repos := home.bootstrapRepoSet()
	got := map[string]bool{}
	for _, r := range repos {
		if got[r] {
			t.Errorf("bootstrap set has duplicate entry %q", r)
		}
		got[r] = true
	}
	// session.GetRepoRoot may resolve /tmp/repoA/subdir to its own root or
	// fall back to the path itself; we only assert that repoA and repoB
	// each appear at least once.
	for _, must := range []string{"/tmp/repoA", "/tmp/repoB"} {
		found := false
		for r := range got {
			if r == must {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("bootstrap set missing %q (got %v)", must, repos)
		}
	}
}
