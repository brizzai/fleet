package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/git"
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

// The same rule when the other spent account resets sooner, which is the case
// that actually broke.
//
// When every candidate is spent Select returns soonestReset — routinely a
// *different* spent account. The old guard only asked "did it pick me again", so
// this moved: the pin was rewritten, the session took the full Restart path with
// its prompt cache discarded, and a toast announced a move onto an account that
// also could not run it.
//
// The reset times are explicit here on purpose. The sibling test above passes
// against either guard, because knownUsage stamps time.Now().Add(time.Hour)
// twice and "second" lands later by the nanoseconds between the two calls — so
// soonestReset happened to return the already-pinned account. Ordering by luck
// of evaluation is not a test of anything.
func TestRestartStaysPutWhenTheSoonerAccountIsAlsoSpent(t *testing.T) {
	spentUntil := func(d time.Duration) claudeaccount.Usage {
		return claudeaccount.Usage{
			FiveHourPct:   100,
			FiveHourReset: time.Now().Add(d),
			FetchedAt:     time.Now(),
		}
	}
	h, s := moveHome(t, map[string]claudeaccount.Usage{
		"first@x.com":  spentUntil(3 * time.Hour),
		"second@x.com": spentUntil(30 * time.Minute),
	})
	if h.healAccountBeforeRelaunch(s) {
		t.Errorf("session moved onto a spent account that resets sooner; account = %q", s.Account)
	}
	if s.Account != "first@x.com" {
		t.Errorf("pin was rewritten to %q", s.Account)
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

// Removing an account cleans up after itself.
//
// The handler used to call Store.Remove and stop, so a deliberate remove leaked
// what an abandoned add cleans up: the dir stayed on disk holding the mirrored
// .claude.json, Provision kept refreshing it on every launch, and re-adding the
// same subscription minted a fresh random dir and orphaned another one — with no
// way to clear any of it from fleet.
//
// The Keychain login is deliberately not touched: deleting it would log that
// subscription out of Claude Code entirely, which is more than "remove from
// fleet" means. The confirm dialog says so.
func TestRemovingAnAccountDeletesItsConfigDir(t *testing.T) {
	h, _ := moveHome(t, map[string]claudeaccount.Usage{})

	dir := filepath.Join(claudeaccount.AccountsRoot(), "deadbeef")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	h.accounts.Upsert(claudeaccount.Account{Email: "third@x.com", ConfigDir: dir})

	_, cmd := h.Update(accountRemoveMsg{email: "third@x.com"})

	// The store is updated synchronously — the row must be gone before the next
	// render, or the user sees an account they just deleted.
	if h.accounts.ConfigDirFor("third@x.com") != "" {
		t.Error("account still in the store after removal")
	}

	// The directory is not. RemoveAll is unbounded filesystem work, which must
	// not run on the Update goroutine, so it comes back as a command for Bubble
	// Tea to run off-thread. Draining the batch here is what a real program does
	// a moment later.
	if cmd == nil {
		t.Fatal("removal produced no cleanup command")
	}
	runCmdTree(t, cmd)

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("config dir survived the remove: %v", err)
	}
}

// runCmdTree executes a tea.Cmd, following tea.Batch one level deep.
//
// Batch does not run anything itself — it returns a BatchMsg holding the
// commands — so a test that just calls cmd() exercises the batching and none of
// the work.
func runCmdTree(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			if c != nil {
				c()
			}
		}
	}
}

// An allowlist that names no configured account must not quietly fall through
// to the ambient login — which may be the very account it excludes.
//
// Select returns false for this the same way it does for "no accounts at all",
// so resolveAccount reports the two apart and the caller refuses rather than
// launching. The refusal is returned rather than logged, so no creation path can
// reach a session without having seen it.
func TestSessionCreationRefusesWhenTheAllowlistNamesNothingConfigured(t *testing.T) {
	h, s := moveHome(t, map[string]claudeaccount.Usage{
		"first@x.com":  knownUsage(10),
		"second@x.com": knownUsage(20),
	})
	h.writeGitInfo(func(m map[string]*git.RepoInfo) bool {
		m[s.ProjectPath] = &git.RepoInfo{OriginKey: "github.com/acme/api"}
		return true
	})
	h.cfg.AllowedAccounts = map[string][]string{
		OriginExpandKey("github.com/acme/api"): {"gone@acme.com"},
	}

	account, blocked := h.resolveAccount(agent.Claude, s.ProjectPath)
	if blocked == "" {
		t.Fatal("an unsatisfiable allowlist resolved silently to the ambient login")
	}
	if account != "" {
		t.Errorf("account = %q, want none", account)
	}
	// The message has to name the accounts, or the user cannot tell a typo from
	// a removed account.
	if !strings.Contains(blocked, "gone@acme.com") {
		t.Errorf("refusal does not name the allowlist: %q", blocked)
	}
}

// The other half, and the one easiest to break by "simplifying" the branch
// above: every allowed account being logged out is a deliberate fallback, not a
// refusal. A fleet whose logins have all expired must still start sessions on
// the login the user is sitting in.
func TestSessionCreationStillLaunchesWhenEveryAllowedAccountIsLoggedOut(t *testing.T) {
	h, s := moveHome(t, map[string]claudeaccount.Usage{
		"first@x.com":  {LoggedOut: true, Err: claudeaccount.ErrNotLoggedIn},
		"second@x.com": {LoggedOut: true, Err: claudeaccount.ErrNotLoggedIn},
	})
	h.writeGitInfo(func(m map[string]*git.RepoInfo) bool {
		m[s.ProjectPath] = &git.RepoInfo{OriginKey: "github.com/acme/api"}
		return true
	})
	h.cfg.AllowedAccounts = map[string][]string{
		OriginExpandKey("github.com/acme/api"): {"first@x.com", "second@x.com"},
	}

	account, blocked := h.resolveAccount(agent.Claude, s.ProjectPath)
	if blocked != "" {
		t.Fatalf("refused a session because the logins expired: %q", blocked)
	}
	if account != "" {
		t.Errorf("account = %q, want the ambient login", account)
	}
	// Silent fallback was the original complaint, so the notice is part of the
	// contract, not decoration.
	if h.infoMsg == "" {
		t.Error("fell back to the ambient login without saying so")
	}
}

// The picker is an assignment path like any other, so the per-origin allowlist
// has to hold here too.
//
// It was the one surface that built its rows straight off Store.List, disabling
// only `current` and `logged out`. A user who restricted an origin to their work
// subscription could open "Move to Account…" on one of its sessions, pick their
// personal account, and fleet would pin and relaunch — billing client work to the
// wrong subscription, with no warning anywhere. A policy that holds on one
// surface and not another is worse than not having one.
func TestPickerRefusesAnAccountTheOriginDisallows(t *testing.T) {
	h, s := moveHome(t, map[string]claudeaccount.Usage{
		"first@x.com":  knownUsage(90),
		"second@x.com": knownUsage(5),
	})
	// Keyed the way allowedAccountsFor resolves it, via the git cache so the
	// lookup never shells out.
	h.writeGitInfo(func(m map[string]*git.RepoInfo) bool {
		m[s.ProjectPath] = &git.RepoInfo{OriginKey: "github.com/acme/api"}
		return true
	})
	h.cfg.AllowedAccounts = map[string][]string{
		OriginExpandKey("github.com/acme/api"): {"first@x.com"},
	}

	h.openAccountPicker()
	for _, r := range h.accountPicker.rows {
		if r.email == "second@x.com" && r.enabled {
			t.Error("the picker offered an account this origin's allowlist excludes")
		}
	}

	// Dimmed rather than dropped: a row that vanishes reads as a missing account,
	// where a disabled one carrying its reason teaches the policy.
	var found bool
	for _, r := range h.accountPicker.rows {
		if r.email == "second@x.com" {
			found = true
			if r.note == "" {
				t.Error("the disallowed row gives no reason")
			}
		}
	}
	if !found {
		t.Error("the disallowed account was hidden instead of dimmed")
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

	// The command is deliberately not run: it calls Restart(), which shells out
	// to tmux and would leave a real session behind. The move and its
	// persistence both happen synchronously inside moveSelectedToAccount, before
	// the command is returned, so the assertions below cover what matters.
	h.Update(accountPickedMsg{email: "second@x.com"})
	if alpha.Account != "second@x.com" {
		t.Errorf("alpha account = %q, want second@x.com — the picker acted on the wrong row", alpha.Account)
	}
	if beta.Account != "first@x.com" {
		t.Errorf("beta was moved to %q; the picker never named it", beta.Account)
	}
}

// The `A` dialog's Account cycler is an assignment path like the move picker,
// so the per-origin allowlist has to hold here too — and for the same reason it
// had to be added to the picker: a mistake bills client work to the wrong
// subscription with nothing on screen to say so.
func TestCreateRowsRefuseAnAccountTheOriginDisallows(t *testing.T) {
	h, s := moveHome(t, map[string]claudeaccount.Usage{
		"first@x.com":  knownUsage(90),
		"second@x.com": knownUsage(5),
	})
	h.writeGitInfo(func(m map[string]*git.RepoInfo) bool {
		m[s.ProjectPath] = &git.RepoInfo{OriginKey: "github.com/acme/api"}
		return true
	})
	h.cfg.AllowedAccounts = map[string][]string{
		OriginExpandKey("github.com/acme/api"): {"first@x.com"},
	}

	rows := h.sessionCreateAccountRows(s.ProjectPath)
	var found bool
	for _, r := range rows {
		if r.email != "second@x.com" {
			continue
		}
		found = true
		if r.enabled {
			t.Error("the create dialog offered an account this origin's allowlist excludes")
		}
		if r.note == "" {
			t.Error("the disallowed option gives no reason")
		}
	}
	if !found {
		t.Error("the disallowed account was dropped from the cycle instead of dimmed")
	}
}

// Below two accounts every session runs on the same credential, so the row
// would be a constant — the rule previewAccountLabel applies to the preview
// footer, kept here so `A` is untouched for everyone who never set up a second
// subscription.
func TestCreateRowsAreSilentBelowTwoAccounts(t *testing.T) {
	h, s := moveHome(t, map[string]claudeaccount.Usage{"first@x.com": knownUsage(10)})
	h.accounts.Remove("second@x.com")
	if rows := h.sessionCreateAccountRows(s.ProjectPath); rows != nil {
		t.Errorf("offered an Account row with %d account(s) configured: %+v", h.accounts.Len(), rows)
	}
}

// The Auto option previews what account_strategy would pick, and resolveAccount
// is what actually picks it. If the two ever build their SelectOpts separately
// they will drift, and a label naming a different account than the one billed is
// exactly the lie the account labelling exists to stop telling.
func TestAutoOptionNamesTheAccountResolveWouldPick(t *testing.T) {
	h, s := moveHome(t, map[string]claudeaccount.Usage{
		"first@x.com":  knownUsage(90),
		"second@x.com": knownUsage(5),
	})

	chosen, blocked := h.resolveAccount(agent.Claude, s.ProjectPath)
	if blocked != "" {
		t.Fatalf("resolveAccount refused: %s", blocked)
	}
	rows := h.sessionCreateAccountRows(s.ProjectPath)
	if len(rows) == 0 {
		t.Fatal("no Account row was offered")
	}
	if rows[0].email != "" {
		t.Errorf("the first option must be Auto (empty email), got %q", rows[0].email)
	}
	if !strings.Contains(rows[0].label, chosen) {
		t.Errorf("Auto reads %q but resolveAccount picks %q", rows[0].label, chosen)
	}
}

// An explicit account skips resolveAccount, and with it the allowlist. The
// dialog refuses one it dimmed, but the policy has to hold at the chokepoint
// too — the same check the CLI makes for an explicit --account.
func TestCreateRefusesAnExplicitAccountTheOriginDisallows(t *testing.T) {
	h, s := moveHome(t, map[string]claudeaccount.Usage{
		"first@x.com":  knownUsage(90),
		"second@x.com": knownUsage(5),
	})
	h.writeGitInfo(func(m map[string]*git.RepoInfo) bool {
		m[s.ProjectPath] = &git.RepoInfo{OriginKey: "github.com/acme/api"}
		return true
	})
	h.cfg.AllowedAccounts = map[string][]string{
		OriginExpandKey("github.com/acme/api"): {"first@x.com"},
	}
	// handleSessionCreate refuses an uninstalled agent before it reaches the
	// account policy, which would pass this test for the wrong reason.
	if _, err := exec.LookPath(agent.Claude.Binary()); err != nil {
		t.Skip("claude is not on PATH; handleSessionCreate returns before the account check")
	}

	before := len(h.sessions)
	_, cmd := h.handleSessionCreate(sessionCreateMsg{
		path: s.ProjectPath, title: "new", agent: agent.Claude, account: "second@x.com",
	})
	if cmd != nil {
		t.Error("a session was started on an account the origin's allowlist excludes")
	}
	if len(h.sessions) != before {
		t.Errorf("session list grew from %d to %d", before, len(h.sessions))
	}
	if h.err == nil {
		t.Error("the refusal was silent — nothing on screen says why nothing happened")
	} else if !strings.Contains(h.err.Error(), "allowed_accounts") {
		t.Errorf("the error must name the policy that refused, got %q", h.err)
	}
}
