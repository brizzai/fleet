package claudeaccount

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A realistically-shaped token. Not a real credential.
const fakeToken = "sk-ant-oat01-AbCdEf0123456789_-GhIjKlMnOpQrStUvWxYz0123456789AbCdEf"

func TestRedactStripsTokens(t *testing.T) {
	// Shaped like what actually reaches a bug report: the add-account flow puts
	// a token on a real tmux pane, and fleet publishes pane excerpts.
	pane := "│ Your token: " + fakeToken + "\n│ Copy it now.\n"
	got := Redact(pane)
	if strings.Contains(got, fakeToken) {
		t.Fatalf("token survived redaction: %q", got)
	}
	if !strings.Contains(got, "sk-ant-<redacted>") {
		t.Fatalf("want a redaction marker, got %q", got)
	}
	// Surrounding text must survive — a redactor that eats the context makes
	// the excerpt useless for diagnosing anything.
	if !strings.Contains(got, "Copy it now.") {
		t.Fatalf("redaction destroyed surrounding text: %q", got)
	}
}

func TestRedactCoversCredentialFamilies(t *testing.T) {
	// Over-matching is the safe error here, so refresh tokens and API keys are
	// covered too, not just the setup-token OAuth shape.
	for _, tok := range []string{
		"sk-ant-oat01-" + strings.Repeat("a", 40),
		"sk-ant-ort01-" + strings.Repeat("b", 40),
		"sk-ant-api03-" + strings.Repeat("c", 40),
	} {
		if got := Redact("prefix " + tok + " suffix"); strings.Contains(got, tok) {
			t.Errorf("token %q… survived redaction", tok[:20])
		}
	}
}

// Whole captured screens get the looser pattern: a half-printed token — the
// exact case a failed capture leaves behind — is shorter than tokenPattern's
// floor and would sail through the strict one.
func TestRedactCapturedCatchesTruncatedTokens(t *testing.T) {
	partial := "sk-ant-oat01-AbC" // cut off mid-print
	if !strings.Contains(Redact(partial), partial) {
		t.Fatal("premise wrong: Redact was expected to miss a short fragment")
	}
	if got := RedactCaptured("pane: " + partial); strings.Contains(got, partial) {
		t.Errorf("RedactCaptured let a truncated token through: %q", got)
	}
	if got := RedactCaptured("pane: " + fakeToken); strings.Contains(got, fakeToken) {
		t.Errorf("RedactCaptured let a whole token through: %q", got)
	}
	// Surrounding text has to survive or the excerpt diagnoses nothing.
	if got := RedactCaptured("error: browser did not open"); got != "error: browser did not open" {
		t.Errorf("RedactCaptured mangled clean text: %q", got)
	}
}

func TestRedactLeavesOrdinaryTextAlone(t *testing.T) {
	in := "no credentials here, just sk-ant- and some words"
	if got := Redact(in); got != in {
		t.Fatalf("Redact mangled clean text: %q", got)
	}
}

func TestStoreUpsertPreservesOrderAndLabel(t *testing.T) {
	// Re-adding an account after its token expires must not reorder the
	// rotation or discard a rename the user made.
	s := &Store{}
	s.Upsert(Account{Email: "a@x.com", ConfigDir: "/d1"})
	s.Upsert(Account{Email: "b@x.com", ConfigDir: "/d2"})
	s.SetLabel("a@x.com", "personal")

	s.Upsert(Account{Email: "a@x.com", ConfigDir: "/refreshed"})

	got, ok := s.Get("a@x.com")
	if !ok {
		t.Fatal("account vanished on re-add")
	}
	if got.ConfigDir != "/refreshed" {
		t.Errorf("config dir = %q, want /refreshed", got.ConfigDir)
	}
	if got.Label != "personal" {
		t.Errorf("label = %q, want personal (preserved across re-add)", got.Label)
	}
	if got.Order != 0 {
		t.Errorf("order = %d, want 0 (preserved across re-add)", got.Order)
	}
}

// Logging the same subscription in a second time must land on the account that
// already exists, not create a duplicate — the org is what decides identity, not
// the config dir it happens to live in. Every session, plus default_account and
// allowed_accounts, references an account by key; a second row for one
// subscription would leave half of them pointing at an account nobody uses.
func TestReLoginLandsOnTheSameAccount(t *testing.T) {
	s := &Store{}
	s.Upsert(Account{Email: "acct-a", ConfigDir: "/d1", OrgUUID: "org-1"})
	s.SetLabel("acct-a", "personal")
	s.Upsert(Account{Email: "other@x.com", ConfigDir: "/d2", OrgUUID: "org-2"})

	// Same subscription, logged in again into a different config dir.
	s.Upsert(Account{Email: "acct-b", ConfigDir: "/d3", OrgUUID: "org-1"})

	if n := s.Len(); n != 2 {
		t.Fatalf("%d accounts after logging the same subscription in again, want 2", n)
	}
	got, ok := s.Get("acct-a")
	if !ok {
		t.Fatal("the original key is gone — every session naming it is now orphaned")
	}
	if got.ConfigDir != "/d3" {
		t.Errorf("config dir = %q, want the re-logged-in one", got.ConfigDir)
	}
	if got.Label != "personal" {
		t.Errorf("label = %q, want personal", got.Label)
	}
	if got.Order != 0 {
		t.Errorf("order = %d, want 0", got.Order)
	}
}

// A different organization is a different subscription. Matching those together
// would bill one account's work to the other — worse than the orphaning.
func TestDifferentOrgIsADifferentAccount(t *testing.T) {
	s := &Store{}
	s.Upsert(Account{Email: "a@x.com", ConfigDir: "/d1", OrgUUID: "org-1"})
	s.Upsert(Account{Email: "b@x.com", ConfigDir: "/d2", OrgUUID: "org-2"})

	if n := s.Len(); n != 2 {
		t.Fatalf("%d accounts, want 2 — two subscriptions collapsed into one", n)
	}
}

// Accounts stored before fleet recorded orgs have none, and so does an account
// whose add-time probe failed. Matching on a blank org would fold every one of
// them onto the first.
func TestBlankOrgNeverMatches(t *testing.T) {
	s := &Store{}
	s.Upsert(Account{Email: "a@x.com", ConfigDir: "/d1"})
	s.Upsert(Account{Email: "b@x.com", ConfigDir: "/d2"})

	if n := s.Len(); n != 2 {
		t.Fatalf("%d accounts, want 2 — accounts with no org matched each other", n)
	}
}

func TestStoreListIsOrdered(t *testing.T) {
	s := &Store{}
	s.Upsert(Account{Email: "a@x.com"})
	s.Upsert(Account{Email: "b@x.com"})
	s.Upsert(Account{Email: "c@x.com"})

	if !s.Reorder("c@x.com", -2) {
		t.Fatal("Reorder failed")
	}
	var got []string
	for _, a := range s.List() {
		got = append(got, a.Email)
	}
	want := []string{"c@x.com", "a@x.com", "b@x.com"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestStoreReorderRejectsOutOfRange(t *testing.T) {
	s := &Store{}
	s.Upsert(Account{Email: "a@x.com"})
	if s.Reorder("a@x.com", -1) {
		t.Error("Reorder past the start should fail")
	}
	if s.Reorder("a@x.com", 1) {
		t.Error("Reorder past the end should fail")
	}
}

func TestStoreRemove(t *testing.T) {
	s := &Store{}
	s.Upsert(Account{Email: "a@x.com", ConfigDir: "/d"})
	if !s.Remove("a@x.com") {
		t.Fatal("Remove reported the account was absent")
	}
	if s.Len() != 0 {
		t.Fatalf("len = %d after remove, want 0", s.Len())
	}
	if s.Remove("a@x.com") {
		t.Error("removing a second time should report false")
	}
}

// "No store" and "no accounts configured" are the same state, so read methods
// must survive a nil receiver — a partially-built caller should degrade to the
// ambient login, not panic.
func TestNilStoreReadsAreSafe(t *testing.T) {
	var s *Store
	if got := s.Len(); got != 0 {
		t.Errorf("Len() = %d on nil store, want 0", got)
	}
	if got := s.List(); got != nil {
		t.Errorf("List() = %v on nil store, want nil", got)
	}
	if _, ok := s.Get("a@x.com"); ok {
		t.Error("Get() found an account on a nil store")
	}
	if got := s.ConfigDirFor("a@x.com"); got != "" {
		t.Errorf("TokenFor() = %q on nil store, want empty", got)
	}
}

func TestAccountName(t *testing.T) {
	if got := (Account{Email: "a@x.com"}).Name(); got != "a@x.com" {
		t.Errorf("Name() = %q, want the email when unlabelled", got)
	}
	if got := (Account{Email: "a@x.com", Label: "work"}).Name(); got != "work" {
		t.Errorf("Name() = %q, want the label when set", got)
	}
}

// An accounts.json written by the token-based implementation carries no config
// dir, so its entries cannot run a session. Keeping them would leave them as
// Select candidates that silently fall back to the ambient login — the exact
// invisible mis-billing this whole mechanism exists to prevent.
func TestLegacyTokenAccountsAreDroppedNotSilentlyKept(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".config", "fleet"), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// The old shape: a fingerprint key and a token, no config_dir.
	if err := os.WriteFile(DefaultPath(), []byte(`[
		{"email":"account-37b1194e","label":"personal","token":"sk-ant-oat01-old"},
		{"email":"work@x.com","config_dir":"/cfg/work"}
	]`), 0600); err != nil {
		t.Fatalf("write accounts: %v", err)
	}

	s := Load()
	if s.Len() != 1 {
		t.Fatalf("loaded %d accounts, want 1 — a legacy entry survived and can still be selected", s.Len())
	}
	if _, ok := s.Get("account-37b1194e"); ok {
		t.Error("the legacy token account is still a candidate")
	}
	if _, ok := s.Get("work@x.com"); !ok {
		t.Error("a usable account was dropped alongside the legacy ones")
	}
}
