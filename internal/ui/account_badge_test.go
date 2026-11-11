package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/x/ansi"
)

func TestAccountBadge(t *testing.T) {
	h, _ := moveHome(t, nil)
	h.accounts.Upsert(claudeaccount.Account{Email: "breno@x.com", ConfigDir: "/d/3", Label: "work"})

	mk := func(ag agent.Type, acct string) *session.Session {
		s := session.NewSession("t", "/tmp/r")
		s.Agent, s.Account = ag, acct
		return s
	}
	tests := []struct {
		name string
		s    *session.Session
		want string
	}{
		{"label wins", mk(agent.Claude, "breno@x.com"), "work"},
		{"email local part", mk(agent.Claude, "first@x.com"), "first"},
		{"no pin is ambient", mk(agent.Claude, ""), "ambient"},
		{"removed account is ambient", mk(agent.Claude, "gone@x.com"), "ambient"},
		{"legacy empty agent is claude", mk("", "first@x.com"), "first"},
		{"codex has no badge", mk(agent.Codex, "first@x.com"), ""},
		{"nil session", nil, ""},
	}
	for _, tc := range tests {
		if got, _ := h.accountBadge(tc.s); got != tc.want {
			t.Errorf("%s: badge = %q, want %q", tc.name, got, tc.want)
		}
	}

	// One account (or none): every session shares a credential, so silence.
	h.accounts = &claudeaccount.Store{}
	h.accounts.Upsert(claudeaccount.Account{Email: "only@x.com", ConfigDir: "/d/1"})
	if got, _ := h.accountBadge(mk(agent.Claude, "only@x.com")); got != "" {
		t.Errorf("single account badge = %q, want none", got)
	}
}

func TestRenderSessionItem_AccountBadge(t *testing.T) {
	s := session.NewSession("a rather long session title for the sidebar", "/tmp/r")
	s.Agent = agent.Claude
	for _, selected := range []bool{false, true} {
		for _, width := range []int{24, 30, 40, 60} {
			out := renderSessionItem(SidebarItem{Session: s, AccountBadge: "breno"}, width, selected, 3)
			if w := ansi.StringWidth(out); w > width {
				t.Errorf("selected=%v width=%d: row is %d cells wide: %q", selected, width, w, out)
			}
		}
		out := renderSessionItem(SidebarItem{Session: s, AccountBadge: "breno"}, 40, selected, -1)
		if !strings.Contains(ansi.Strip(out), "breno") {
			t.Errorf("selected=%v: badge missing: %q", selected, out)
		}
		// Right-aligned: the badge ends the row.
		if plain := strings.TrimRight(ansi.Strip(out), " "); !strings.HasSuffix(plain, "breno") {
			t.Errorf("selected=%v: badge not at the right edge: %q", selected, plain)
		}
		out = renderSessionItem(SidebarItem{Session: s}, 40, selected, -1)
		if strings.Contains(ansi.Strip(out), "breno") {
			t.Errorf("selected=%v: badge shown with none set: %q", selected, out)
		}
	}
}

// Moving restarts the process, so a busy session must be asked about first and
// an idle one must not be.
func TestMoveToAccountConfirmsOnlyWhenBusy(t *testing.T) {
	tests := []struct {
		name        string
		status      session.Status
		wantConfirm bool
	}{
		{"running", session.StatusRunning, true},
		{"waiting on the user", session.StatusWaiting, true},
		{"starting", session.StatusStarting, true},
		{"idle", session.StatusIdle, false},
		{"finished", session.StatusFinished, false},
		{"error", session.StatusError, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, s := moveHome(t, nil)
			s.SetStatus(tc.status)

			cmd := h.requestMoveToAccount("second@x.com")
			if tc.wantConfirm {
				if cmd != nil || s.Account != "first@x.com" {
					t.Fatalf("busy session moved without asking (account=%q)", s.Account)
				}
				if !h.confirmDialog.IsVisible() {
					t.Fatal("no confirmation shown for a busy session")
				}
				// Confirming performs the move.
				if _, cmd := h.Update(accountMoveConfirmedMsg{id: s.ID, email: "second@x.com"}); cmd == nil || s.Account != "second@x.com" {
					t.Fatalf("confirmed move did not happen (account=%q)", s.Account)
				}
				return
			}
			if cmd == nil || s.Account != "second@x.com" {
				t.Fatalf("idle session was not moved (account=%q)", s.Account)
			}
			if h.confirmDialog.IsVisible() {
				t.Fatal("idle session asked for confirmation")
			}
		})
	}
}

func TestMoveConfirmedForAnotherSessionIsRefused(t *testing.T) {
	h, s := moveHome(t, nil)
	h.Update(accountMoveConfirmedMsg{id: "someone-else", email: "second@x.com"})
	if s.Account != "first@x.com" {
		t.Fatalf("a confirm for a different session moved this one to %q", s.Account)
	}
}

func TestAtKeyRefusesWithAToastWhenThereIsNothingToMove(t *testing.T) {
	h, s := moveHome(t, nil)
	s.Agent = agent.Codex
	h.openAccountPickerForKey()
	if h.accountPicker.IsVisible() {
		t.Fatal("picker opened for a non-Claude session")
	}
	s.Agent = agent.Claude
	h.openAccountPickerForKey()
	if !h.accountPicker.IsVisible() {
		t.Fatal("picker did not open for a Claude session with two accounts")
	}
}

func TestAccountsDialogShowsLoginMismatch(t *testing.T) {
	d := NewAccountsDialog()
	d.SetSize(120, 40)
	d.Show(accountsFixture(), map[string]claudeaccount.Usage{
		"first@x.com": {Mismatch: "someone@else.com"},
	}, "", claudeaccount.StrategyLeastUsedWeekly)
	view := ansi.Strip(d.View())
	for _, want := range []string{"login mismatch", "someone@else.com"} {
		if !strings.Contains(view, want) {
			t.Errorf("dialog missing %q:\n%s", want, view)
		}
	}
}

func TestMismatchedAccountIsNotOfferedInThePicker(t *testing.T) {
	h, _ := moveHome(t, map[string]claudeaccount.Usage{"second@x.com": {Mismatch: "x@y.com"}})
	h.openAccountPicker()
	if !h.accountPicker.IsVisible() {
		t.Fatal("picker not shown")
	}
	for _, r := range h.accountPicker.rows {
		if r.email == "second@x.com" && (r.enabled || r.note != "login mismatch") {
			t.Errorf("mismatched row = %+v, want disabled with a note", r)
		}
	}
}

func TestAmbientIdentityLabelsAndBadges(t *testing.T) {
	h, _ := moveHome(t, nil)
	h.accounts.Upsert(claudeaccount.Account{Email: "me@x.com", ConfigDir: "/d/3", Label: "work"})
	amb := session.NewSession("t", "/tmp/r")
	amb.Agent = agent.Claude

	tests := []struct {
		name      string
		ambient   string
		wantLabel string
		wantBadge string
	}{
		{"unresolved", "", "your logged-in account", "ambient"},
		{"unregistered mailbox", "stranger@y.com", "ambient · stranger@y.com", "stranger"},
		{"registered mailbox uses that account's name", "ME@x.com", "work", "work"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h.ambientMu.Lock()
			h.ambientEmail = tc.ambient
			h.ambientMu.Unlock()
			if got := h.accountLabel(""); got != tc.wantLabel {
				t.Errorf("label = %q, want %q", got, tc.wantLabel)
			}
			if got, _ := h.accountBadge(amb); got != tc.wantBadge {
				t.Errorf("badge = %q, want %q", got, tc.wantBadge)
			}
		})
	}
}

func TestAccountsDialogLReloginsTheHighlightedAccount(t *testing.T) {
	d := NewAccountsDialog()
	d.Show(accountsFixture(), nil, "", claudeaccount.StrategyLeastUsedWeekly)
	d.cursor = 1
	_, cmd := d.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if cmd == nil {
		t.Fatal("l produced no command")
	}
	msg, ok := cmd().(accountLoginMsg)
	if !ok || msg.relogin != "second@x.com" {
		t.Fatalf("msg = %#v, want a relogin of second@x.com", cmd())
	}
	// Nothing highlighted, nothing to log in.
	d.Show(nil, nil, "", claudeaccount.StrategyLeastUsedWeekly)
	if _, cmd := d.Update(tea.KeyPressMsg{Code: 'l', Text: "l"}); cmd != nil {
		t.Error("l with no accounts produced a command")
	}
}

func TestAccountsDialogFooterShowsRealEmailAndFolder(t *testing.T) {
	d := NewAccountsDialog()
	d.SetSize(120, 40)
	d.Show(accountsFixture(), map[string]claudeaccount.Usage{"first@x.com": {Mismatch: "real@y.com"}}, "", claudeaccount.StrategyLeastUsedWeekly)
	view := ansi.Strip(d.View())
	for _, want := range []string{"Logged in as real@y.com (registered as first@x.com)", "Folder /d/1", "l re-login"} {
		if !strings.Contains(view, want) {
			t.Errorf("dialog missing %q:\n%s", want, view)
		}
	}
}

func TestReloginWrongLogin(t *testing.T) {
	tests := []struct {
		name string
		msg  accountLoggedInMsg
		want string
	}{
		{"plain add", accountLoggedInMsg{account: claudeaccount.Account{Email: "a@x.com"}}, ""},
		{"relogin as the same person", accountLoggedInMsg{account: claudeaccount.Account{Email: "A@x.com"}, relogin: "a@x.com"}, ""},
		{"relogin as someone else", accountLoggedInMsg{account: claudeaccount.Account{Email: "b@x.com"}, relogin: "a@x.com"}, "b@x.com"},
	}
	for _, tc := range tests {
		if got := reloginWrongLogin(tc.msg); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Landing on the wrong person must not rewrite the account the user was fixing.
func TestReloginAsSomeoneElseKeepsTheAccountAndFlagsIt(t *testing.T) {
	h, _ := moveHome(t, nil)
	h.Update(accountLoggedInMsg{
		account: claudeaccount.Account{Email: "intruder@x.com", ConfigDir: "/d/1"},
		relogin: "first@x.com",
	})
	if _, ok := h.accounts.Get("first@x.com"); !ok || h.accounts.Len() != 2 {
		t.Fatalf("account set changed: len=%d", h.accounts.Len())
	}
	if got := h.accountUsageSnapshot()["first@x.com"].Mismatch; got != "intruder@x.com" {
		t.Errorf("mismatch = %q, want intruder@x.com", got)
	}
}

func TestReloginOfAnUnknownAccountFailsWithoutALoginPane(t *testing.T) {
	h, _ := moveHome(t, nil)
	msg, ok := h.runAccountLogin("gone@x.com")().(accountLoggedInMsg)
	if !ok || msg.err == nil || msg.relogin != "gone@x.com" {
		t.Fatalf("msg = %#v, want an error carrying the relogin key", msg)
	}
}

func TestAccountsDialogStarShowsOutsideManualWithANote(t *testing.T) {
	tests := []struct {
		name     string
		strategy string
		def      string
		wantStar bool
		wantNote bool
	}{
		{"manual", claudeaccount.StrategyManual, "first@x.com", true, false},
		{"automatic with a default", claudeaccount.StrategyLeastUsedWeekly, "first@x.com", true, true},
		{"automatic, no default", claudeaccount.StrategyLeastUsedWeekly, "", false, false},
		{"default names a removed account", claudeaccount.StrategyLeastUsedWeekly, "gone@x.com", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := NewAccountsDialog()
			d.SetSize(120, 40)
			d.Show(accountsFixture(), nil, tc.def, tc.strategy)
			view := ansi.Strip(d.View())
			if got := strings.Contains(view, "★ first"); got != tc.wantStar {
				t.Errorf("star on the row = %v, want %v:\n%s", got, tc.wantStar, view)
			}
			if got := strings.Contains(view, "used only in manual"); got != tc.wantNote {
				t.Errorf("manual-only note = %v, want %v:\n%s", got, tc.wantNote, view)
			}
		})
	}
}

func TestSetDefaultAccountSwitchesToManualAndSaysSo(t *testing.T) {
	h, _ := moveHome(t, nil)
	h.cfg.AccountStrategy = claudeaccount.StrategyLeastUsedWeekly
	t.Setenv("HOME", t.TempDir())

	h.Update(accountDefaultPickedMsg{email: "second@x.com"})
	if h.cfg.DefaultAccount != "second@x.com" {
		t.Errorf("default = %q, want second@x.com", h.cfg.DefaultAccount)
	}
	if got := h.cfg.GetAccountStrategy(); got != claudeaccount.StrategyManual {
		t.Errorf("strategy = %q, want manual", got)
	}

	// Unknown account: nothing changes.
	h.cfg.DefaultAccount = "first@x.com"
	h.Update(accountDefaultPickedMsg{email: "gone@x.com"})
	if h.cfg.DefaultAccount != "first@x.com" {
		t.Errorf("default moved to an account that does not exist: %q", h.cfg.DefaultAccount)
	}
}

func TestDefaultPickerGreysOutCurrentAndBrokenAccounts(t *testing.T) {
	h, _ := moveHome(t, map[string]claudeaccount.Usage{"second@x.com": {Mismatch: "x@y.com"}})
	h.cfg.DefaultAccount = "first@x.com"
	h.openDefaultAccountPicker()
	if !h.accountPicker.IsVisible() {
		t.Fatal("picker not shown")
	}
	for _, r := range h.accountPicker.rows {
		if r.enabled {
			t.Errorf("row %q should be inert (default / mismatch), got enabled", r.email)
		}
	}
}
