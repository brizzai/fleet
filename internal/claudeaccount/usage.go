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
	"os/exec"
	"strings"
	"time"
)

// Quota comes from Anthropic's own endpoint, read with the account's own login.
//
// This is the cheap, official route, and it is available here only because each
// account is a real login: the credential carries user:profile, so
// /api/oauth/usage answers. Under fleet's previous token mechanism it did not —
// a `claude setup-token` credential is inference-only — which forced a probe
// that POSTed a tiny message to /v1/messages and read rate-limit headers off the
// reply. That worked, but it spent real quota to measure quota, reported
// fractions where this endpoint reports percent, and returned nothing at all for
// a token the API refused. All of that is gone.
const usageEndpoint = "https://api.anthropic.com/api/oauth/usage"

// usageEndpointVar lets tests point the fetch at a local server.
var usageEndpointVar = usageEndpoint

// ErrNoCredential means the account's Keychain item could not be read: the dir
// has never been logged in, or the OS denied access. Not a verdict on the
// subscription.
var ErrNoCredential = errors.New("no stored credential for this config dir")

// keychainService returns the macOS Keychain service name Claude Code stores a
// config dir's credential under.
//
// The suffix is how two config dirs hold two different logins at once, which is
// the mechanism fleet's whole multi-account feature rests on. Derived by reading
// the shipped binary and confirmed against a live login: the default dir uses
// the bare name, and any explicit CLAUDE_CONFIG_DIR appends the first 8 hex of
// the SHA-256 of its NFC-normalised path.
//
// Undocumented, so treat a miss as "no reading" rather than an error worth
// raising — quota is an optimization, and Select degrades to configured order
// without it.
//
// Claude Code normalises the path to NFC before hashing; fleet doesn't, to
// avoid a dependency on golang.org/x/text for one call. The two agree for any
// path that is already NFC, which is every path fleet builds (the account dir
// is 8 hex characters under ~/.config/fleet) unless the user's home directory
// itself is non-ASCII in a decomposed form. When they disagree the lookup
// misses, which costs the quota reading and nothing else: the account reads as
// unpolled and least-used falls back to configured order.
func keychainService(dir string) string {
	if dir == "" {
		return "Claude Code-credentials"
	}
	sum := sha256.Sum256([]byte(dir))
	return "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]
}

// accessToken reads the account's OAuth access token out of the Keychain.
//
// fleet does not store this and never writes it anywhere: it is read at poll
// time, used for one request, and dropped. That is a deliberate improvement on
// the previous mechanism, which kept year-long credentials in a file of its own
// and needed redaction at every logging site to keep them out of bug reports.
func accessToken(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "security", "find-generic-password",
		"-w", "-s", keychainService(dir))
	out, err := cmd.Output()
	if err != nil {
		return "", ErrNoCredential
	}
	var cred struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(out, &cred); err != nil {
		return "", ErrNoCredential
	}
	if cred.ClaudeAiOauth.AccessToken == "" {
		return "", ErrNoCredential
	}
	return cred.ClaudeAiOauth.AccessToken, nil
}

// FetchUsage reads one account's quota.
//
// Consumes no message quota. Errors are for the caller to record rather than
// raise: an account fleet cannot poll reads as *unknown*, not spent, so
// least-used degrades into configured order instead of wrongly excluding a
// healthy subscription.
func FetchUsage(ctx context.Context, dir string) (Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()

	tok, err := accessToken(ctx, dir)
	if err != nil {
		return Usage{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageEndpointVar, nil)
	if err != nil {
		return Usage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", fleetUserAgent())

	resp, err := httpClient.Do(req)
	if err != nil {
		return Usage{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	// A refusal is a verdict on the login, not a transient failure, and it is
	// the signal that an account has been logged out or revoked elsewhere.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Usage{}, fmt.Errorf("%w (%d)", ErrNotLoggedIn, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return Usage{}, fmt.Errorf("usage endpoint returned %d", resp.StatusCode)
	}

	var raw struct {
		FiveHour struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    string  `json:"resets_at"`
		} `json:"five_hour"`
		SevenDay struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    string  `json:"resets_at"`
		} `json:"seven_day"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Usage{}, err
	}

	now := time.Now()
	// Already percent (0..100), unlike the rate-limit headers the old probe
	// read, which were fractions. Exactly one of the two ever needed scaling
	// and getting it backwards reported a spent account as 1% used.
	return Usage{
		FiveHourPct:   int(raw.FiveHour.Utilization + 0.5),
		SevenDayPct:   int(raw.SevenDay.Utilization + 0.5),
		FiveHourReset: parseResetTime(raw.FiveHour.ResetsAt),
		SevenDayReset: parseResetTime(raw.SevenDay.ResetsAt),
		FetchedAt:     now,
		AttemptedAt:   now,
	}, nil
}

// parseResetTime reads an RFC3339 reset stamp, tolerating the fractional
// seconds the endpoint actually sends. A missing or unparseable time is left
// zero, which Exhausted reads as "no known reset" rather than "already reset".
func parseResetTime(s string) time.Time {
	if strings.TrimSpace(s) == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
