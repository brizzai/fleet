// Package claudeaccount manages multiple Claude subscriptions so sessions can
// run under different accounts in parallel.
//
// The mechanism is one environment variable per tmux session:
// CLAUDE_CODE_OAUTH_TOKEN, a year-long token minted by `claude setup-token`.
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
// Email is the identity: it comes from `claude auth status --json` rather than
// the user, so it is never typed and never wrong, and re-adding an account
// after a token expires keeps existing sessions pointed at it. Label is the
// separate, renameable display name.
type Account struct {
	Email string `json:"email"`
	Plan  string `json:"plan,omitempty"` // "max", "pro", … — display only
	Label string `json:"label,omitempty"`
	Token string `json:"token"`
	Order int    `json:"order"` // waterfall order; also the tie-break for least-used
}

// Name is what the UI shows: the user's label if they set one, else the email.
func (a Account) Name() string {
	if a.Label != "" {
		return a.Label
	}
	return a.Email
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
	if err := os.WriteFile(path, data, 0600); err != nil {
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

// Upsert adds an account or replaces the existing one with the same email,
// preserving that account's Order and Label so re-adding an expired token
// doesn't silently reorder the rotation or discard a rename.
func (s *Store) Upsert(a Account) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.accounts {
		if s.accounts[i].Email == a.Email {
			a.Order = s.accounts[i].Order
			if a.Label == "" {
				a.Label = s.accounts[i].Label
			}
			s.accounts[i] = a
			return
		}
	}
	a.Order = len(s.accounts)
	s.accounts = append(s.accounts, a)
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

// Redact replaces any Anthropic credential in s with a placeholder.
//
// Required anywhere text can reach a public GitHub issue or the debug log: the
// add-account flow puts a token on a real tmux pane, and fleet publishes pane
// excerpts in bug reports. This mirrors the existing rule that a reporter's
// prompt never reaches an issue body.
func Redact(s string) string {
	return tokenPattern.ReplaceAllString(s, "sk-ant-<redacted>")
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
