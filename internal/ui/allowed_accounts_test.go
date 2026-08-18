package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/git"
)

// allowedHome puts the cursor on an origin header for a fleet with two accounts,
// which is the shape every case below varies.
func allowedHome(t *testing.T, usage map[string]claudeaccount.Usage) *Home {
	t.Helper()
	h, s := moveHome(t, usage)
	h.writeGitInfo(func(m map[string]*git.RepoInfo) bool {
		m[s.ProjectPath] = &git.RepoInfo{OriginKey: "github.com/acme/api"}
		return true
	})
	h.flatItems = []SidebarItem{{
		IsOriginHeader: true,
		OriginKey:      "github.com/acme/api",
		OriginLabel:    "api",
		Expanded:       true,
	}}
	h.cursor = 0
	// The real app sizes every dialog from the startup WindowSizeMsg; without it
	// the box budgets against width 0 and every line truncates to one column.
	h.allowedAccounts.SetSize(h.width, h.height)
	return h
}

// press feeds one key to the open dialog and runs whatever command it returns,
// so a test exercises the same path Update does.
func press(t *testing.T, h *Home, key string) tea.Msg {
	t.Helper()
	d, cmd := h.allowedAccounts.Update(tea.KeyPressMsg{Code: []rune(key)[0], Text: key})
	h.allowedAccounts = d
	if cmd == nil {
		return nil
	}
	return cmd()
}

// An unrestricted origin opens with everything ticked, because that is what
// unrestricted is. Opening on a blank slate would misreport the live state as
// the one state the dialog refuses to save.
func TestUnrestrictedOriginOpensFullyTicked(t *testing.T) {
	h := allowedHome(t, map[string]claudeaccount.Usage{})
	h.openAllowedAccounts()

	if !h.allowedAccounts.IsVisible() {
		t.Fatal("the dialog did not open on an origin header")
	}
	if got, want := h.allowedAccounts.selectedCount(), 2; got != want {
		t.Errorf("ticked %d of 2 rows on an unrestricted origin, want %d", got, want)
	}
}

// The trap this feature is most likely to fall into: storing the full list when
// everything is ticked. It reads identically today and silently locks out the
// next account added, with nothing on screen to explain it.
func TestSavingEveryAccountStoresNoRestriction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := allowedHome(t, map[string]claudeaccount.Usage{})
	h.cfg.AllowedAccounts = map[string][]string{
		OriginExpandKey("github.com/acme/api"): {"first@x.com"},
	}
	h.openAllowedAccounts()

	press(t, h, "a") // tick all
	msg := press(t, h, "enter")

	set, ok := msg.(allowedAccountsSetMsg)
	if !ok {
		t.Fatalf("enter produced %T, want allowedAccountsSetMsg", msg)
	}
	if len(set.emails) != 0 {
		t.Errorf("saving every account stored %v, want no restriction", set.emails)
	}

	if _, cmd := h.Update(set); cmd != nil {
		t.Fatal("the handler returned a command it has no work for")
	}
	if _, still := h.cfg.AllowedAccounts[OriginExpandKey("github.com/acme/api")]; still {
		t.Error("the origin kept an allowlist entry after being set to all accounts")
	}
	// Round-trip through the resolver every assignment path actually uses.
	if got := h.allowedAccountsFor("/tmp/move-e2e"); len(got) != 0 {
		t.Errorf("allowedAccountsFor = %v, want unrestricted", got)
	}
}

// The other half: a genuine restriction is stored, and stored in row order so
// the config file diffs cleanly rather than recording click order.
func TestSavingSomeAccountsStoresThem(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := allowedHome(t, map[string]claudeaccount.Usage{})
	h.openAllowedAccounts()

	press(t, h, "j") // move to second@x.com
	press(t, h, " ") // untick it
	msg := press(t, h, "enter")

	set, ok := msg.(allowedAccountsSetMsg)
	if !ok {
		t.Fatalf("enter produced %T, want allowedAccountsSetMsg", msg)
	}
	if len(set.emails) != 1 || set.emails[0] != "first@x.com" {
		t.Fatalf("stored %v, want just first@x.com", set.emails)
	}

	h.Update(set)
	got := h.allowedAccountsFor("/tmp/move-e2e")
	if len(got) != 1 || got[0] != "first@x.com" {
		t.Errorf("allowedAccountsFor = %v, want [first@x.com]", got)
	}
	// The whole point of the restriction: the excluded account stops being
	// assignable at this origin.
	if accountAllowedFor("second@x.com", got) {
		t.Error("an account the user unticked is still allowed here")
	}
}

// Zero ticked is refused, not saved. An empty list already means unrestricted in
// config, so saving it would do the exact opposite of what ticking nothing looks
// like it asks for.
func TestZeroTickedRefusesToSave(t *testing.T) {
	h := allowedHome(t, map[string]claudeaccount.Usage{})
	h.openAllowedAccounts()

	// Opens fully ticked (unrestricted), so one toggle-all clears it.
	press(t, h, "a")

	if got := h.allowedAccounts.selectedCount(); got != 0 {
		t.Fatalf("toggle-all left %d rows ticked, want 0", got)
	}
	if msg := press(t, h, "enter"); msg != nil {
		t.Errorf("enter saved %v from an empty selection", msg)
	}
	if !h.allowedAccounts.IsVisible() {
		t.Error("the dialog closed on a refused save, hiding the reason")
	}
	if !strings.Contains(h.allowedAccounts.View(), "pick at least one") {
		t.Error("nothing on screen says why enter did nothing")
	}
}

// An allowlist naming an account that no longer exists is the state
// resolveAccount refuses new sessions on. Opening the dialog must offer a way
// out of it rather than reproducing it.
func TestStaleAllowlistHealsOnSave(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := allowedHome(t, map[string]claudeaccount.Usage{})
	h.cfg.AllowedAccounts = map[string][]string{
		OriginExpandKey("github.com/acme/api"): {"gone@acme.com", "first@x.com"},
	}
	h.openAllowedAccounts()

	// Only the surviving account has a row to tick, so the removed one cannot be
	// carried forward by accident.
	if got := h.allowedAccounts.selectedCount(); got != 1 {
		t.Fatalf("ticked %d rows, want just the account that still exists", got)
	}

	msg := press(t, h, "enter")
	set, ok := msg.(allowedAccountsSetMsg)
	if !ok {
		t.Fatalf("enter produced %T, want allowedAccountsSetMsg", msg)
	}
	for _, e := range set.emails {
		if e == "gone@acme.com" {
			t.Fatal("saving carried a removed account back into the allowlist")
		}
	}

	h.Update(set)
	// The refusal is gone: sessions can be created at this origin again.
	if _, blocked := h.resolveAccount("claude", "/tmp/move-e2e"); blocked != "" {
		t.Errorf("session creation is still refused after healing: %q", blocked)
	}
}

// A logged-out account stays tickable. This is policy, not availability — "client
// work runs on the work subscription" is still true while that subscription is
// logged out, and refusing to record it would force a login just to write a rule
// down. Deliberately the opposite of the move picker, which cannot move a session
// onto a dead login.
func TestLoggedOutAccountIsStillSelectable(t *testing.T) {
	h := allowedHome(t, map[string]claudeaccount.Usage{
		"second@x.com": {LoggedOut: true, Err: claudeaccount.ErrNotLoggedIn},
	})
	h.openAllowedAccounts()

	press(t, h, "a") // none
	press(t, h, "j") // second@x.com
	press(t, h, " ") // tick the logged-out one

	// Checked before enter closes the box: the state is still reported, so the
	// user can see what they are picking.
	if !strings.Contains(h.allowedAccounts.View(), "logged out") {
		t.Error("the row hid that the account has no login")
	}

	msg := press(t, h, "enter")
	set, ok := msg.(allowedAccountsSetMsg)
	if !ok {
		t.Fatalf("enter refused to save a logged-out account: got %T", msg)
	}
	if len(set.emails) != 1 || set.emails[0] != "second@x.com" {
		t.Errorf("stored %v, want [second@x.com]", set.emails)
	}
}

// The box is fixed width and the verdict line swaps in place, so a box that grew
// a row mid-keystroke would move the footer out from under the eye reading it.
func TestAllowedAccountsHeightIsStable(t *testing.T) {
	h := allowedHome(t, map[string]claudeaccount.Usage{})
	h.openAllowedAccounts()

	want := strings.Count(h.allowedAccounts.View(), "\n")
	for _, keys := range [][]string{{"a"}, {"a"}, {" "}} {
		for _, k := range keys {
			press(t, h, k)
		}
		if got := strings.Count(h.allowedAccounts.View(), "\n"); got != want {
			t.Fatalf("box height changed to %d rows (was %d) after %v — the footer moves under the cursor",
				got, want, keys)
		}
	}
}

// Every guard clause carries its own note. A constant one would tell a user with
// no accounts configured that they have "only one", which is a false claim about
// their setup sitting right next to the dialog that disproves it.
func TestOriginMenuGuardNamesTheFailingClause(t *testing.T) {
	for _, tc := range []struct {
		name    string
		emails  []string
		enabled bool
		note    string
	}{
		{"none", nil, false, "no accounts configured"},
		{"one", []string{"solo@x.com"}, false, "only one account"},
		{"two", []string{"a@x.com", "b@x.com"}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := allowedHome(t, map[string]claudeaccount.Usage{})
			h.accounts = &claudeaccount.Store{}
			for i, e := range tc.emails {
				h.accounts.Upsert(claudeaccount.Account{Email: e, ConfigDir: string(rune('a' + i))})
			}

			_, items := h.originContextMenu()
			var item *ContextMenuItem
			for i := range items {
				if items[i].ID == "allowed_accounts" {
					item = &items[i]
				}
			}
			if item == nil {
				t.Fatal("the origin menu has no allowed_accounts row")
			}
			if item.Enabled != tc.enabled {
				t.Errorf("enabled = %v, want %v", item.Enabled, tc.enabled)
			}
			if item.Note != tc.note {
				t.Errorf("note = %q, want %q", item.Note, tc.note)
			}
		})
	}
}

// The dialog is origin-scoped, so it must refuse to open from anywhere the title
// would name an origin the user is not standing on.
func TestAllowedAccountsOnlyOpensOnAnOriginHeader(t *testing.T) {
	h, s := moveHome(t, map[string]claudeaccount.Usage{})
	h.flatItems = []SidebarItem{sessionRow(s)}
	h.cursor = 0

	h.openAllowedAccounts()
	if h.allowedAccounts.IsVisible() {
		t.Error("the origin-wide editor opened from a session row")
	}
}
