package claudeaccount

import (
	"testing"
	"time"
)

var testNow = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

// accts builds an ordered account set: a=0, b=1, c=2.
func accts(emails ...string) []Account {
	out := make([]Account, len(emails))
	for i, e := range emails {
		out[i] = Account{Email: e, ConfigDir: "/d/" + e, Order: i}
	}
	return out
}

// used builds a known usage reading at the given utilization in *both* windows,
// so a test about ranking says the same thing whichever window the strategy
// ranks on. Tests that care about the two diverging build the Usage themselves
// (see TestLeastUsedRanksOnTheStrategysWindow).
//
// SevenDayReset is deliberately left zero, as it always was: every value here is
// below ExhaustedPct, so no window reads as spent and the reset logic is
// untouched by the second field.
func used(pct int) Usage {
	return Usage{FiveHourPct: pct, SevenDayPct: pct, FetchedAt: testNow, FiveHourReset: testNow.Add(time.Hour)}
}

// loggedOut builds the reading for an account with no live login. Note it
// carries no successful fetch, which is exactly the shape that scores as
// "unknown" unless the flag is respected.
func loggedOut() Usage {
	return Usage{AttemptedAt: testNow, Err: ErrNotLoggedIn, LoggedOut: true}
}

// The bug this closes: an account with no login never gets a reading, so
// pctOf scored it at unknownPct (50) — which beats a healthy account at 59% and
// handed every new session the one credential that cannot run.
func TestSelectSkipsLoggedOutEvenWhenTheAliveAccountIsBusier(t *testing.T) {
	got, ok := Select(SelectOpts{
		Accounts: accts("dead", "alive"),
		Usage:    map[string]Usage{"dead": loggedOut(), "alive": used(59)},
		Strategy: StrategyLeastUsed,
		Now:      testNow,
	})
	if !ok || got.Email != "alive" {
		t.Fatalf("least_used = %q (ok=%v), want alive — a logged-out account outranked a working one", got.Email, ok)
	}
}

// Manual is strict about *spent* because a wait ends. A rejection never does, so
// it is dropped ahead of every strategy rather than pinned to.
func TestSelectManualDoesNotPinToALoggedOutAccount(t *testing.T) {
	got, ok := Select(SelectOpts{
		Accounts: accts("dead", "alive"),
		Usage:    map[string]Usage{"dead": loggedOut(), "alive": used(90)},
		Strategy: StrategyManual,
		Manual:   "dead",
		Now:      testNow,
	})
	if !ok || got.Email != "alive" {
		t.Fatalf("manual = %q (ok=%v), want alive", got.Email, ok)
	}
}

func TestSelectWaterfallSkipsLoggedOut(t *testing.T) {
	got, _ := Select(SelectOpts{
		Accounts: accts("dead", "alive"),
		Usage:    map[string]Usage{"dead": loggedOut()},
		Strategy: StrategyWaterfall,
		Now:      testNow,
	})
	if got.Email != "alive" {
		t.Fatalf("waterfall = %q, want alive", got.Email)
	}
}

// With every credential refused, the ambient login is the better answer: it may
// well work, and a token the API rejects certainly won't. Unlike all-spent,
// which still returns the soonest to reset because that account recovers.
func TestSelectAllLoggedOutFallsBackToAmbient(t *testing.T) {
	_, ok := Select(SelectOpts{
		Accounts: accts("a", "b"),
		Usage:    map[string]Usage{"a": loggedOut(), "b": loggedOut()},
		Strategy: StrategyLeastUsed,
		Now:      testNow,
	})
	if ok {
		t.Fatal("Select handed out a logged-out account instead of falling back to the ambient login")
	}
}

// A rejection must not be inferred from an unreadable endpoint: fleet failing to
// reach Anthropic says nothing about the credential, and excluding on that would
// empty the candidate list during any outage.
func TestSelectKeepsAnAccountWhoseProbeMerelyFailed(t *testing.T) {
	got, ok := Select(SelectOpts{
		Accounts: accts("blip"),
		Usage:    map[string]Usage{"blip": {AttemptedAt: testNow, Err: ErrNoCredential}},
		Strategy: StrategyLeastUsed,
		Now:      testNow,
	})
	if !ok || got.Email != "blip" {
		t.Fatalf("Select = %q (ok=%v), want blip — a failed poll is not a logged-out verdict", got.Email, ok)
	}
}

// The verdict rides on the live reading, so a later successful poll (or a fresh
// login, which replaces the entry outright) brings the account straight back.
func TestHealedAccountBecomesSelectableAgain(t *testing.T) {
	usage := map[string]Usage{"a": loggedOut(), "b": used(70)}
	if got, _ := Select(SelectOpts{Accounts: accts("a", "b"), Usage: usage, Now: testNow}); got.Email != "b" {
		t.Fatalf("while logged out: %q, want b", got.Email)
	}
	usage["a"] = used(5) // a good poll overwrites the entry, Rejected included
	if got, _ := Select(SelectOpts{Accounts: accts("a", "b"), Usage: usage, Now: testNow}); got.Email != "a" {
		t.Fatalf("after healing: %q, want a — a recovered account never became a candidate again", got.Email)
	}
}

func TestSelectLeastUsedPicksMostHeadroom(t *testing.T) {
	got, ok := Select(SelectOpts{
		Accounts: accts("a", "b", "c"),
		Usage:    map[string]Usage{"a": used(80), "b": used(10), "c": used(45)},
		Strategy: StrategyLeastUsed,
		Now:      testNow,
	})
	if !ok || got.Email != "b" {
		t.Fatalf("least_used = %q (ok=%v), want b", got.Email, ok)
	}
}

func TestSelectLeastUsedBreaksTiesByOrder(t *testing.T) {
	// Equal utilization must resolve by configured order, not arbitrarily —
	// otherwise assignment is unstable between identical calls.
	got, _ := Select(SelectOpts{
		Accounts: accts("a", "b"),
		Usage:    map[string]Usage{"a": used(20), "b": used(20)},
		Strategy: StrategyLeastUsed,
		Now:      testNow,
	})
	if got.Email != "a" {
		t.Fatalf("tie = %q, want a (lowest Order)", got.Email)
	}
}

func TestSelectLeastUsedDegradesToWaterfallWhenNothingPolled(t *testing.T) {
	// The documented degradation: with the usage endpoint unavailable every
	// account is unknown, ties, and order decides.
	got, ok := Select(SelectOpts{
		Accounts: accts("a", "b", "c"),
		Usage:    map[string]Usage{},
		Strategy: StrategyLeastUsed,
		Now:      testNow,
	})
	if !ok || got.Email != "a" {
		t.Fatalf("all-unknown = %q, want a (waterfall order)", got.Email)
	}
}

func TestSelectLeastUsedPrefersKnownIdleOverUnknown(t *testing.T) {
	got, _ := Select(SelectOpts{
		Accounts: accts("a", "b"),
		Usage:    map[string]Usage{"b": used(5)},
		Strategy: StrategyLeastUsed,
		Now:      testNow,
	})
	if got.Email != "b" {
		t.Fatalf("known-idle vs unknown = %q, want b", got.Email)
	}
}

func TestSelectWaterfallSkipsExhausted(t *testing.T) {
	got, ok := Select(SelectOpts{
		Accounts: accts("a", "b", "c"),
		Usage:    map[string]Usage{"a": used(99), "b": used(70)},
		Strategy: StrategyWaterfall,
		Now:      testNow,
	})
	if !ok || got.Email != "b" {
		t.Fatalf("waterfall = %q, want b (a is spent)", got.Email)
	}
}

func TestSelectWaterfallIgnoresUtilizationOrder(t *testing.T) {
	// Waterfall must drain in configured order even when a later account is
	// emptier — that is the whole difference from least_used.
	got, _ := Select(SelectOpts{
		Accounts: accts("a", "b"),
		Usage:    map[string]Usage{"a": used(90), "b": used(1)},
		Strategy: StrategyWaterfall,
		Now:      testNow,
	})
	if got.Email != "a" {
		t.Fatalf("waterfall = %q, want a", got.Email)
	}
}

func TestSelectManualIsStrictEvenWhenSpent(t *testing.T) {
	// Manual is the opt-out. Quietly rotating away from the chosen account
	// would defeat the only reason to select this mode.
	got, ok := Select(SelectOpts{
		Accounts: accts("a", "b"),
		Usage:    map[string]Usage{"a": used(100), "b": used(0)},
		Strategy: StrategyManual,
		Manual:   "a",
		Now:      testNow,
	})
	if !ok || got.Email != "a" {
		t.Fatalf("manual = %q, want a even though spent", got.Email)
	}
}

func TestSelectManualFallsThroughWhenDefaultGone(t *testing.T) {
	// A default that was removed must not strand the session.
	got, ok := Select(SelectOpts{
		Accounts: accts("a", "b"),
		Usage:    map[string]Usage{"a": used(80), "b": used(10)},
		Strategy: StrategyManual,
		Manual:   "deleted@example.com",
		Now:      testNow,
	})
	if !ok || got.Email != "b" {
		t.Fatalf("manual with missing default = %q, want b", got.Email)
	}
}

func TestSelectAllowlistRestrictsCandidates(t *testing.T) {
	got, ok := Select(SelectOpts{
		Accounts: accts("a", "b", "c"),
		Usage:    map[string]Usage{"a": used(90), "b": used(1), "c": used(50)},
		Strategy: StrategyLeastUsed,
		Allowed:  []string{"a", "c"},
		Now:      testNow,
	})
	if !ok || got.Email != "c" {
		t.Fatalf("allowlisted least_used = %q, want c (b is disallowed)", got.Email)
	}
}

func TestSelectAllowlistNeverEscapesToDisallowedAccount(t *testing.T) {
	// The point of restricting an origin is that waiting beats billing the
	// wrong subscription. Every allowed account being spent must still return
	// an allowed one, never fall back to a disallowed or ambient account.
	got, ok := Select(SelectOpts{
		Accounts: accts("work", "personal"),
		Usage: map[string]Usage{
			"work":     {FiveHourPct: 100, FetchedAt: testNow, FiveHourReset: testNow.Add(2 * time.Hour)},
			"personal": used(0),
		},
		Strategy: StrategyLeastUsed,
		Allowed:  []string{"work"},
		Now:      testNow,
	})
	if !ok {
		t.Fatal("want an account, got none — session would fall back to ambient login")
	}
	if got.Email != "work" {
		t.Fatalf("allowlisted+spent = %q, want work", got.Email)
	}
}

func TestSelectAllSpentPicksSoonestReset(t *testing.T) {
	got, _ := Select(SelectOpts{
		Accounts: accts("a", "b"),
		Usage: map[string]Usage{
			"a": {FiveHourPct: 100, FetchedAt: testNow, FiveHourReset: testNow.Add(3 * time.Hour)},
			"b": {FiveHourPct: 100, FetchedAt: testNow, FiveHourReset: testNow.Add(30 * time.Minute)},
		},
		Strategy: StrategyLeastUsed,
		Now:      testNow,
	})
	if got.Email != "b" {
		t.Fatalf("all-spent = %q, want b (resets soonest)", got.Email)
	}
}

func TestSelectNoAccountsReturnsFalse(t *testing.T) {
	if _, ok := Select(SelectOpts{Strategy: StrategyLeastUsed, Now: testNow}); ok {
		t.Fatal("want ok=false with no accounts so the caller uses the ambient login")
	}
}

func TestSelectAllowlistExcludingEverythingReturnsFalse(t *testing.T) {
	if _, ok := Select(SelectOpts{
		Accounts: accts("a"),
		Allowed:  []string{"nobody@example.com"},
		Strategy: StrategyLeastUsed,
		Now:      testNow,
	}); ok {
		t.Fatal("want ok=false when the allowlist excludes every account")
	}
}

// Select answers false for three different situations, and the caller has to be
// able to tell them apart because two of them want opposite responses.
//
// AllowedConfigured is the discriminator: it asks whether the allowlist names
// anything that exists, which is a question about configuration, not state.
func TestAllowedConfiguredSeparatesPolicyFromState(t *testing.T) {
	all := accts("a", "b")

	// No allowlist means unrestricted, matching Select.
	if !AllowedConfigured(all, nil) {
		t.Error("an empty allowlist must read as unrestricted")
	}
	// Names something real: the origin is satisfiable, whatever the accounts'
	// current state happens to be.
	if !AllowedConfigured(all, []string{"b"}) {
		t.Error("an allowlist naming a configured account must read as satisfiable")
	}
	// Names nothing real — a typo, or the account was removed. This is the case
	// callers must refuse on rather than fall back to the ambient login.
	if AllowedConfigured(all, []string{"gone@example.com"}) {
		t.Error("an allowlist naming no configured account must not read as satisfiable")
	}
	// No accounts at all is not a policy failure: it is the ordinary
	// single-account setup, where the ambient login is the right answer.
	if AllowedConfigured(nil, nil) {
		t.Error("no configured accounts must not read as satisfiable")
	}
}

// The distinction above is load-bearing, so this pins the half that is easiest
// to lose.
//
// A reviewer's suggested patch for the ambient-fallback hole collapsed "the
// allowlist excludes everything" and "everything is logged out" into one exit.
// That would mean a fleet whose logins have all expired can no longer create
// sessions at all — where dropLoggedOut deliberately falls back, because the
// login the user is already sitting in probably works and one nobody is logged
// into certainly does not.
func TestAllLoggedOutIsSatisfiablePolicyNotAPolicyFailure(t *testing.T) {
	all := accts("a", "b")
	usage := map[string]Usage{"a": loggedOut(), "b": loggedOut()}

	if _, ok := Select(SelectOpts{Accounts: all, Usage: usage, Strategy: StrategyLeastUsed, Now: testNow}); ok {
		t.Fatal("want ok=false so the caller falls back to the ambient login")
	}
	// ...but the policy is satisfied, so the caller must warn and continue
	// rather than refuse.
	if !AllowedConfigured(all, []string{"a", "b"}) {
		t.Error("logged-out accounts must still count as configured — state is not policy")
	}
}

func TestExhaustedRespectsPassedReset(t *testing.T) {
	// A stale high reading whose window has already rolled over is not
	// evidence of exhaustion — otherwise a failed re-poll would strand an
	// account that recovered hours ago.
	u := Usage{FiveHourPct: 100, FetchedAt: testNow.Add(-6 * time.Hour), FiveHourReset: testNow.Add(-time.Hour)}
	if u.Exhausted(testNow) {
		t.Fatal("account whose reset has passed must not read as exhausted")
	}
}

func TestUnpolledAccountIsNotExhausted(t *testing.T) {
	// An account fleet has never managed to poll must read as available, not
	// spent — excluding a healthy subscription because a network call failed is
	// the worse of the two errors.
	if (Usage{}).Exhausted(testNow) {
		t.Fatal("a never-polled account must not read as exhausted")
	}
}

func TestParseStrategyNormalizes(t *testing.T) {
	for in, want := range map[string]string{
		" Waterfall ":       StrategyWaterfall,
		"MANUAL":            StrategyManual,
		"least_used_5h":     StrategyLeastUsed5H,
		" Least_Used_5H ":   StrategyLeastUsed5H,
		"least_used_weekly": StrategyLeastUsedWeekly,
		"":                  StrategyLeastUsedWeekly,
		"nonsense":          StrategyLeastUsedWeekly,
		// The pre-split name. It resolves to the same thing an unset config
		// does, so a file carrying it and a file missing it behave alike —
		// mapping it to the 5-hour variant instead would make presence of the
		// key change behaviour, which is miserable to debug from a bug report.
		StrategyLeastUsed: StrategyLeastUsedWeekly,
	} {
		if got := ParseStrategy(in); got != want {
			t.Errorf("ParseStrategy(%q) = %q, want %q", in, got, want)
		}
	}
}

// The split's whole content: the two least-used modes must order the same
// accounts differently when the windows disagree. A fixture where they agree
// would pass with the window plumbing removed entirely.
func TestLeastUsedRanksOnTheStrategysWindow(t *testing.T) {
	// a has burned its week but its 5-hour bucket just reset; b is the mirror.
	// Neither is spent, so both stay candidates and only the ranking decides.
	usage := map[string]Usage{
		"a": {FiveHourPct: 10, SevenDayPct: 80, FetchedAt: testNow, FiveHourReset: testNow.Add(time.Hour)},
		"b": {FiveHourPct: 80, SevenDayPct: 10, FetchedAt: testNow, FiveHourReset: testNow.Add(time.Hour)},
	}
	for _, tc := range []struct {
		strategy string
		want     string
	}{
		{StrategyLeastUsed5H, "a"},
		{StrategyLeastUsedWeekly, "b"},
		{StrategyLeastUsed, "b"}, // legacy alias follows the new default
		{"", "b"},                // unset likewise
	} {
		got, ok := Select(SelectOpts{Accounts: accts("a", "b"), Usage: usage, Strategy: tc.strategy, Now: testNow})
		if !ok || got.Email != tc.want {
			t.Errorf("Select(%q) = %q (ok=%v), want %q", tc.strategy, got.Email, ok, tc.want)
		}
	}
}

// Weekly is the default because the two windows fail on different timescales: a
// 5-hour bucket refills this afternoon, a weekly one is gone for days. Ranking
// on the fast window lets every account's slow window drain in lockstep, which
// is the state that takes the whole fleet down at once.
func TestWeeklyIsTheDefaultStrategy(t *testing.T) {
	if got := ParseStrategy(""); got != StrategyLeastUsedWeekly {
		t.Errorf("default strategy = %q, want %q", got, StrategyLeastUsedWeekly)
	}
	if Strategies[0] != StrategyLeastUsedWeekly {
		t.Errorf("Strategies[0] = %q, want the default first", Strategies[0])
	}
	// The alias must never be offered as a choice — it is a value ParseStrategy
	// accepts, not one a user should be able to land on by cycling.
	for _, s := range Strategies {
		if s == StrategyLeastUsed {
			t.Error("the legacy alias is offered in the strategy picker")
		}
	}
}

// Labels are what the Settings cycler and the accounts dialog both render, so
// they have to be distinct — two modes sharing a label is a picker that looks
// broken.
func TestStrategyLabelsAreDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, s := range Strategies {
		l := StrategyLabel(s)
		if l == "" {
			t.Errorf("strategy %q has no label", s)
		}
		if prev, dup := seen[l]; dup {
			t.Errorf("strategies %q and %q share the label %q", prev, s, l)
		}
		seen[l] = s
	}
}

// A spent weekly bucket rejects work exactly as hard as a spent 5-hour one. An
// account whose week is gone but whose 5-hour window just reset used to report
// itself available, win selection, and fail on the first real call.
func TestSevenDayExhaustionCountsAsSpent(t *testing.T) {
	weekGone := Usage{
		FiveHourPct: 5, FiveHourReset: testNow.Add(time.Hour),
		SevenDayPct: 100, SevenDayReset: testNow.Add(72 * time.Hour),
		FetchedAt: testNow,
	}
	if !weekGone.Exhausted(testNow) {
		t.Fatal("a spent weekly window did not count as exhausted")
	}
	got, ok := Select(SelectOpts{
		Accounts: accts("weekgone", "fine"),
		Usage:    map[string]Usage{"weekgone": weekGone, "fine": used(80)},
		Strategy: StrategyLeastUsed,
		Now:      testNow,
	})
	if !ok || got.Email != "fine" {
		t.Fatalf("selected %q, want fine — the weekly-spent account was still a candidate", got.Email)
	}
}

// A passed reset means refilled, per window independently.
func TestSevenDayResetClearsExhaustion(t *testing.T) {
	u := Usage{SevenDayPct: 100, SevenDayReset: testNow.Add(-time.Minute), FetchedAt: testNow}
	if u.Exhausted(testNow) {
		t.Error("a weekly window whose reset has passed still read as spent")
	}
}

// With everything spent Select still returns the one that comes back first —
// and "comes back" has to mean the window actually blocking it. A 5-hour reset
// an hour away does not fix a weekly bucket with three days left on it.
func TestAllSpentPicksTheAccountWhoseBlockingWindowClearsFirst(t *testing.T) {
	weekly := Usage{ // 5h clear soon, but the week blocks for 3 days
		FiveHourPct: 100, FiveHourReset: testNow.Add(time.Hour),
		SevenDayPct: 100, SevenDayReset: testNow.Add(72 * time.Hour),
		FetchedAt: testNow,
	}
	hourly := Usage{ // only the 5h window is spent, back in two hours
		FiveHourPct: 100, FiveHourReset: testNow.Add(2 * time.Hour),
		SevenDayPct: 10, SevenDayReset: testNow.Add(96 * time.Hour),
		FetchedAt: testNow,
	}
	got, ok := Select(SelectOpts{
		Accounts: accts("weekly", "hourly"),
		Usage:    map[string]Usage{"weekly": weekly, "hourly": hourly},
		Strategy: StrategyLeastUsed,
		Now:      testNow,
	})
	if !ok || got.Email != "hourly" {
		t.Fatalf("selected %q, want hourly — it is the one that actually returns first", got.Email)
	}
}

// Manual falls through to the automatic ranking when its pin names no
// configured account — and that fall-through must rank on the *default*
// window, not on whatever placeholder the strategy lookup happened to return.
//
// rankWindow used to hand back (WindowFiveHour, false) for manual and Select
// dropped the bool, so this path silently balanced the fast window while the
// documented default is weekly.
func TestManualFallThroughRanksOnTheDefaultWindow(t *testing.T) {
	// a wins on 5-hour, b wins on weekly. Neither is spent.
	usage := map[string]Usage{
		"a": {FiveHourPct: 10, SevenDayPct: 80, FetchedAt: testNow, FiveHourReset: testNow.Add(time.Hour)},
		"b": {FiveHourPct: 80, SevenDayPct: 10, FetchedAt: testNow, FiveHourReset: testNow.Add(time.Hour)},
	}
	got, ok := Select(SelectOpts{
		Accounts: accts("a", "b"),
		Usage:    usage,
		Strategy: StrategyManual,
		Manual:   "removed@example.com", // no longer configured → falls through
		Now:      testNow,
	})
	if !ok || got.Email != "b" {
		t.Fatalf("manual fall-through = %q, want b — it ranked on the 5-hour window, not the weekly default", got.Email)
	}
}

// RanksByUsage exists so callers outside this package can ask "does the quota
// reading matter here" without naming members — the split broke a caller that
// compared against a single constant, and would break it again.
func TestRanksByUsageCoversTheWholeLeastUsedFamily(t *testing.T) {
	for _, s := range []string{StrategyLeastUsedWeekly, StrategyLeastUsed5H, StrategyLeastUsed, ""} {
		if !RanksByUsage(s) {
			t.Errorf("RanksByUsage(%q) = false, want true", s)
		}
	}
	for _, s := range []string{StrategyWaterfall, StrategyManual} {
		if RanksByUsage(s) {
			t.Errorf("RanksByUsage(%q) = true, want false", s)
		}
	}
}
