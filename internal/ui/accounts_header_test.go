package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/charmbracelet/x/ansi"
)

var hdrNow = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

// The readout alternates windows on a wall-clock phase, so tests pick the
// phase they mean rather than depending on where hdrNow happens to land.
func atWindow(w claudeaccount.Window) time.Time {
	t := hdrNow
	for i := 0; i < 2*accountWindowRotateSecs; i++ {
		if windowAt(t) == w {
			return t
		}
		t = t.Add(time.Second)
	}
	panic("no time found in the rotation for the requested window")
}

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
		SevenDayReset: hdrNow.Add(5 * 24 * time.Hour),
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

// The readout adapts rather than disappearing, so it hides only when even the
// tightest form would overflow — at which point drawing it would push the
// What's New badge out of its corner.
func TestAccountHeaderHiddenWhenEvenTheTightestOverflows(t *testing.T) {
	got := renderAccountUsageHeader(
		hdrAccounts("a@x.com", "b@x.com"),
		map[string]claudeaccount.Usage{"a@x.com": hdrUsage(10, 20), "b@x.com": hdrUsage(30, 40)},
		12, hdrNow)
	if got != "" {
		t.Fatalf("want empty when nothing fits, got %q", got)
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

// The readout alternates between the two windows, and each turn must show one
// window's numbers only — mixing them would be worse than showing either.
func TestAccountHeaderRotatesBetweenWindows(t *testing.T) {
	usage := map[string]claudeaccount.Usage{
		"yuval@x.com": hdrUsage(10, 34),
		"work@x.com":  hdrUsage(20, 71),
	}
	accts := hdrAccounts("yuval@x.com", "work@x.com")

	weekly := renderAccountUsageHeader(accts, usage, 140, atWindow(windowSevenDay))
	for _, want := range []string{"34%", "71%", "weekly", "yuval", "work"} {
		if !strings.Contains(weekly, want) {
			t.Errorf("weekly turn missing %q: %q", want, weekly)
		}
	}
	if strings.Contains(weekly, "10%") || strings.Contains(weekly, "20%") {
		t.Errorf("weekly turn leaked the 5-hour figures: %q", weekly)
	}

	fiveHour := renderAccountUsageHeader(accts, usage, 140, atWindow(windowFiveHour))
	for _, want := range []string{"10%", "20%", "5-hour"} {
		if !strings.Contains(fiveHour, want) {
			t.Errorf("5-hour turn missing %q: %q", want, fiveHour)
		}
	}
	if strings.Contains(fiveHour, "34%") || strings.Contains(fiveHour, "71%") {
		t.Errorf("5-hour turn leaked the weekly figures: %q", fiveHour)
	}
}

// The phase has to actually change, or the rotation is a no-op that only looks
// implemented.
func TestWindowRotationAdvances(t *testing.T) {
	base := atWindow(windowSevenDay)
	if windowAt(base) == windowAt(base.Add(accountWindowRotateSecs*time.Second)) {
		t.Fatal("window did not change after a full rotation period")
	}
	// And it must be stable *within* a period, or the header flickers.
	if windowAt(base) != windowAt(base.Add(time.Second)) {
		t.Error("window changed within a single rotation period")
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
	if !strings.Contains(got, "spent ") {
		t.Errorf("want a relative countdown after \"spent\": %q", got)
	}
	if strings.Contains(got, "44%") {
		t.Errorf("spent account still showed its weekly percentage: %q", got)
	}
}

// A spent *weekly* bucket must be timed by the weekly clock.
//
// Exhausted covers both windows but this branch always printed FiveHourReset, so
// an account at 99% weekly whose 5-hour bucket had just reset rendered a
// twenty-minute countdown for an account blocked for five days. The same string
// drives the account picker, where acting on it costs a real relaunch.
func TestSpentWeeklyWindowIsTimedByTheWeeklyClock(t *testing.T) {
	got := renderAccountUsageHeader(
		hdrAccounts("a@x.com", "b@x.com"),
		map[string]claudeaccount.Usage{
			"a@x.com": {
				FiveHourPct: 4, FiveHourReset: hdrNow.Add(20 * time.Minute),
				SevenDayPct: 99, SevenDayReset: hdrNow.Add(5 * 24 * time.Hour),
				FetchedAt: hdrNow,
			},
			"b@x.com": hdrUsage(5, 12),
		},
		140, hdrNow)

	// Stripped before matching: the countdown is styled, and an SGR escape is a
	// run of digits ending in "m" — so a bare "20m" needle matches ";120m" inside
	// a colour code, and whether it does depends on the palette another test
	// happened to leave installed.
	plain := ansi.Strip(got)
	if !strings.Contains(plain, "spent") {
		t.Fatalf("a 99%% weekly bucket did not read as spent: %q", plain)
	}
	if !strings.Contains(plain, "5d") {
		t.Errorf("want the weekly horizon (5d), got: %q", plain)
	}
	if strings.Contains(plain, "20m") {
		t.Errorf("timed the spent weekly window by the 5-hour clock: %q", plain)
	}
}

// The strip gives things up in order as the budget shrinks, rather than
// switching between two fixed layouts. Each step must actually be narrower than
// the one before, or a density is dead weight.
func TestReadoutGetsTighterAsTheBudgetShrinks(t *testing.T) {
	accounts := hdrAccounts("yuval@x.com", "work@x.com")
	usage := map[string]claudeaccount.Usage{
		"yuval@x.com": hdrUsage(10, 34), "work@x.com": hdrUsage(20, 71),
	}

	seen := map[int]bool{}
	prev := 1 << 30
	for budget := 200; budget >= 20; budget-- {
		got := renderAccountUsageHeader(accounts, usage, budget, hdrNow)
		if got == "" {
			continue
		}
		w := lipgloss.Width(got)
		if w > budget {
			t.Fatalf("budget %d produced %d columns: %q", budget, w, got)
		}
		if w > prev {
			t.Fatalf("budget %d widened to %d columns from %d — the ladder must only tighten", budget, w, prev)
		}
		prev = w
		seen[w] = true
		// Whatever it gives up, the numbers are the point and must survive.
		if !strings.Contains(got, "34%") || !strings.Contains(got, "71%") {
			t.Errorf("budget %d lost a percentage: %q", budget, got)
		}
	}
	if len(seen) < 4 {
		t.Errorf("only %d distinct widths across the sweep; the density ladder is barely being used", len(seen))
	}
}

// The horizon is the whole reason the reset was added: "54% of a week" means
// nothing until you know whether it refills tomorrow or in six days. It must
// survive further down the ladder than the account names do.
func TestCountdownOutlivesTheAccountNames(t *testing.T) {
	accounts := hdrAccounts("yuval@x.com", "work@x.com")
	usage := map[string]claudeaccount.Usage{
		"yuval@x.com": hdrUsage(10, 34), "work@x.com": hdrUsage(20, 71),
	}
	tight := renderAccountUsageHeader(accounts, usage, 30, hdrNow)
	if strings.Contains(tight, "yuval") {
		t.Errorf("names survived at 30 columns: %q", tight)
	}
	if !strings.Contains(tight, "5d") {
		t.Errorf("countdown dropped before the names did: %q", tight)
	}
}

// The window is named as a word so it can never be read as one of the
// countdowns beside it — the two are the same shape and opposite meanings.
func TestWindowIsNamedNotDurationShaped(t *testing.T) {
	got := renderAccountUsageHeader(
		hdrAccounts("a@x.com", "b@x.com"),
		map[string]claudeaccount.Usage{"a@x.com": hdrUsage(10, 34), "b@x.com": hdrUsage(20, 71)},
		200, atWindow(windowSevenDay))
	if strings.Contains(got, "7d ") || strings.HasSuffix(got, "7d") {
		t.Errorf("window rendered as a bare duration, indistinguishable from a countdown: %q", got)
	}
	if !strings.Contains(got, "weekly") {
		t.Errorf("window not named: %q", got)
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

// The readout is fitted against the space left of the badge, not against the
// whole screen.
//
// View() passed rightEdge — an x-coordinate — straight in as the width budget, so
// on a 100-column terminal the strip was allowed ~99 columns, picked its widest
// density (~74 for two labelled chips with gauges and countdowns), and was then
// overlaid at x=25 straight over the breadcrumb, cutting it mid-word. The What's
// New badge shares the pattern and gets away with it only because it is ~14
// columns wide.
func TestReadoutBudgetLeavesRoomForTheHeader(t *testing.T) {
	const width = 100
	// The widest thing the strip could render, and the one that used to fit.
	widest := renderReadout(
		hdrAccounts("yuval@x.com", "work@x.com"),
		map[string]claudeaccount.Usage{
			"yuval@x.com": hdrUsage(10, 34), "work@x.com": hdrUsage(20, 71),
		},
		windowFiveHour, densities[0], hdrNow)

	// A breadcrumb of realistic length: origin, checkout and a session title.
	headerW := lipgloss.Width("❯_ fleet  ›  brizzai/fleet  ›  feat-multi-account  ›  close the gaps review found")
	budget := width - 1 - headerW - accountReadoutGap

	if budget >= lipgloss.Width(widest) {
		t.Fatalf("test is not exercising the squeeze: budget %d already fits the widest form (%d)",
			budget, lipgloss.Width(widest))
	}

	got := renderAccountUsageHeader(
		hdrAccounts("yuval@x.com", "work@x.com"),
		map[string]claudeaccount.Usage{
			"yuval@x.com": hdrUsage(10, 34), "work@x.com": hdrUsage(20, 71),
		},
		budget, hdrNow)

	if lipgloss.Width(got) > budget {
		t.Errorf("readout is %d cells for a %d-cell budget — it would overprint the breadcrumb: %q",
			lipgloss.Width(got), budget, got)
	}
}

// A budget that has gone negative — a breadcrumb wide enough to leave nothing —
// must yield no strip rather than a clipped one.
func TestReadoutYieldsNothingWhenThereIsNoRoom(t *testing.T) {
	got := renderAccountUsageHeader(
		hdrAccounts("yuval@x.com", "work@x.com"),
		map[string]claudeaccount.Usage{
			"yuval@x.com": hdrUsage(10, 34), "work@x.com": hdrUsage(20, 71),
		},
		-6, hdrNow)
	if got != "" {
		t.Errorf("rendered %q with no room to render into", got)
	}
}

func TestRenderQuotaBarEdges(t *testing.T) {
	if got := lipgloss.Width(renderQuotaBar(0, accountBarCells)); got != accountBarCells {
		t.Errorf("bar width at 0%% = %d, want %d", got, accountBarCells)
	}
	if got := lipgloss.Width(renderQuotaBar(100, accountBarCells)); got != accountBarCells {
		t.Errorf("bar width at 100%% = %d, want %d", got, accountBarCells)
	}
	// Any nonzero usage must show a cell, or a busy account reads as untouched.
	if !strings.Contains(renderQuotaBar(1, accountBarCells), "▰") {
		t.Error("1% usage rendered as completely empty")
	}
	if strings.Contains(renderQuotaBar(0, accountBarCells), "▰") {
		t.Error("0% usage rendered as partially filled")
	}
}

// truncateLabel measured display width but sliced by rune index, so a label of
// double-width characters panicked and took the render loop down with it. The
// account rename box accepts anything the user types.
func TestTruncateLabelSurvivesWideCharacters(t *testing.T) {
	for _, s := range []string{
		"日本語のアカウント", // every rune double-width
		"🎉🎉🎉🎉🎉🎉🎉🎉",
		"work",
		"a-very-long-ascii-label",
		"日本語work混在",
	} {
		got := truncateLabel(s) // must not panic
		if lipgloss.Width(got) > accountLabelMax {
			t.Errorf("truncateLabel(%q) = %q, %d columns — over the %d budget",
				s, got, lipgloss.Width(got), accountLabelMax)
		}
	}
}

// Red means "you cannot use this now", not "this number is high" — the same
// thing it means for an error dot, a spent window and a logged-out account.
// It used to start at 85%, which cried wolf on an account with a fifth of its
// window still to go and left nothing louder for when it actually ran out.
func TestRedIsReservedForActuallySpent(t *testing.T) {
	// Compared through the rendered output: lipgloss styles hold a color slice
	// and are not comparable.
	isRed := func(pct int) bool {
		return quotaStyle(pct).Render("x") == lipgloss.NewStyle().Foreground(ColorRed).Render("x")
	}
	for _, pct := range []int{0, 42, 60, 84, 85, 90, 97} {
		if isRed(pct) {
			t.Errorf("%d%% renders red, but the account is still usable", pct)
		}
	}
	for _, pct := range []int{claudeaccount.ExhaustedPct, 99, 100} {
		if !isRed(pct) {
			t.Errorf("%d%% does not render red, but Select will not hand this account out", pct)
		}
	}
}
