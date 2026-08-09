// Package claudeaccount manages multiple Claude subscriptions so sessions can
// run under different accounts in parallel.
//
// The mechanism is one environment variable per tmux session: CLAUDE_CONFIG_DIR
// (ConfigDirEnvVar), pointing at a directory that holds that account's own
// claude.ai login. Each session authenticates exactly as a normal `claude` in a
// terminal does, with no credential layered over another — which is what keeps
// claude.ai connectors, Remote Control and the usage endpoint working.
//
// See configdir.go for why this replaced an earlier ANTHROPIC_AUTH_TOKEN
// implementation, and what Provision shares back from the user's real ~/.claude
// so an account dir isn't a stripped-down home.
package claudeaccount

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/brizzai/fleet/internal/debuglog"
)

// Account is one Claude subscription fleet can launch sessions under.
//
// Email is the key and it is a real email address, read from `claude auth
// status` once the account is logged in. Worth stating because the earlier
// token-based implementation could not do it: a `claude setup-token` credential
// refuses to name its owner, so accounts were keyed by a hash of the token, and
// re-running setup-token minted a second identity for one subscription that
// orphaned every session pinned to the old one.
type Account struct {
	Email string `json:"email"`
	Plan  string `json:"plan,omitempty"`  // "max", "pro", … — display only
	Label string `json:"label,omitempty"` // optional rename; display prefers it

	// ConfigDir is this account's Claude Code home, holding its own login.
	// Stored verbatim rather than derived: the Keychain item is keyed by a hash
	// of this exact path, so recomputing it from anything the user can change
	// would orphan the login on a rename.
	ConfigDir string `json:"config_dir"`

	Order int `json:"order"` // waterfall order; also the tie-break for least-used

	// OrgUUID and OrgName identify the subscription behind the login. The org
	// is what Upsert matches on, so logging the same subscription in a second
	// time updates the existing account rather than duplicating it.
	OrgUUID string `json:"org_uuid,omitempty"`
	OrgName string `json:"org_name,omitempty"`

	// initialUsage carries the reading taken while adding the account, so it
	// doesn't wait a poll interval for its first number. Not persisted — quota
	// is live state, not configuration.
	initialUsage Usage
}

// FromIdentity builds an account record from a resolved login.
func FromIdentity(id Identity, dir string) Account {
	return Account{
		Email:     id.Email,
		Plan:      id.Plan,
		ConfigDir: dir,
		OrgUUID:   id.OrgUUID,
		OrgName:   id.OrgName,
	}
}

// WithUsage attaches a reading taken while adding the account, so its first row
// shows a number instead of waiting out a poll interval.
func WithUsage(a Account, u Usage) Account {
	a.initialUsage = u
	return a
}

// InitialUsage returns the quota reading taken while adding the account, if any.
func (a Account) InitialUsage() Usage { return a.initialUsage }

// Name is what the UI shows: the user's label if they set one, else the email.
func (a Account) Name() string {
	if a.Label != "" {
		return a.Label
	}
	return a.Email
}

// Store is the on-disk set of accounts.
//
// Holds no credentials: those live in the macOS Keychain, one item per account
// config dir, written by Claude Code's own login. This file records only which
// directories exist and who is logged into them, which is why it survived the
// move off tokens almost unchanged while the security story got much smaller.
type Store struct {
	mu       sync.RWMutex
	accounts []Account
}

// DefaultPath is ~/.config/fleet/accounts.json.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fleet", "accounts.json")
}

// Load reads the store from disk. A missing or unparseable file yields an empty
// store rather than an error — with no accounts configured fleet sets no env
// var and behaves exactly as it did before this feature, so "no accounts" is a
// valid state, not a failure.
func Load() *Store {
	s := &Store{}
	path := DefaultPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Not an error — this is the single-subscription default. Logged so
			// "why is my account being ignored" has a first answer.
			debuglog.Logger.Info("no accounts file; sessions use the ambient claude login", "path", path)
		} else {
			debuglog.Logger.Error("failed to read accounts file", "path", path, "error", err)
		}
		return s
	}
	if err := json.Unmarshal(data, &s.accounts); err != nil {
		debuglog.Logger.Error("failed to parse accounts file", "path", path, "error", err)
		return &Store{}
	}
	// Drop anything without a config dir. That is exactly the shape written by
	// the earlier token-based implementation, and such an entry cannot run a
	// session: there is no login to point CLAUDE_CONFIG_DIR at, and the token it
	// used to carry is no longer read by anything.
	//
	// Dropped rather than kept, because a kept entry would still be a candidate
	// in Select and would hand new sessions an account that silently falls back
	// to the ambient login. Sessions naming it render as "your logged-in account
	// (its account was removed)", which is true and is the prompt to re-add it.
	kept := s.accounts[:0]
	for _, a := range s.accounts {
		if a.ConfigDir == "" {
			debuglog.Logger.Error("account has no config dir and cannot be used; log it in again with Ctrl+K → Manage Claude Accounts",
				"email", a.Email, "label", a.Label)
			continue
		}
		kept = append(kept, a)
	}
	s.accounts = kept

	// Identity only; there is no credential in this file.
	for i, a := range s.accounts {
		debuglog.Logger.Info("account loaded", "order", i, "email", a.Email, "plan", a.Plan,
			"label", a.Label, "org", a.OrgUUID)
	}
	debuglog.Logger.Info("accounts loaded", "count", len(s.accounts), "path", path)
	return s
}

// Save writes the store to disk at mode 0600.
func (s *Store) Save() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.accounts, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	path := DefaultPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		debuglog.Logger.Error("failed to create config directory", "path", path, "error", err)
		return err
	}
	// Temp-and-rename, not WriteFile: two goroutines reach here — the Update loop
	// when the user edits accounts, and the worker after a quota poll. Load
	// answers an unparseable file with an empty store, so a torn write would
	// silently drop every session back to the ambient login with nothing on
	// screen to say why. CreateTemp opens at 0600, same as the file it replaces.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".accounts-*.json")
	if err != nil {
		debuglog.Logger.Error("failed to stage accounts file", "path", path, "error", err)
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		debuglog.Logger.Error("failed to write accounts file", "path", path, "error", err)
		return err
	}
	// Count only — the emails are identity and stay out of the log line.
	debuglog.Logger.Info("accounts saved", "count", len(s.accounts))
	return nil
}

// The read methods below tolerate a nil receiver: "no store" and "no accounts
// configured" are the same state, and both mean sessions fall back to the
// ambient /login account. Callers therefore never need a nil check of their own.

// List returns the accounts ordered by Order, then email for stability.
func (s *Store) List() []Account {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Account, len(s.accounts))
	copy(out, s.accounts)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Email < out[j].Email
	})
	return out
}

// Len reports how many accounts are configured.
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.accounts)
}

// Get returns the account with the given email.
func (s *Store) Get(email string) (Account, bool) {
	if s == nil {
		return Account{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.accounts {
		if a.Email == email {
			return a, true
		}
	}
	return Account{}, false
}

// ConfigDirFor returns the config dir for an email, or "" if unknown. A caller
// that gets "" must fall back to the ambient login (set no env var) rather than
// failing — an account can legitimately disappear from the store while sessions
// still reference it.
func (s *Store) ConfigDirFor(email string) string {
	a, ok := s.Get(email)
	if !ok {
		return ""
	}
	return a.ConfigDir
}

// Upsert adds an account or updates the one it refers to, preserving that
// account's key, Order and Label so re-adding a token doesn't silently reorder
// the rotation, discard a rename, or mint a second identity for a subscription
// fleet already knows.
//
// Returns the key the account is stored under, which is NOT always a.Email — an
// org match keeps the existing key. Callers that index anything by account (the
// usage map, config) must use the returned key or they will write under a name
// nothing else reads.
func (s *Store) Upsert(a Account) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.indexOfLocked(a); i >= 0 {
		old := s.accounts[i]
		if a.Label == "" {
			a.Label = old.Label
		}
		// The stored key wins. Sessions, default_account and allowed_accounts all
		// reference an account by this string, so changing it orphans every one of
		// them — which is exactly what re-adding a rotated token used to do.
		a.Email, a.Order = old.Email, old.Order
		s.accounts[i] = a
		return a.Email
	}
	a.Order = len(s.accounts)
	s.accounts = append(s.accounts, a)
	return a.Email
}

// indexOfLocked finds the account a refers to, organization first.
//
// The org is the one identity that survives `claude setup-token` being run
// again; the key may not. An account the API declined to name is keyed by a
// hash of its token, so rotating that token used to mint a whole new account
// and leave every session pointed at a name that no longer existed — which
// reads in the UI as "your logged-in account (its account was removed)" and
// silently bills the ambient login on the next restart.
//
// Callers must hold s.mu.
func (s *Store) indexOfLocked(a Account) int {
	if a.OrgUUID != "" {
		for i := range s.accounts {
			if s.accounts[i].OrgUUID == a.OrgUUID {
				return i
			}
		}
	}
	for i := range s.accounts {
		if s.accounts[i].Email == a.Email {
			return i
		}
	}
	return -1
}

// Remove drops the account with the given email, reporting whether it existed.
func (s *Store) Remove(email string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.accounts {
		if s.accounts[i].Email == email {
			s.accounts = append(s.accounts[:i], s.accounts[i+1:]...)
			return true
		}
	}
	return false
}

// SetLabel renames an account for display.
func (s *Store) SetLabel(email, label string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.accounts {
		if s.accounts[i].Email == email {
			s.accounts[i].Label = strings.TrimSpace(label)
			return true
		}
	}
	return false
}

// Reorder moves an account delta positions in the waterfall order, shifting the
// accounts it passes rather than swapping with the one at the destination.
// Reports false if the account is unknown or the move would leave the list.
func (s *Store) Reorder(email string, delta int) bool {
	list := s.List()
	idx := -1
	for i, a := range list {
		if a.Email == email {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	target := idx + delta
	if target < 0 || target >= len(list) {
		return false
	}

	moved := list[idx]
	rest := make([]Account, 0, len(list)-1)
	rest = append(rest, list[:idx]...)
	rest = append(rest, list[idx+1:]...)

	ordered := make([]Account, 0, len(list))
	ordered = append(ordered, rest[:target]...)
	ordered = append(ordered, moved)
	ordered = append(ordered, rest[target:]...)

	pos := make(map[string]int, len(ordered))
	for i, a := range ordered {
		pos[a.Email] = i
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.accounts {
		if p, ok := pos[s.accounts[i].Email]; ok {
			s.accounts[i].Order = p
		}
	}
	return true
}

// tokenPattern matches Anthropic credentials broadly on purpose — oat01 (the
// setup-token OAuth token), ort01 (refresh), api03 (API key) and any future
// sibling. Redaction is the one place where over-matching is the safe error.
var tokenPattern = regexp.MustCompile(`sk-ant-[a-z0-9]{3,8}-[A-Za-z0-9_-]{16,}`)

// anyCredentialish is deliberately looser than tokenPattern: it eats anything
// beginning `sk-ant-`, however short or malformed. Used where whole captured
// text is logged, since a half-printed token is exactly the case tokenPattern's
// length floor lets through.
var anyCredentialish = regexp.MustCompile(`sk-ant-\S*`)

// Redact replaces any Anthropic credential in s with a placeholder.
//
// Required anywhere text can reach a public GitHub issue or the debug log: the
// add-account flow puts a token on a real tmux pane, and fleet publishes pane
// excerpts in bug reports. This mirrors the existing rule that a reporter's
// prompt never reaches an issue body.
func Redact(s string) string {
	return tokenPattern.ReplaceAllString(s, "sk-ant-<redacted>")
}

// RedactCaptured scrubs a whole captured screen for logging, using the looser
// pattern. Call this — never Redact — when the text is raw pane content, where
// a truncated or mid-print token would slip under Redact's length floor.
func RedactCaptured(s string) string {
	return anyCredentialish.ReplaceAllString(s, "sk-ant-<redacted>")
}
