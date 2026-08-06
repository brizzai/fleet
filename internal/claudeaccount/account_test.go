package claudeaccount

import (
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

func TestExtractToken(t *testing.T) {
	// The capture matches the token's own format rather than Claude's wording,
	// so rephrasing the surrounding prompt must not break it.
	out := "Success! Here is your long-lived token:\n\n  " + fakeToken + "\n\nStore it safely.\n"
	got, ok := ExtractToken(out)
	if !ok || got != fakeToken {
		t.Fatalf("ExtractToken = %q (ok=%v), want the token", got, ok)
	}

	if _, ok := ExtractToken("browser opened, waiting for approval…"); ok {
		t.Fatal("ExtractToken found a token in output that has none")
	}
}

func TestStoreUpsertPreservesOrderAndLabel(t *testing.T) {
	// Re-adding an account after its token expires must not reorder the
	// rotation or discard a rename the user made.
	s := &Store{}
	s.Upsert(Account{Email: "a@x.com", Token: "t1"})
	s.Upsert(Account{Email: "b@x.com", Token: "t2"})
	s.SetLabel("a@x.com", "personal")

	s.Upsert(Account{Email: "a@x.com", Token: "refreshed"})

	got, ok := s.Get("a@x.com")
	if !ok {
		t.Fatal("account vanished on re-add")
	}
	if got.Token != "refreshed" {
		t.Errorf("token = %q, want refreshed", got.Token)
	}
	if got.Label != "personal" {
		t.Errorf("label = %q, want personal (preserved across re-add)", got.Label)
	}
	if got.Order != 0 {
		t.Errorf("order = %d, want 0 (preserved across re-add)", got.Order)
	}
}

// The orphaning bug this exists to prevent: an account the API declines to name
// is keyed by a hash of its token, so running `claude setup-token` again used to
// produce a second account for one subscription. Every session, plus
// default_account and allowed_accounts, still named the old key — they fell back
// to the ambient login and billed the wrong place, with only a parenthetical in
// the sidebar to say so.
func TestRotatedTokenLandsOnTheSameAccount(t *testing.T) {
	s := &Store{}
	s.Upsert(Account{Email: FingerprintPrefix + "aaaaaaaa", Token: "t1", OrgUUID: "org-1"})
	s.SetLabel(FingerprintPrefix+"aaaaaaaa", "personal")
	s.Upsert(Account{Email: "other@x.com", Token: "t2", OrgUUID: "org-2"})

	// Same subscription, new token — so a different fingerprint key.
	s.Upsert(Account{Email: FingerprintPrefix + "bbbbbbbb", Token: "t3", OrgUUID: "org-1"})

	if n := s.Len(); n != 2 {
		t.Fatalf("%d accounts after a token rotation, want 2 — a rotation minted a new identity", n)
	}
	got, ok := s.Get(FingerprintPrefix + "aaaaaaaa")
	if !ok {
		t.Fatal("the original key is gone — every session naming it is now orphaned")
	}
	if got.Token != "t3" {
		t.Errorf("token = %q, want the rotated one", got.Token)
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
	s.Upsert(Account{Email: "a@x.com", Token: "t1", OrgUUID: "org-1"})
	s.Upsert(Account{Email: "b@x.com", Token: "t2", OrgUUID: "org-2"})

	if n := s.Len(); n != 2 {
		t.Fatalf("%d accounts, want 2 — two subscriptions collapsed into one", n)
	}
}

// Accounts stored before fleet recorded orgs have none, and so does an account
// whose add-time probe failed. Matching on a blank org would fold every one of
// them onto the first.
func TestBlankOrgNeverMatches(t *testing.T) {
	s := &Store{}
	s.Upsert(Account{Email: "a@x.com", Token: "t1"})
	s.Upsert(Account{Email: "b@x.com", Token: "t2"})

	if n := s.Len(); n != 2 {
		t.Fatalf("%d accounts, want 2 — accounts with no org matched each other", n)
	}
}

// The key must not move, but an email fleet has just managed to resolve is
// still worth showing. Label is where display already looks first.
func TestResolvedEmailBecomesTheLabelNotTheKey(t *testing.T) {
	s := &Store{}
	s.Upsert(Account{Email: FingerprintPrefix + "aaaaaaaa", Token: "t1", OrgUUID: "org-1"})

	s.Upsert(Account{Email: "real@x.com", Token: "t2", OrgUUID: "org-1"})

	a, ok := s.Get(FingerprintPrefix + "aaaaaaaa")
	if !ok {
		t.Fatal("the key moved to the newly resolved email, orphaning existing sessions")
	}
	if a.Label != "real@x.com" {
		t.Errorf("label = %q, want the resolved email", a.Label)
	}
	if a.NeedsLabel() {
		t.Error("account still asks for a label when fleet knows its email")
	}
}

func TestSetOrgUUIDFillsOnlyABlank(t *testing.T) {
	s := &Store{}
	s.Upsert(Account{Email: "a@x.com", Token: "t"})

	if !s.SetOrgUUID("a@x.com", "org-1") {
		t.Fatal("first backfill should report new information")
	}
	if s.SetOrgUUID("a@x.com", "org-1") {
		t.Error("repeat backfill should report nothing new, so it triggers no save")
	}
	// A changed org means a different subscription, not a correction.
	if s.SetOrgUUID("a@x.com", "org-2") {
		t.Error("backfill overwrote a known org")
	}
	if a, _ := s.Get("a@x.com"); a.OrgUUID != "org-1" {
		t.Errorf("org = %q, want org-1", a.OrgUUID)
	}
	if s.SetOrgUUID("a@x.com", "") {
		t.Error("an empty org is not information")
	}
	if s.SetOrgUUID("ghost@x.com", "org-3") {
		t.Error("backfilling an unknown account should report false")
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

// The verdict is permanent and the question isn't free, so it has to survive a
// restart — otherwise every launch re-asks, and a transient 429 in place of the
// 403 leaves fleet retrying on a timer against an answer that cannot change.
func TestQuotaUnavailableIsRememberedAndIdempotent(t *testing.T) {
	s := &Store{}
	s.Upsert(Account{Email: "a@x.com", Token: "t"})

	if !s.MarkUsageEndpointForbidden("a@x.com") {
		t.Fatal("first mark should report new information")
	}
	if s.MarkUsageEndpointForbidden("a@x.com") {
		t.Error("second mark should report nothing new, so it triggers no save")
	}
	if a, _ := s.Get("a@x.com"); !a.UsageEndpointForbidden {
		t.Error("flag not set on the account")
	}
	if s.MarkUsageEndpointForbidden("ghost@x.com") {
		t.Error("marking an unknown account should report false")
	}
}

func TestReAddGivesAFreshTokenAFreshVerdict(t *testing.T) {
	// A new token deserves to be re-checked, unlike Order and Label which are
	// the user's own choices and are preserved.
	//
	// The asymmetry is deliberate: carrying the flag onto a replacement token
	// would silently disable quota forever if a future token did carry the
	// scope, whereas clearing it costs exactly one HTTP call that re-marks it
	// immediately. A wasted call beats a feature that quietly stops working.
	s := &Store{}
	s.Upsert(Account{Email: "a@x.com", Token: "old", Label: "work"})
	s.MarkUsageEndpointForbidden("a@x.com")

	s.Upsert(Account{Email: "a@x.com", Token: "new"})

	a, _ := s.Get("a@x.com")
	if a.UsageEndpointForbidden {
		t.Error("a replacement token inherited the old token's verdict")
	}
	if a.Token != "new" {
		t.Errorf("token = %q, want the refreshed one", a.Token)
	}
	if a.Label != "work" {
		t.Errorf("label = %q, want it preserved — it is the user's choice, not a verdict", a.Label)
	}
}

func TestStoreRemove(t *testing.T) {
	s := &Store{}
	s.Upsert(Account{Email: "a@x.com", Token: "t"})
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

func TestTokenForUnknownAccountIsEmpty(t *testing.T) {
	// An account removed while sessions still reference it must resolve to no
	// token, so those sessions fall back to the ambient login rather than
	// failing to launch.
	s := &Store{}
	if tok := s.TokenFor("ghost@x.com"); tok != "" {
		t.Fatalf("TokenFor(unknown) = %q, want empty", tok)
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
	if got := s.TokenFor("a@x.com"); got != "" {
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
