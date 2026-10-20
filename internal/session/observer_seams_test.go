package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/hooks"
)

// idlePane is a Claude pane at rest: no spinner, an empty input box.
const idlePane = "⏺ Interrupted · What should Claude do instead?\n" +
	"────────────────────────────────────────────────────────────\n" +
	"❯ \n" +
	"────────────────────────────────────────────────────────────\n" +
	"  ⏸ manual mode on\n"

func runningHookSession(pane *mockPane) *Session {
	s := &Session{ID: "seed-test", Status: StatusRunning, paneCapturer: pane, convActiveFn: func() bool { return false }}
	s.UpdateHookStatus(&HookStatus{Status: "running", SessionID: "c1", UpdatedAt: time.Now().Add(-time.Minute)}, false)
	return s
}

// An interrupted turn fires no Stop hook. The TUI's worker reads the "running"
// hook as stale once the pane has sat unchanged for 10s; a one-shot observer
// never watches that long, so it borrows tmux's activity clock instead.
func TestSeedStableContent(t *testing.T) {
	debuglog.Init()
	quiet := time.Now().Add(-time.Minute)

	t.Run("a quiet pane under a running hook settles to finished", func(t *testing.T) {
		pane := &mockPane{content: idlePane, activityUnix: quiet.Unix(), hasActivity: true}
		s := runningHookSession(pane)
		s.SeedStableContent(idlePane, quiet)
		s.UpdateStatus()
		if got := s.GetStatus(); got != StatusFinished {
			t.Errorf("status = %s, want finished", got)
		}
	})

	t.Run("without the seed a single look keeps running", func(t *testing.T) {
		pane := &mockPane{content: idlePane, activityUnix: quiet.Unix(), hasActivity: true}
		s := runningHookSession(pane)
		s.UpdateStatus()
		if got := s.GetStatus(); got != StatusRunning {
			t.Errorf("status = %s, want running (this is the gap the seed fills)", got)
		}
	})

	t.Run("recent pane output keeps trusting the hook", func(t *testing.T) {
		recent := time.Now().Add(-2 * time.Second)
		pane := &mockPane{content: idlePane, activityUnix: recent.Unix(), hasActivity: true}
		s := runningHookSession(pane)
		s.SeedStableContent(idlePane, recent)
		s.UpdateStatus()
		if got := s.GetStatus(); got != StatusRunning {
			t.Errorf("status = %s, want running", got)
		}
	})

	// In the waiting path the same clock means "content changed recently";
	// seeding it there would flip a prompt that just appeared to running.
	t.Run("a waiting hook is left alone", func(t *testing.T) {
		s := &Session{ID: "seed-test", Status: StatusWaiting, paneCapturer: &mockPane{content: idlePane}}
		s.UpdateHookStatus(&HookStatus{Status: "waiting", SessionID: "c1", UpdatedAt: time.Now()}, false)
		s.SeedStableContent(idlePane, quiet)
		if s.lastContentHash != "" || !s.lastContentChangeAt.IsZero() {
			t.Error("seeded the content clock under a waiting hook")
		}
	})
}

func TestAdoptExternalRelaunch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	hookFile := filepath.Join(hooks.GetHooksDir(), "ext-test.json")
	if err := os.MkdirAll(filepath.Dir(hookFile), 0o755); err != nil {
		t.Fatal(err)
	}
	// The new agent's hook may already be on disk; adopting must not delete it.
	if err := os.WriteFile(hookFile, []byte(`{"status":"running"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Session{ID: "ext-test", Status: StatusError, TmuxSessionName: "fleet_old_1"}
	s.UpdateHookStatus(&HookStatus{Status: "dead", SessionID: "c1", AgentPID: 42, UpdatedAt: time.Now()}, false)
	s.deathRecorded = true

	s.AdoptExternalRelaunch("fleet_new_2", "c2")
	if s.TmuxSessionName != "fleet_new_2" || s.GetTmuxSession().Name != "fleet_new_2" {
		t.Errorf("tmux = %q / %q, want fleet_new_2", s.TmuxSessionName, s.GetTmuxSession().Name)
	}
	if s.GetStatus() != StatusStarting {
		t.Errorf("status = %s, want starting", s.GetStatus())
	}
	if s.GetClaudeSessionID() != "c2" {
		t.Errorf("conversation = %q, want c2", s.GetClaudeSessionID())
	}
	if s.GetHookStatus() != "" || s.deathRecorded {
		t.Errorf("old agent's state survived: hook %q, deathRecorded %v", s.GetHookStatus(), s.deathRecorded)
	}
	if _, err := os.Stat(hookFile); err != nil {
		t.Errorf("the status file was removed: %v", err)
	}
}

func TestAdoptExternalSuspend(t *testing.T) {
	s := &Session{ID: "ext-test", Status: StatusError}
	s.UpdateHookStatus(&HookStatus{Status: "dead", SessionID: "c1", UpdatedAt: time.Now()}, false)
	s.AdoptExternalSuspend()
	if s.GetStatus() != StatusSuspended || s.GetHookStatus() != "" {
		t.Errorf("status = %s, hook = %q", s.GetStatus(), s.GetHookStatus())
	}
	// Suspended short-circuits the pipeline: no tmux, and no crash.
	s.UpdateStatus()
	if s.GetStatus() != StatusSuspended {
		t.Errorf("UpdateStatus moved a suspended session to %s", s.GetStatus())
	}
}

func TestLoadSessionLaunch(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SaveSession(&SessionRow{ID: "s1", Title: "t", Status: "suspended", TmuxSession: "fleet_x", ClaudeSessionID: "c9"}); err != nil {
		t.Fatal(err)
	}
	row, ok, err := db.LoadSessionLaunch("s1")
	if err != nil || !ok || row.Status != "suspended" || row.TmuxSession != "fleet_x" || row.ClaudeSessionID != "c9" {
		t.Errorf("row = %+v, ok = %v, err = %v", row, ok, err)
	}
	if _, ok, err := db.LoadSessionLaunch("gone"); ok || err != nil {
		t.Errorf("missing row: ok = %v, err = %v", ok, err)
	}
}

func TestRelaunching(t *testing.T) {
	s := &Session{ID: "r"}
	if s.Relaunching() {
		t.Fatal("a fresh session is not relaunching")
	}
	s.beginRelaunch()
	if !s.Relaunching() {
		t.Error("Relaunching = false inside beginRelaunch/endRelaunch")
	}
	s.endRelaunch()
	if s.Relaunching() {
		t.Error("Relaunching = true after endRelaunch")
	}
}
