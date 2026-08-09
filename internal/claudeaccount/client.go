package claudeaccount

import (
	"net/http"
	"time"
)

const (
	// MinPollInterval is the floor between quota polls of one account. The
	// reading barely moves inside five minutes and each poll shells out to the
	// Keychain, so this is a floor, not a target.
	MinPollInterval = 180 * time.Second

	// ExhaustedPct is the utilization at which an account is treated as spent.
	// Not 100: the last percent is unusable in practice and rounding means a
	// truly empty bucket can report 99.
	ExhaustedPct = 98

	httpTimeout = 10 * time.Second
)

var httpClient = &http.Client{Timeout: httpTimeout}

// Usage is one account's quota state as last read.
//
// FetchedAt and AttemptedAt are deliberately separate. A network blip must not
// erase a reading taken three minutes ago — a stale measurement is much closer
// to the truth than "unknown", which would drop the account out of the readout
// and make Select rank it at the neutral midpoint. So a failure advances only
// AttemptedAt (which paces the retry) and leaves the numbers alone.
type Usage struct {
	FiveHourPct   int
	FiveHourReset time.Time
	SevenDayPct   int
	SevenDayReset time.Time

	// FetchedAt is when the percentages above were last successfully read.
	FetchedAt time.Time
	// AttemptedAt is when a read was last tried, successfully or not.
	AttemptedAt time.Time
	Err         error // last poll error; nil once a poll succeeds

	// LoggedOut records that this account has no live claude.ai login — never
	// logged in, logged out elsewhere, or the credential expired or was revoked.
	//
	// The one failure that is real information. Every other poll error means
	// "fleet could not ask", which says nothing about the account; this one says
	// the account cannot run a session, so Select must skip it. Scoring it as
	// merely unknown ranks it at the midpoint, ahead of a healthy account in
	// active use — which is how every new session ends up on the one account
	// that cannot work.
	//
	// Live state, never persisted: an account heals the moment it is logged in
	// again, and a verdict on disk would outlive the fix.
	LoggedOut bool
}

// Usable reports whether a session launched on this account could actually run.
//
// Only a missing login makes it false. Spent is not unusable — it is a wait,
// and it clears at the reset — and an unpollable account is not unusable
// either, since fleet failing to read the Keychain or reach Anthropic says
// nothing about the login.
func (u Usage) Usable() bool { return !u.LoggedOut }

// Exhausted reports whether this account should be skipped for new work.
//
// Either window counts. The 5-hour bucket is the one that usually bites, but a
// spent weekly bucket rejects work just as hard — and an account whose week is
// gone while its 5-hour window has just reset would otherwise report itself
// available, win selection, and fail on the first real call.
func (u Usage) Exhausted(now time.Time) bool {
	return u.windowSpent(u.FiveHourPct, u.FiveHourReset, now) ||
		u.windowSpent(u.SevenDayPct, u.SevenDayReset, now)
}

// windowSpent reports whether one bucket is used up and has not yet refilled.
//
// A stale reading is not evidence of exhaustion: a window whose reset has passed
// is available again whether or not we have managed to re-poll it.
func (u Usage) windowSpent(pct int, reset time.Time, now time.Time) bool {
	if !reset.IsZero() && !reset.After(now) {
		return false
	}
	return pct >= ExhaustedPct
}

// SpentWindowReset is when the exhausted bucket refills — the later of the two
// when both are spent, since the account is unusable until the slower one
// returns. Zero when nothing is spent or no reset time is known.
func (u Usage) SpentWindowReset(now time.Time) time.Time {
	var at time.Time
	if u.windowSpent(u.FiveHourPct, u.FiveHourReset, now) {
		at = u.FiveHourReset
	}
	if u.windowSpent(u.SevenDayPct, u.SevenDayReset, now) && u.SevenDayReset.After(at) {
		at = u.SevenDayReset
	}
	return at
}

// Known reports whether this usage carries a real reading, as opposed to the
// zero value a never-polled account has. Selection needs the distinction: an
// unknown account is a candidate ranked at the midpoint, a spent one is not.
//
// Deliberately independent of Err: a reading that succeeded once stays usable
// through a later failure. Err describes the last *attempt*, not the data.
func (u Usage) Known() bool {
	return !u.FetchedAt.IsZero()
}
