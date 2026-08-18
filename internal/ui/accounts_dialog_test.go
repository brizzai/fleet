package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
)

func dlgAccounts(emails ...string) []claudeaccount.Account {
	out := make([]claudeaccount.Account, len(emails))
	for i, e := range emails {
		out[i] = claudeaccount.Account{Email: e, Order: i}
	}
	return out
}

// The account was saved and the dialog still said "Waiting for the login…" —
// Refresh updated the data but left the in-flight mode set, so a *successful*
// add looked like a hang.
func TestRefreshClearsTheInFlightState(t *testing.T) {
	d := NewAccountsDialog()
	// Without a size, lipgloss.Place clips View() to nothing and every
	// assertion on its text passes vacuously.
	d.SetSize(120, 40)
	d.Show(nil, nil, "", claudeaccount.StrategyLeastUsedWeekly)
	d.SetBusy("Waiting for the login…")

	d.Refresh(dlgAccounts("a@x.com"), nil, "")

	if d.mode == accountsWaitingLogin {
		t.Fatal("dialog still in the waiting state after a completed operation")
	}
	if strings.Contains(d.View(), "Waiting for the browser login") {
		t.Error("view still shows the in-flight message after refresh")
	}
}

func TestAddedDoesNotPromptWhenNamed(t *testing.T) {
	d := NewAccountsDialog()
	// Without a size, lipgloss.Place clips View() to nothing and every
	// assertion on its text passes vacuously.
	d.SetSize(120, 40)
	d.Show(nil, nil, "", claudeaccount.StrategyLeastUsedWeekly)
	d.SetBusy("Waiting for the login…")
	d.Refresh(dlgAccounts("real@x.com"), nil, "")

	d.Added("real@x.com")

	if d.mode != accountsList {
		t.Fatalf("mode = %v, want the list — an already-named account needs nothing", d.mode)
	}
}

// The box is a fixed width and nothing inside it may wrap. Getting the content
// width wrong by two columns split the separator, the rows and the footer all
// at once — visible as a stray "—" and a "close" on its own line.
func TestDialogNeverWraps(t *testing.T) {
	long := claudeaccount.Account{
		Email: "a-really-quite-long-account-address@some-long-domain.example.com",
		Plan:  "max",
		Order: 0,
	}
	fp := claudeaccount.Account{Email: "work@x.com", Order: 1}

	// Every mode, at terminal widths from cramped to roomy.
	modes := []struct {
		name  string
		setup func(d *AccountsDialog)
	}{
		{"empty", func(d *AccountsDialog) { d.Refresh(nil, nil, "") }},
		{"list", func(d *AccountsDialog) {
			d.Refresh([]claudeaccount.Account{long, fp}, nil, "")
		}},
		{"list with quota", func(d *AccountsDialog) {
			d.Refresh([]claudeaccount.Account{long, fp}, map[string]claudeaccount.Usage{
				long.Email: {FiveHourPct: 42, FiveHourReset: hdrNow.Add(time.Hour), FetchedAt: hdrNow},
				fp.Email:   {Err: claudeaccount.ErrNoCredential, AttemptedAt: hdrNow},
			}, "")
		}},
		{"manual default", func(d *AccountsDialog) {
			d.Show([]claudeaccount.Account{long, fp}, nil, long.Email, claudeaccount.StrategyManual)
		}},
		{"waiting for login", func(d *AccountsDialog) {
			d.Refresh([]claudeaccount.Account{long}, nil, "")
			d.SetBusy("Waiting for the login…")
		}},
		{"rename", func(d *AccountsDialog) {
			// Set the mode explicitly: Added no longer opens the rename box (the
			// login reports its own email), so going through it would render the
			// list view and leave the rename layout unchecked at every width.
			d.Refresh([]claudeaccount.Account{fp}, nil, "")
			d.beginInput(accountsRename, fp.Label, namePlaceholder)
		}},
		{"confirm remove", func(d *AccountsDialog) {
			d.Refresh([]claudeaccount.Account{long}, nil, "")
			d.mode = accountsConfirmRm
		}},
		{"error", func(d *AccountsDialog) {
			d.Refresh(nil, nil, "")
			d.SetError(errors.New("no account was logged in — run /login in the pane and wait"))
		}},
	}

	for _, m := range modes {
		for _, termW := range []int{50, 80, 140} {
			d := NewAccountsDialog()
			d.SetSize(termW, 40)
			d.Show(nil, nil, "", claudeaccount.StrategyLeastUsedWeekly)
			m.setup(d)

			boxW := min(accountsDialogWidth, max(40, termW-4))
			for _, line := range strings.Split(d.View(), "\n") {
				if w := lipgloss.Width(line); w > termW {
					t.Errorf("%s @ term %d: line is %d cols, wider than the terminal:\n%s",
						m.name, termW, w, line)
					break
				}
			}
			// Every box line is exactly boxW: a wrapped interior would produce
			// a short line where the right border should be.
			var boxLines int
			for _, line := range strings.Split(d.View(), "\n") {
				if strings.ContainsAny(line, "╭│╰") {
					boxLines++
					if w := lipgloss.Width(strings.TrimRight(line, " ")); w != (termW-boxW)/2+boxW {
						t.Errorf("%s @ term %d: ragged box line (%d cols):\n%q", m.name, termW, w, line)
						break
					}
				}
			}
			if boxLines == 0 {
				t.Errorf("%s @ term %d: no box rendered", m.name, termW)
			}
		}
	}
}

// Quota now reaches every account via the header probe, so the row shows a
// real number — and a *failure* to read one is a genuine fault worth saying,
// not the expected silence it used to be.
func TestQuotaRowShowsTheNumberAndNamesTheGap(t *testing.T) {
	d := NewAccountsDialog()
	d.SetSize(120, 40)
	d.Show(nil, nil, "", claudeaccount.StrategyLeastUsedWeekly)

	fp := claudeaccount.Account{Email: "work@x.com", Order: 0}
	ok := claudeaccount.Account{Email: "real@x.com", Order: 1}
	d.Refresh([]claudeaccount.Account{fp, ok}, map[string]claudeaccount.Usage{
		// Its poll failed — no reading, but the login is not in question.
		fp.Email: {Err: claudeaccount.ErrNoCredential, AttemptedAt: hdrNow},
		// Named, with a live reading.
		ok.Email: {FiveHourPct: 42, FiveHourReset: hdrNow.Add(time.Hour), FetchedAt: hdrNow},
	}, "")

	view := d.View()
	if !strings.Contains(view, "42%") {
		t.Errorf("a polled account does not show its utilization:\n%s", view)
	}
	if !strings.Contains(view, "quota unavailable") {
		t.Errorf("a failed poll is silent; it should say so now that quota is expected:\n%s", view)
	}
}

// A session whose account was removed is not running on that account — fleet
// sets no config dir for an unknown key, so it falls back to the ambient
// login. The
// label has to say what is true now, or it reads as "running on a dead
// account", which is both alarming and wrong.
func TestRemovedAccountLabelNamesTheFallback(t *testing.T) {
	h := &Home{accounts: &claudeaccount.Store{}}

	got := h.accountLabel("gone@x.com")
	if !strings.Contains(got, "logged-in account") {
		t.Errorf("label = %q, want it to name the ambient login it actually uses", got)
	}
	if !strings.Contains(got, "removed") {
		t.Errorf("label = %q, want it to explain why", got)
	}

	// An empty key is the same destination, reached without a removal.
	if got := h.accountLabel(""); got != "your logged-in account" {
		t.Errorf("unset account label = %q", got)
	}
}

// The strategy is stated above the list because it governs the list — and
// because it is what makes the ★ legible. Without it, a marker that appears
// only under one mode reads as a property of the account, not of the mode.
func TestAccountsDialogShowsTheStrategy(t *testing.T) {
	d := NewAccountsDialog()
	d.SetSize(120, 40)
	d.Show(accountsFixture(), nil, "", claudeaccount.StrategyLeastUsedWeekly)

	if got := d.View(); !strings.Contains(got, "Least used · weekly") {
		t.Errorf("the dialog does not name the strategy in force:\n%s", got)
	}
	if got := d.View(); !strings.Contains(got, "s strategy") {
		t.Error("the footer does not advertise the key that changes it")
	}
}

// s cycles forward through every offered mode and wraps, and the dialog updates
// its own copy on the same frame so the row repaints with the keypress rather
// than a round-trip later.
func TestAccountsDialogCyclesStrategy(t *testing.T) {
	d := NewAccountsDialog()
	d.SetSize(120, 40)
	d.Show(accountsFixture(), nil, "", claudeaccount.Strategies[0])

	for i := 1; i <= len(claudeaccount.Strategies); i++ {
		want := claudeaccount.Strategies[i%len(claudeaccount.Strategies)]
		var cmd tea.Cmd
		d, cmd = d.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
		if cmd == nil {
			t.Fatalf("step %d: s produced no message", i)
		}
		msg, ok := cmd().(accountStrategyMsg)
		if !ok {
			t.Fatalf("step %d: got %T, want accountStrategyMsg", i, cmd())
		}
		if msg.strategy != want {
			t.Errorf("step %d: cycled to %q, want %q", i, msg.strategy, want)
		}
		if d.strategy != want {
			t.Errorf("step %d: dialog shows %q but emitted %q — the row lags the keypress", i, d.strategy, want)
		}
	}
}

// ⏎ default and the ★ are advertised only under the manual strategy, since
// that is the only mode that reads the pin.
func TestAccountsDialogDefaultHintFollowsStrategy(t *testing.T) {
	d := NewAccountsDialog()
	d.SetSize(120, 40)

	d.Show(accountsFixture(), nil, "", claudeaccount.StrategyLeastUsedWeekly)
	if strings.Contains(strings.Join(d.footer(60), " "), "default") {
		t.Error("offered ⏎ default under a strategy that ignores it")
	}

	d.Show(accountsFixture(), nil, "", claudeaccount.StrategyManual)
	if !strings.Contains(strings.Join(d.footer(60), " "), "default") {
		t.Error("hid ⏎ default under the one strategy that reads it")
	}
}

// Manual with nothing pinned is a mode that silently does nothing — Select
// falls through to the automatic modes — so the dialog has to know to ask for
// the second keystroke.
func TestManualNeedsDefaultDetectsAnUnpinnedManual(t *testing.T) {
	d := NewAccountsDialog()
	accts := accountsFixture()

	d.Show(accts, nil, "", claudeaccount.StrategyManual)
	if !d.manualNeedsDefault() {
		t.Error("manual with no pin did not ask for one")
	}
	d.Show(accts, nil, accts[0].Email, claudeaccount.StrategyManual)
	if d.manualNeedsDefault() {
		t.Error("asked for a pin that is already set")
	}
	// A pin naming an account that has since been removed is not a pin.
	d.Show(accts, nil, "gone@example.com", claudeaccount.StrategyManual)
	if !d.manualNeedsDefault() {
		t.Error("a pin naming no configured account counted as pinned")
	}
	d.Show(accts, nil, "", claudeaccount.StrategyLeastUsedWeekly)
	if d.manualNeedsDefault() {
		t.Error("asked for a pin under an automatic strategy")
	}
}

// The paste-a-token login path was removed with the config-dir rewrite; the
// footer hint outlived it, advertising a key that did nothing.
func TestAccountsFooterAdvertisesNoDeadKeys(t *testing.T) {
	d := NewAccountsDialog()
	d.Show(accountsFixture(), nil, "", claudeaccount.StrategyLeastUsedWeekly)
	if strings.Contains(strings.Join(d.footer(80), " "), "paste") {
		t.Error("the footer still advertises the removed paste flow")
	}
}

func accountsFixture() []claudeaccount.Account {
	return []claudeaccount.Account{
		{Email: "first@x.com", ConfigDir: "/d/1"},
		{Email: "second@x.com", ConfigDir: "/d/2"},
	}
}

// The test that would have caught the shipped bug, and the reason it exists in
// this shape: every other test here calls Select directly or hands Show an
// already-normalized literal, so nothing crossed the config.Config boundary —
// which is exactly where a getter was flattening three strategies into one.
//
// So this drives the Settings cycler the way the dialog does, writing back to
// config each step. With the flattening getter it visited least_used_5h forever
// and never reached Waterfall or Manual.
func TestSettingsCyclerVisitsEveryStrategy(t *testing.T) {
	for _, dir := range []int{1, -1} {
		cfg := &config.Config{}
		seen := map[string]bool{}
		for i := 0; i < len(accountStrategySet); i++ {
			cfg.AccountStrategy = cycleString(cfg.GetAccountStrategy(), accountStrategySet, dir)
			seen[cfg.GetAccountStrategy()] = true
		}
		for _, s := range accountStrategySet {
			if !seen[s] {
				t.Errorf("dir=%d: cycling never reached %q (visited %d of %d)", dir, s, len(seen), len(accountStrategySet))
			}
		}
		// A full cycle must return to where it started, or the picker drifts.
		if got := cfg.GetAccountStrategy(); got != claudeaccount.Strategies[0] {
			t.Errorf("dir=%d: a full cycle ended on %q, want %q", dir, got, claudeaccount.Strategies[0])
		}
	}
}

// The label is read off config, so a getter that loses the value makes the row
// look frozen: the key writes something the label never reflects.
func TestSettingsLabelFollowsTheStoredStrategy(t *testing.T) {
	for _, s := range claudeaccount.Strategies {
		cfg := &config.Config{AccountStrategy: s}
		if got, want := claudeaccount.StrategyLabel(cfg.GetAccountStrategy()), claudeaccount.StrategyLabel(s); got != want {
			t.Errorf("stored %q renders %q, want %q", s, got, want)
		}
	}
}

// And the dialog agrees, since it is seeded from the same getter — with the old
// one, 5-hour on disk opened the dialog reading "weekly" and the first s
// re-saved the value already in force, so the keypress appeared to do nothing.
func TestAccountsDialogOpensOnTheStoredStrategy(t *testing.T) {
	for _, s := range claudeaccount.Strategies {
		cfg := &config.Config{AccountStrategy: s}
		d := NewAccountsDialog()
		d.SetSize(120, 40)
		d.Show(accountsFixture(), nil, "", cfg.GetAccountStrategy())
		if d.strategy != s {
			t.Errorf("stored %q, dialog opened on %q", s, d.strategy)
		}
	}
}
