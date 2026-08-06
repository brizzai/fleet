package claudeaccount

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

// The probe reads quota for tokens the usage endpoint refuses.
//
// `claude setup-token` mints an inference-only credential — Anthropic's own
// wording is "limited to inference-only for security reasons" — so
// /api/oauth/usage answers 403 and always will. But every Messages response
// carries the same numbers in `anthropic-ratelimit-unified-*` headers, and
// inference is exactly what these tokens are for. So fleet asks the smallest
// possible question and reads the headers off the answer.
//
// This is deliberately not an attempt to defeat the scope limit: it reads what
// the API volunteers on a request the token is entitled to make. Identity —
// which that limit genuinely withholds — is not taken from here.
const (
	// probeModel is the cheapest model available. The request is one token in,
	// one token out; the point is the headers, not the answer.
	probeModel = "claude-haiku-4-5-20251001"
	// orgIDHeader identifies which account a token belongs to. Not an email —
	// see LocalIdentity for the only honest way to turn it into one.
	orgIDHeader = "anthropic-organization-id"
)

// messagesEndpoint is a var only so tests can point it at a local server. How
// this function classifies a response decides whether a dead credential keeps
// winning new sessions, which is not a thing to leave untested.
var messagesEndpoint = "https://api.anthropic.com/v1/messages"

// ErrNoQuotaHeaders means the response carried no rate-limit headers, so the
// probe told us nothing. Distinct from a failed request.
var ErrNoQuotaHeaders = errors.New("response carried no rate-limit headers")

// ErrTokenRejected means the API refused the credential itself. It is a verdict
// about the token, not about the network, and it does not change on retry.
//
// The distinction is the whole point: everywhere else a failed poll is
// deliberately treated as no information, because a stale reading beats none
// and a blip must not evict a healthy account. A rejected token is the opposite
// — it is the strongest possible information, and scoring it as "unknown" ranks
// a dead credential at the midpoint, ahead of a healthy account with real usage.
// That is how every new session ends up on the one account that cannot run.
var ErrTokenRejected = errors.New("token rejected")

// ProbeUsage reads quota (and the owning organization) by making the smallest
// possible Messages request and parsing its response headers.
//
// Costs a real, if negligible, amount of quota — roughly nine tokens. Callers
// must respect MinPollInterval, both to stay under Anthropic's rate limits and
// because spending a user's quota to watch their quota deserves restraint.
func ProbeUsage(ctx context.Context, token string) (Usage, string, error) {
	if strings.TrimSpace(token) == "" {
		return Usage{}, "", ErrNoToken
	}

	body := []byte(`{"model":"` + probeModel + `","max_tokens":1,` +
		`"messages":[{"role":"user","content":"hi"}]}`)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, messagesEndpoint, bytes.NewReader(body))
	if err != nil {
		return Usage{}, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fleetUserAgent())

	resp, err := httpClient.Do(req)
	if err != nil {
		return Usage{}, "", err
	}
	defer resp.Body.Close()
	// Drain so the connection can be reused; the payload is one token.
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	// Checked before the headers, not after: a refusal is a verdict on the
	// credential whatever else the response carries, and reading quota off a
	// response that rejected the token would report numbers for work it cannot do.
	// Deliberately not permanent — the caller re-probes on the usual cadence, so a
	// re-issued token (or an org-side change) heals the account on its own.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Usage{}, "", fmt.Errorf("%w (%d)", ErrTokenRejected, resp.StatusCode)
	}

	// Headers arrive on a rate-limit too — a spent bucket answers 429 and still
	// reports its utilization and reset, which is exactly when we most want it.
	u, org, ok := usageFromHeaders(resp.Header)
	if !ok {
		debuglog.Logger.Warn("quota probe returned no headers",
			"status", resp.StatusCode, "body", Redact(strings.TrimSpace(string(respBody))))
		return Usage{}, "", ErrNoQuotaHeaders
	}
	return u, org, nil
}

// usageFromHeaders parses the unified rate-limit family.
//
// The unit trap that bites everyone: these headers are fractions in 0..1,
// while /api/oauth/usage reports 0..100 for the same quantity. Both feed the
// same Usage struct, so exactly one of them has to be scaled — this one.
func usageFromHeaders(h http.Header) (Usage, string, bool) {
	five, fiveOK := headerPct(h, "anthropic-ratelimit-unified-5h-utilization")
	seven, _ := headerPct(h, "anthropic-ratelimit-unified-7d-utilization")

	// The 5h bucket specifically, not "either bucket": a missing utilization
	// header parses to zero, and zero is a claim, not an absence. Everything
	// downstream keys off FiveHourPct — an account fabricated at 0% reads as
	// completely free and wins every least-used comparison, so a 7d-only
	// response would quietly stampede new sessions onto it. Discarding the
	// reading costs a dim "quota unavailable"; trusting it misroutes work.
	if !fiveOK {
		return Usage{}, "", false
	}

	u := Usage{
		FiveHourPct:   five,
		SevenDayPct:   seven,
		FiveHourReset: headerTime(h, "anthropic-ratelimit-unified-5h-reset"),
		SevenDayReset: headerTime(h, "anthropic-ratelimit-unified-7d-reset"),
		FetchedAt:     time.Now(),
		AttemptedAt:   time.Now(),
	}

	// The status field is authoritative where the percentage is a guess: a
	// bucket can be refused before it reads 100. Claude Code's own parser
	// treats a missing value as "allowed", so absence is never exhaustion.
	switch strings.ToLower(h.Get("anthropic-ratelimit-unified-5h-status")) {
	case "rejected", "exhausted", "rate_limited":
		u.FiveHourPct = 100
	}
	switch strings.ToLower(h.Get("anthropic-ratelimit-unified-status")) {
	case "rejected", "exhausted":
		u.FiveHourPct = 100
	}
	return u, h.Get(orgIDHeader), true
}

// headerPct reads a 0..1 fraction and returns whole percent.
func headerPct(h http.Header, key string) (int, bool) {
	raw := h.Get(key)
	if raw == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	// Defensive: a future switch to 0..100 here would otherwise read as 1%.
	if f > 1 {
		return int(f), true
	}
	return int(f*100 + 0.5), true
}

func headerTime(h http.Header, key string) time.Time {
	raw := h.Get(key)
	if raw == "" {
		return time.Time{}
	}
	secs, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(secs, 0)
}
