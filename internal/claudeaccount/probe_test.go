package claudeaccount

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func hdr(kv map[string]string) http.Header {
	h := http.Header{}
	for k, v := range kv {
		h.Set(k, v)
	}
	return h
}

// probeAgainst points ProbeUsage at a stub for the duration of a test.
func probeAgainst(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	prev := messagesEndpoint
	messagesEndpoint = srv.URL
	t.Cleanup(func() { messagesEndpoint = prev; srv.Close() })
}

// A refused credential must be distinguishable from a poll that failed to land.
// Everything downstream treats a plain failure as "no information" — which is
// right for a blip and catastrophic for a dead token, because an account with no
// reading scores at the midpoint and outranks every healthy account in use.
func TestRejectedTokenIsReportedAsRejected(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		probeAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		})
		_, _, err := ProbeUsage(context.Background(), "sk-ant-oat01-whatever")
		if !errors.Is(err, ErrTokenRejected) {
			t.Errorf("status %d gave err %v, want ErrTokenRejected", status, err)
		}
	}
}

// The inverse, and the one that matters for stability: an outage, a timeout or a
// malformed reply must never read as a rejection, or a bad afternoon at
// Anthropic would empty fleet's candidate list and drop every session onto the
// ambient login.
func TestOrdinaryProbeFailureIsNotARejection(t *testing.T) {
	probeAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	_, _, err := ProbeUsage(context.Background(), "sk-ant-oat01-whatever")
	if err == nil {
		t.Fatal("a 500 should still be an error")
	}
	if errors.Is(err, ErrTokenRejected) {
		t.Error("a 500 was classified as a rejected token")
	}
}

// A 429 carries the real numbers and is exactly when they matter most, so it
// must be read, not thrown away as a failure.
func TestRateLimitedResponseStillYieldsItsHeaders(t *testing.T) {
	probeAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "1.0")
		w.Header().Set("anthropic-ratelimit-unified-5h-status", "rejected")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	u, _, err := ProbeUsage(context.Background(), "sk-ant-oat01-whatever")
	if err != nil {
		t.Fatalf("429 with headers returned %v, want the reading", err)
	}
	if !u.Exhausted(time.Now()) {
		t.Errorf("five hour = %d%%, want it read as spent", u.FiveHourPct)
	}
	if u.Rejected {
		t.Error("a spent account was marked as a rejected credential — it recovers at the reset")
	}
}

// The unit trap: these headers are fractions in 0..1, while /api/oauth/usage
// reports 0..100 for the same quantity. Both feed one Usage struct, so exactly
// one must be scaled. Two other projects document being bitten by this.
func TestHeaderUtilizationIsScaledToPercent(t *testing.T) {
	u, _, ok := usageFromHeaders(hdr(map[string]string{
		"anthropic-ratelimit-unified-5h-utilization": "0.43",
		"anthropic-ratelimit-unified-7d-utilization": "0.09",
	}))
	if !ok {
		t.Fatal("headers not recognised")
	}
	if u.FiveHourPct != 43 {
		t.Errorf("five hour = %d%%, want 43 — 0.43 is forty-three percent, not zero", u.FiveHourPct)
	}
	if u.SevenDayPct != 9 {
		t.Errorf("seven day = %d%%, want 9", u.SevenDayPct)
	}
}

func TestHeaderUtilizationSurvivesAUnitChange(t *testing.T) {
	// If Anthropic ever switched these to 0..100, naive scaling would report a
	// spent account as 4300%. Values above 1 are taken as already-percent.
	u, _, _ := usageFromHeaders(hdr(map[string]string{
		"anthropic-ratelimit-unified-5h-utilization": "43",
	}))
	if u.FiveHourPct != 43 {
		t.Errorf("five hour = %d%%, want 43", u.FiveHourPct)
	}
}

func TestHeaderRounding(t *testing.T) {
	for raw, want := range map[string]int{
		"0":     0,
		"0.001": 0,
		"0.005": 1, // rounds up, so a barely-used account isn't reported as idle
		"0.995": 100,
		"1":     100,
	} {
		u, _, _ := usageFromHeaders(hdr(map[string]string{
			"anthropic-ratelimit-unified-5h-utilization": raw,
		}))
		if u.FiveHourPct != want {
			t.Errorf("utilization %q = %d%%, want %d", raw, u.FiveHourPct, want)
		}
	}
}

// A bucket can be refused before its percentage reads 100, so the status field
// is authoritative where the number is a guess.
func TestRejectedStatusMeansExhausted(t *testing.T) {
	for _, key := range []string{
		"anthropic-ratelimit-unified-5h-status",
		"anthropic-ratelimit-unified-status",
	} {
		u, _, _ := usageFromHeaders(hdr(map[string]string{
			"anthropic-ratelimit-unified-5h-utilization": "0.80",
			key: "rejected",
		}))
		if !u.Exhausted(time.Now()) {
			t.Errorf("%s=rejected did not read as exhausted (pct=%d)", key, u.FiveHourPct)
		}
	}
}

// Claude Code's own parser defaults a missing status to "allowed", so absence
// must never be read as exhaustion.
func TestMissingStatusIsNotExhaustion(t *testing.T) {
	u, _, _ := usageFromHeaders(hdr(map[string]string{
		"anthropic-ratelimit-unified-5h-utilization": "0.10",
		"anthropic-ratelimit-unified-5h-reset":       "9999999999",
	}))
	if u.Exhausted(time.Now()) {
		t.Error("an account at 10% with no status header read as exhausted")
	}
}

func TestResetTimesParsed(t *testing.T) {
	u, _, _ := usageFromHeaders(hdr(map[string]string{
		"anthropic-ratelimit-unified-5h-utilization": "0.5",
		"anthropic-ratelimit-unified-5h-reset":       "1785850800",
		"anthropic-ratelimit-unified-7d-reset":       "1786392000",
	}))
	if got := u.FiveHourReset.Unix(); got != 1785850800 {
		t.Errorf("5h reset = %d, want 1785850800", got)
	}
	if got := u.SevenDayReset.Unix(); got != 1786392000 {
		t.Errorf("7d reset = %d, want 1786392000", got)
	}
}

func TestOrgIDExtracted(t *testing.T) {
	_, org, _ := usageFromHeaders(hdr(map[string]string{
		"anthropic-ratelimit-unified-5h-utilization": "0.1",
		"anthropic-organization-id":                  "3675552f-509d-4c55-b960-c0710c979d25",
	}))
	if org != "3675552f-509d-4c55-b960-c0710c979d25" {
		t.Errorf("org = %q, want the header value", org)
	}
}

// A network blip must not erase a reading taken minutes ago: unknown is worse
// than stale, because it drops the account out of the readout and ranks it at
// the neutral midpoint for selection.
func TestAFailedAttemptDoesNotDiscardTheReading(t *testing.T) {
	good := Usage{FiveHourPct: 42, FetchedAt: testNow, AttemptedAt: testNow}
	if !good.Known() {
		t.Fatal("a fresh successful reading should be known")
	}

	// What the poll does on failure: record the error and the attempt, keep
	// the numbers.
	afterFailure := good
	afterFailure.Err = errors.New("context deadline exceeded")
	afterFailure.AttemptedAt = testNow.Add(MinPollInterval)

	if !afterFailure.Known() {
		t.Error("a failed attempt discarded a previously good reading")
	}
	if afterFailure.FiveHourPct != 42 {
		t.Errorf("percentages changed on failure: %d", afterFailure.FiveHourPct)
	}
	if !afterFailure.FetchedAt.Equal(testNow) {
		t.Error("FetchedAt moved on a failed attempt; it must mark the last success")
	}
}

func TestNoQuotaHeadersIsRecognised(t *testing.T) {
	// A response without the family tells us nothing — it must not be mistaken
	// for an account sitting at zero, which would make it look idle and win
	// every least-used comparison.
	if _, _, ok := usageFromHeaders(hdr(map[string]string{"content-type": "application/json"})); ok {
		t.Fatal("a response with no rate-limit headers was accepted as a reading")
	}
}

// A response carrying only the weekly bucket must not be read as "5h at 0%".
// Zero is a claim: it makes the account look completely free, so least-used
// would send every new session to it on the strength of a header that wasn't
// there.
func TestSevenDayAloneIsNotAReading(t *testing.T) {
	if _, _, ok := usageFromHeaders(hdr(map[string]string{
		"anthropic-ratelimit-unified-7d-utilization": "0.20",
	})); ok {
		t.Fatal("a 7d-only response was accepted, fabricating a 5h reading of 0%")
	}
}

func TestGarbageHeaderIsNotAReading(t *testing.T) {
	if _, _, ok := usageFromHeaders(hdr(map[string]string{
		"anthropic-ratelimit-unified-5h-utilization": "not-a-number",
	})); ok {
		t.Fatal("an unparseable utilization was accepted as a reading")
	}
}
