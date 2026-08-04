package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/claudeaccount"
)

func dlgAccounts(emails ...string) []claudeaccount.Account {
	out := make([]claudeaccount.Account, len(emails))
	for i, e := range emails {
		out[i] = claudeaccount.Account{Email: e, Order: i}
	}
	return out
}

// The account was saved and the dialog still said "Checking the token…" —
// Refresh updated the data but left the in-flight mode set, so a *successful*
// add looked like a hang.
func TestRefreshClearsTheInFlightState(t *testing.T) {
	d := NewAccountsDialog()
	// Without a size, lipgloss.Place clips View() to nothing and every
	// assertion on its text passes vacuously.
	d.SetSize(120, 40)
	d.Show(nil, nil, "", false)
	d.SetBusy("Checking the token…")

	d.Refresh(dlgAccounts("a@x.com"), nil, "")

	if d.mode == accountsWaitingToken {
		t.Fatal("dialog still in the waiting state after a completed operation")
	}
	if strings.Contains(d.View(), "Checking the token") {
		t.Error("view still shows the in-flight message after refresh")
	}
}

// A token the API won't identify is keyed by a hash, so the name has to be
// asked for while the user still knows which account they just logged into.
func TestAddedPromptsForANameWhenUnidentified(t *testing.T) {
	d := NewAccountsDialog()
	// Without a size, lipgloss.Place clips View() to nothing and every
	// assertion on its text passes vacuously.
	d.SetSize(120, 40)
	d.Show(nil, nil, "", false)
	d.SetBusy("Checking the token…")
	d.Refresh(dlgAccounts("other@x.com", claudeaccount.FingerprintPrefix+"7ee6c0f8"), nil, "")

	d.Added(claudeaccount.FingerprintPrefix+"7ee6c0f8", true)

	if d.mode != accountsRename {
		t.Fatalf("mode = %v, want the rename prompt", d.mode)
	}
	if got := d.selectedEmail(); got != claudeaccount.FingerprintPrefix+"7ee6c0f8" {
		t.Errorf("cursor landed on %q, want the account just added", got)
	}
	// The box must explain itself, or it reads as a pointless chore.
	if !strings.Contains(d.View(), "won't say which account") {
		t.Errorf("rename prompt does not explain why a name is needed:\n%s", d.View())
	}
}

func TestAddedDoesNotPromptWhenNamed(t *testing.T) {
	d := NewAccountsDialog()
	// Without a size, lipgloss.Place clips View() to nothing and every
	// assertion on its text passes vacuously.
	d.SetSize(120, 40)
	d.Show(nil, nil, "", false)
	d.SetBusy("Checking the token…")
	d.Refresh(dlgAccounts("real@x.com"), nil, "")

	d.Added("real@x.com", false)

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
	fp := claudeaccount.Account{Email: claudeaccount.FingerprintPrefix + "7ee6c0f8", Order: 1}

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
				fp.Email:   {Err: claudeaccount.ErrNoQuotaHeaders, AttemptedAt: hdrNow},
			}, "")
		}},
		{"manual default", func(d *AccountsDialog) {
			d.Show([]claudeaccount.Account{long, fp}, nil, long.Email, true)
		}},
		{"paste", func(d *AccountsDialog) {
			d.Refresh([]claudeaccount.Account{long}, nil, "")
			d.mode = accountsPaste
		}},
		{"rename unnamed", func(d *AccountsDialog) {
			d.Refresh([]claudeaccount.Account{fp}, nil, "")
			d.Added(fp.Email, true)
		}},
		{"confirm remove", func(d *AccountsDialog) {
			d.Refresh([]claudeaccount.Account{long}, nil, "")
			d.mode = accountsConfirmRm
		}},
		{"error", func(d *AccountsDialog) {
			d.Refresh(nil, nil, "")
			d.SetError(errors.New("token was rejected — generate a fresh one with `claude setup-token`"), false)
		}},
	}

	for _, m := range modes {
		for _, termW := range []int{50, 80, 140} {
			d := NewAccountsDialog()
			d.SetSize(termW, 40)
			d.Show(nil, nil, "", false)
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
	d.Show(nil, nil, "", false)

	fp := claudeaccount.Account{Email: claudeaccount.FingerprintPrefix + "7ee6c0f8", Order: 0}
	ok := claudeaccount.Account{Email: "real@x.com", Order: 1}
	d.Refresh([]claudeaccount.Account{fp, ok}, map[string]claudeaccount.Usage{
		// Unnamed, and its poll failed.
		fp.Email: {Err: claudeaccount.ErrNoQuotaHeaders, AttemptedAt: hdrNow},
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
	// The unnamed row still points at its own fix.
	if !strings.Contains(view, "r to name") {
		t.Errorf("unnamed account gives no hint that it can be named:\n%s", view)
	}
}

// One text box serves both modes, so its placeholder has to be set on entry —
// otherwise the rename prompt asks for a name while offering a token as the
// example of what to type.
func TestInputPlaceholderMatchesTheMode(t *testing.T) {
	fp := claudeaccount.Account{Email: claudeaccount.FingerprintPrefix + "7ee6c0f8"}

	d := NewAccountsDialog()
	d.SetSize(120, 40)
	d.Show([]claudeaccount.Account{fp}, nil, "", false)

	d.Added(fp.Email, true)
	if got := d.input.Placeholder; got != namePlaceholder {
		t.Errorf("rename placeholder = %q, want the name example", got)
	}
	if strings.Contains(d.View(), "sk-ant") {
		t.Errorf("rename prompt offers a token as the example:\n%s", d.View())
	}

	// And back the other way.
	d.SetError(errors.New("capture missed"), true)
	if got := d.input.Placeholder; got != tokenPlaceholder {
		t.Errorf("paste placeholder = %q, want the token example", got)
	}
}

// A session whose account was removed is not running on that account — fleet
// sets no token for an unknown key, so it falls back to the ambient login. The
// label has to say what is true now, or it reads as "running on a dead
// account", which is both alarming and wrong.
func TestRemovedAccountLabelNamesTheFallback(t *testing.T) {
	h := &Home{accounts: &claudeaccount.Store{}}

	got := h.accountLabel(claudeaccount.FingerprintPrefix + "4666551e")
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

func TestNeedsLabel(t *testing.T) {
	fp := claudeaccount.FingerprintPrefix + "abc12345"
	if !(claudeaccount.Account{Email: fp}).NeedsLabel() {
		t.Error("a fingerprint-keyed account with no label needs one")
	}
	if (claudeaccount.Account{Email: fp, Label: "work"}).NeedsLabel() {
		t.Error("a labelled account needs nothing")
	}
	if (claudeaccount.Account{Email: "real@x.com"}).NeedsLabel() {
		t.Error("an email-keyed account is already meaningful")
	}
}
