package claudeaccount

import (
	"errors"
	"strings"
	"testing"
)

// Measured against a real spent subscription: `claude -p` exits 1, puts the
// actual reason on STDOUT, and puts only advisories on stderr. Reading stderr
// first reported a settings.json permission rule as the cause of a quota
// refusal — a false statement about the user's setup that sends them to fix a
// file which was never the problem.
func TestAskFailureReasonPrefersStdoutOverWarnings(t *testing.T) {
	stdout := "You've hit your session limit · resets 3:50pm (Asia/Jerusalem)"
	stderr := `Permission allow rule (/Users/x/.config/fleet/accounts/1/settings.json): Bash(find ~/go/pkg/mod/github.com/hinshun/vt10x* -type f -name "*.go" | head -20) has a wildcard before the rest of the command, so it also matches any options inserted at that position and approves them without a prompt.
Shell cwd was reset to /Users/x/code/fleet`

	got := askFailureReason(stdout, stderr, errors.New("exit status 1"))
	if !strings.Contains(got, "session limit") {
		t.Errorf("reason = %q, want the quota refusal that actually stopped it", got)
	}
	if strings.Contains(got, "Permission allow rule") {
		t.Error("reported a settings warning as the cause of the failure")
	}
}

// With nothing on stdout, the advisories are still skipped: they accompany a
// failure rather than being one.
func TestAskFailureReasonSkipsNotesOnStderrToo(t *testing.T) {
	stderr := "Permission allow rule (x): bad\nShell cwd was reset to /tmp\nAPI Error: overloaded"
	if got := askFailureReason("", stderr, errors.New("exit status 1")); got != "API Error: overloaded" {
		t.Errorf("reason = %q, want the real error under the notes", got)
	}
	// Nothing but notes leaves the exit status, which is at least true.
	if got := askFailureReason("", "Shell cwd was reset to /tmp", errors.New("exit status 1")); got != "exit status 1" {
		t.Errorf("reason = %q, want the exit status", got)
	}
}

// The reason lands in a one-line footer, and claude's warnings run to a
// paragraph. A wrapped toast pushes the keys off the screen.
func TestAskFailureReasonFitsAFooter(t *testing.T) {
	long := strings.Repeat("something went wrong ", 40)
	got := askFailureReason(long, "", errors.New("exit status 1"))
	if len([]rune(got)) > askReasonMax {
		t.Errorf("reason is %d runes, over the %d cap", len([]rune(got)), askReasonMax)
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("a truncated reason does not say it was truncated")
	}
}
