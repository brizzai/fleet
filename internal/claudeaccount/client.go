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
	_, at, _ := u.SpentWindow(now)
	return at
}

// SpentWindow names the bucket that is actually blocking the account, and when
// it refills.
//
// The companion Exhausted needed once it stopped meaning "the 5-hour window":
// every surface that reports a spent account has to name the right one, because
// the two differ by days. An account at 99% weekly whose 5-hour bucket just
// reset is blocked for five days, and telling the user it comes back in twenty
// minutes invites them to move a session onto it — a real relaunch, prompt cache
// discarded, onto an account that still cannot answer.
//
// When both are spent it reports the later one, matching SpentWindowReset: the
// account is unusable until the slower bucket returns, so that is the honest
// horizon.
func (u Usage) SpentWindow(now time.Time) (Window, time.Time, bool) {
	five := u.windowSpent(u.FiveHourPct, u.FiveHourReset, now)
	seven := u.windowSpent(u.SevenDayPct, u.SevenDayReset, now)
	switch {
	case five && seven:
		if u.SevenDayReset.After(u.FiveHourReset) {
			return WindowSevenDay, u.SevenDayReset, true
		}
		return WindowFiveHour, u.FiveHourReset, true
	case seven:
		return WindowSevenDay, u.SevenDayReset, true
	case five:
		return WindowFiveHour, u.FiveHourReset, true
	}
	return WindowFiveHour, time.Time{}, false
}

// StaleAfterReset reports whether a window refilled between the last poll
// attempt and now, which makes the percentages here describe a bucket that no
// longer exists.
//
// It is the one condition worth breaking MinPollInterval for. The reading is
// not merely old, it is about the wrong window, and it is stale in the
// direction that matters: an account that has just come back keeps reading
// spent for another three minutes, exactly while its owner is watching the
// countdown run out.
//
// Bounded below by AttemptedAt rather than simply "the reset is in the past":
// a poll that fails at the boundary moves AttemptedAt past the reset, so the
// account backs off at the normal cadence instead of retrying every tick
// against an endpoint that just refused.
func (u Usage) StaleAfterReset(now time.Time) bool {
	return refilledBetween(u.FiveHourReset, u.AttemptedAt, now) ||
		refilledBetween(u.SevenDayReset, u.AttemptedAt, now)
}

// refilledBetween reports whether a known reset falls in (since, now].
func refilledBetween(reset, since, now time.Time) bool {
	return !reset.IsZero() && reset.After(since) && !reset.After(now)
}

// Window is one of the two quota buckets Anthropic reports.
type Window int

const (
	WindowFiveHour Window = iota
	WindowSevenDay
)

// Name is the window as a word, for anywhere it is shown to a user.
//
// Deliberately not "5h"/"7d": those are bare durations, and they sit beside
// countdowns that are also bare durations meaning something entirely different
// ("resets in 5 days" next to "these are the 7-day figures"). A word cannot be
// misread as a countdown.
//
// It lives here rather than in the UI because three surfaces name a window and
// they must agree; two copies of these strings is how they stop agreeing.
func (w Window) Name() string {
	if w == WindowFiveHour {
		return "5-hour"
	}
	return "weekly"
}

// Pct is this usage's utilization in the given window.
func (u Usage) Pct(w Window) int {
	if w == WindowFiveHour {
		return u.FiveHourPct
	}
	return u.SevenDayPct
}

// Reset is when the given window refills, zero if unknown.
func (u Usage) Reset(w Window) time.Time {
	if w == WindowFiveHour {
		return u.FiveHourReset
	}
	return u.SevenDayReset
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
