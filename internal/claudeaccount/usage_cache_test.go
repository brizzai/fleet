package claudeaccount

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func launchAccounts() []Account {
	return []Account{
		{Email: "a@x", ConfigDir: "/dirs/a", Order: 0},
		{Email: "b@x", ConfigDir: "/dirs/b", Order: 1},
	}
}

func fetchByDir(m map[string]Usage, calls *atomic.Int32) func(context.Context, string) (Usage, error) {
	return func(_ context.Context, dir string) (Usage, error) {
		calls.Add(1)
		u, ok := m[dir]
		if !ok {
			return Usage{}, errors.New("unexpected dir")
		}
		return u, nil
	}
}

func TestUsageForLaunchFetchesAndCaches(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "cache.json")
	var calls atomic.Int32
	fetch := fetchByDir(map[string]Usage{
		"/dirs/a": {SevenDayPct: 70, FetchedAt: now},
		"/dirs/b": {SevenDayPct: 10, FetchedAt: now},
	}, &calls)

	usage, unread := UsageForLaunch(context.Background(), LaunchUsageOpts{
		Accounts: launchAccounts(), CachePath: path, Now: now, Fetch: fetch,
	})
	if len(unread) != 0 || calls.Load() != 2 {
		t.Fatalf("unread=%v calls=%d", unread, calls.Load())
	}
	acct, _ := Select(SelectOpts{Accounts: launchAccounts(), Usage: usage, Strategy: StrategyLeastUsedWeekly, Now: now})
	if acct.Email != "b@x" {
		t.Fatalf("picked %s, want b@x (least weekly use, second in order)", acct.Email)
	}

	// A second run inside the poll floor reuses the cache and asks nothing.
	usage, unread = UsageForLaunch(context.Background(), LaunchUsageOpts{
		Accounts: launchAccounts(), CachePath: path, Now: now.Add(time.Minute), Fetch: fetch,
	})
	if calls.Load() != 2 || len(unread) != 0 || usage["b@x"].SevenDayPct != 10 {
		t.Fatalf("expected cached reuse: calls=%d unread=%v usage=%v", calls.Load(), unread, usage)
	}
}

func TestUsageForLaunchFallsBackWhenFetchFails(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "cache.json")
	fail := func(context.Context, string) (Usage, error) { return Usage{}, ErrNoCredential }

	usage, unread := UsageForLaunch(context.Background(), LaunchUsageOpts{
		Accounts: launchAccounts(), CachePath: path, Now: now, Fetch: fail,
	})
	if len(unread) != 2 || unread[0].Reason != "couldn't read its Keychain credential" {
		t.Fatalf("unread = %+v", unread)
	}
	acct, ok := Select(SelectOpts{Accounts: launchAccounts(), Usage: usage, Strategy: StrategyLeastUsedWeekly, Now: now})
	if !ok || acct.Email != "a@x" {
		t.Fatalf("picked %s ok=%v, want configured order a@x", acct.Email, ok)
	}
}

func TestUsageForLaunchDropsReadingsPastMaxAge(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "cache.json")
	old := now.Add(-UsageCacheMaxAge - time.Minute)
	if err := SaveUsageCache(path, map[string]Usage{
		"a@x": {SevenDayPct: 90, FetchedAt: old, AttemptedAt: old},
		"b@x": {SevenDayPct: 5, FetchedAt: old, AttemptedAt: old},
	}); err != nil {
		t.Fatal(err)
	}
	fail := func(context.Context, string) (Usage, error) { return Usage{}, statusError(503) }

	usage, unread := UsageForLaunch(context.Background(), LaunchUsageOpts{
		Accounts: launchAccounts(), CachePath: path, Now: now, Fetch: fail,
	})
	if len(unread) != 2 || unread[1].Reason != "usage endpoint returned 503" {
		t.Fatalf("unread = %+v", unread)
	}
	if usage["b@x"].Known() {
		t.Fatalf("a reading past UsageCacheMaxAge must not rank: %+v", usage["b@x"])
	}
	acct, _ := Select(SelectOpts{Accounts: launchAccounts(), Usage: usage, Strategy: StrategyLeastUsedWeekly, Now: now})
	if acct.Email != "a@x" {
		t.Fatalf("picked %s, want configured order a@x", acct.Email)
	}
}

func TestUsageForLaunchTimesOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	var accts []Account
	for _, e := range []string{"a", "b", "c", "d", "e"} {
		accts = append(accts, Account{Email: e, ConfigDir: "/" + e})
	}
	// Like `security` killed by the deadline: the error doesn't say timeout.
	hang := func(ctx context.Context, _ string) (Usage, error) {
		<-ctx.Done()
		return Usage{}, ErrNoCredential
	}
	timeout := 200 * time.Millisecond
	start := time.Now()
	_, unread := UsageForLaunch(context.Background(), LaunchUsageOpts{
		Accounts: accts, CachePath: path, Timeout: timeout, Fetch: hang,
	})
	if el := time.Since(start); el > 3*timeout {
		t.Fatalf("took %s; accounts must be fetched in parallel under one timeout", el)
	}
	if len(unread) != 5 || unread[0].Reason != "timed out" {
		t.Fatalf("unread = %+v", unread)
	}
}

// One account cached, the others fetched: the map must not be written from
// two goroutines at once (run under -race).
func TestUsageForLaunchMixedCachedAndFetched(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "cache.json")
	accts := []Account{{Email: "due", ConfigDir: "/due"}}
	seed := map[string]Usage{}
	for i := range 50 {
		e := fmt.Sprintf("cached%d", i)
		accts = append(accts, Account{Email: e, ConfigDir: "/" + e})
		seed[e] = Usage{SevenDayPct: 10, FetchedAt: now, AttemptedAt: now}
	}
	if err := SaveUsageCache(path, seed); err != nil {
		t.Fatal(err)
	}
	usage, unread := UsageForLaunch(context.Background(), LaunchUsageOpts{
		Accounts: accts, CachePath: path, Now: now,
		Fetch: func(context.Context, string) (Usage, error) { return Usage{}, ErrNoCredential },
	})
	if len(usage) != 51 || len(unread) != 1 || unread[0].Email != "due" {
		t.Fatalf("usage=%d unread=%+v", len(usage), unread)
	}
}

// An old reading that shows an account spent still keeps Select off it.
func TestUsageForLaunchKeepsOldSpentReading(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "cache.json")
	old := now.Add(-UsageCacheMaxAge - time.Minute)
	if err := SaveUsageCache(path, map[string]Usage{
		"a@x": {SevenDayPct: 100, SevenDayReset: now.Add(96 * time.Hour), FetchedAt: old, AttemptedAt: old},
		"b@x": {SevenDayPct: 40, FetchedAt: old, AttemptedAt: old},
	}); err != nil {
		t.Fatal(err)
	}
	fail := func(context.Context, string) (Usage, error) { return Usage{}, statusError(503) }
	usage, _ := UsageForLaunch(context.Background(), LaunchUsageOpts{
		Accounts: launchAccounts(), CachePath: path, Now: now, Fetch: fail,
	})
	acct, _ := Select(SelectOpts{Accounts: launchAccounts(), Usage: usage, Strategy: StrategyLeastUsedWeekly, Now: now})
	if acct.Email != "b@x" {
		t.Fatalf("picked %s, want b@x (a@x is spent for days)", acct.Email)
	}
}

func TestResetCachedUsageAfterLogin(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "cache.json")
	seed := map[string]Usage{
		"a@x": {AttemptedAt: now.Add(-time.Minute), LoggedOut: true, Err: ErrNotLoggedIn},
		"b@x": {SevenDayPct: 5, FetchedAt: now, AttemptedAt: now},
	}
	if err := SaveUsageCache(path, seed); err != nil {
		t.Fatal(err)
	}

	// A login whose own fetch failed: the stale verdict is dropped.
	if err := ResetCachedUsage(path, "a@x", Usage{}); err != nil {
		t.Fatal(err)
	}
	m := LoadUsageCache(path)
	if _, ok := m["a@x"]; ok || m["b@x"].SevenDayPct != 5 {
		t.Fatalf("cache = %+v", m)
	}

	// A login with a reading: it is kept, so the CLI reuses it inside the poll floor.
	if err := SaveUsageCache(path, seed); err != nil {
		t.Fatal(err)
	}
	if err := ResetCachedUsage(path, "a@x", Usage{SevenDayPct: 30, FetchedAt: now, AttemptedAt: now}); err != nil {
		t.Fatal(err)
	}
	got := LoadUsageCache(path)["a@x"]
	if got.LoggedOut || got.SevenDayPct != 30 || got.PollDue(now) {
		t.Fatalf("a@x = %+v, want the login reading", got)
	}
}

func TestUsageForLaunchLoggedOutIsFlagged(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "cache.json")
	fetch := func(_ context.Context, dir string) (Usage, error) {
		if dir == "/dirs/a" {
			return Usage{}, ErrNotLoggedIn
		}
		return Usage{SevenDayPct: 80, FetchedAt: now}, nil
	}
	usage, unread := UsageForLaunch(context.Background(), LaunchUsageOpts{
		Accounts: launchAccounts(), CachePath: path, Now: now, Fetch: fetch,
	})
	if len(unread) != 1 || unread[0].Reason != "not logged in" {
		t.Fatalf("unread = %+v", unread)
	}
	acct, _ := Select(SelectOpts{Accounts: launchAccounts(), Usage: usage, Strategy: StrategyLeastUsedWeekly, Now: now})
	if acct.Email != "b@x" {
		t.Fatalf("picked %s, want b@x (a@x is logged out)", acct.Email)
	}
}

// The cache stores a failure as its reason, never the error's text.
func TestUsageCacheNeverStoresErrorText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	secret := "sk-ant-oat01-" + strings.Repeat("Z", 40)
	fetch := func(context.Context, string) (Usage, error) { return Usage{}, errors.New("boom " + secret) }
	_, unread := UsageForLaunch(context.Background(), LaunchUsageOpts{
		Accounts: launchAccounts(), CachePath: path, Fetch: fetch,
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(unread[0].Reason, secret) {
		t.Fatalf("error text leaked: %s / %s", data, unread[0].Reason)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode %v, want 0600", st.Mode().Perm())
	}
}

func TestSaveUsageCacheKeepsNewerAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	now := time.Now()
	if err := SaveUsageCache(path, map[string]Usage{"a@x": {SevenDayPct: 40, FetchedAt: now, AttemptedAt: now}}); err != nil {
		t.Fatal(err)
	}
	older := now.Add(-time.Minute)
	if err := SaveUsageCache(path, map[string]Usage{"a@x": {SevenDayPct: 10, FetchedAt: older, AttemptedAt: older}}); err != nil {
		t.Fatal(err)
	}
	if got := LoadUsageCache(path)["a@x"].SevenDayPct; got != 40 {
		t.Fatalf("SevenDayPct = %d, want the newer 40", got)
	}
}
