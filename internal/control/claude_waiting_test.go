package control

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/hooks"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/tmux"
)

// A question's PermissionRequest lands before the dialog renders, so the pane
// still shows the idle prompt while Claude's own status file already says
// "input needed". The observer's "no prompt on screen means it was answered"
// rule must not overrule that, or `fleet status` reports running while the TUI
// shows waiting.
func TestObserverKeepsWaitingFromClaudesStatusFile(t *testing.T) {
	if err := tmux.IsTmuxAvailable(); err != nil {
		t.Skipf("tmux not available: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	debuglog.Init()

	fixture, err := filepath.Abs(filepath.Join("..", "session", "testdata", "pane_finished_idle_prompt.txt"))
	if err != nil {
		t.Fatal(err)
	}
	ts := tmux.NewSessionWithPrefix("fleettest_", "claude-file-waiting", t.TempDir())
	create := exec.Command("tmux", "new-session", "-d", "-s", ts.Name, "-x", "120", "-y", "40",
		"-c", ts.WorkDir, "cat '"+fixture+"'; exec sleep 60")
	if out, err := create.CombinedOutput(); err != nil {
		t.Skipf("could not create tmux session: %v (%s)", err, out)
	}
	t.Cleanup(func() { _ = ts.Kill() })
	waitForCapture(t, ts.Name, "❯")

	// The test process stands in for the live Claude the hooks name.
	pid := os.Getpid()
	sessDir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"pid":%d,"sessionId":"conv-1","status":"waiting","waitingFor":"input needed"}`, pid)
	if err := os.WriteFile(filepath.Join(sessDir, fmt.Sprintf("%d.json", pid)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	const id = "claude-file-waiting-1"
	if err := hooks.WriteStatusFile(hooks.GetHooksDir(), id, &hooks.StatusFile{
		Status: "waiting", SessionID: "conv-1", Event: "PermissionRequest",
		Timestamp: time.Now().Unix(), AgentPID: pid,
	}); err != nil {
		t.Fatal(err)
	}
	row := &session.SessionRow{
		ID: id, Title: "question", ProjectPath: ts.WorkDir, Agent: "claude",
		Status: string(session.StatusWaiting), TmuxSession: ts.Name, ClaudeSessionID: "conv-1",
		CreatedAt: time.Now(),
	}

	obs := NewObserver(Options{}).Observe([]*session.SessionRow{row})
	if got := obs[0].Status; got != session.StatusWaiting {
		t.Fatalf("one-shot status = %q, want waiting (Claude's status file says input needed)", got)
	}
}
