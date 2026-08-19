package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
	"github.com/charmbracelet/x/ansi"
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
		FiveHourPct: fiveHour,
		// Deliberately not a round 90m: that is exactly %.0f's rounding boundary
		// (1.5h), so advancing the clock one second flips "2h" to "1h" and any
		// stability check fails for a reason it isn't about.
		FiveHourReset: hdrNow.Add(140 * time.Minute),
		SevenDayPct:   sevenDay,
		SevenDayReset: hdrNow.Add(5 * 24 * time.Hour),
		FetchedAt:     hdrNow,
	}
}

// hdrSplit renders the default style at a budget wide enough for everything.
func hdrSplit(accounts []claudeaccount.Account, usage map[string]claudeaccount.Usage, now time.Time) string {
	return renderAccountUsageHeader(accounts, usage, config.AccountUsageSplit, 200, now)
}

// With one subscription there is no choice being made, so the number is trivia
// and the header row is better spent on nothing.
func TestAccountHeaderHiddenForSingleAccount(t *testing.T) {
	got := hdrSplit(
		hdrAccounts("a@x.com"),
		map[string]claudeaccount.Usage{"a@x.com": hdrUsage(10, 20)},
		hdrNow)
	if got != "" {
		t.Fatalf("want empty for a single account, got %q", got)
	}
}

// The readout drops the names before it disappears, so it hides only when even
// the nameless form would overflow — at which point drawing it would push the
// What's New badge out of its corner.
func TestAccountHeaderHiddenWhenEvenTheTightestOverflows(t *testing.T) {
	got := renderAccountUsageHeader(
		hdrAccounts("a@x.com", "b@x.com"),
		map[string]claudeaccount.Usage{"a@x.com": hdrUsage(10, 20), "b@x.com": hdrUsage(30, 40)},
		config.AccountUsageSplit, 8, hdrNow)
	if got != "" {
		t.Fatalf("want empty when nothing fits, got %q", got)
	}
}

// Off is honoured by the renderer itself, not only by View()'s guard.
//
// accountChip branches on Grouped and falls through to Split for everything
// else, so without an explicit check the function renders a full strip for a
// user who asked for none — and the rule lives in one call site, where the next
// caller (an Appearance preview, say) would not find it.
func TestOffRendersNothing(t *testing.T) {
	got := renderAccountUsageHeader(
		hdrAccounts("a@x.com", "b@x.com"),
		map[string]claudeaccount.Usage{"a@x.com": hdrUsage(10, 20), "b@x.com": hdrUsage(30, 40)},
		config.AccountUsageOff, 200, hdrNow)
	if got != "" {
		t.Fatalf("Off still rendered a strip: %q", ansi.Strip(got))
	}
}

// The two-account rule counts what actually renders, not what is configured.
//
// A second account that has never been polled is filtered out (no reading, not
// logged out), so counting the configured accounts left a single chip on screen
// — the exact "no comparison to make" case the doc says returns empty, and an
// extra shape besides, since it would grow to two chips when the poll landed.
func TestSecondAccountWithNoReadingHidesTheStrip(t *testing.T) {
	got := renderAccountUsageHeader(
		hdrAccounts("polled@x.com", "fresh@x.com"),
		map[string]claudeaccount.Usage{
			"polled@x.com": hdrUsage(10, 20),
			// "fresh@x.com" was just added: no FetchedAt, and not logged out.
		},
		config.AccountUsageSplit, 200, hdrNow)
	if got != "" {
		t.Fatalf("rendered a lone chip for a two-account setup: %q", ansi.Strip(got))
	}

	// Once its first poll lands, both chips appear together.
	full := renderAccountUsageHeader(
		hdrAccounts("polled@x.com", "fresh@x.com"),
		map[string]claudeaccount.Usage{
			"polled@x.com": hdrUsage(10, 20), "fresh@x.com": hdrUsage(30, 40),
		},
		config.AccountUsageSplit, 200, hdrNow)
	if !strings.Contains(full, "10%") || !strings.Contains(full, "30%") {
		t.Errorf("both accounts should render once both are polled: %q", ansi.Strip(full))
	}
}

func TestAccountHeaderHiddenBeforeFirstPoll(t *testing.T) {
	// Placeholder percentages would be worse than nothing — they read as real.
	got := hdrSplit(
		hdrAccounts("a@x.com", "b@x.com"),
		map[string]claudeaccount.Usage{},
		hdrNow)
	if got != "" {
		t.Fatalf("want empty with no readings, got %q", got)
	}
}

// The whole point of the change: nothing in the corner may move on its own.
//
// The readout used to swap windows every six seconds off the wall clock, so the
// same inputs rendered differently second to second and a user watching a number
// had to wait for the other one to come back around.
func TestReadoutIsStableOverTime(t *testing.T) {
	accts := hdrAccounts("yuval@x.com", "work@x.com")
	usage := map[string]claudeaccount.Usage{
		"yuval@x.com": hdrUsage(12, 34), "work@x.com": hdrUsage(20, 71),
	}

	for _, style := range []string{config.AccountUsageSplit, config.AccountUsageGrouped} {
		base := renderAccountUsageHeader(accts, usage, style, 200, hdrNow)
		if base == "" {
			t.Fatalf("%s: nothing rendered at a 200-column budget", style)
		}
		// Same second, same minute, and across what used to be several full
		// rotations of the old 6s phase.
		for _, after := range []time.Duration{time.Second, 7 * time.Second, 61 * time.Second} {
			if got := renderAccountUsageHeader(accts, usage, style, 200, hdrNow.Add(after)); got != base {
				t.Errorf("%s: readout changed after %s with identical input:\n was %q\n now %q",
					style, after, ansi.Strip(base), ansi.Strip(got))
			}
		}
		// And every figure is present in the one frame, rather than taking turns.
		for _, want := range []string{"12%", "34%", "20%", "71%"} {
			if !strings.Contains(base, want) {
				t.Errorf("%s: missing %q — both windows must render every frame: %q",
					style, want, ansi.Strip(base))
			}
		}
	}
}

// Order is the only thing naming the windows now that neither is labelled, so
// the 5-hour figure leads in every style. The countdowns cannot stand in for it:
// a weekly window about to roll over reads "3h" exactly like a 5-hour one.
func TestSplitStyleShowsBothWindowsInOrder(t *testing.T) {
	got := ansi.Strip(hdrSplit(
		hdrAccounts("yuval@x.com", "work@x.com"),
		map[string]claudeaccount.Usage{
			"yuval@x.com": hdrUsage(12, 34), "work@x.com": hdrUsage(20, 71),
		},
		hdrNow))

	const want = "yuval 12%(2h) 34%(5d) │ work 20%(2h) 71%(5d)"
	if got != want {
		t.Fatalf("split style:\n got %q\nwant %q", got, want)
	}
	if strings.Index(got, "12%") > strings.Index(got, "34%") {
		t.Errorf("weekly figure preceded the 5-hour one — nothing else names them: %q", got)
	}
}

func TestGroupedStyleFusesThePair(t *testing.T) {
	got := ansi.Strip(renderAccountUsageHeader(
		hdrAccounts("yuval@x.com", "work@x.com"),
		map[string]claudeaccount.Usage{
			"yuval@x.com": hdrUsage(12, 34), "work@x.com": hdrUsage(20, 71),
		},
		config.AccountUsageGrouped, 200, hdrNow))

	const want = "yuval 12%/34% (2h/5d) │ work 20%/71% (2h/5d)"
	if got != want {
		t.Fatalf("grouped style:\n got %q\nwant %q", got, want)
	}
	if strings.Index(got, "2h") > strings.Index(got, "5d") {
		t.Errorf("countdowns are out of order with their figures: %q", got)
	}
}

// In the fused form the two countdowns are pooled, so half a group is unreadable:
// "12%/34% (5d)" gives no way to tell which window the 5d belongs to.
func TestGroupedDropsAHalfKnownCountdownGroup(t *testing.T) {
	half := hdrUsage(12, 34)
	half.FiveHourReset = time.Time{} // never polled a reset for this window

	got := ansi.Strip(renderAccountUsageHeader(
		hdrAccounts("yuval@x.com", "work@x.com"),
		map[string]claudeaccount.Usage{"yuval@x.com": half, "work@x.com": hdrUsage(20, 71)},
		config.AccountUsageGrouped, 200, hdrNow))

	chip, _, ok := strings.Cut(got, "│")
	if !ok {
		t.Fatalf("expected two chips: %q", got)
	}
	if strings.Contains(chip, "(") {
		t.Errorf("half-known chip kept an ambiguous countdown group: %q", chip)
	}
	if !strings.Contains(chip, "12%/34%") {
		t.Errorf("figures dropped along with the countdowns: %q", chip)
	}
	// The other account is unaffected — this is per-chip, not per-strip.
	if !strings.Contains(got, "(2h/5d)") {
		t.Errorf("a fully-known chip lost its countdowns too: %q", got)
	}
}

// The split style attaches each parenthetical to its own figure, so there is no
// ambiguity and a missing reset drops only that one.
func TestSplitStyleDropsOnlyTheUnknownCountdown(t *testing.T) {
	half := hdrUsage(12, 34)
	half.FiveHourReset = time.Time{}

	got := ansi.Strip(hdrSplit(
		hdrAccounts("yuval@x.com", "work@x.com"),
		map[string]claudeaccount.Usage{"yuval@x.com": half, "work@x.com": hdrUsage(20, 71)},
		hdrNow))

	if !strings.Contains(got, "yuval 12% 34%(5d)") {
		t.Errorf("want the weekly countdown kept and only the 5-hour one dropped: %q", got)
	}
}

// The two styles are the same width by construction: four tokens plus five
// characters of glue either way (split spends them on two paren pairs and a
// space, grouped on a slash, a space, a paren pair and a slash).
//
// Pinned because it is the reason neither is called "compact", and because it is
// what lets the fit ladder be style-independent — if one style were narrower,
// the budget would silently decide which grouping you saw, putting the shape
// back under the breadcrumb's control.
func TestBothStylesAreTheSameWidth(t *testing.T) {
	accts := hdrAccounts("yuval@x.com", "work@x.com")
	usage := map[string]claudeaccount.Usage{
		"yuval@x.com": hdrUsage(12, 34), "work@x.com": hdrUsage(20, 71),
	}
	split := renderAccountUsageHeader(accts, usage, config.AccountUsageSplit, 200, hdrNow)
	grouped := renderAccountUsageHeader(accts, usage, config.AccountUsageGrouped, 200, hdrNow)

	if lipgloss.Width(split) != lipgloss.Width(grouped) {
		t.Errorf("split is %d columns, grouped is %d — they are meant to be interchangeable",
			lipgloss.Width(split), lipgloss.Width(grouped))
	}
	// Same width, but the setting must actually do something.
	if ansi.Strip(split) == ansi.Strip(grouped) {
		t.Errorf("both styles rendered identically: %q", ansi.Strip(split))
	}
}

// Exactly one thing is given up before the strip disappears. The old six-step
// density ladder is what made the corner reshape whenever the breadcrumb changed
// length, so a wider ladder is a regression, not an improvement.
func TestLadderHasExactlyTwoSteps(t *testing.T) {
	accounts := hdrAccounts("yuval@x.com", "work@x.com")
	usage := map[string]claudeaccount.Usage{
		"yuval@x.com": hdrUsage(12, 34), "work@x.com": hdrUsage(20, 71),
	}

	for _, style := range []string{config.AccountUsageSplit, config.AccountUsageGrouped} {
		widths := map[int]string{}
		for budget := 200; budget >= 0; budget-- {
			got := renderAccountUsageHeader(accounts, usage, style, budget, hdrNow)
			if got == "" {
				continue
			}
			w := lipgloss.Width(got)
			if w > budget {
				t.Fatalf("%s: budget %d produced %d columns: %q", style, budget, w, ansi.Strip(got))
			}
			widths[w] = got
			// Whatever it gives up, the numbers are the point and must survive.
			for _, want := range []string{"12%", "34%", "20%", "71%"} {
				if !strings.Contains(got, want) {
					t.Fatalf("%s: budget %d lost %q: %q", style, budget, want, ansi.Strip(got))
				}
			}
		}
		if len(widths) != 2 {
			t.Errorf("%s: %d distinct widths, want exactly 2 (with names, without)", style, len(widths))
		}
		for w, out := range widths {
			named := strings.Contains(out, "yuval")
			if w == maxKey(widths) && !named {
				t.Errorf("%s: the widest form dropped the account names: %q", style, ansi.Strip(out))
			}
			if w != maxKey(widths) && named {
				t.Errorf("%s: the narrow form kept the account names: %q", style, ansi.Strip(out))
			}
		}
	}
}

func maxKey(m map[int]string) int {
	max := 0
	for k := range m {
		if k > max {
			max = k
		}
	}
	return max
}

// The horizon is the whole reason the reset was added: "54% of a week" means
// nothing until you know whether it refills tomorrow or in six days. It must
// survive further down the ladder than the account names do.
func TestCountdownOutlivesTheAccountNames(t *testing.T) {
	accounts := hdrAccounts("yuval@x.com", "work@x.com")
	usage := map[string]claudeaccount.Usage{
		"yuval@x.com": hdrUsage(10, 34), "work@x.com": hdrUsage(20, 71),
	}
	tight := renderAccountUsageHeader(accounts, usage, config.AccountUsageGrouped, 34, hdrNow)
	if tight == "" {
		t.Fatal("nothing rendered at 34 columns")
	}
	if strings.Contains(tight, "yuval") {
		t.Errorf("names survived at 34 columns: %q", ansi.Strip(tight))
	}
	if !strings.Contains(tight, "5d") {
		t.Errorf("countdown dropped before the names did: %q", ansi.Strip(tight))
	}
}

// A spent window no longer collapses the chip to "spent 2h". quotaStyle already
// paints it red at the threshold Select stops handing the account out at, and
// the countdown sits beside it — so the state is legible without the shape
// changing, and the *other* window's figure survives instead of being hidden.
func TestSpentWindowStaysInPlace(t *testing.T) {
	got := ansi.Strip(hdrSplit(
		hdrAccounts("a@x.com", "b@x.com"),
		map[string]claudeaccount.Usage{
			"a@x.com": {
				FiveHourPct: 4, FiveHourReset: hdrNow.Add(20 * time.Minute),
				SevenDayPct: 99, SevenDayReset: hdrNow.Add(5 * 24 * time.Hour),
				FetchedAt: hdrNow,
			},
			"b@x.com": hdrUsage(5, 12),
		},
		hdrNow))

	if !strings.Contains(got, "a 4%(21m) 99%(5d)") {
		t.Fatalf("spent weekly chip lost its shape: %q", got)
	}
	// The trap the old code fell into: timing a spent *weekly* window by the
	// 5-hour clock offered a twenty-minute wait for an account blocked five days.
	// Each figure carrying its own countdown makes that structurally impossible,
	// so this pins it.
	if strings.Contains(got, "99%(21m)") {
		t.Errorf("weekly figure timed by the 5-hour clock: %q", got)
	}
}

// A spent window renders red, because red is what "you cannot use this now"
// means everywhere else in fleet.
func TestSpentWindowRendersRed(t *testing.T) {
	got := hdrSplit(
		hdrAccounts("a@x.com", "b@x.com"),
		map[string]claudeaccount.Usage{
			"a@x.com": hdrUsage(4, 99),
			"b@x.com": hdrUsage(5, 12),
		},
		hdrNow)
	red := lipgloss.NewStyle().Foreground(ColorRed).Render("99%")
	if !strings.Contains(got, red) {
		t.Errorf("a 99%% window did not render red: %q", got)
	}
}

// A missing login is the one state here with no numbers to show, so it takes the
// whole chip — and it shows even with no reading at all, since an account that
// was never pollable is exactly the one you need to be told about.
func TestLoggedOutStillShows(t *testing.T) {
	for _, style := range []string{config.AccountUsageSplit, config.AccountUsageGrouped} {
		got := ansi.Strip(renderAccountUsageHeader(
			hdrAccounts("dead@x.com", "b@x.com"),
			map[string]claudeaccount.Usage{
				"dead@x.com": {LoggedOut: true}, // never polled: no FetchedAt
				"b@x.com":    hdrUsage(5, 12),
			},
			style, 200, hdrNow))
		if !strings.Contains(got, "✕ logged out") {
			t.Errorf("%s: logged-out account not surfaced: %q", style, got)
		}
		if !strings.Contains(got, "5%") {
			t.Errorf("%s: healthy account lost its numbers: %q", style, got)
		}
	}
}

// The readout shares the header row with the What's New badge, so it must stay
// inside the width it is handed.
func TestAccountHeaderFitsItsWidth(t *testing.T) {
	for _, style := range []string{config.AccountUsageSplit, config.AccountUsageGrouped} {
		for _, w := range []int{72, 90, 100, 140, 200} {
			got := renderAccountUsageHeader(
				hdrAccounts("averylongaccountname@example.com", "another-long-one@example.com"),
				map[string]claudeaccount.Usage{
					"averylongaccountname@example.com": hdrUsage(10, 34),
					"another-long-one@example.com":     hdrUsage(20, 71),
				},
				style, w, hdrNow)
			if lipgloss.Width(got) > w {
				t.Errorf("%s width %d: readout is %d cells wide: %q",
					style, w, lipgloss.Width(got), ansi.Strip(got))
			}
		}
	}
}

// The readout is fitted against the space left of the badge, not against the
// whole screen.
//
// View() passed rightEdge — an x-coordinate — straight in as the width budget, so
// on a 100-column terminal the strip was allowed ~99 columns, picked its widest
// form, and was then overlaid at x=25 straight over the breadcrumb, cutting it
// mid-word. The What's New badge shares the pattern and gets away with it only
// because it is ~14 columns wide.
func TestReadoutBudgetLeavesRoomForTheHeader(t *testing.T) {
	const width = 100
	accounts := hdrAccounts("yuval@x.com", "work@x.com")
	usage := map[string]claudeaccount.Usage{
		"yuval@x.com": hdrUsage(10, 34), "work@x.com": hdrUsage(20, 71),
	}
	// The widest thing the strip could render, and the one that used to fit.
	widest := renderReadout(accounts, usage, config.AccountUsageSplit, true, hdrNow)

	// A breadcrumb of realistic length: origin, checkout and a session title.
	headerW := lipgloss.Width("❯_ fleet  ›  brizzai/fleet  ›  feat-multi-account  ›  close the gaps review found")
	budget := width - 1 - headerW - accountReadoutGap

	if budget >= lipgloss.Width(widest) {
		t.Fatalf("test is not exercising the squeeze: budget %d already fits the widest form (%d)",
			budget, lipgloss.Width(widest))
	}

	got := renderAccountUsageHeader(accounts, usage, config.AccountUsageSplit, budget, hdrNow)
	if lipgloss.Width(got) > budget {
		t.Errorf("readout is %d cells for a %d-cell budget — it would overprint the breadcrumb: %q",
			lipgloss.Width(got), budget, ansi.Strip(got))
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
		config.AccountUsageSplit, -6, hdrNow)
	if got != "" {
		t.Errorf("rendered %q with no room to render into", got)
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
