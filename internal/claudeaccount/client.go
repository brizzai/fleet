package claudeaccount

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

const (
	// usageEndpoint reports per-token quota and, importantly, consumes no
	// message quota itself. It is undocumented — Anthropic closed the tracking
	// issue as not-planned — so every caller must degrade gracefully rather
	// than treat a failure here as fatal.
	usageEndpoint = "https://api.anthropic.com/api/oauth/usage"

	// MinPollInterval is the floor between polls of one account. The endpoint
	// rate-limits hard and, once tripped, stays tripped for a long time even if
	// the caller backs off — so this is a floor, not a target.
	MinPollInterval = 180 * time.Second

	// ExhaustedPct is the utilization at which an account is treated as spent.
	// Not 100: the last percent is unusable in practice and rounding means a
	// truly empty bucket can report 99.
	ExhaustedPct = 98

	httpTimeout = 10 * time.Second
	execTimeout = 30 * time.Second
)

// ErrNoToken is returned when an operation needs a token and none was given.
var ErrNoToken = errors.New("no token")

// profileEndpoint identifies the account behind a token.
//
// `claude auth status --json` cannot do this job, which is worth recording
// because it is the obvious thing to reach for. With CLAUDE_CODE_OAUTH_TOKEN
// set it answers only {"loggedIn":true,"authMethod":"oauth_token"} — no email,
// no plan, and `loggedIn:true` even for a string of A's. It verifies nothing
// and identifies nothing, so it is useless both as a liveness check and as a
// source of names.
const profileEndpoint = "https://api.anthropic.com/api/oauth/profile"

// Validate checks a token is live and identifies the account behind it.
//
// Liveness is the HTTP status: the endpoints reject a bad bearer with 401/403,
// which is the only real verification available (see profileEndpoint for why
// the CLI cannot do it). Identity is best-effort on top — a valid token whose
// owner we cannot name is still a usable account, so it gets a stable
// fingerprint name rather than being refused.
func Validate(ctx context.Context, token string) (Account, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Account{}, ErrNoToken
	}

	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	// Token length only — never the token. Length is enough to tell a truncated
	// paste from a whole one, which is the common failure.
	debuglog.Logger.Info("validating claude account token", "token_len", len(token), "timeout", execTimeout)

	body, status, err := getWithToken(ctx, profileEndpoint, token)
	if err != nil {
		return Account{}, fmt.Errorf("could not reach Anthropic to check the token: %w", err)
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		debuglog.Logger.Error("token rejected by anthropic", "status", status)
		return Account{}, errors.New("token was rejected — generate a fresh one with `claude setup-token`")
	case status != http.StatusOK:
		// Some other failure (rate limit, outage). Fall back to the usage
		// endpoint purely as a liveness probe before giving up: refusing a
		// working token because one endpoint hiccuped is the worse error.
		debuglog.Logger.Warn("profile endpoint unavailable, falling back to usage probe", "status", status)
		if _, err := FetchUsage(ctx, token); err != nil {
			return Account{}, fmt.Errorf("could not verify the token: %w", err)
		}
		body = nil
	}

	// The response shape is undocumented, so log it (redacted) — this is how we
	// learn what identity fields exist without guessing at a struct.
	if len(body) > 0 {
		debuglog.Logger.Info("account profile response", "body", Redact(strings.TrimSpace(string(body))))
	}

	email, plan := identityFrom(body)
	if email == "" {
		// Valid but unnamed. Fingerprint the token so the name is stable across
		// re-adds of the same token and distinct between accounts, without the
		// credential being recoverable from it.
		email = "account-" + fingerprint(token)
		debuglog.Logger.Warn("token is valid but carries no identity; using a fingerprint name",
			"name", email)
	}

	debuglog.Logger.Info("claude account validated", "email", email, "plan", plan)
	return Account{Email: email, Plan: plan, Token: token}, nil
}

// getWithToken performs an authenticated GET, returning the body and status.
func getWithToken(ctx context.Context, url, token string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "claude-code/"+claudeVersion())
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// identityFrom digs an email and plan out of an undocumented JSON response.
//
// Deliberately a walk rather than a struct: the shape isn't published, and a
// struct that guesses one nesting wrong silently yields an unnamed account. A
// walk finds the field wherever it sits and keeps working if it moves.
func identityFrom(body []byte) (email, plan string) {
	if len(body) == 0 {
		return "", ""
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return "", ""
	}
	var walk func(any)
	walk = func(n any) {
		switch t := n.(type) {
		case map[string]any:
			for k, val := range t {
				s, _ := val.(string)
				lk := strings.ToLower(k)
				// An "@" is what makes it an address rather than a flag like
				// "email_verified".
				if email == "" && strings.Contains(lk, "email") && strings.Contains(s, "@") {
					email = s
				}
				if plan == "" && s != "" &&
					(lk == "subscriptiontype" || lk == "subscription_type" || lk == "plan" || lk == "tier") {
					plan = s
				}
				walk(val)
			}
		case []any:
			for _, val := range t {
				walk(val)
			}
		}
	}
	walk(v)
	return email, plan
}

// fingerprint is a short, stable, non-reversible id for a token, used to name
// an account whose owner the API would not tell us.
func fingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:8]
}

// Usage is one account's quota state as reported by the usage endpoint.
type Usage struct {
	FiveHourPct   int
	FiveHourReset time.Time
	SevenDayPct   int
	SevenDayReset time.Time

	FetchedAt time.Time
	Err       error // last poll error; nil once a poll succeeds
}

// Exhausted reports whether this account should be skipped for new work.
//
// The endpoint is the only source: an account fleet cannot poll reads as
// unknown rather than spent, so least-used degrades to waterfall order instead
// of wrongly excluding a healthy subscription.
func (u Usage) Exhausted(now time.Time) bool {
	// A stale reading is not evidence of exhaustion: an account whose reset has
	// passed is available again whether or not we have managed to re-poll it.
	if !u.FiveHourReset.IsZero() && !u.FiveHourReset.After(now) {
		return false
	}
	return u.FiveHourPct >= ExhaustedPct
}

// Known reports whether this usage carries a real reading, as opposed to the
// zero value that a never-polled or failing account has. Selection needs the
// distinction: an unknown account is a candidate, a spent one is not.
func (u Usage) Known() bool {
	return !u.FetchedAt.IsZero() && u.Err == nil
}

// window is one quota window. The field names are defensive: the endpoint is
// undocumented, and the two shapes below have both been observed in the wild
// (`utilization` here, `used_percentage` on the statusline payload).
type window struct {
	Utilization    *float64 `json:"utilization"`
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       flexTime `json:"resets_at"`
}

func (w window) pct() int {
	switch {
	case w.Utilization != nil:
		return int(*w.Utilization)
	case w.UsedPercentage != nil:
		return int(*w.UsedPercentage)
	}
	return 0
}

// flexTime accepts either a unix timestamp or an ISO 8601 string, for the same
// reason as window's duplicate fields.
type flexTime struct{ time.Time }

func (f *flexTime) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == `""` {
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		f.Time = time.Unix(n, 0)
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return err
	}
	t, err := time.Parse(time.RFC3339, str)
	if err != nil {
		return err
	}
	f.Time = t
	return nil
}

type usageResponse struct {
	FiveHour window `json:"five_hour"`
	SevenDay window `json:"seven_day"`
}

// claudeVersion reads the installed Claude Code version once per process, for
// the User-Agent below. Falls back to a plausible version rather than failing:
// a missing binary must not make quota unreadable for the accounts fleet knows.
var claudeVersion = sync.OnceValue(func() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "claude", "--version").Output()
	if err != nil {
		return "2.1.0"
	}
	// "2.1.220 (Claude Code)" -> "2.1.220"
	if f := strings.Fields(strings.TrimSpace(string(out))); len(f) > 0 {
		return f[0]
	}
	return "2.1.0"
})

var httpClient = &http.Client{Timeout: httpTimeout}

// FetchUsage reads one account's quota.
//
// The User-Agent is not optional: without a claude-code User-Agent the endpoint
// drops the caller into an aggressively rate-limited bucket and returns 429
// indefinitely. Callers must respect MinPollInterval.
func FetchUsage(ctx context.Context, token string) (Usage, error) {
	if strings.TrimSpace(token) == "" {
		return Usage{}, ErrNoToken
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageEndpoint, nil)
	if err != nil {
		return Usage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "claude-code/"+claudeVersion())
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return Usage{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Usage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		// 429 here almost always means the poll interval was violated or the
		// User-Agent was wrong — both worth telling apart from a 401.
		debuglog.Logger.Warn("usage endpoint returned non-200",
			"status", resp.StatusCode, "user_agent", "claude-code/"+claudeVersion(),
			"body", Redact(strings.TrimSpace(string(body))))
		return Usage{}, fmt.Errorf("usage endpoint returned %d", resp.StatusCode)
	}

	var r usageResponse
	if err := json.Unmarshal(body, &r); err != nil {
		debuglog.Logger.Error("could not parse usage response",
			"error", err, "body", Redact(strings.TrimSpace(string(body))))
		return Usage{}, fmt.Errorf("could not parse usage response: %w", err)
	}

	u := Usage{
		FiveHourPct:   r.FiveHour.pct(),
		FiveHourReset: r.FiveHour.ResetsAt.Time,
		SevenDayPct:   r.SevenDay.pct(),
		SevenDayReset: r.SevenDay.ResetsAt.Time,
		FetchedAt:     time.Now(),
	}
	debuglog.Logger.Debug("fetched account usage", "five_hour_pct", u.FiveHourPct, "seven_day_pct", u.SevenDayPct)
	return u, nil
}

// GuardConflictingAuth reports the name of an ambient credential that would
// outrank a per-session token, or "" when the coast is clear.
//
// Without this the feature fails silently and expensively: fleet sets
// CLAUDE_CODE_OAUTH_TOKEN, Claude ignores it in favour of the higher-priority
// credential, and every session bills to the wrong account with no error
// anywhere. A wrong-billing failure must be loud.
func GuardConflictingAuth() string {
	for _, name := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if os.Getenv(name) != "" {
			debuglog.Logger.Warn("ambient credential outranks fleet's per-session token",
				"var", name, "effect", "session would bill to that credential, not the chosen account")
			return name
		}
	}
	return ""
}
