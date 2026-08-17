package ui

import (
	"os"
	"testing"
)

// TestMain points HOME at a throwaway directory for the whole package.
//
// Not a nicety. Config.Save and claudeaccount.Store.Save resolve their paths
// through os.UserHomeDir(), so every test that builds a Home was writing to the
// developer's real ~/.config/fleet. Config survived because Save merges, but the
// account store does not: a test that removed one of its two fixture accounts
// persisted the remainder, and the user's actual accounts.json — the index
// naming their real subscriptions and the config dirs holding their logins —
// was replaced by first@x.com and second@x.com. Recoverable only because the
// logins live in the Keychain and the dirs were untouched.
//
// Package-wide rather than per-test, because the hazard is in the constructor:
// any new test that calls NewHome inherits the fix without knowing it exists,
// which is the only version of this that stays true. Tests that need their own
// home still call t.Setenv("HOME", …) and are restored to this one afterwards.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "fleet-ui-test-home")
	if err != nil {
		panic("test home: " + err.Error())
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
