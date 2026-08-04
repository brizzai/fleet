package claudeaccount

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

const (

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

	// One call does both jobs: the status code discriminates, the body names.
	//
	//   401 → genuinely bad token. (Measured against a string of A's.)
	//   403 → real token, scope-limited. `claude setup-token` mints one without
	//         `user:profile`, and the API says so in as many words: "OAuth
	//         token does not meet scope requirement user:profile". Valid, just
	//         not entitled to this endpoint — accept it.
	//   200 → valid and entitled; the body carries the identity.
	//
	// Anything else (network, rate limit, outage) is not evidence against the
	// token, so it is accepted rather than refused for an unrelated failure.
	body, status, err := getWithToken(ctx, profileEndpoint, token)
	switch {
	case err != nil:
		debuglog.Logger.Warn("could not reach Anthropic to check the token; accepting it unverified", "err", err)
		body = nil
	case status == http.StatusUnauthorized:
		debuglog.Logger.Error("token rejected by anthropic", "status", status)
		return Account{}, errors.New("token was rejected — generate a fresh one with `claude setup-token`")
	case status == http.StatusOK:
		// The response shape is undocumented, so log it (redacted) — this is how
		// we learn what identity fields exist without guessing at a struct.
		debuglog.Logger.Info("account profile response", "body", Redact(strings.TrimSpace(string(body))))
	default:
		debuglog.Logger.Info("token is not entitled to identify itself; trying the local profile",
			"profile_status", status)
		body = nil
	}

	email, plan := identityFrom(body)

	// Second chance, entirely local: if this token's organization is the one
	// logged in on this machine, ~/.claude.json already holds the email. See
	// identity.go for why this is a fair thing to do and where the line is.
	usage, org, probeErr := ProbeUsage(ctx, token)
	if probeErr != nil {
		debuglog.Logger.Info("quota probe unavailable at add time", "err", Redact(probeErr.Error()))
	}
	if email == "" && org != "" {
		if name := NameForOrg(org); name != "" {
			email = name
			debuglog.Logger.Info("named the account from the local profile", "email", email, "org", org)
		}
	}

	if email == "" {
		// Still anonymous, which is the expected outcome for any account other
		// than the one logged in here. Fingerprint the token so the key is
		// stable across re-adds and distinct between accounts, without the
		// credential being recoverable from it. The user supplies a label.
		email = FingerprintPrefix + fingerprint(token)
		debuglog.Logger.Info("token carries no identity fleet can resolve; using a fingerprint name",
			"name", email)
	}

	debuglog.Logger.Info("claude account validated",
		"email", email, "plan", plan, "org", org, "quota_readable", probeErr == nil)
	return Account{Email: email, Plan: plan, Token: token, OrgUUID: org, initialUsage: usage}, nil
}

// getWithToken performs an authenticated GET, returning the body and status.
func getWithToken(ctx context.Context, url, token string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", fleetUserAgent())
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

// Usage is one account's quota state as last read.
//
// FetchedAt and AttemptedAt are deliberately separate. A network blip must not
// erase a reading taken three minutes ago — a stale measurement is much closer
// to the truth than "unknown", which would drop the account out of the readout
// and make Select rank it at the neutral midpoint. So a failure advances only
// AttemptedAt (which paces the retry) and leaves the numbers alone.
type Usage struct {
	FiveHourPct   int
	FiveHourReset time.Time
	SevenDayPct   int
	SevenDayReset time.Time

	// FetchedAt is when the percentages above were last successfully read.
	FetchedAt time.Time
	// AttemptedAt is when a read was last tried, successfully or not.
	AttemptedAt time.Time
	Err         error // last poll error; nil once a poll succeeds
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
// zero value a never-polled account has. Selection needs the distinction: an
// unknown account is a candidate ranked at the midpoint, a spent one is not.
//
// Deliberately independent of Err: a reading that succeeded once stays usable
// through a later failure. Err describes the last *attempt*, not the data.
func (u Usage) Known() bool {
	return !u.FetchedAt.IsZero()
}

var httpClient = &http.Client{Timeout: httpTimeout}

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
