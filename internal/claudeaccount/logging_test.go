package claudeaccount

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/debuglog"
)

// The store used to hold year-long credentials, and its logs were the obvious
// place to leak one. It holds none now — the login lives in the Keychain — but
// the file still records identity, and debug.log is pasted into public issues
// by the bug-report flow, so the shape of what it prints still matters.
func TestLoadLogsIdentityButNeverACredential(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".config", "fleet"), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(DefaultPath(),
		[]byte(`[{"email":"work@x.com","plan":"max","config_dir":"/cfg/a"}]`), 0600); err != nil {
		t.Fatalf("write accounts: %v", err)
	}

	var buf bytes.Buffer
	prev := debuglog.Logger
	debuglog.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { debuglog.Logger = prev })

	if n := Load().Len(); n != 1 {
		t.Fatalf("loaded %d accounts, want 1", n)
	}
	out := buf.String()
	if strings.Contains(out, "sk-ant-") {
		t.Errorf("a credential reached the log:\n%s", out)
	}
	if !strings.Contains(out, "work@x.com") {
		t.Errorf("load log should name the account:\n%s", out)
	}
}

// The scrub in configDirEnv is the whole correctness argument for every command
// that acts on one account: fleet is often launched *from* a fleet session, so
// its own environment can carry a credential that outranks the config dir's
// login. Leaving one in place would make `claude auth status` report whoever
// that credential belongs to, and fleet would file the wrong email against the
// account the user just logged in.
func TestConfigDirEnvScrubsCredentialsThatWouldOutrankTheLogin(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-ant-oat01-inherited")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api03-inherited")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-also-inherited")
	t.Setenv("CLAUDE_CONFIG_DIR", "/somewhere/else")

	env := configDirEnv("/cfg/target")

	for _, kv := range env {
		for _, bad := range []string{"ANTHROPIC_AUTH_TOKEN=", "ANTHROPIC_API_KEY=", "CLAUDE_CODE_OAUTH_TOKEN="} {
			if strings.HasPrefix(kv, bad) {
				t.Errorf("%s survived the scrub — it outranks the config dir's login",
					strings.TrimSuffix(bad, "="))
			}
		}
	}
	var dirs []string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "CLAUDE_CONFIG_DIR="); ok {
			dirs = append(dirs, v)
		}
	}
	if len(dirs) != 1 || dirs[0] != "/cfg/target" {
		t.Errorf("CLAUDE_CONFIG_DIR = %v, want exactly [/cfg/target] — an inherited one must not survive", dirs)
	}
}

// The Keychain item is what makes two config dirs two independent logins, and
// its name is derived, not stored. A change here silently costs every account
// its quota reading.
func TestKeychainServiceIsScopedToTheConfigDir(t *testing.T) {
	if got := keychainService(""); got != "Claude Code-credentials" {
		t.Errorf("default dir service = %q, want the unsuffixed name", got)
	}
	a, b := keychainService("/cfg/a"), keychainService("/cfg/b")
	if a == b {
		t.Error("two config dirs share a Keychain item — their logins would clobber each other")
	}
	if !strings.HasPrefix(a, "Claude Code-credentials-") || len(a) != len("Claude Code-credentials-")+8 {
		t.Errorf("service = %q, want the bare name plus an 8-hex suffix", a)
	}
	// Confirmed against a live login: Claude Code 2.1.226 stored /tmp/cc-spike-a
	// under this exact name.
	if got := keychainService("/tmp/cc-spike-a"); got != "Claude Code-credentials-5205b77d" {
		t.Errorf("service = %q, want the name Claude Code actually used", got)
	}
}
