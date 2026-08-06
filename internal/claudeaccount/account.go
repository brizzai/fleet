// Package claudeaccount manages multiple Claude subscriptions so sessions can
// run under different accounts in parallel.
//
// The mechanism is one environment variable per tmux session (AuthEnvVar,
// see client.go for why it is not the obvious one), carrying a year-long token
// minted by `claude setup-token`.
// It overrides authentication ONLY — ~/.claude stays shared, so hooks,
// projects/ (auto-naming and --resume), skills, plugins and local MCP servers
// keep working untouched. CLAUDE_CONFIG_DIR was deliberately rejected: it forks
// all of that and would force per-account hook injection.
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
// Email is the key, and it is only sometimes an actual email. A `claude
// setup-token` credential is inference-only by design and will not say who it
// belongs to, so most accounts are keyed by a fingerprint of the token and
// carry a user-supplied Label. The exception is an account whose organization
// matches the login cached on this machine — that one names itself. See
// identity.go.
//
// Whatever the key is, it is stable across re-adds of the same token, which is
// what keeps existing sessions pointed at the right account.
type Account struct {
	Email string `json:"email"`
	Plan  string `json:"plan,omitempty"` // "max", "pro", … — display only
	Label string `json:"label,omitempty"`
	Token string `json:"token"`
	Order int    `json:"order"` // waterfall order; also the tie-break for least-used

	// OrgUUID is the organization the token belongs to, from the
	// anthropic-organization-id response header. Not an email, but a stable
	// identity — and the thing that lets fleet recognise a token as the
	// account already logged in on this machine.
	OrgUUID string `json:"org_uuid,omitempty"`

	// UsageEndpointForbidden records that /api/oauth/usage refuses this token
	// on scope, so quota must come from the probe instead. Permanent for a
	// given token; persisted so every restart doesn't re-ask, and so a
	// transient 429 can't be mistaken for the scope answer and restart the
	// retry loop.
	UsageEndpointForbidden bool `json:"usage_endpoint_forbidden,omitempty"`

	// initialUsage carries the reading taken while validating, so adding an
	// account doesn't spend a second probe seconds later. Not persisted —
	// quota is live state, not configuration.
	initialUsage Usage
}

// InitialUsage returns the quota reading taken during validation, if any.
func (a Account) InitialUsage() Usage { return a.initialUsage }

// FingerprintPrefix marks an account the API declined to identify, whose key is
// a hash of its token rather than an email.
const FingerprintPrefix = "account-"

// Name is what the UI shows: the user's label if they set one, else the email.
func (a Account) Name() string {
	if a.Label != "" {
		return a.Label
	}
	return a.Email
}

// NeedsLabel reports that this account has no human-meaningful name — the API
// wouldn't identify the token and the user hasn't labelled it. Callers use this
// to ask for a name at the moment the user still knows which account it is.
func (a Account) NeedsLabel() bool {
	return a.Label == "" && strings.HasPrefix(a.Email, FingerprintPrefix)
}

// Store is the on-disk set of accounts.
//
// Deliberately a separate file from config.json rather than a field in it:
// the bug-report flow publishes config.json into public GitHub issues, and
// these tokens are year-long credentials. Nothing in the diagnostics path
// reads this file. See Redact for the matching rule on logs and pane excerpts.
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
	// Emails and plans only — never the tokens.
	for i, a := range s.accounts {
		debuglog.Logger.Info("account loaded", "order", i, "email", a.Email, "plan", a.Plan,
			"label", a.Label, "has_token", a.Token != "")
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
	// Temp-and-rename, not WriteFile: two goroutines reach here now — the Update
	// loop when the user edits accounts, and the worker when a quota poll
	// backfills an organization. A torn write costs every token in the file, and
	// Load answers an unparseable file with an empty store, so every session
	// would fall back to the ambient login with nothing on screen to say why.
	// CreateTemp opens at 0600, same as the file it replaces.
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
	// Count only — never the emails or tokens themselves.
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

// TokenFor returns the token for an email, or "" if unknown. A caller that gets
// "" must fall back to the ambient login (set no env var) rather than failing —
// an account can legitimately disappear from the store while sessions still
// reference it.
func (s *Store) TokenFor(email string) string {
	a, ok := s.Get(email)
	if !ok {
		return ""
	}
	return a.Token
}

// Upsert adds an account or updates the one it refers to, preserving that
// account's key, Order and Label so re-adding a token doesn't silently reorder
// the rotation, discard a rename, or mint a second identity for a subscription
// fleet already knows.
//
// QuotaUnavailable is deliberately NOT carried over: it is a verdict about a
// specific token, and a replacement deserves its own. Inheriting it would
// silently disable quota forever if a future token did carry the scope, where
// clearing it costs one HTTP call that re-marks it immediately.
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
		// An email fleet has only just managed to resolve is worth showing, but
		// must not become the key. Label is where display already looks first.
		if a.Label == "" && a.Email != old.Email && !strings.HasPrefix(a.Email, FingerprintPrefix) {
			a.Label = a.Email
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

// SetOrgUUID records the organization behind an account, so one added before
// its org was readable still gains the identity that survives a token
// rotation. Reports whether this was new information.
//
// Only ever fills a blank: an account whose org came back *different* is a
// different subscription, and quietly re-pointing one at the other is the
// mis-billing this whole mechanism exists to prevent.
func (s *Store) SetOrgUUID(email, org string) bool {
	if org == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.accounts {
		if s.accounts[i].Email != email {
			continue
		}
		if s.accounts[i].OrgUUID != "" {
			return false
		}
		s.accounts[i].OrgUUID = org
		return true
	}
	return false
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

// MarkUsageEndpointForbidden records that an account's token can never read quota,
// so nothing asks again. Reports whether this was new information.
func (s *Store) MarkUsageEndpointForbidden(email string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.accounts {
		if s.accounts[i].Email == email {
			if s.accounts[i].UsageEndpointForbidden {
				return false
			}
			s.accounts[i].UsageEndpointForbidden = true
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

// ExtractToken pulls the first Anthropic token out of arbitrary text, which is
// how the add-account flow reads the result of `claude setup-token` off its
// pane. It matches the token's own format rather than Claude's wording, so
// rephrasing the surrounding prompt doesn't break it.
func ExtractToken(s string) (string, bool) {
	m := tokenPattern.FindString(s)
	if m == "" {
		return "", false
	}
	return m, true
}
