package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/claudeaccount"
)

var hdrNow = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

func hdrAccounts(emails ...string) []claudeaccount.Account {
	out := make([]claudeaccount.Account, len(emails))
	for i, e := range emails {
		out[i] = claudeaccount.Account{Email: e, Order: i}
	}
	return out
}

func hdrUsage(fiveHour, sevenDay int) claudeaccount.Usage {
	return claudeaccount.Usage{
		FiveHourPct:   fiveHour,
		FiveHourReset: hdrNow.Add(90 * time.Minute),
		SevenDayPct:   sevenDay,
		FetchedAt:     hdrNow,
	}
}

// With one subscription there is no choice being made, so the number is trivia
// and the header row is better spent on nothing.
func TestAccountHeaderHiddenForSingleAccount(t *testing.T) {
	got := renderAccountUsageHeader(
		hdrAccounts("a@x.com"),
		map[string]claudeaccount.Usage{"a@x.com": hdrUsage(10, 20)},
		120, hdrNow)
	if got != "" {
		t.Fatalf("want empty for a single account, got %q", got)
	}
}

func TestAccountHeaderHiddenWhenNarrow(t *testing.T) {
	got := renderAccountUsageHeader(
		hdrAccounts("a@x.com", "b@x.com"),
		map[string]claudeaccount.Usage{"a@x.com": hdrUsage(10, 20), "b@x.com": hdrUsage(30, 40)},
		60, hdrNow)
	if got != "" {
		t.Fatalf("want empty below the width floor, got %q", got)
	}
}

func TestAccountHeaderHiddenBeforeFirstPoll(t *testing.T) {
	// Placeholder percentages would be worse than nothing — they read as real.
	got := renderAccountUsageHeader(
		hdrAccounts("a@x.com", "b@x.com"),
		map[string]claudeaccount.Usage{},
		120, hdrNow)
	if got != "" {
		t.Fatalf("want empty with no readings, got %q", got)
	}
}

func TestAccountHeaderShowsWeeklyPercentages(t *testing.T) {
	// The readout is the *weekly* window: the 5-hour bucket swings all day and
	// would make the header twitch.
	got := renderAccountUsageHeader(
		hdrAccounts("yuval@x.com", "work@x.com"),
		map[string]claudeaccount.Usage{
			"yuval@x.com": hdrUsage(10, 34),
			"work@x.com":  hdrUsage(20, 71),
		},
		140, hdrNow)

	for _, want := range []string{"34%", "71%", "yuval", "work"} {
		if !strings.Contains(got, want) {
			t.Errorf("header missing %q: %q", want, got)
		}
	}
	// The 5-hour figures must not appear — they're a different question.
	if strings.Contains(got, "10%") || strings.Contains(got, "20%") {
		t.Errorf("header leaked the 5-hour window: %q", got)
	}
}

func TestAccountHeaderSpentAccountShowsCountdownNotPercent(t *testing.T) {
	// A spent 5-hour bucket is the one thing here you can act on, so it
	// displaces the weekly figure rather than sitting beside it.
	got := renderAccountUsageHeader(
		hdrAccounts("a@x.com", "b@x.com"),
		map[string]claudeaccount.Usage{
			"a@x.com": {FiveHourPct: 100, FiveHourReset: hdrNow.Add(90 * time.Minute), SevenDayPct: 44, FetchedAt: hdrNow},
			"b@x.com": hdrUsage(5, 12),
		},
		140, hdrNow)

	if !strings.Contains(got, "spent") {
		t.Errorf("spent account not marked: %q", got)
	}
	if !strings.Contains(got, "in 2h") {
		t.Errorf("want a relative countdown, got %q", got)
	}
	if strings.Contains(got, "44%") {
		t.Errorf("spent account still showed its weekly percentage: %q", got)
	}
}

func TestAccountHeaderDropsLabelsWhenCramped(t *testing.T) {
	wide := renderAccountUsageHeader(
		hdrAccounts("yuval@x.com", "work@x.com"),
		map[string]claudeaccount.Usage{"yuval@x.com": hdrUsage(10, 34), "work@x.com": hdrUsage(20, 71)},
		140, hdrNow)
	narrow := renderAccountUsageHeader(
		hdrAccounts("yuval@x.com", "work@x.com"),
		map[string]claudeaccount.Usage{"yuval@x.com": hdrUsage(10, 34), "work@x.com": hdrUsage(20, 71)},
		80, hdrNow)

	if strings.Contains(narrow, "yuval") {
		t.Errorf("labels should drop at narrow widths: %q", narrow)
	}
	if lipgloss.Width(narrow) >= lipgloss.Width(wide) {
		t.Errorf("narrow readout (%d) should be shorter than wide (%d)", lipgloss.Width(narrow), lipgloss.Width(wide))
	}
	// Still has to carry the numbers, or dropping the labels bought nothing.
	if !strings.Contains(narrow, "34%") || !strings.Contains(narrow, "71%") {
		t.Errorf("compact readout lost its percentages: %q", narrow)
	}
}

// The readout shares the header row with the What's New badge, so it must stay
// inside the width it is handed.
func TestAccountHeaderFitsItsWidth(t *testing.T) {
	for _, w := range []int{72, 90, 100, 140, 200} {
		got := renderAccountUsageHeader(
			hdrAccounts("averylongaccountname@example.com", "another-long-one@example.com"),
			map[string]claudeaccount.Usage{
				"averylongaccountname@example.com": hdrUsage(10, 34),
				"another-long-one@example.com":     hdrUsage(20, 71),
			},
			w, hdrNow)
		if lipgloss.Width(got) > w {
			t.Errorf("width %d: readout is %d cells wide: %q", w, lipgloss.Width(got), got)
		}
	}
}

func TestRenderQuotaBarEdges(t *testing.T) {
	if got := lipgloss.Width(renderQuotaBar(0)); got != accountBarCells {
		t.Errorf("bar width at 0%% = %d, want %d", got, accountBarCells)
	}
	if got := lipgloss.Width(renderQuotaBar(100)); got != accountBarCells {
		t.Errorf("bar width at 100%% = %d, want %d", got, accountBarCells)
	}
	// Any nonzero usage must show a cell, or a busy account reads as untouched.
	if !strings.Contains(renderQuotaBar(1), "▰") {
		t.Error("1% usage rendered as completely empty")
	}
	if strings.Contains(renderQuotaBar(0), "▰") {
		t.Error("0% usage rendered as partially filled")
	}
}

func TestNextAccountAfterWraps(t *testing.T) {
	accts := hdrAccounts("a@x.com", "b@x.com", "c@x.com")

	got, ok := nextAccountAfter(accts, "a@x.com")
	if !ok || got.Email != "b@x.com" {
		t.Errorf("next after a = %q, want b", got.Email)
	}
	got, ok = nextAccountAfter(accts, "c@x.com")
	if !ok || got.Email != "a@x.com" {
		t.Errorf("next after last = %q, want a (wraps)", got.Email)
	}
	// A session on the ambient login, or on an account since removed, moves to
	// the first configured one rather than nowhere.
	got, ok = nextAccountAfter(accts, "")
	if !ok || got.Email != "a@x.com" {
		t.Errorf("next after unknown = %q, want a", got.Email)
	}
	if _, ok := nextAccountAfter(hdrAccounts("only@x.com"), "only@x.com"); ok {
		t.Error("a single account has nowhere to move to")
	}
}
