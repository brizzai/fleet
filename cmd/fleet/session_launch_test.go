package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/git"
)

func threeAccounts() []claudeaccount.Account {
	return []claudeaccount.Account{
		{Email: "first@x", ConfigDir: "/dirs/first", Order: 0},
		{Email: "second@x", ConfigDir: "/dirs/second", Order: 1},
		{Email: "third@x", ConfigDir: "/dirs/third", Order: 2},
	}
}

// stubUsage replaces readLaunchUsage and records which accounts it was asked for.
func stubUsage(t *testing.T, usage map[string]claudeaccount.Usage, unread []claudeaccount.Unread) *[]string {
	t.Helper()
	var asked []string
	prev := readLaunchUsage
	t.Cleanup(func() { readLaunchUsage = prev })
	readLaunchUsage = func(accts []claudeaccount.Account) (map[string]claudeaccount.Usage, []claudeaccount.Unread) {
		for _, a := range accts {
			asked = append(asked, a.Email)
		}
		return usage, unread
	}
	return &asked
}

func TestSelectLaunchAccountPicksLeastUsedNotFirst(t *testing.T) {
	now := time.Now()
	stubUsage(t, map[string]claudeaccount.Usage{
		"first@x":  {SevenDayPct: 80, FetchedAt: now},
		"second@x": {SevenDayPct: 20, FetchedAt: now},
		"third@x":  {SevenDayPct: 60, FetchedAt: now},
	}, nil)

	acct, ok, note := selectLaunchAccount(&config.Config{AccountStrategy: "least_used_weekly"}, threeAccounts(), nil)
	if !ok || acct.Email != "second@x" || note != "" {
		t.Fatalf("picked %s note %q, want second@x and no note", acct.Email, note)
	}
}

func TestSelectLaunchAccountFallsBackWithReason(t *testing.T) {
	stubUsage(t, map[string]claudeaccount.Usage{}, []claudeaccount.Unread{
		{Email: "first@x", Reason: "timed out"},
		{Email: "second@x", Reason: "timed out"},
	})

	acct, ok, note := selectLaunchAccount(&config.Config{}, threeAccounts(), []string{"first@x", "second@x"})
	want := "Note: couldn't read quota (first@x: timed out; second@x: timed out), so configured order chose first@x."
	if !ok || acct.Email != "first@x" || note != want {
		t.Fatalf("picked %s note %q, want first@x %q", acct.Email, note, want)
	}
}

// An old reading kept because it shows an account spent decided the pick, so
// the note must not credit configured order.
func TestSelectLaunchAccountNoteNamesOldReadings(t *testing.T) {
	now := time.Now()
	stubUsage(t, map[string]claudeaccount.Usage{
		"first@x": {SevenDayPct: 100, SevenDayReset: now.Add(96 * time.Hour), FetchedAt: now.Add(-time.Hour)},
	}, []claudeaccount.Unread{
		{Email: "first@x", Reason: "usage endpoint returned 503"},
		{Email: "second@x", Reason: "usage endpoint returned 503"},
	})

	acct, _, note := selectLaunchAccount(&config.Config{}, threeAccounts(), []string{"first@x", "second@x"})
	want := "Note: couldn't read fresh quota (first@x: usage endpoint returned 503; second@x: usage endpoint returned 503); chose second@x from the last known readings."
	if acct.Email != "second@x" || note != want {
		t.Fatalf("picked %s note %q, want second@x %q", acct.Email, note, want)
	}
}

func TestSelectLaunchAccountPartialRead(t *testing.T) {
	now := time.Now()
	stubUsage(t, map[string]claudeaccount.Usage{
		"second@x": {SevenDayPct: 70, FetchedAt: now},
	}, []claudeaccount.Unread{{Email: "first@x", Reason: "usage endpoint returned 503"}})

	acct, _, note := selectLaunchAccount(&config.Config{}, threeAccounts(), []string{"first@x", "second@x"})
	// first@x ranks as unknown (midpoint), below second@x's 70.
	if acct.Email != "first@x" || note != "Note: couldn't read quota for first@x: usage endpoint returned 503." {
		t.Fatalf("picked %s note %q", acct.Email, note)
	}
}

// Quota is read only for accounts the origin allows.
func TestSelectLaunchAccountReadsOnlyAllowed(t *testing.T) {
	asked := stubUsage(t, nil, nil)
	selectLaunchAccount(&config.Config{}, threeAccounts(), []string{"second@x", "third@x"})
	if strings.Join(*asked, ",") != "second@x,third@x" {
		t.Fatalf("read quota for %v, want only the allowed accounts", *asked)
	}
}

func TestSelectLaunchAccountOtherStrategiesReadNoQuota(t *testing.T) {
	asked := stubUsage(t, nil, nil)
	for strategy, want := range map[string]string{"waterfall": "first@x", "manual": "second@x"} {
		acct, ok, note := selectLaunchAccount(&config.Config{AccountStrategy: strategy, DefaultAccount: "second@x"}, threeAccounts(), nil)
		if !ok || acct.Email != want || note != "" {
			t.Fatalf("%s: picked %s note %q, want %s", strategy, acct.Email, note, want)
		}
	}
	// One allowed account is no choice.
	selectLaunchAccount(&config.Config{}, threeAccounts(), []string{"second@x"})
	if len(*asked) != 0 {
		t.Fatalf("quota read for %v", *asked)
	}
}

func TestExplicitAccountOverridesStrategy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	store := claudeaccount.Load()
	for _, a := range threeAccounts() {
		a.OrgUUID = a.Email
		store.Upsert(a)
	}
	asked := stubUsage(t, nil, nil)

	got := resolveLaunchAccount(&config.Config{AccountStrategy: "least_used_weekly"}, store, t.TempDir(), agent.Claude, "second@x")
	if got != "second@x" || len(*asked) != 0 {
		t.Fatalf("account = %q, quota read for %v; want second@x and no read", got, *asked)
	}
}

// fleet add and fleet worktree must share one account path.
func TestAddAndWorktreeShareAccountResolution(t *testing.T) {
	for _, f := range []string{"add.go", "worktree.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "resolveLaunchAccount(") || strings.Contains(string(src), "claudeaccount.Select(") {
			t.Fatalf("%s must pick accounts through resolveLaunchAccount", f)
		}
	}
}

// An area pins the account for its directories; --account still wins, and an
// area account the origin disallows (or fleet doesn't know) falls back to the
// normal selection.
func TestAreaAccountResolution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	store := claudeaccount.Load()
	for _, a := range threeAccounts() {
		a.OrgUUID = a.Email
		store.Upsert(a)
	}
	stubUsage(t, nil, nil)

	dir := t.TempDir()
	areas := []config.Area{{Name: "work", Paths: []string{dir}, Account: "second@x"}}
	origin := originExpandKey(git.GetOriginKey(dir))

	tests := []struct {
		name     string
		cfg      *config.Config
		explicit string
		want     string
	}{
		{"area account used", &config.Config{Areas: areas, AccountStrategy: "waterfall"}, "", "second@x"},
		{"explicit --account wins", &config.Config{Areas: areas, AccountStrategy: "waterfall"}, "third@x", "third@x"},
		{"not allowed here falls to normal selection", &config.Config{Areas: areas, AccountStrategy: "waterfall",
			AllowedAccounts: map[string][]string{origin: {"first@x", "third@x"}}}, "", "first@x"},
		{"unknown area account falls to normal selection", &config.Config{AccountStrategy: "waterfall",
			Areas: []config.Area{{Name: "work", Paths: []string{dir}, Account: "ghost@x"}}}, "", "first@x"},
		{"no areas: normal selection", &config.Config{AccountStrategy: "waterfall"}, "", "first@x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveLaunchAccount(tc.cfg, store, dir, agent.Claude, tc.explicit); got != tc.want {
				t.Errorf("account = %q, want %q", got, tc.want)
			}
		})
	}
}
