package claudeaccount

import (
	"strings"
	"time"
)

// Assignment strategies for new sessions.
const (
	// StrategyLeastUsed gives a new session whichever allowed account has the
	// most headroom in its 5-hour window. The default.
	StrategyLeastUsed = "least_used"
	// StrategyWaterfall drains accounts in configured order, skipping spent
	// ones. Needs only a binary "is it spent" signal, so it is the mode that
	// keeps working if the usage endpoint disappears.
	StrategyWaterfall = "waterfall"
	// StrategyManual pins every new session to one chosen account and never
	// rotates. The opt-out.
	StrategyManual = "manual"
)

// unknownPct is the utilization assumed for an account fleet has not managed to
// poll. The midpoint is chosen so that a never-polled account ranks below a
// known-idle one and above a known-busy one — and so that when *nothing* can be
// polled every account ties and least-used degrades cleanly into waterfall
// order rather than picking arbitrarily.
const unknownPct = 50

// ParseStrategy normalizes a configured strategy, falling back to least-used
// for anything unrecognized (including empty).
func ParseStrategy(s string) string {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case StrategyWaterfall:
		return StrategyWaterfall
	case StrategyManual:
		return StrategyManual
	default:
		return StrategyLeastUsed
	}
}

// SelectOpts are the inputs to an assignment decision.
type SelectOpts struct {
	Accounts []Account        // ordered, as returned by Store.List
	Usage    map[string]Usage // keyed by email; missing entries are "unknown"
	Strategy string
	// Manual is the account chosen under StrategyManual.
	Manual string
	// Allowed restricts the candidate set to these emails. Empty means all —
	// which is the default, so the common case configures nothing.
	Allowed []string
	Now     time.Time
}

// Select picks the account for a new session.
//
// The second return is false only when there is genuinely nothing to choose
// from — no accounts configured, or an allowlist that excludes every one of
// them. Callers must then fall back to the ambient login by setting no env var.
//
// When candidates exist but all are spent it still returns one (the soonest to
// reset) rather than falling back. That matters most under an allowlist: the
// whole point of restricting an origin is that using a disallowed account is
// worse than waiting, and the ambient login could be exactly such an account.
// The session gets created and carries the spent marker.
func Select(o SelectOpts) (Account, bool) {
	candidates := filterAllowed(o.Accounts, o.Allowed)
	if len(candidates) == 0 {
		return Account{}, false
	}

	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}

	// Manual is strict: it returns the chosen account even when spent. Quietly
	// rotating away from it would defeat the only reason to pick this mode.
	if ParseStrategy(o.Strategy) == StrategyManual {
		for _, a := range candidates {
			if a.Email == o.Manual {
				return a, true
			}
		}
		// A default that was removed or is disallowed here falls through to the
		// automatic modes rather than stranding the session.
	}

	live := make([]Account, 0, len(candidates))
	for _, a := range candidates {
		if !o.Usage[a.Email].Exhausted(now) {
			live = append(live, a)
		}
	}
	if len(live) == 0 {
		return soonestReset(candidates, o.Usage), true
	}

	if ParseStrategy(o.Strategy) == StrategyWaterfall {
		return live[0], true // already ordered by Store.List
	}

	best := live[0]
	bestPct := pctOf(o.Usage, best.Email)
	for _, a := range live[1:] {
		// Strictly-less keeps ties on the earlier account, so equal utilization
		// resolves by configured order rather than arbitrarily.
		if p := pctOf(o.Usage, a.Email); p < bestPct {
			best, bestPct = a, p
		}
	}
	return best, true
}

func pctOf(usage map[string]Usage, email string) int {
	u, ok := usage[email]
	if !ok || !u.Known() {
		return unknownPct
	}
	return u.FiveHourPct
}

// filterAllowed keeps only accounts named in allowed. An empty allowlist means
// no restriction, which is the default for every origin.
func filterAllowed(accounts []Account, allowed []string) []Account {
	if len(allowed) == 0 {
		return accounts
	}
	set := make(map[string]bool, len(allowed))
	for _, e := range allowed {
		set[e] = true
	}
	out := make([]Account, 0, len(accounts))
	for _, a := range accounts {
		if set[a.Email] {
			out = append(out, a)
		}
	}
	return out
}

// soonestReset picks the account that will come back first. An account with no
// known reset time sorts last: we cannot promise it recovers sooner than one
// that has actually told us when it will.
func soonestReset(accounts []Account, usage map[string]Usage) Account {
	best := accounts[0]
	bestAt, bestKnown := resetOf(usage, best.Email)
	for _, a := range accounts[1:] {
		at, known := resetOf(usage, a.Email)
		switch {
		case known && !bestKnown:
			best, bestAt, bestKnown = a, at, true
		case known && bestKnown && at.Before(bestAt):
			best, bestAt = a, at
		}
	}
	return best
}

func resetOf(usage map[string]Usage, email string) (time.Time, bool) {
	u, ok := usage[email]
	if !ok {
		return time.Time{}, false
	}
	if !u.FiveHourReset.IsZero() {
		return u.FiveHourReset, true
	}
	return time.Time{}, false
}
