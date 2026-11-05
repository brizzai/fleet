package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
)

func twoAccounts() []claudeaccount.Account {
	return []claudeaccount.Account{
		{Email: "first@x", ConfigDir: "/dirs/first", Order: 0},
		{Email: "second@x", ConfigDir: "/dirs/second", Order: 1},
	}
}

func readerOf(usage map[string]claudeaccount.Usage, unread []claudeaccount.Unread, called *bool) usageReader {
	return func([]claudeaccount.Account) (map[string]claudeaccount.Usage, []claudeaccount.Unread) {
		*called = true
		return usage, unread
	}
}

func TestSelectLaunchAccountPicksLeastUsedNotFirst(t *testing.T) {
	now := time.Now()
	var called bool
	read := readerOf(map[string]claudeaccount.Usage{
		"first@x":  {SevenDayPct: 80, FetchedAt: now},
		"second@x": {SevenDayPct: 20, FetchedAt: now},
	}, nil, &called)

	acct, ok, notes := selectLaunchAccount(&config.Config{AccountStrategy: "least_used_weekly"}, twoAccounts(), nil, read)
	if !ok || acct.Email != "second@x" {
		t.Fatalf("picked %s, want second@x", acct.Email)
	}
	if len(notes) != 1 || strings.Contains(notes[0], "configured order") {
		t.Fatalf("notes = %q", notes)
	}
}

func TestSelectLaunchAccountFallsBackWithReason(t *testing.T) {
	var called bool
	read := readerOf(map[string]claudeaccount.Usage{}, []claudeaccount.Unread{
		{Email: "first@x", Reason: "timed out"},
		{Email: "second@x", Reason: "timed out"},
	}, &called)

	acct, ok, notes := selectLaunchAccount(&config.Config{AccountStrategy: "least_used_weekly"}, twoAccounts(), nil, read)
	if !ok || acct.Email != "first@x" {
		t.Fatalf("picked %s, want configured order first@x", acct.Email)
	}
	want := "Note: couldn't read quota (first@x: timed out; second@x: timed out), so configured order chose first@x."
	if len(notes) != 1 || notes[0] != want {
		t.Fatalf("notes = %q, want %q", notes, want)
	}
}

func TestSelectLaunchAccountPartialRead(t *testing.T) {
	now := time.Now()
	var called bool
	read := readerOf(map[string]claudeaccount.Usage{
		"second@x": {SevenDayPct: 70, FetchedAt: now},
	}, []claudeaccount.Unread{{Email: "first@x", Reason: "usage endpoint returned 503"}}, &called)

	acct, _, notes := selectLaunchAccount(&config.Config{AccountStrategy: "least_used_weekly"}, twoAccounts(), nil, read)
	// first@x ranks at the unknown midpoint (50), below second@x's 70.
	if acct.Email != "first@x" || len(notes) != 2 || !strings.Contains(notes[1], "first@x: usage endpoint returned 503") {
		t.Fatalf("picked %s, notes = %q", acct.Email, notes)
	}
}

func TestSelectLaunchAccountOtherStrategiesReadNoQuota(t *testing.T) {
	for _, strategy := range []string{"waterfall", "manual"} {
		var called bool
		acct, ok, notes := selectLaunchAccount(&config.Config{AccountStrategy: strategy, DefaultAccount: "second@x"},
			twoAccounts(), nil, readerOf(nil, nil, &called))
		if called || !ok || len(notes) != 0 {
			t.Fatalf("%s: called=%v ok=%v notes=%q", strategy, called, ok, notes)
		}
		want := map[string]string{"waterfall": "first@x", "manual": "second@x"}[strategy]
		if acct.Email != want {
			t.Fatalf("%s: picked %s, want %s", strategy, acct.Email, want)
		}
	}
}

// One allowed account is no choice, so quota isn't read.
func TestSelectLaunchAccountSingleCandidateReadsNoQuota(t *testing.T) {
	var called bool
	acct, _, notes := selectLaunchAccount(&config.Config{}, twoAccounts(), []string{"second@x"}, readerOf(nil, nil, &called))
	if called || acct.Email != "second@x" || len(notes) != 0 {
		t.Fatalf("called=%v picked=%s notes=%q", called, acct.Email, notes)
	}
}

func TestExplicitAccountOverridesStrategy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	store := claudeaccount.Load()
	for _, a := range twoAccounts() {
		a.OrgUUID = a.Email
		store.Upsert(a)
	}
	prev := readLaunchUsage
	t.Cleanup(func() { readLaunchUsage = prev })
	readLaunchUsage = func([]claudeaccount.Account) (map[string]claudeaccount.Usage, []claudeaccount.Unread) {
		t.Fatal("quota read despite an explicit --account")
		return nil, nil
	}

	got := resolveLaunchAccount(&config.Config{AccountStrategy: "least_used_weekly"}, store, t.TempDir(), agent.Claude, "second@x")
	if got != "second@x" {
		t.Fatalf("account = %q, want second@x", got)
	}
}

// fleet add and fleet worktree must share one account path.
func TestAddAndWorktreeShareAccountResolution(t *testing.T) {
	for _, f := range []string{"add.go", "worktree.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "resolveLaunchAccount(") {
			t.Fatalf("%s no longer calls resolveLaunchAccount", f)
		}
		if strings.Contains(string(src), "claudeaccount.Select(") {
			t.Fatalf("%s calls claudeaccount.Select directly; go through resolveLaunchAccount", f)
		}
	}
}
