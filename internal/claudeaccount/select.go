package claudeaccount

import (
	"strings"
	"time"
)

// Assignment strategies for new sessions.
const (
	// StrategyLeastUsedWeekly gives a new session whichever allowed account has
	// the most headroom in its *weekly* window. The default.
	//
	// Weekly rather than 5-hour because the two windows fail differently. A
	// 5-hour bucket refills this afternoon, so spending it early costs an hour
	// of waiting; a weekly bucket spent on Tuesday is gone until the following
	// week. Balancing the slow window is therefore what keeps the fleet running
	// at all, and the fast window largely takes care of itself.
	StrategyLeastUsedWeekly = "least_used_weekly"
	// StrategyLeastUsed5H ranks on the 5-hour window instead — the right choice
	// when the day's throughput matters more than the week's, e.g. one long
	// session per account where nothing is close to a weekly cap.
	StrategyLeastUsed5H = "least_used_5h"
	// StrategyLeastUsed is the pre-split name. Kept as an alias rather than a
	// value: every config carrying it got it as the default, so it follows the
	// default forward. ParseStrategy resolves it; nothing else should compare
	// against it.
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

// ParseStrategy normalizes a configured strategy, falling back to weekly
// least-used for anything unrecognized (including empty).
//
// The legacy "least_used" resolves to the weekly variant, so an unset config and
// one carrying the old name mean the same thing. The alternative — mapping the
// old name to the 5-hour variant — would make the same file behave differently
// depending on whether the key was present or absent, which is a miserable thing
// to debug from a bug report.
func ParseStrategy(s string) string {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case StrategyWaterfall:
		return StrategyWaterfall
	case StrategyManual:
		return StrategyManual
	case StrategyLeastUsed5H:
		return StrategyLeastUsed5H
	default:
		return StrategyLeastUsedWeekly
	}
}

// Strategies is the assignment modes in the order surfaces should offer them,
// least surprising first. The legacy alias is deliberately absent: it is a value
// ParseStrategy accepts, never one a user should be able to select.
var Strategies = []string{
	StrategyLeastUsedWeekly,
	StrategyLeastUsed5H,
	StrategyWaterfall,
	StrategyManual,
}

// StrategyLabel is the strategy as shown to a user.
//
// Here rather than in the UI for the same reason Window.Name is: the Settings
// cycler and the accounts dialog both name these, and two copies of the strings
// is how they stop agreeing. The least-used pair names its window, because
// which window it ranks on is the entire difference between them.
func StrategyLabel(s string) string {
	switch ParseStrategy(s) {
	case StrategyLeastUsed5H:
		return "Least used · 5-hour"
	case StrategyWaterfall:
		return "Waterfall"
	case StrategyManual:
		return "Manual"
	default:
		return "Least used · weekly"
	}
}

// StrategyLabels is every label, for width budgeting.
func StrategyLabels() []string {
	out := make([]string, 0, len(Strategies))
	for _, s := range Strategies {
		out = append(out, StrategyLabel(s))
	}
	return out
}

// RanksByUsage reports whether a strategy orders accounts by how much quota
// they have left — true for the least-used family, false for waterfall
// (configured order) and manual (the account it was given).
//
// Exported because callers outside this package need to say "does the usage
// reading matter here" without naming the members, which is how the least-used
// split broke a caller that compared against a single constant.
func RanksByUsage(strategy string) bool {
	switch ParseStrategy(strategy) {
	case StrategyLeastUsed5H, StrategyLeastUsedWeekly:
		return true
	default:
		return false
	}
}

// rankWindow says which usage window orders accounts under this strategy.
//
// Total, and deliberately so. It used to return a second "does this strategy
// rank at all" bool alongside a placeholder window, and Select discarded the
// bool — so the one path that reaches the ranking with a non-least-used
// strategy (manual, whose pin names no configured account, falling through by
// design) balanced the 5-hour window while the default is weekly. A return
// value every caller drops is not a safeguard.
//
// The non-ranking strategies answer with the default strategy's window, which
// is also the semantically right answer: a user on manual has expressed no
// ranking preference, so the fall-through should behave like an unset config.
func rankWindow(strategy string) Window {
	if ParseStrategy(strategy) == StrategyLeastUsed5H {
		return WindowFiveHour
	}
	return WindowSevenDay
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
	candidates := dropLoggedOut(filterAllowed(o.Accounts, o.Allowed), o.Usage)
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
		return soonestReset(candidates, o.Usage, now), true
	}

	if ParseStrategy(o.Strategy) == StrategyWaterfall {
		return live[0], true // already ordered by Store.List
	}

	// Which window "least used" means is the strategy's whole content: the two
	// buckets fail on different timescales, so ranking on the wrong one balances
	// a resource that was never scarce.
	win := rankWindow(o.Strategy)
	best := live[0]
	bestPct := pctOf(o.Usage, best.Email, win)
	for _, a := range live[1:] {
		// Strictly-less keeps ties on the earlier account, so equal utilization
		// resolves by configured order rather than arbitrarily. Ties are routine
		// on the weekly window early in the week, where every account reads 0.
		if p := pctOf(o.Usage, a.Email, win); p < bestPct {
			best, bestPct = a, p
		}
	}
	return best, true
}

func pctOf(usage map[string]Usage, email string, win Window) int {
	u, ok := usage[email]
	if !ok || !u.Known() {
		return unknownPct
	}
	return u.Pct(win)
}

// dropLoggedOut removes accounts with no live claude.ai login.
//
// Ahead of every strategy, including manual — a logged-out account cannot run a
// session at all, so there is no mode in which handing one out is the right
// answer. Categorically different from spent, which Select deliberately still
// returns (soonest to reset) because a wait ends on its own.
//
// When it empties the candidate list Select reports false and the caller falls
// back to the ambient login. That is the better failure: the login the user is
// already using probably works, and an account nobody is logged into certainly
// doesn't — even under an allowlist, whose job is to route work between
// accounts that can do it.
//
// Nothing here is sticky. The verdict lives in Usage and the poll keeps running,
// so logging the account back in makes it a candidate again on the next poll.
func dropLoggedOut(accounts []Account, usage map[string]Usage) []Account {
	out := make([]Account, 0, len(accounts))
	for _, a := range accounts {
		if usage[a.Email].Usable() {
			out = append(out, a)
		}
	}
	return out
}

// AllowedConfigured reports whether an origin's allowlist names at least one
// account that actually exists.
//
// Exists because Select's single false answer covers three situations that call
// for three different responses, and only the caller can act on the difference:
//
//   - nothing configured at all — the ambient login is correct, and silence is
//     correct with it;
//   - an allowlist naming no configured account — a typo, or an account since
//     removed. Falling back here is the one genuinely wrong answer: Select's own
//     contract says using a disallowed account is worse than waiting, and the
//     ambient login may be precisely such an account;
//   - every allowed account logged out — dropLoggedOut deliberately falls back,
//     because the login the user is already sitting in probably works and one
//     nobody is logged into certainly does not.
//
// This answers the middle case, so callers can refuse there without having to
// reimplement filterAllowed or reverse the third decision by accident. It is a
// question about configuration, not about state: usage never enters into it.
func AllowedConfigured(accounts []Account, allowed []string) bool {
	return len(filterAllowed(accounts, allowed)) > 0
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
//
// "Comes back" means whichever window is actually blocking it — a weekly bucket
// spent for five more days is not fixed by a 5-hour reset an hour from now.
func soonestReset(accounts []Account, usage map[string]Usage, now time.Time) Account {
	best := accounts[0]
	bestAt, bestKnown := resetOf(usage, best.Email, now)
	for _, a := range accounts[1:] {
		at, known := resetOf(usage, a.Email, now)
		switch {
		case known && !bestKnown:
			best, bestAt, bestKnown = a, at, true
		case known && bestKnown && at.Before(bestAt):
			best, bestAt = a, at
		}
	}
	return best
}

func resetOf(usage map[string]Usage, email string, now time.Time) (time.Time, bool) {
	u, ok := usage[email]
	if !ok {
		return time.Time{}, false
	}
	if at := u.SpentWindowReset(now); !at.IsZero() {
		return at, true
	}
	// Not spent by our reading (or no reset known): fall back to the 5-hour
	// clock, which is the sooner of the two whenever both are present.
	if !u.FiveHourReset.IsZero() {
		return u.FiveHourReset, true
	}
	return time.Time{}, false
}
