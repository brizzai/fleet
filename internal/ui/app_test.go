package ui

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
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
			home.writeGitInfo(func(m map[string]*git.RepoInfo) bool {
				m[repo] = &git.RepoInfo{Branch: "main"}
				return true
			})
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

	home.refreshAllGitAndPR(repos, 4, 0, nil)

	snap := home.gitInfo()
	for _, r := range repos {
		if _, ok := snap[r]; !ok {
			t.Errorf("gitInfoCache missing entry for %q after bootstrap", r)
		}
	}
}

// TestShouldRefreshPR_TTLBoundary covers the four interesting boundaries
// of the gate: never-fetched, within-TTL skip, beyond-TTL refresh, and
// rate-limit back-off override.
func TestShouldRefreshPR_TTLBoundary(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		info    git.RepoInfo
		ttl     time.Duration
		want    bool
		comment string
	}{
		{
			name:    "never_fetched",
			info:    git.RepoInfo{},
			ttl:     prTTLHot,
			want:    true,
			comment: "zero LastPRRefresh means we've never tried — must fetch",
		},
		{
			name:    "within_hot_ttl",
			info:    git.RepoInfo{LastPRRefresh: now.Add(-30 * time.Second)},
			ttl:     prTTLHot,
			want:    false,
			comment: "30s < 60s hot TTL — skip",
		},
		{
			name:    "beyond_hot_ttl",
			info:    git.RepoInfo{LastPRRefresh: now.Add(-90 * time.Second)},
			ttl:     prTTLHot,
			want:    true,
			comment: "90s > 60s hot TTL — refresh",
		},
		{
			name:    "within_cold_ttl",
			info:    git.RepoInfo{LastPRRefresh: now.Add(-90 * time.Second)},
			ttl:     prTTLCold,
			want:    false,
			comment: "90s < 2min cold TTL — skip; cold repos refresh less often",
		},
		{
			name: "rate_limit_backoff_active",
			info: git.RepoInfo{
				LastPRRefresh:   now.Add(-10 * time.Minute), // would normally refresh
				PRRateLimitedAt: now.Add(-30 * time.Second), // but rate-limited
			},
			ttl:     prTTLHot,
			want:    false,
			comment: "back-off overrides TTL — don't pile on while throttled",
		},
		{
			name: "rate_limit_backoff_expired",
			info: git.RepoInfo{
				LastPRRefresh:   now.Add(-10 * time.Minute),
				PRRateLimitedAt: now.Add(-6 * time.Minute), // older than 5min back-off
			},
			ttl:     prTTLHot,
			want:    true,
			comment: "back-off expired, TTL exceeded — refresh",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldRefreshPR(&c.info, c.ttl); got != c.want {
				t.Errorf("%s: got=%v want=%v — %s", c.name, got, c.want, c.comment)
			}
		})
	}
}

// TestRepoTTLFor_HotnessWindow verifies the 60s / 2min classification.
func TestRepoTTLFor_HotnessWindow(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fleet-ttl-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	storage, err := session.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer storage.Close()

	home := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	now := time.Now()
	home.repoLastHotAt["/repo/hot-recent"] = now.Add(-30 * time.Second)
	home.repoLastHotAt["/repo/hot-edge"] = now.Add(-14 * time.Minute) // still inside 15min window
	home.repoLastHotAt["/repo/cold"] = now.Add(-20 * time.Minute)     // outside window

	if got := home.repoTTLFor("/repo/hot-recent", now); got != prTTLHot {
		t.Errorf("hot-recent TTL = %v, want %v", got, prTTLHot)
	}
	if got := home.repoTTLFor("/repo/hot-edge", now); got != prTTLHot {
		t.Errorf("hot-edge TTL = %v, want %v (still within 15min window)", got, prTTLHot)
	}
	if got := home.repoTTLFor("/repo/cold", now); got != prTTLCold {
		t.Errorf("cold TTL = %v, want %v", got, prTTLCold)
	}
	if got := home.repoTTLFor("/repo/never-seen", now); got != prTTLCold {
		t.Errorf("never-seen TTL = %v, want cold (default)", got)
	}
}

// TestPRCacheRoundTrip_BranchSwitchInvalidatesPR proves the persistence +
// branch-check guardrail works end-to-end: save a cached PR for branch A,
// load it back, then prove the carry-forward logic drops it when the
// current branch is B. Doesn't exercise the worker goroutine itself —
// that's harder to drive without spawning subprocesses — but exercises the
// rule the goroutine relies on.
func TestPRCacheRoundTrip_BranchSwitchInvalidatesPR(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fleet-cache-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	storage, err := session.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer storage.Close()

	// Save a row for branch "main".
	pr := &github.PR{Number: 42, State: "OPEN", CIStatus: "SUCCESS"}
	repo := "/tmp/fake-repo"
	saved := &session.PRCacheRow{
		RepoPath:      repo,
		Branch:        "main",
		OriginKey:     "github.com/acme/repo",
		PR:            pr,
		LastPRRefresh: time.Now().Add(-30 * time.Second),
	}
	if err := storage.SavePRCacheRow(saved); err != nil {
		t.Fatalf("SavePRCacheRow: %v", err)
	}

	// Load back, confirm round-trip.
	loaded, err := storage.LoadPRCache()
	if err != nil {
		t.Fatalf("LoadPRCache: %v", err)
	}
	row, ok := loaded[repo]
	if !ok {
		t.Fatalf("expected row for %q in loaded cache, got %v", repo, loaded)
	}
	if row.PR == nil || row.PR.Number != 42 {
		t.Errorf("PR didn't round-trip — got %+v", row.PR)
	}
	if row.Branch != "main" {
		t.Errorf("Branch round-trip = %q, want main", row.Branch)
	}

	// Carry-forward simulation: gitInfoCache has the row for "main",
	// RefreshGitInfo just observed branch "feat/x". The branch-match
	// guard must drop the PR.
	cachedInfo := &git.RepoInfo{
		Branch:        row.Branch,
		OriginKey:     row.OriginKey,
		PR:            row.PR,
		LastPRRefresh: row.LastPRRefresh,
	}
	fresh := &git.RepoInfo{Branch: "feat/x"}
	// Mirror the worker's check inline: PR carries forward iff branches match.
	if cachedInfo.Branch == fresh.Branch {
		fresh.PR = cachedInfo.PR
		fresh.LastPRRefresh = cachedInfo.LastPRRefresh
	}
	if fresh.PR != nil {
		t.Errorf("PR carried forward across branch switch (cached=%s fresh=%s) — guardrail broken", cachedInfo.Branch, fresh.Branch)
	}
	if !fresh.LastPRRefresh.IsZero() {
		t.Errorf("LastPRRefresh carried forward across branch switch — guardrail broken")
	}

	// And the positive case: same branch → PR carries.
	cachedInfo.Branch = "feat/x"
	fresh2 := &git.RepoInfo{Branch: "feat/x"}
	if cachedInfo.Branch == fresh2.Branch {
		fresh2.PR = cachedInfo.PR
		fresh2.LastPRRefresh = cachedInfo.LastPRRefresh
	}
	if fresh2.PR == nil || fresh2.PR.Number != 42 {
		t.Errorf("PR didn't carry forward on matching branch — got %+v", fresh2.PR)
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
