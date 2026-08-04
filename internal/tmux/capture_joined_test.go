package tmux

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// tokenLike mirrors the pattern claudeaccount uses to pull a setup-token off a
// pane. Duplicated rather than imported to keep tmux free of that dependency.
var tokenLike = regexp.MustCompile(`sk-ant-[a-z0-9]{3,8}-[A-Za-z0-9_-]{16,}`)

// A `claude setup-token` token is ~108 characters. The shell prompt consumes
// columns before it, so on any realistic pane width an unjoined capture returns
// it split across physical lines and the match stops at the wrap — producing a
// truncated string that then fails validation as "not a valid token", pointing
// the blame at Claude instead of at the capture.
//
// This pins the capture at the widths that actually broke it.
func TestCapturePaneJoinedSurvivesWrapping(t *testing.T) {
	if err := IsTmuxAvailable(); err != nil {
		t.Skipf("tmux not available: %v", err)
	}

	token := "sk-ant-oat01-" + strings.Repeat("A", 95)
	if len(token) != 108 {
		t.Fatalf("fixture drifted: token is %d chars, want 108", len(token))
	}

	// 60 is narrower than any real terminal, 120 is wider than the token — the
	// point being that width does not save you, so both must pass.
	for _, width := range []int{60, 80, 120} {
		s := NewSessionWithPrefix("fleettest_", "capjoin", t.TempDir())

		// Size the pane explicitly; the default would follow the test runner's
		// terminal and make this pass or fail by accident.
		create := exec.Command("tmux", "new-session", "-d", "-s", s.Name,
			"-c", s.WorkDir, "-x", itoa(width), "-y", "10")
		if out, err := create.CombinedOutput(); err != nil {
			t.Skipf("could not create tmux session: %v (%s)", err, out)
		}
		t.Cleanup(func() { _ = s.Kill() })

		send := exec.Command("tmux", "send-keys", "-t", s.Name,
			"clear; printf '%s\\n' '"+token+"'", "Enter")
		if err := send.Run(); err != nil {
			t.Fatalf("width %d: send-keys: %v", width, err)
		}
		waitForPane(t, s, token[:20])

		pane, err := s.CapturePaneJoined()
		if err != nil {
			t.Fatalf("width %d: CapturePaneJoined: %v", width, err)
		}
		got := lastMatch(pane)
		if got != token {
			t.Errorf("width %d: captured %d chars, want %d (wrapped capture truncates)",
				width, len(got), len(token))
		}
		_ = s.Kill()
	}
}

// waitForPane polls until the pane contains want, so the test doesn't race the
// shell's echo on a loaded machine.
func waitForPane(t *testing.T, s *Session, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if out, err := s.CapturePaneJoined(); err == nil && strings.Contains(out, want) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the pane to render the token")
}

// lastMatch takes the final occurrence: the shell echoes the command line
// before printing its output, so the first match is the echo.
func lastMatch(pane string) string {
	m := tokenLike.FindAllString(pane, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
