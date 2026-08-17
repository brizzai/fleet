package claudeaccount

import (
	"os"
	"testing"
)

// TestMain points HOME at a throwaway directory for the whole package.
//
// This package owns accounts.json, and DefaultPath resolves it through
// os.UserHomeDir() — so any test that reaches Store.Save writes over the real
// one. Unlike config.json, which Save merges, the account store is written
// whole: one careless test replaces the index naming the user's actual
// subscriptions and the config dirs holding their logins. That happened, and it
// was recoverable only because the credentials live in the Keychain and the
// dirs themselves were untouched.
//
// Package-wide rather than per-test, because a rule that has to be remembered in
// each new test is a rule that eventually isn't. Tests needing their own home
// still call t.Setenv("HOME", …) and are restored to this one afterwards.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "fleet-account-test-home")
	if err != nil {
		panic("test home: " + err.Error())
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
