package ui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/session"
)

// moveHome builds a Home with two Claude accounts and one session pinned to the
// first, which is the shape every case below varies.
func moveHome(t *testing.T, usage map[string]claudeaccount.Usage) (*Home, *session.Session) {
	t.Helper()
	storage, err := session.Open(filepath.Join(t.TempDir(), "move.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	h.width, h.height = 120, 40
	h.accounts = &claudeaccount.Store{}
	h.accounts.Upsert(claudeaccount.Account{Email: "first@x.com", ConfigDir: "/d/1"})
	h.accounts.Upsert(claudeaccount.Account{Email: "second@x.com", ConfigDir: "/d/2"})
	h.accountUsage.Store(&usage)

	s := session.NewSession("api-work", "/tmp/move-e2e")
	s.Agent = agent.Claude
	s.Account = "first@x.com"
	if err := storage.SaveSession(s.ToRow()); err != nil {
		t.Fatalf("save session: %v", err)
	}
	h.sessions = []*session.Session{s}
	h.flatItems = []SidebarItem{sessionRow(s)}
	h.cursor = 0
	return h, s
}

func knownUsage(pct int) claudeaccount.Usage {
	return claudeaccount.Usage{FiveHourPct: pct, FetchedAt: time.Now(),
		FiveHourReset: time.Now().Add(time.Hour)}
}

// The reason this exists: an account with no login cannot run the session, so a
// restart that faithfully re-uses the pinned account just fails again. Pinning
// protects the prompt cache, and there is no cache worth protecting on an
// account nobody is logged into.
func TestRestartMovesOffALoggedOutAccount(t *testing.T) {
	h, s := moveHome(t, map[string]claudeaccount.Usage{
		"first@x.com":  {LoggedOut: true, Err: claudeaccount.ErrNotLoggedIn},
		"second@x.com": knownUsage(30),
	})

	if !h.healAccountBeforeRelaunch(s) {
		t.Fatal("restart kept a session on a logged-out account")
	}
	if s.Account != "second@x.com" {
		t.Errorf("moved to %q, want second@x.com", s.Account)
	}
	// Persisted, or the session silently returns to the dead account on the next
	// fleet start and the user gets to diagnose it twice.
	rows, err := h.storage.LoadSessions()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if rows[0].Account != "second@x.com" {
		t.Errorf("stored account = %q, want second@x.com", rows[0].Account)
	}
}

func TestRestartMovesOffASpentAccount(t *testing.T) {
	h, s := moveHome(t, map[string]claudeaccount.Usage{
		"first@x.com":  knownUsage(100),
		"second@x.com": knownUsage(10),
	})
	if !h.healAccountBeforeRelaunch(s) {
		t.Fatal("restart kept a session on a spent account")
	}
	if s.Account != "second@x.com" {
		t.Errorf("moved to %q, want second@x.com", s.Account)
	}
}

// The pin is the default and it is load-bearing: Claude's prompt cache is
// per-account, so a restart that rotates a perfectly good session throws away
// the state the restart was meant to keep.
func TestRestartKeepsAWorkingAccount(t *testing.T) {
	h, s := moveHome(t, map[string]claudeaccount.Usage{
		"first@x.com":  knownUsage(80), // busy, but working
		"second@x.com": knownUsage(1),  // emptier, and irrelevant
	})
	if h.healAccountBeforeRelaunch(s) {
		t.Fatal("restart rotated a session whose account was merely busy")
	}
	if s.Account != "first@x.com" {
		t.Errorf("account = %q, want it left alone", s.Account)
	}
}

// Fleet failing to reach Anthropic says nothing about the credential. Healing on
// that would rotate every session in the fleet during an outage — and onto
// accounts whose state is equally unknown.
func TestRestartDoesNotMoveOnAnUnreadableAccount(t *testing.T) {
	h, s := moveHome(t, map[string]claudeaccount.Usage{
		"first@x.com": {AttemptedAt: time.Now(), Err: claudeaccount.ErrNoCredential},
	})
	if h.healAccountBeforeRelaunch(s) {
		t.Fatal("an unpollable account was treated as broken")
	}
}

// With nowhere better to go, moving costs the prompt cache and buys nothing.
func TestRestartStaysPutWhenEveryAccountIsSpent(t *testing.T) {
	h, s := moveHome(t, map[string]claudeaccount.Usage{
		"first@x.com":  knownUsage(100),
		"second@x.com": knownUsage(100),
	})
	if h.healAccountBeforeRelaunch(s) {
		t.Error("session moved between two equally spent accounts")
	}
}

// A session on the ambient login was never assigned an account, so there is no
// pin to heal — and rotating it onto a subscription would start billing one
// without being asked.
func TestRestartLeavesAnAmbientSessionAlone(t *testing.T) {
	h, s := moveHome(t, map[string]claudeaccount.Usage{"second@x.com": knownUsage(10)})
	s.Account = ""
	if h.healAccountBeforeRelaunch(s) {
		t.Error("a session on the ambient login was assigned an account by a restart")
	}
}

func TestRestartLeavesNonClaudeAgentsAlone(t *testing.T) {
	h, s := moveHome(t, map[string]claudeaccount.Usage{
		"first@x.com": {LoggedOut: true},
	})
	s.Agent = agent.Codex
	if h.healAccountBeforeRelaunch(s) {
		t.Error("a Codex session was moved between Claude accounts")
	}
}

// The manual move is only honest if it relaunches: the token is baked into the
// tmux environment at launch, so a move that just rewrote the record would
// relabel the row while the live process kept billing the old subscription.
func TestManualMovePersistsAndRelaunches(t *testing.T) {
	h, s := moveHome(t, map[string]claudeaccount.Usage{
		"first@x.com":  knownUsage(90),
		"second@x.com": knownUsage(5),
	})

	cmd := h.moveSelectedToAccount("second@x.com")
	if cmd == nil {
		t.Fatal("move produced no relaunch command")
	}
	if s.Account != "second@x.com" {
		t.Errorf("account = %q, want second@x.com", s.Account)
	}
	rows, err := h.storage.LoadSessions()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if rows[0].Account != "second@x.com" {
		t.Errorf("stored account = %q, want second@x.com", rows[0].Account)
	}
}

func TestManualMoveToTheCurrentAccountIsANoOp(t *testing.T) {
	h, _ := moveHome(t, map[string]claudeaccount.Usage{"first@x.com": knownUsage(10)})
	if cmd := h.moveSelectedToAccount("first@x.com"); cmd != nil {
		t.Error("moving to the account already in use produced a restart")
	}
}

// The picker must offer only accounts that would actually help: the one the
// session is already on is a no-op, and an account with no login is a downgrade.
func TestPickerMarksCurrentAndLoggedOutAsUnpickable(t *testing.T) {
	h, _ := moveHome(t, map[string]claudeaccount.Usage{
		"first@x.com":  knownUsage(90),
		"second@x.com": {LoggedOut: true},
	})
	h.openAccountPicker()
	if !h.accountPicker.IsVisible() {
		t.Fatal("picker did not open")
	}
	for _, r := range h.accountPicker.rows {
		if r.enabled {
			t.Errorf("%s is offered; it is %s", r.email, r.note)
		}
	}
	if h.accountPicker.rows[0].note != "current" {
		t.Errorf("first row note = %q, want current", h.accountPicker.rows[0].note)
	}
	if h.accountPicker.rows[1].note != "logged out" {
		t.Errorf("second row note = %q, want logged out", h.accountPicker.rows[1].note)
	}
}

// Same discipline as the context menu and the snooze picker: the dialog named a
// row, and an async rebuild can move the cursor while it is open. Getting this
// wrong bills a different session's work to the chosen subscription.
func TestPickedAccountFollowsTheTargetNotTheCursor(t *testing.T) {
	h, alpha := moveHome(t, map[string]claudeaccount.Usage{
		"first@x.com":  knownUsage(90),
		"second@x.com": knownUsage(5),
	})
	beta := session.NewSession("beta", "/tmp/move-e2e")
	beta.Agent = agent.Claude
	beta.Account = "first@x.com"
	if err := h.storage.SaveSession(beta.ToRow()); err != nil {
		t.Fatalf("save beta: %v", err)
	}
	h.sessions = append(h.sessions, beta)
	h.flatItems = append(h.flatItems, sessionRow(beta))

	h.openAccountPicker() // opened on alpha
	h.cursor = 1          // an async rebuild lands the cursor on beta

	if _, cmd := h.Update(accountPickedMsg{email: "second@x.com"}); cmd != nil {
		cmd()
	}
	if alpha.Account != "second@x.com" {
		t.Errorf("alpha account = %q, want second@x.com — the picker acted on the wrong row", alpha.Account)
	}
	if beta.Account != "first@x.com" {
		t.Errorf("beta was moved to %q; the picker never named it", beta.Account)
	}
}
