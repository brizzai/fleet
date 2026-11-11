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

// The quota cache is the TUI's usage map on disk, so a one-shot `fleet worktree`
// / `fleet add` ranks on the same readings. The CLI never polls inside the floor
// the last poller set.

const (
	// UsageCacheMaxAge: a running TUI rewrites the cache every MinPollInterval,
	// so this tolerates one missed poll.
	UsageCacheMaxAge = 2 * MinPollInterval

	// CLIFetchTimeout bounds the CLI's whole quota read; a healthy one takes
	// under half a second.
	CLIFetchTimeout = 3 * time.Second
)

// UsageCachePath is ~/.config/fleet/account_usage.json.
func UsageCachePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fleet", "account_usage.json")
}

// cacheEntry never holds a credential or error text; a failure is stored as its
// FailureReason.
type cacheEntry struct {
	FiveHourPct   int       `json:"five_hour_pct"`
	FiveHourReset time.Time `json:"five_hour_reset,omitzero"`
	SevenDayPct   int       `json:"seven_day_pct"`
	SevenDayReset time.Time `json:"seven_day_reset,omitzero"`
	FetchedAt     time.Time `json:"fetched_at,omitzero"`
	AttemptedAt   time.Time `json:"attempted_at,omitzero"`
	Failure       string    `json:"failure,omitempty"`
	LoggedOut     bool      `json:"logged_out,omitempty"`
	Mismatch      string    `json:"mismatch,omitempty"`
}

type cachedFailure string

func (c cachedFailure) Error() string { return string(c) }

// LoadUsageCache reads the cache; missing or unreadable means empty.
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
			LoggedOut: e.LoggedOut, Mismatch: e.Mismatch,
		}
		if e.Failure != "" {
			u.Err = cachedFailure(e.Failure)
		}
		out[email] = u
	}
	return out
}

// SaveUsageCache merges readings in, keeping per account the later attempt.
func SaveUsageCache(path string, readings map[string]Usage) error {
	merged := LoadUsageCache(path)
	for email, u := range readings {
		if prev, ok := merged[email]; ok && prev.AttemptedAt.After(u.AttemptedAt) {
			continue
		}
		merged[email] = u
	}
	return writeUsageCache(path, merged)
}

// ResetCachedUsage replaces one account's entry after a re-login: with the
// login's own reading when it has one, else by dropping it, so a cached
// logged-out verdict can't outlive the fix.
func ResetCachedUsage(path, email string, u Usage) error {
	m := LoadUsageCache(path)
	if u.Known() {
		m[email] = u
	} else if _, ok := m[email]; ok {
		delete(m, email)
	} else {
		return nil
	}
	return writeUsageCache(path, m)
}

func writeUsageCache(path string, m map[string]Usage) error {
	raw := make(map[string]cacheEntry, len(m))
	for email, u := range m {
		e := cacheEntry{
			FiveHourPct: u.FiveHourPct, FiveHourReset: u.FiveHourReset,
			SevenDayPct: u.SevenDayPct, SevenDayReset: u.SevenDayReset,
			FetchedAt: u.FetchedAt, AttemptedAt: u.AttemptedAt,
			LoggedOut: u.LoggedOut, Mismatch: u.Mismatch,
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

// PollDue is the per-account poll throttle, shared by the TUI and the CLI.
func (u Usage) PollDue(now time.Time) bool {
	return now.Sub(u.AttemptedAt) >= MinPollInterval || u.StaleAfterReset(now)
}

// WithFailure records a failed poll, keeping the last numbers.
func (u Usage) WithFailure(err error, now time.Time) Usage {
	u.Err = err
	u.AttemptedAt = now
	u.LoggedOut = errors.Is(err, ErrNotLoggedIn)
	return u
}

// FailureReason names why a reading is missing, in words safe to print.
func FailureReason(err error) string {
	var c cachedFailure
	var se statusError
	switch {
	case err == nil:
		return "no recent reading"
	case errors.As(err, &c):
		return string(c)
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, ErrNotLoggedIn):
		return "not logged in"
	case errors.Is(err, ErrNoCredential):
		return "couldn't read its Keychain credential"
	case errors.As(err, &se):
		return se.Error()
	}
	return "usage request failed"
}

type statusError int

func (s statusError) Error() string { return fmt.Sprintf("usage endpoint returned %d", int(s)) }

// Unread is an account whose quota couldn't rank it, and why.
type Unread struct {
	Email  string
	Reason string
}

type LaunchUsageOpts struct {
	Accounts  []Account
	CachePath string
	Now       time.Time
	Timeout   time.Duration
	Fetch     func(ctx context.Context, dir string) (Usage, error) // default FetchUsage
}

// UsageForLaunch returns the readings a one-shot command hands to Select, and
// the accounts it couldn't read. Readings attempted within the poll floor come
// from the cache; the rest are fetched in parallel under one Timeout. A reading
// older than UsageCacheMaxAge is dropped unless it shows the account spent.
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
	var due []Account
	for _, a := range o.Accounts {
		if prev, ok := cached[a.Email]; ok && !prev.PollDue(now) {
			usage[a.Email] = prev
		} else {
			due = append(due, a)
		}
	}

	results := make([]Usage, len(due))
	fctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var wg sync.WaitGroup
	for i, a := range due {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, err := fetch(fctx, a.ConfigDir)
			if err != nil {
				// A killed `security` doesn't report the deadline itself.
				if fctx.Err() != nil && !errors.Is(err, ErrNotLoggedIn) {
					err = fctx.Err()
				}
				results[i] = cached[a.Email].WithFailure(err, now)
				return
			}
			u.AttemptedAt = now
			results[i] = u
		}()
	}
	wg.Wait()
	if len(due) > 0 {
		fetched := make(map[string]Usage, len(due))
		for i, a := range due {
			usage[a.Email] = results[i]
			fetched[a.Email] = results[i]
		}
		_ = SaveUsageCache(o.CachePath, fetched)
	}

	var unread []Unread
	for _, a := range o.Accounts {
		u := usage[a.Email]
		fresh := u.Known() && now.Sub(u.FetchedAt) <= UsageCacheMaxAge
		if u.Known() && !fresh && !u.Exhausted(now) {
			usage[a.Email] = Usage{AttemptedAt: u.AttemptedAt, Err: u.Err, LoggedOut: u.LoggedOut, Mismatch: u.Mismatch}
		}
		switch {
		case u.LoggedOut:
			unread = append(unread, Unread{Email: a.Email, Reason: "not logged in"})
		case u.Mismatch != "":
			unread = append(unread, Unread{Email: a.Email, Reason: "logged in as " + u.Mismatch})
		case !fresh:
			unread = append(unread, Unread{Email: a.Email, Reason: FailureReason(u.Err)})
		}
	}
	return usage, unread
}
