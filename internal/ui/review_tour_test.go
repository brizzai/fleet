package ui

import (
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/session"
)

// A session is allowed to sit on a spent account because a person waits and the
// window resets. A tour is fired once when the reader opens and never retried,
// so handing it a spent account is not a delay — it is the feature silently not
// existing. This is the bug the first real run hit, every time.
func TestTourSkipsTheSessionsAccountWhenItIsSpent(t *testing.T) {
	spent := "spent@example.com"
	live := "live@example.com"
	dirs := map[string]string{spent: "/dirs/spent", live: "/dirs/live"}

	store := &claudeaccount.Store{}
	store.Upsert(claudeaccount.Account{Email: spent, ConfigDir: dirs[spent], OrgUUID: "o1"})
	store.Upsert(claudeaccount.Account{Email: live, ConfigDir: dirs[live], OrgUUID: "o2"})

	k := reviewKey{repo: "brizzai/brizzai", pr: 5169}
	s := &session.Session{
		ID: "rev11111-1700000000", Title: "#5169",
		ProjectPath: "/tmp/fleet-test/brizzai-reviews/5169", Account: spent,
	}
	h := &Home{cfg: &config.Config{}, accounts: store, sessions: []*session.Session{s}}

	// Nothing known about usage: the pin stands, because "spent" is a fact and
	// ignorance is not.
	empty := map[string]claudeaccount.Usage{}
	h.accountUsage.Store(&empty)
	if got := h.tourAccountDir(k); got != dirs[spent] {
		t.Errorf("with no usage known, dir = %q, want the session's own account", got)
	}

	// Now it is known to be spent, so the tour must go somewhere it can be
	// answered rather than to the account that cannot answer it.
	known := map[string]claudeaccount.Usage{spent: exhaustedUsage()}
	h.accountUsage.Store(&known)
	if got := h.tourAccountDir(k); got == dirs[spent] {
		t.Error("the tour still went to the exhausted account — Select filters " +
			"these, and preferring the session's pin opted out of that check")
	}
}

func exhaustedUsage() claudeaccount.Usage {
	return claudeaccount.Usage{
		FetchedAt:     time.Now(),
		FiveHourPct:   100,
		FiveHourReset: time.Now().Add(time.Hour),
		SevenDayPct:   11,
	}
}
