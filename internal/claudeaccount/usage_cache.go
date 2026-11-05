package claudeaccount

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The quota cache lets a one-shot `fleet worktree` / `fleet add` rank accounts
// the way the TUI does without a TUI running. It is the TUI's in-memory usage
// map, written to disk after each poll, and the CLI reads and writes the same
// file. One reading, two readers: nothing here measures or ranks differently.
//
// It also keeps the two from polling past MinPollInterval between them — the
// endpoint 429s persistently once tripped, and a launchd job firing a burst of
// worktrees would otherwise poll once per worktree.

const (
	// UsageCacheMaxAge is how old a reading may be and still rank an account
	// when a fresh one can't be had. Two poll intervals: a running TUI rewrites
	// the cache every MinPollInterval, so this tolerates one missed poll, and
	// anything older means nothing has been polling.
	UsageCacheMaxAge = 2 * MinPollInterval

	// CLIFetchTimeout bounds the CLI's whole quota read (Keychain + HTTP, all
	// accounts in parallel). A healthy read takes well under a second; the TUI's
	// 10s httpTimeout suits a background worker, not a command a script waits on.
	CLIFetchTimeout = 3 * time.Second
)

// UsageCachePath is ~/.config/fleet/account_usage.json.
func UsageCachePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fleet", "account_usage.json")
}

// cacheEntry holds numbers and timestamps only — never a credential. A failed
// attempt is stored as its FailureReason, not its error text.
type cacheEntry struct {
	FiveHourPct   int       `json:"five_hour_pct"`
	FiveHourReset time.Time `json:"five_hour_reset,omitzero"`
	SevenDayPct   int       `json:"seven_day_pct"`
	SevenDayReset time.Time `json:"seven_day_reset,omitzero"`
	FetchedAt     time.Time `json:"fetched_at,omitzero"`
	AttemptedAt   time.Time `json:"attempted_at,omitzero"`
	Failure       string    `json:"failure,omitempty"`
	LoggedOut     bool      `json:"logged_out,omitempty"`
}

// cachedFailure stands in for an error read back from the cache.
type cachedFailure string

func (c cachedFailure) Error() string { return string(c) }

// LoadUsageCache reads the cache. Missing or unreadable means empty: quota is an
// optimization, so a bad file costs the reading and nothing else.
func LoadUsageCache(path string) map[string]Usage {
	out := map[string]Usage{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var raw map[string]cacheEntry
	if json.Unmarshal(data, &raw) != nil {
		return out
	}
	for email, e := range raw {
		u := Usage{
			FiveHourPct: e.FiveHourPct, FiveHourReset: e.FiveHourReset,
			SevenDayPct: e.SevenDayPct, SevenDayReset: e.SevenDayReset,
			FetchedAt: e.FetchedAt, AttemptedAt: e.AttemptedAt,
			LoggedOut: e.LoggedOut,
		}
		if e.Failure != "" {
			u.Err = cachedFailure(e.Failure)
		}
		out[email] = u
	}
	return out
}

// SaveUsageCache merges readings into the cache, keeping per account whichever
// entry was attempted later, so a TUI and a CLI writing at once can't replace a
// newer reading with an older one.
func SaveUsageCache(path string, readings map[string]Usage) error {
	merged := LoadUsageCache(path)
	for email, u := range readings {
		if prev, ok := merged[email]; ok && prev.AttemptedAt.After(u.AttemptedAt) {
			continue
		}
		merged[email] = u
	}
	raw := make(map[string]cacheEntry, len(merged))
	for email, u := range merged {
		e := cacheEntry{
			FiveHourPct: u.FiveHourPct, FiveHourReset: u.FiveHourReset,
			SevenDayPct: u.SevenDayPct, SevenDayReset: u.SevenDayReset,
			FetchedAt: u.FetchedAt, AttemptedAt: u.AttemptedAt,
			LoggedOut: u.LoggedOut,
		}
		if u.Err != nil {
			e.Failure = FailureReason(u.Err)
		}
		raw[email] = e
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".account_usage-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// PollDue reports whether this account may be polled again: the TUI's throttle,
// shared so the CLI keeps the same floor.
func (u Usage) PollDue(now time.Time) bool {
	return now.Sub(u.AttemptedAt) >= MinPollInterval || u.StaleAfterReset(now)
}

// WithFailure records a failed poll. The numbers are kept — a reading from a
// few minutes ago beats none — and only the attempt clock moves.
func (u Usage) WithFailure(err error, now time.Time) Usage {
	u.Err = err
	u.AttemptedAt = now
	u.LoggedOut = errors.Is(err, ErrNotLoggedIn)
	return u
}

// FailureReason names why a poll failed, in words safe to print. It never
// echoes error text from the HTTP or Keychain layers.
func FailureReason(err error) string {
	var c cachedFailure
	switch {
	case err == nil:
		return ""
	case errors.As(err, &c):
		return string(c)
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, ErrNotLoggedIn):
		return "not logged in"
	case errors.Is(err, ErrNoCredential):
		return "couldn't read its Keychain credential"
	}
	var se statusError
	if errors.As(err, &se) {
		return fmt.Sprintf("usage endpoint returned %d", int(se))
	}
	return "usage request failed"
}

// statusError is a non-200, non-auth reply from the usage endpoint.
type statusError int

func (s statusError) Error() string { return fmt.Sprintf("usage endpoint returned %d", int(s)) }

// Unread is an account whose quota couldn't rank it, and why.
type Unread struct {
	Email  string
	Reason string
	// LoggedOut accounts are skipped by Select rather than ranked as unknown.
	LoggedOut bool
}

// LaunchUsageOpts are the inputs to UsageForLaunch.
type LaunchUsageOpts struct {
	Accounts  []Account
	CachePath string
	Now       time.Time
	Timeout   time.Duration
	// Fetch defaults to FetchUsage; tests replace it.
	Fetch func(ctx context.Context, dir string) (Usage, error)
}

// UsageForLaunch produces the usage map a one-shot command hands to Select.
//
// Per account: a cached reading attempted within MinPollInterval is used as is;
// otherwise it is fetched, all accounts in parallel under one Timeout. The
// cache is updated with every attempt. Readings older than UsageCacheMaxAge are
// then dropped, so Select scores those accounts as unknown, and each such
// account is returned in unread with the reason.
func UsageForLaunch(ctx context.Context, o LaunchUsageOpts) (map[string]Usage, []Unread) {
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	fetch := o.Fetch
	if fetch == nil {
		fetch = FetchUsage
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = CLIFetchTimeout
	}
	cached := LoadUsageCache(o.CachePath)

	usage := make(map[string]Usage, len(o.Accounts))
	fetched := map[string]Usage{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	fctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for _, a := range o.Accounts {
		prev, had := cached[a.Email]
		if had && !prev.PollDue(now) {
			usage[a.Email] = prev
			continue
		}
		wg.Add(1)
		go func(a Account, prev Usage) {
			defer wg.Done()
			u, err := fetch(fctx, a.ConfigDir)
			if err != nil {
				u = prev.WithFailure(err, now)
			} else {
				u.AttemptedAt = now
			}
			mu.Lock()
			usage[a.Email] = u
			fetched[a.Email] = u
			mu.Unlock()
		}(a, prev)
	}
	wg.Wait()
	if len(fetched) > 0 {
		_ = SaveUsageCache(o.CachePath, fetched)
	}

	var unread []Unread
	for _, a := range o.Accounts {
		u := usage[a.Email]
		age := now.Sub(u.FetchedAt)
		switch {
		case u.LoggedOut:
			unread = append(unread, Unread{Email: a.Email, Reason: "not logged in", LoggedOut: true})
		case u.Known() && age <= UsageCacheMaxAge:
			continue
		case u.Err != nil:
			unread = append(unread, Unread{Email: a.Email, Reason: FailureReason(u.Err)})
		case u.Known():
			unread = append(unread, Unread{Email: a.Email, Reason: fmt.Sprintf("last reading is %s old", age.Round(time.Minute))})
		default:
			unread = append(unread, Unread{Email: a.Email, Reason: "never read"})
		}
		if u.Known() && age > UsageCacheMaxAge {
			usage[a.Email] = Usage{AttemptedAt: u.AttemptedAt, Err: u.Err, LoggedOut: u.LoggedOut}
		}
	}
	return usage, unread
}
