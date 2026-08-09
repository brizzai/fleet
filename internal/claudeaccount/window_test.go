package claudeaccount

import (
	"testing"
	"time"
)

// Exhausted covers both buckets, so every surface that reports a spent account
// has to name the right one — they differ by days. Naming the 5-hour window for
// a spent weekly one offers a twenty-minute wait on an account blocked until
// next week, and the account picker turns that into a real relaunch with the
// prompt cache discarded.
func TestSpentWindowNamesTheBucketThatIsActuallyBlocking(t *testing.T) {
	now := time.Now()
	weekly := Usage{
		FiveHourPct:   10,
		FiveHourReset: now.Add(20 * time.Minute),
		SevenDayPct:   99,
		SevenDayReset: now.Add(5 * 24 * time.Hour),
		FetchedAt:     now,
	}
	win, reset, spent := weekly.SpentWindow(now)
	if !spent {
		t.Fatal("a 99% weekly bucket must read as spent")
	}
	if win != WindowSevenDay {
		t.Errorf("window = %v (%s), want weekly", win, win.Name())
	}
	if !reset.Equal(weekly.SevenDayReset) {
		t.Errorf("reset = %v, want the weekly reset %v", reset, weekly.SevenDayReset)
	}
}

// When both are spent the account is unusable until the slower one returns, so
// that is the horizon to report — matching SpentWindowReset, which callers still
// use.
func TestSpentWindowReportsTheLaterBucketWhenBothAreSpent(t *testing.T) {
	now := time.Now()
	both := Usage{
		FiveHourPct:   100,
		FiveHourReset: now.Add(time.Hour),
		SevenDayPct:   100,
		SevenDayReset: now.Add(3 * 24 * time.Hour),
		FetchedAt:     now,
	}
	win, reset, spent := both.SpentWindow(now)
	if !spent || win != WindowSevenDay {
		t.Errorf("window = %s, spent = %v; want weekly and spent", win.Name(), spent)
	}
	if !reset.Equal(both.SpentWindowReset(now)) {
		t.Error("SpentWindow and SpentWindowReset disagree on the horizon")
	}
}

// A healthy account reports no spent window at all, so the caller falls through
// to the percentage rather than rendering a countdown to nothing.
func TestSpentWindowIsQuietWhenNothingIsSpent(t *testing.T) {
	now := time.Now()
	fine := Usage{FiveHourPct: 40, SevenDayPct: 55, FetchedAt: now}
	if _, _, spent := fine.SpentWindow(now); spent {
		t.Error("a healthy account reported a spent window")
	}
}
