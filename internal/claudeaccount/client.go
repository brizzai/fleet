package claudeaccount

import (
	"context"
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

// authStatus is the subset of `claude auth status --json` fleet reads.
type authStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	Email            string `json:"email"`
	OrgName          string `json:"orgName"`
	SubscriptionType string `json:"subscriptionType"`
}

// tokenEnv builds a child environment where the given token is the credential
// that wins.
//
// The higher-precedence variables are stripped rather than merely overridden:
// CLAUDE_CODE_OAUTH_TOKEN sits at priority 5, below ANTHROPIC_AUTH_TOKEN (2)
// and ANTHROPIC_API_KEY (3). Leaving either in place would validate the wrong
// credential and cheerfully report success for a token that will never be used.
// (apiKeyHelper, priority 4, is a settings.json key rather than an env var and
// cannot be stripped here — GuardConflictingAuth covers it.)
func tokenEnv(token string) []string {
	src := os.Environ()
	out := make([]string, 0, len(src)+1)
	for _, e := range src {
		if strings.HasPrefix(e, "CLAUDE_CODE_OAUTH_TOKEN=") ||
			strings.HasPrefix(e, "ANTHROPIC_API_KEY=") ||
			strings.HasPrefix(e, "ANTHROPIC_AUTH_TOKEN=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "CLAUDE_CODE_OAUTH_TOKEN="+token)
}

// Validate asks Claude who a token belongs to, returning a ready-to-store
// Account. This is what makes accounts self-naming: the email and plan come
// from Claude, never from the user, so a label can't be wrong. It doubles as
// the liveness check for an expired token.
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

	cmd := exec.CommandContext(ctx, "claude", "auth", "status", "--json")
	cmd.Env = tokenEnv(token)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			// Redact defensively: the token can appear in an error echo.
			stderr := Redact(strings.TrimSpace(string(ee.Stderr)))
			debuglog.Logger.Error("claude auth status failed", "stderr", stderr, "exit_code", ee.ExitCode())
			return Account{}, fmt.Errorf("claude auth status: %s", stderr)
		}
		debuglog.Logger.Error("claude auth status could not run", "error", err)
		return Account{}, fmt.Errorf("claude auth status: %w", err)
	}

	var st authStatus
	if err := json.Unmarshal(out, &st); err != nil {
		// The output can't hold a token (it echoes identity, not credentials),
		// but redact anyway — this line is bound for debug.log.
		debuglog.Logger.Error("could not parse claude auth status output",
			"error", err, "output", Redact(strings.TrimSpace(string(out))))
		return Account{}, fmt.Errorf("could not parse claude auth status output: %w", err)
	}
	// The email test is the load-bearing one, not LoggedIn. Verified against a
	// deliberately invalid token: `claude auth status` still answers
	// loggedIn=true and simply omits the email. Checking LoggedIn alone would
	// accept any garbage string and store it as a working account.
	if !st.LoggedIn || st.Email == "" {
		debuglog.Logger.Error("token rejected by claude",
			"logged_in", st.LoggedIn, "email_present", st.Email != "")
		return Account{}, errors.New("token is not valid — claude did not return an account for it")
	}

	debuglog.Logger.Info("claude account validated",
		"email", st.Email, "plan", st.SubscriptionType, "org", st.OrgName)
	return Account{
		Email: st.Email,
		Plan:  st.SubscriptionType,
		Token: token,
	}, nil
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
