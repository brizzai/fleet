package control

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/hooks"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/tmux"
)

// After an Esc interrupt, agy fires no hook, so the status file still says
// running while the pane sits at its idle prompt. The TUI settles that over two
// passes; a one-shot `fleet status` must reach the same answer in one call
// rather than report the interrupted turn as running until the next one.
func TestOneShotObserverSettlesAnInterruptedAntigravityTurn(t *testing.T) {
	if err := tmux.IsTmuxAvailable(); err != nil {
		t.Skipf("tmux not available: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	debuglog.Init()

	fixture, err := filepath.Abs(filepath.Join("..", "session", "testdata", "antigravity", "interrupted.txt"))
	if err != nil {
		t.Fatal(err)
	}
	ts := tmux.NewSessionWithPrefix("fleettest_", "agy-second-look", t.TempDir())
	// Print the real interrupted screen, then sit in a non-shell process the
	// way agy does, so the pane reads as a live agent at rest.
	create := exec.Command("tmux", "new-session", "-d", "-s", ts.Name, "-x", "120", "-y", "40",
		"-c", ts.WorkDir, "cat '"+fixture+"'; exec sleep 60")
	if out, err := create.CombinedOutput(); err != nil {
		t.Skipf("could not create tmux session: %v (%s)", err, out)
	}
	t.Cleanup(func() { _ = ts.Kill() })
	waitForCapture(t, ts.Name, "for shortcuts")

	const id = "agy-second-look-1"
	if err := hooks.WriteStatusFile(hooks.GetHooksDir(), id, &hooks.StatusFile{
		Status: "running", SessionID: "conv-1", Event: "UserPromptSubmit", Timestamp: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	row := &session.SessionRow{
		ID: id, Title: "interrupted", ProjectPath: ts.WorkDir, Agent: "antigravity",
		Status: string(session.StatusRunning), TmuxSession: ts.Name, ClaudeSessionID: "conv-1",
		CreatedAt: time.Now(),
	}

	start := time.Now()
	obs := NewObserver(Options{}).Observe([]*session.SessionRow{row})
	if got := obs[0].Status; got != session.StatusFinished {
		t.Fatalf("one-shot status = %q, want finished", got)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("observe took %v; the second look should cost about %v", took, session.PaneDemotionConfirmDelay)
	}
}

func waitForCapture(t *testing.T, name, want string) {
	t.Helper()
	for range 100 {
		out, _ := exec.Command("tmux", "capture-pane", "-p", "-t", name).Output()
		if strings.Contains(string(out), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pane never showed %q", want)
}
