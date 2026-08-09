package claudeaccount

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

// Identity is who a config dir is logged in as.
//
// It comes from `claude auth status`, which reports the real account behind a
// real login — email, organization and plan, exactly. This is the payoff of
// running each account as its own login rather than a token: fleet's previous
// mechanism used a `claude setup-token` credential, which is inference-only and
// refuses to identify itself, so accounts had to be keyed by a hash of the
// token and a rotation orphaned every session pointing at the old hash. Nothing
// here can drift, because the account tells us who it is.
type Identity struct {
	LoggedIn   bool
	Email      string
	OrgUUID    string
	OrgName    string
	Plan       string
	AuthMethod string
}

// ErrNotLoggedIn means the config dir has no live claude.ai login: never logged
// in, logged out, or the credential expired or was revoked.
//
// Actionable and specific, unlike a failure to run the CLI at all: the fix is
// to log that account in again, and Select must not hand new sessions to it in
// the meantime.
var ErrNotLoggedIn = errors.New("not logged in")

const identifyTimeout = 20 * time.Second

// Identify asks Claude Code who the given config dir is logged in as.
//
// Local and free — no API call, no quota. Called at add time to name the
// account, and on the quota poll to notice a login that has since expired.
func Identify(ctx context.Context, dir string) (Identity, error) {
	ctx, cancel := context.WithTimeout(ctx, identifyTimeout)
	defer cancel()

	// claudeBinary, not agent.Type's launch command: this is a bare status
	// query, and going through the agent package would import the launch
	// machinery into the account store for one string.
	cmd := exec.CommandContext(ctx, claudeBinary, "auth", "status")
	cmd.Env = configDirEnv(dir)

	// Output, then parse regardless of the exit code. `claude auth status` exits
	// 1 when logged out while still printing perfectly good JSON, so treating a
	// non-zero exit as a failure would throw away the one answer this function
	// exists to get — and report "fleet could not run claude" for the ordinary
	// case of an account that simply needs logging in.
	out, runErr := cmd.Output()
	return parseAuthStatus(out, runErr)
}

// parseAuthStatus reads `claude auth status` output, tolerating the non-zero
// exit it pairs with a perfectly good logged-out answer.
func parseAuthStatus(out []byte, runErr error) (Identity, error) {
	var raw struct {
		LoggedIn         bool   `json:"loggedIn"`
		AuthMethod       string `json:"authMethod"`
		Email            string `json:"email"`
		OrgID            string `json:"orgId"`
		OrgName          string `json:"orgName"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		// No usable answer. Now the exit error is the real story: fleet could not
		// run claude at all, which is fleet's problem and must not cost the user
		// an account in Select.
		if runErr != nil {
			return Identity{}, runErr
		}
		return Identity{}, err
	}
	id := Identity{
		LoggedIn:   raw.LoggedIn,
		Email:      raw.Email,
		OrgUUID:    raw.OrgID,
		OrgName:    raw.OrgName,
		Plan:       raw.SubscriptionType,
		AuthMethod: raw.AuthMethod,
	}
	if !id.LoggedIn {
		return id, ErrNotLoggedIn
	}
	return id, nil
}

// claudeBinary is looked up on PATH like every other agent fleet launches.
const claudeBinary = "claude"

// configDirEnv builds the environment for a command that must act on one
// specific account.
//
// The scrub is not defensive tidying, it is the whole correctness argument.
// fleet is frequently launched *from a fleet session*, so its own environment
// can carry an inherited credential — and any of these variables outranks the
// config dir's login, which would make `auth status` report the wrong account
// and the usage endpoint bill the wrong subscription.
func configDirEnv(dir string) []string {
	drop := map[string]bool{
		"CLAUDE_CONFIG_DIR":       true,
		"ANTHROPIC_AUTH_TOKEN":    true,
		"ANTHROPIC_API_KEY":       true,
		"CLAUDE_CODE_OAUTH_TOKEN": true,
	}
	out := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && drop[k] {
			continue
		}
		out = append(out, kv)
	}
	return append(out, ConfigDirEnvVar+"="+dir)
}

// ConfigDirEnvVar is the one variable fleet sets to choose which subscription a
// Claude session runs on.
//
// It points Claude Code at a config directory holding its own claude.ai login,
// so the session authenticates the way a normal `claude` in a terminal does.
// Nothing is layered over anything: there is no precedence conflict, so
// claude.ai connectors, Remote Control and the usage endpoint all work.
//
// The obvious alternative, ANTHROPIC_AUTH_TOKEN, is what fleet shipped first
// and it is strictly worse — see the file comment in configdir.go for the full
// account of why it was replaced.
const ConfigDirEnvVar = "CLAUDE_CONFIG_DIR"

// TmuxEnv returns the environment overrides a tmux session must carry to run as
// this account, in the `KEY=VALUE` form tmux -e takes.
//
// The empty values are the important part, and getting this wrong is how a
// session silently bills the wrong thing. tmux -e can only *add* variables; it
// cannot remove one the server already inherited. So the credentials that
// outrank a config dir's login are set to empty rather than omitted — Claude
// Code tests them for truthiness, so an empty value reads as unset.
//
// Passing a full process environment here instead would be both enormous and
// useless: hundreds of -e flags, and the inherited credential still in place.
// That bug shipped for exactly one commit, and the tell was a fresh login pane
// announcing "API Usage Billing".
func TmuxEnv(dir string) []string {
	return []string{
		ConfigDirEnvVar + "=" + dir,
		"ANTHROPIC_AUTH_TOKEN=",
		"ANTHROPIC_API_KEY=",
		"CLAUDE_CODE_OAUTH_TOKEN=",
	}
}

// WaitForLogin polls until the config dir has a live login, the context ends,
// or the deadline passes. Reports the resolved identity.
//
// Polling `auth status` rather than watching the pane: the previous mechanism
// scraped a token off the screen with a regex, which coupled fleet to Claude
// Code's exact wording. A status query is the same question asked properly.
func WaitForLogin(ctx context.Context, dir string, every time.Duration) (Identity, error) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return Identity{}, ctx.Err()
		case <-t.C:
			id, err := Identify(ctx, dir)
			if err == nil {
				debuglog.Logger.Info("account login completed",
					"email", id.Email, "plan", id.Plan, "org", id.OrgUUID)
				return id, nil
			}
			if !errors.Is(err, ErrNotLoggedIn) {
				debuglog.Logger.Debug("waiting for login; status query failed", "err", err)
			}
		}
	}
}

// GuardConflictingAuth reports the name of an ambient credential that would
// outrank an account's login, or "" when the coast is clear.
//
// This guard was nearly deleted and turns out to matter more here than it did
// before. Under the previous ANTHROPIC_AUTH_TOKEN mechanism fleet set the
// highest-precedence variable itself, so almost nothing could outrank it. A
// config dir's login sits at the *bottom* of the precedence order — below
// ANTHROPIC_AUTH_TOKEN, ANTHROPIC_API_KEY and apiKeyHelper — so any of those
// silently overrides every account at once, and the whole fleet quietly bills
// one credential.
//
// tmux -e can add variables but not remove them, so a session cannot scrub what
// it inherits. Saying so is the only honest answer.
func GuardConflictingAuth() string {
	for _, name := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if os.Getenv(name) != "" {
			debuglog.Logger.Warn("ambient credential outranks every account login",
				"var", name, "effect", "sessions would use that credential, not the chosen account")
			return name
		}
	}
	// apiKeyHelper is not an environment variable, so it survives every scrub and
	// every fresh shell — and it outranks a config dir's login just the same. A
	// user who configured one once, months ago, gets a clean pass from the env
	// checks above while Claude authenticates through the helper.
	if helperConfigured() {
		debuglog.Logger.Warn("apiKeyHelper is configured and outranks every account login",
			"file", "~/.claude/settings.json",
			"effect", "sessions would authenticate through the helper, not the chosen account")
		return "apiKeyHelper (in ~/.claude/settings.json)"
	}
	return ""
}

// helperConfigured reports whether ~/.claude/settings.json sets apiKeyHelper.
//
// Read rather than cached: it is one small file, consulted at session launch
// and at CLI startup, and a stale "no" here is exactly the silent misbilling
// this function exists to prevent. An unreadable or unparseable file answers
// false — a guard that cannot read the file must not invent a conflict.
func helperConfigured() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return false
	}
	var cfg struct {
		APIKeyHelper string `json:"apiKeyHelper"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false
	}
	return strings.TrimSpace(cfg.APIKeyHelper) != ""
}
