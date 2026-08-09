package claudeaccount

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/brizzai/fleet/internal/debuglog"
)

// A fleet account is a Claude Code config directory with its own login.
//
// CLAUDE_CONFIG_DIR relocates everything Claude Code keeps per user, including
// the credential: on macOS the Keychain service name is suffixed with a hash of
// the resolved config dir, so two dirs hold two independent logins and neither
// clobbers the other. That is the whole mechanism, and it is why a fleet session
// gets a *real* claude.ai login rather than a token layered over one.
//
// The alternative fleet shipped first — ANTHROPIC_AUTH_TOKEN carrying a
// `claude setup-token` credential — was rejected after measurement. It sits at
// auth precedence 2 and displaces the claude.ai login, so every session lost
// claude.ai connectors and Remote Control and printed a banner saying so. The
// credential is also inference-only: it cannot name its own owner (hence a
// fingerprint key, hence sessions orphaned by a token rotation) and cannot read
// the usage endpoint (hence a hand-rolled probe against /v1/messages that spent
// real quota to measure quota). Every one of those problems is an artefact of
// the token, and all of them disappear here.
//
// What it costs: the dir is a fresh Claude Code home, so anything the user keeps
// in ~/.claude has to be brought across. Provision() does that — see there for
// what is shared and what deliberately isn't.

// accountsSubdir is where per-account config dirs live, under fleet's own
// config directory rather than beside ~/.claude — these are fleet's to create
// and delete, and mixing them into the user's home would make that ambiguous.
const accountsSubdir = "accounts"

// AccountsRoot is ~/.config/fleet/accounts.
func AccountsRoot() string {
	return filepath.Join(filepath.Dir(DefaultPath()), accountsSubdir)
}

// NewConfigDirPath returns a fresh, unused config-dir path for an account.
//
// The name is random rather than derived from the email, and it is stored
// verbatim on the Account. The Keychain item is keyed by a hash of this path,
// so deriving it from anything the user can change — an email, a label — would
// mean a rename silently orphans the login. A random id can't drift.
func NewConfigDirPath() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return filepath.Join(AccountsRoot(), hex.EncodeToString(b[:])), nil
}

// sharedDirs are symlinked from the user's real ~/.claude into every account
// dir, so switching account never means losing your own setup.
//
// projects/ is the load-bearing one: it holds the conversation transcripts that
// `claude --resume` replays and that fleet reads for auto-naming. Sharing it is
// also what lets a session move between accounts and still resume the same
// conversation. Concurrent writers are not a new risk — fleet has always run
// many sessions against this one directory.
var sharedDirs = []string{"projects", "skills", "plugins", "agents", "commands", "ide"}

// provisionMu serializes Provision. Package-level rather than per-dir: it is
// held for a handful of syscalls on every session launch, and a map of mutexes
// would be more machinery than the contention justifies.
var provisionMu sync.Mutex

// Provision makes dir usable as a Claude Code home for a fleet session.
//
// Idempotent, and called on every launch rather than only at add time: the
// user's ~/.claude keeps moving — a new MCP server, a new skill, an edited
// settings.json — and an account dir that captured its state once would drift
// into a subtly different environment from the one the user thinks they have.
//
// Deliberately NOT shared: .credentials / the Keychain item (the entire point),
// and history.jsonl (per-account by nature).
func Provision(dir string) error {
	// Serialized because the shared links are replaced, not written in place, so
	// two provisions of the same dir have a window where the link is gone.
	// reloadAll fans out per session, and two sessions on one account provision
	// concurrently: one removes projects/ and recreates it while the other, having
	// already Lstat'd, removes the fresh link and symlinks over it. Whoever loses
	// gets EEXIST — but the worse outcome is the gap itself, since a
	// `claude --resume` starting inside it finds no transcript and opens an empty
	// conversation. Same reasoning (and same remedy) as GitWorktreeProvider's
	// mutex around concurrent removes.
	provisionMu.Lock()
	defer provisionMu.Unlock()

	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	realClaude := filepath.Join(home, ".claude")

	var firstErr error
	for _, name := range sharedDirs {
		src := filepath.Join(realClaude, name)
		if _, err := os.Stat(src); err != nil {
			continue // the user doesn't have this one; nothing to share
		}
		dst := filepath.Join(dir, name)
		// Replace only a symlink we own. A real directory here means something
		// wrote into the account dir directly, and deleting it would destroy
		// data we didn't create.
		if fi, err := os.Lstat(dst); err == nil {
			if fi.Mode()&os.ModeSymlink == 0 {
				debuglog.Logger.Warn("account dir holds a real directory where a shared link belongs; leaving it",
					"dir", dir, "entry", name)
				continue
			}
			_ = os.Remove(dst)
		}
		// Recorded and carried on, not returned. A failure on one link used to
		// skip both mirrors below — so a single bad symlink cost the account dir
		// its copy of settings.json, which is what carries fleet's hooks, and
		// sessionEnv launches anyway. A partial provision that still refreshes
		// hooks is strictly better than one that doesn't; the error still comes
		// back at the end.
		if err := os.Symlink(src, dst); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if err := mirrorSettings(realClaude, dir); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := mirrorClaudeJSON(home, dir); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// mirrorSettings copies ~/.claude/settings.json into the account dir.
//
// Copied, not symlinked: fleet's hook injector rewrites settings.json in place,
// and a symlink would make every account dir a second path to the same file —
// harmless today, but it would silently turn a per-account settings change into
// a global one the moment anyone wanted per-account settings.
//
// The copy is what carries fleet's own hooks, so status detection works exactly
// as it does on the ambient login.
func mirrorSettings(realClaude, dir string) error {
	data, err := os.ReadFile(filepath.Join(realClaude, "settings.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.WriteFile(filepath.Join(dir, "settings.json"), data, 0600)
}

// firstRunKeys are the "you have already been asked this" flags in
// ~/.claude.json. Copied into every account dir so a login pane opens on a
// prompt rather than on the theme picker.
//
// An allowlist rather than "everything except oauthAccount": most of that file
// is caches, counters and experiment state that belong to the installation that
// wrote them, and carrying it wholesale would be guessing. These are settled
// answers.
var firstRunKeys = []string{
	"theme",
	"hasCompletedOnboarding",
	"lastOnboardingVersion",
	"installMethod",
	"autoUpdates",
	"shiftEnterKeyBindingInstalled",
	"hasIdeOnboardingBeenShown",
	"hasCompletedClaudeInChromeOnboarding",
	"editorMode",
	"diffTool",
	"todoFeatureEnabled",
	"verbose",
	"messageIdleNotifThresholdMs",
}

// mirrorClaudeJSON carries the parts of ~/.claude.json that a session needs but
// that a fresh config dir would not have.
//
// This file sits beside ~/.claude rather than inside it and moves with
// CLAUDE_CONFIG_DIR too, which means a new account dir starts with no MCP
// servers and no record that the user trusts any of their own folders. Left
// alone that shows up as "my MCP servers vanished" and a trust prompt on every
// session — both of which read as fleet breaking Claude Code.
//
// The first-run keys are carried for the same reason. A fresh config dir is a
// fresh Claude Code install as far as Claude Code is concerned, so it opens on
// the theme picker and the welcome flow — questions the user has already
// answered, asked again inside a pane whose only job is a login. Preferences
// are not per-login, so copying them is honest as well as convenient.
//
// oauthAccount is deliberately NOT copied: that one really is per-login, and
// claiming an identity the dir hasn't authenticated as would be lying to Claude
// Code about who is logged in.
func mirrorClaudeJSON(home, dir string) error {
	src, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var user map[string]any
	if err := json.Unmarshal(src, &user); err != nil {
		debuglog.Logger.Warn("could not parse ~/.claude.json; account dir gets no MCP servers", "err", err)
		return nil
	}

	path := filepath.Join(dir, ".claude.json")
	acct := map[string]any{}
	if existing, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(existing, &acct); err != nil {
			// A corrupt file here would otherwise be preserved forever by the
			// merge below; starting clean costs only the mirrored keys.
			acct = map[string]any{}
		}
	}

	if mcp, ok := user["mcpServers"]; ok {
		acct["mcpServers"] = mcp
	}
	// Answers the user has already given. Without these the login pane opens on
	// the theme picker and the welcome flow instead of a prompt they can type
	// /login into.
	for _, k := range firstRunKeys {
		if v, ok := user[k]; ok {
			acct[k] = v
		}
	}
	// Folder trust: carried per project, and only the trust flag. Copying whole
	// project entries would drag conversation pointers and per-project history
	// into an account that never had them.
	if projects, ok := user["projects"].(map[string]any); ok {
		out, _ := acct["projects"].(map[string]any)
		if out == nil {
			out = map[string]any{}
		}
		for path, raw := range projects {
			p, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			trusted, ok := p["hasTrustDialogAccepted"]
			if !ok {
				continue
			}
			entry, _ := out[path].(map[string]any)
			if entry == nil {
				entry = map[string]any{}
			}
			entry["hasTrustDialogAccepted"] = trusted
			// Claude Code re-prompts unless it also believes onboarding for the
			// project is done; the two are checked together.
			if v, ok := p["hasCompletedProjectOnboarding"]; ok {
				entry["hasCompletedProjectOnboarding"] = v
			}
			out[path] = entry
		}
		acct["projects"] = out
	}

	// Pre-trust the account dir itself. The login pane runs there, and Claude
	// Code asks about its working directory before it will do anything — so
	// without this the pane opens on a security question instead of a prompt.
	// Safe to assert: fleet created this directory moments ago and it holds
	// nothing but symlinks fleet made.
	out, _ := acct["projects"].(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	out[dir] = map[string]any{
		"hasTrustDialogAccepted":        true,
		"hasCompletedProjectOnboarding": true,
	}
	acct["projects"] = out

	data, err := json.MarshalIndent(acct, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// RemoveConfigDir deletes an account's config dir.
//
// The Keychain item is deliberately left behind: it is keyed by a hash of this
// path, so it is already unreachable, and `security` prompts on delete in some
// configurations — which would turn removing an account into a modal the user
// didn't ask for.
func RemoveConfigDir(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) || filepath.Dir(dir) != AccountsRoot() {
		// Refuse anything that isn't ours. This function takes a path from a
		// JSON file and hands it to RemoveAll.
		return os.ErrInvalid
	}
	return os.RemoveAll(dir)
}
