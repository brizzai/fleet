package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/brizzai/fleet/internal/proc"
)

// writeProcStatus writes a Claude per-process status file the way Claude Code
// 2.1.327+ does (field names from a live ~/.claude/sessions/<pid>.json).
func writeProcStatus(t *testing.T, dir string, pid int, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeProcStatusTrustedWaitingFor(t *testing.T) {
	const sid = "d54d8bb7-ec1f-4be5-90ae-ec50e3bd11d2"
	cases := []struct {
		name string
		st   claudeProcStatus
		want string
	}{
		{"question", claudeProcStatus{SessionID: sid, Status: "waiting", WaitingFor: "input needed"}, "input needed"},
		{"sandbox", claudeProcStatus{SessionID: sid, Status: "waiting", WaitingFor: "sandbox request"}, "sandbox request"},
		// A /model or /config dialog the user opened reports this too.
		{"dialog open is not trusted", claudeProcStatus{SessionID: sid, Status: "waiting", WaitingFor: "dialog open"}, ""},
		{"unknown reason", claudeProcStatus{SessionID: sid, Status: "waiting", WaitingFor: "something new"}, ""},
		{"busy", claudeProcStatus{SessionID: sid, Status: "busy"}, ""},
		{"idle", claudeProcStatus{SessionID: sid, Status: "idle"}, ""},
		// A recycled pid, or a conversation /clear rotated away from.
		{"other session", claudeProcStatus{SessionID: "other", Status: "waiting", WaitingFor: "input needed"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.st.trustedWaitingFor(sid); got != c.want {
				t.Errorf("trustedWaitingFor = %q, want %q", got, c.want)
			}
		})
	}
	if got := (claudeProcStatus{Status: "waiting", WaitingFor: "input needed"}).trustedWaitingFor(""); got != "" {
		t.Errorf("no owner session: got %q, want \"\"", got)
	}
}

// TestClaudeWaitingForReadsTheOwnersFile runs the real reader end to end: the
// file is found under ~/.claude/sessions by the hook-reported pid, and only a
// live process counts.
func TestClaudeWaitingForReadsTheOwnersFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "sessions")
	const sid = "d54d8bb7-ec1f-4be5-90ae-ec50e3bd11d2"
	waiting := `{"pid":%d,"sessionId":"` + sid + `","status":"waiting","waitingFor":"input needed","statusUpdatedAt":1795088954448}`

	live := os.Getpid()
	writeProcStatus(t, dir, live, fmt.Sprintf(waiting, live))
	s := &Session{ownerPID: live, ownerSessionID: sid}
	if got := s.claudeWaitingFor(); got != "input needed" {
		t.Errorf("live owner: got %q, want \"input needed\"", got)
	}

	// No pid from the hooks yet: no file to read.
	if got := (&Session{ownerSessionID: sid}).claudeWaitingFor(); got != "" {
		t.Errorf("no owner pid: got %q, want \"\"", got)
	}

	// A crashed Claude leaves its last word behind.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot spawn a short-lived process: %v", err)
	}
	dead := cmd.Process.Pid
	writeProcStatus(t, dir, dead, fmt.Sprintf(waiting, dead))
	got := (&Session{ownerPID: dead, ownerSessionID: sid}).claudeWaitingFor()
	// The OS may hand a reaped pid to another process under load; that is a
	// live owner, not a failure of the dead-owner rule.
	if got != "" && !proc.Alive(dead) {
		t.Errorf("dead owner: got %q, want \"\"", got)
	}

	// A torn or foreign file reads as no signal.
	writeProcStatus(t, dir, live, `{"pid":`)
	if got := s.claudeWaitingFor(); got != "" {
		t.Errorf("garbage file: got %q, want \"\"", got)
	}
}
