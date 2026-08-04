package ui

import (
	"strings"
	"testing"

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
	if !strings.Contains(d.View(), "doesn't reveal") {
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
