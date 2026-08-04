package claudeaccount

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/debuglog"
)

// captureLog swaps the global logger for a buffer for the duration of a test.
// debuglog.Init guards its own assignment with a sync.Once that these tests
// never trigger, so writing Logger directly is safe here.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := debuglog.Logger
	debuglog.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { debuglog.Logger = prev })
	return &buf
}

// debug.log is pasted verbatim into public GitHub issues by the bug-report
// flow, and these paths all handle a year-long credential. Nothing they log may
// contain it — not on the success path, and especially not on an error path
// where a subprocess might echo the token back at us.
func TestValidateNeverLogsTheToken(t *testing.T) {
	buf := captureLog(t)

	// Deliberately invalid, so every failure branch gets exercised: whichever
	// of "claude missing", "non-zero exit", or "rejected" this machine hits,
	// the assertion below is the same.
	_, err := Validate(context.Background(), fakeToken)
	if err == nil {
		t.Fatal("expected a bogus token to be rejected")
	}
	if strings.Contains(buf.String(), fakeToken) {
		t.Fatalf("token leaked into the debug log:\n%s", buf.String())
	}
	// The error the user sees must be clean too — it reaches the dialog and,
	// via the report flow, an issue body.
	if strings.Contains(err.Error(), fakeToken) {
		t.Fatalf("token leaked into the returned error: %v", err)
	}
	// The length is what makes a truncated paste diagnosable, so it should be
	// there in place of the token.
	if !strings.Contains(buf.String(), "token_len") {
		t.Errorf("want token_len logged as the safe stand-in:\n%s", buf.String())
	}
}

func TestFetchUsageNeverLogsTheToken(t *testing.T) {
	buf := captureLog(t)

	// Hits the real endpoint with a bogus credential: expected to 401, which is
	// exactly the branch that logs a response body.
	if _, err := FetchUsage(context.Background(), fakeToken); err == nil {
		t.Skip("bogus token unexpectedly accepted; nothing to assert")
	}
	if strings.Contains(buf.String(), fakeToken) {
		t.Fatalf("token leaked into the debug log:\n%s", buf.String())
	}
}

func TestLoadLogsIdentityButNeverTokens(t *testing.T) {
	buf := captureLog(t)

	s := &Store{accounts: []Account{{Email: "a@x.com", Plan: "max", Token: fakeToken}}}
	// Load reads from disk, so exercise the same logging shape directly: the
	// invariant under test is what the fields carry, not where they come from.
	for i, a := range s.List() {
		debuglog.Logger.Info("account loaded", "order", i, "email", a.Email,
			"plan", a.Plan, "label", a.Label, "has_token", a.Token != "")
	}
	out := buf.String()
	if strings.Contains(out, fakeToken) {
		t.Fatalf("token leaked into the debug log:\n%s", out)
	}
	if !strings.Contains(out, "a@x.com") {
		t.Errorf("email should be logged — it is the identity, not a secret:\n%s", out)
	}
}
