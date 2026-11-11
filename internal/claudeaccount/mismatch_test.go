package claudeaccount

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoginMismatch(t *testing.T) {
	tests := []struct {
		name       string
		registered string
		id         Identity
		want       string
	}{
		{"same email", "a@x.com", Identity{LoggedIn: true, Email: "a@x.com"}, ""},
		{"same email, other case", "A@x.com", Identity{LoggedIn: true, Email: "a@X.com"}, ""},
		{"different email", "a@x.com", Identity{LoggedIn: true, Email: "b@x.com"}, "b@x.com"},
		{"logged out has no email to compare", "a@x.com", Identity{LoggedIn: false, Email: "b@x.com"}, ""},
		{"empty reported email is a missing answer", "a@x.com", Identity{LoggedIn: true}, ""},
		{"placeholder key names nobody to compare", "acct-1f3a", Identity{LoggedIn: true, Email: "b@x.com"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := LoginMismatch(tc.registered, tc.id); got != tc.want {
				t.Errorf("LoginMismatch = %q, want %q", got, tc.want)
			}
		})
	}
}

// A mismatched dir still runs sessions — just as someone else — so Select must
// treat it like a logged-out one and never hand it to a new session.
func TestSelectSkipsMismatchedLogin(t *testing.T) {
	for _, strat := range []string{StrategyLeastUsed, StrategyWaterfall, StrategyManual} {
		got, ok := Select(SelectOpts{
			Accounts: accts("wrong", "alive"),
			Usage:    map[string]Usage{"wrong": {Mismatch: "other@x.com"}, "alive": used(90)},
			Strategy: strat,
			Manual:   "wrong",
			Now:      testNow,
		})
		if !ok || got.Email != "alive" {
			t.Errorf("%s: picked %q (ok=%v), want alive", strat, got.Email, ok)
		}
	}
}

func TestUsableTable(t *testing.T) {
	tests := []struct {
		u    Usage
		want bool
	}{
		{Usage{}, true},
		{Usage{LoggedOut: true}, false},
		{Usage{Mismatch: "b@x.com"}, false},
	}
	for _, tc := range tests {
		if got := tc.u.Usable(); got != tc.want {
			t.Errorf("%+v Usable = %v, want %v", tc.u, got, tc.want)
		}
	}
}

func TestIdentityCheckDue(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		last time.Time
		want bool
	}{
		{"never checked", time.Time{}, true},
		{"just checked", now.Add(-time.Minute), false},
		{"just under the interval", now.Add(-IdentityCheckInterval + time.Second), false},
		{"interval elapsed", now.Add(-IdentityCheckInterval), true},
	}
	for _, tc := range tests {
		if got := IdentityCheckDue(tc.last, now); got != tc.want {
			t.Errorf("%s: due = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The CLI ranks from the cache, so the verdict has to survive the round trip.
func TestMismatchSurvivesTheUsageCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := SaveUsageCache(path, map[string]Usage{"a@x.com": {AttemptedAt: time.Now(), Mismatch: "b@x.com"}}); err != nil {
		t.Fatal(err)
	}
	if got := LoadUsageCache(path)["a@x.com"].Mismatch; got != "b@x.com" {
		t.Errorf("cached mismatch = %q, want b@x.com", got)
	}
}

// The ambient login is "no CLAUDE_CONFIG_DIR at all", not an empty one, and the
// credential overrides still have to be scrubbed.
func TestConfigDirEnvAmbientSetsNoConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/leaked")
	t.Setenv("ANTHROPIC_API_KEY", "leaked")
	for _, kv := range configDirEnv("") {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") || strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			t.Errorf("ambient env carries %q", kv)
		}
	}
	found := false
	for _, kv := range configDirEnv("/d/1") {
		found = found || kv == "CLAUDE_CONFIG_DIR=/d/1"
	}
	if !found {
		t.Error("pinned env lost its config dir")
	}
}
