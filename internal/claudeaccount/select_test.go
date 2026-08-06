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
		out[i] = Account{Email: e, Token: "tok-" + e, Order: i}
	}
	return out
}

// used builds a known usage reading at the given 5-hour utilization.
func used(pct int) Usage {
	return Usage{FiveHourPct: pct, FetchedAt: testNow, FiveHourReset: testNow.Add(time.Hour)}
}

// rejected builds the reading for an account the API refuses. Note it carries no
// successful fetch, which is exactly the shape that used to score as "unknown".
func rejected() Usage {
	return Usage{AttemptedAt: testNow, Err: ErrTokenRejected, Rejected: true}
}

// The bug this closes: a token the API answers 403 to never gets a reading, so
// pctOf scored it at unknownPct (50) — which beats a healthy account at 59% and
// handed every new session the one credential that cannot run.
func TestSelectSkipsRejectedEvenWhenTheAliveAccountIsBusier(t *testing.T) {
	got, ok := Select(SelectOpts{
		Accounts: accts("dead", "alive"),
		Usage:    map[string]Usage{"dead": rejected(), "alive": used(59)},
		Strategy: StrategyLeastUsed,
		Now:      testNow,
	})
	if !ok || got.Email != "alive" {
		t.Fatalf("least_used = %q (ok=%v), want alive — a rejected token outranked a working account", got.Email, ok)
	}
}

// Manual is strict about *spent* because a wait ends. A rejection never does, so
// it is dropped ahead of every strategy rather than pinned to.
func TestSelectManualDoesNotPinToARejectedAccount(t *testing.T) {
	got, ok := Select(SelectOpts{
		Accounts: accts("dead", "alive"),
		Usage:    map[string]Usage{"dead": rejected(), "alive": used(90)},
		Strategy: StrategyManual,
		Manual:   "dead",
		Now:      testNow,
	})
	if !ok || got.Email != "alive" {
		t.Fatalf("manual = %q (ok=%v), want alive", got.Email, ok)
	}
}

func TestSelectWaterfallSkipsRejected(t *testing.T) {
	got, _ := Select(SelectOpts{
		Accounts: accts("dead", "alive"),
		Usage:    map[string]Usage{"dead": rejected()},
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
func TestSelectAllRejectedFallsBackToAmbient(t *testing.T) {
	_, ok := Select(SelectOpts{
		Accounts: accts("a", "b"),
		Usage:    map[string]Usage{"a": rejected(), "b": rejected()},
		Strategy: StrategyLeastUsed,
		Now:      testNow,
	})
	if ok {
		t.Fatal("Select handed out a rejected account instead of falling back to the ambient login")
	}
}

// A rejection must not be inferred from an unreadable endpoint: fleet failing to
// reach Anthropic says nothing about the credential, and excluding on that would
// empty the candidate list during any outage.
func TestSelectKeepsAnAccountWhoseProbeMerelyFailed(t *testing.T) {
	got, ok := Select(SelectOpts{
		Accounts: accts("blip"),
		Usage:    map[string]Usage{"blip": {AttemptedAt: testNow, Err: ErrNoQuotaHeaders}},
		Strategy: StrategyLeastUsed,
		Now:      testNow,
	})
	if !ok || got.Email != "blip" {
		t.Fatalf("Select = %q (ok=%v), want blip — a failed poll is not a rejection", got.Email, ok)
	}
}

// The verdict rides on the live reading, so a later successful poll (or a fresh
// token, which replaces the entry outright) brings the account straight back.
func TestHealedAccountBecomesSelectableAgain(t *testing.T) {
	usage := map[string]Usage{"a": rejected(), "b": used(70)}
	if got, _ := Select(SelectOpts{Accounts: accts("a", "b"), Usage: usage, Now: testNow}); got.Email != "b" {
		t.Fatalf("while rejected: %q, want b", got.Email)
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
		" Waterfall ": StrategyWaterfall,
		"MANUAL":      StrategyManual,
		"":            StrategyLeastUsed,
		"nonsense":    StrategyLeastUsed,
	} {
		if got := ParseStrategy(in); got != want {
			t.Errorf("ParseStrategy(%q) = %q, want %q", in, got, want)
		}
	}
}
