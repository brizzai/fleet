package claudeaccount

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A logged-out account never reaches the HTTP call that returns 401, because
// /logout deletes the Keychain item and there is no bearer to send. It used to
// fail as ErrNoCredential, which Select reads as "fleet could not ask" rather
// than "this cannot run" — so LoggedOut stayed false, dropLoggedOut kept the
// account, and pctOf scored it at the unknown midpoint of 50. That beats a
// healthy account in active use, so every new session went to the one account
// that could not start.
//
// `security` reports the absence with exit 44 (errSecItemNotFound), which is the
// one outcome here that is evidence.
func TestMissingKeychainItemReadsAsLoggedOut(t *testing.T) {
	withFakeSecurity(t, `exit 44`)

	_, err := accessToken(context.Background(), "/tmp/does-not-matter")
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("err = %v, want ErrNotLoggedIn", err)
	}
}

// Everything else stays ignorance, not evidence. A Keychain fleet cannot read
// says nothing about whether the account is logged in, and treating it as a
// verdict would report the whole fleet logged out the moment `security` had a
// bad day — pulling every account out of selection at once.
func TestUnreadableKeychainIsNotEvidence(t *testing.T) {
	withFakeSecurity(t, `exit 1`)

	_, err := accessToken(context.Background(), "/tmp/does-not-matter")
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
	if errors.Is(err, ErrNotLoggedIn) {
		t.Error("an unreadable keychain must not be reported as a missing login")
	}
}

// An item that parses but carries no token is a login that isn't one: same
// verdict as a missing item, since no session can start from it.
func TestEmptyAccessTokenReadsAsLoggedOut(t *testing.T) {
	withFakeSecurity(t, `echo '{"claudeAiOauth":{"accessToken":""}}'`)

	_, err := accessToken(context.Background(), "/tmp/does-not-matter")
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("err = %v, want ErrNotLoggedIn", err)
	}
}

// An item fleet cannot parse is an item fleet cannot judge — the stored format
// may simply have moved on.
func TestUnparseableKeychainItemIsNotEvidence(t *testing.T) {
	withFakeSecurity(t, `echo 'not json at all'`)

	_, err := accessToken(context.Background(), "/tmp/does-not-matter")
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
}

// withFakeSecurity puts a stub `security` on PATH for the duration of the test.
func withFakeSecurity(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "security"), []byte(script), 0700); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir)
}
