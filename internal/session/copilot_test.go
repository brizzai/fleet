package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/debuglog"
)

// Screens captured from a live Copilot CLI 1.0.90 pane (paths sanitized).
func copilotFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "copilot", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCopilotPaneDetectors(t *testing.T) {
	cases := []struct {
		fixture          string
		waiting, running bool
	}{
		{"idle", false, false},
		{"done", false, false},
		{"working", false, true},
		{"permission", true, false},
		{"trust", true, false}, // a lost trust seed must read as waiting, not a silent idle
		// Reconstructed from the probe's capture notes (footer verbatim), not a raw capture.
		{"ask_user", true, false},
		// Same footer words as the permission prompt, but outside a box: a
		// picker the user opened, not Copilot blocked on them.
		{"theme_picker", false, false},
	}
	for _, c := range cases {
		pane := copilotFixture(t, c.fixture)
		if got := copilotPaneWaiting(pane); got != c.waiting {
			t.Errorf("%s: copilotPaneWaiting = %v, want %v", c.fixture, got, c.waiting)
		}
		if got := copilotPaneRunning(pane); got != c.running {
			t.Errorf("%s: copilotPaneRunning = %v, want %v", c.fixture, got, c.running)
		}
		if got := PaneIndicatesWaiting(agent.Copilot, pane); got != c.waiting {
			t.Errorf("%s: PaneIndicatesWaiting = %v, want %v", c.fixture, got, c.waiting)
		}
	}
}

// A Claude-style numbered menu deep in scrollback must not flip Copilot: only
// Copilot's own footers, in the live bottom region, count.
func TestUpdateStatusCopilotNeverRunsClaudePaneHeuristics(t *testing.T) {
	claudeMenu := "Do you want to proceed?\n❯ 1. Yes\n  2. No\n\nEsc to cancel\n"
	s, _ := newCopilotTestSession(t, claudeMenu)
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusIdle {
		t.Fatalf("status = %q, want idle", got)
	}
}

func newCopilotTestSession(t *testing.T, pane string) (*Session, *mockPane) {
	t.Helper()
	debuglog.Init()
	mock := &mockPane{content: pane}
	s := &Session{
		ID:               "test-copilot",
		Status:           StatusStarting,
		Agent:            agent.Copilot,
		ClaudeSessionID:  "own-id",
		paneCapturer:     mock,
		paneCmdFn:        func() string { return "node" },
		copilotAbortedFn: func() bool { return false },
	}
	return s, mock
}

func copilotHook(status, sessionID string) *HookStatus {
	return &HookStatus{Status: status, SessionID: sessionID, UpdatedAt: time.Now()}
}

func TestCopilotInitialRunStatusIdle(t *testing.T) {
	s := &Session{Agent: agent.Copilot}
	if got := s.initialRunStatus(); got != StatusIdle {
		t.Fatalf("initialRunStatus = %q, want idle", got)
	}
}

func TestCopilotNoHookSettlesIdle(t *testing.T) {
	s, _ := newCopilotTestSession(t, copilotFixture(t, "idle"))
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusIdle {
		t.Fatalf("status = %q, want idle", got)
	}
}

// Approving a permission fires no hook until the tool finishes, so the stored
// hook stays "waiting" while the tool runs. The working footer overrides it.
func TestCopilotPaneRunningOverridesStaleWaiting(t *testing.T) {
	s, mock := newCopilotTestSession(t, copilotFixture(t, "permission"))
	s.UpdateHookStatus(copilotHook("waiting", "own-id"), true)
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusWaiting {
		t.Fatalf("at the prompt: status = %q, want waiting", got)
	}
	mock.content = copilotFixture(t, "working")
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusRunning {
		t.Fatalf("after approving: status = %q, want running", got)
	}
}

// Ctrl+C mid-turn and Esc at a permission prompt fire no hook; events.jsonl's
// trailing abort is the only record, and without it the row sticks on running.
func TestCopilotAbortedTurnSettlesFinished(t *testing.T) {
	s, _ := newCopilotTestSession(t, copilotFixture(t, "done"))
	aborted := false
	s.copilotAbortedFn = func() bool { return aborted }
	s.UpdateHookStatus(copilotHook("running", "own-id"), true)
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusRunning {
		t.Fatalf("before abort: status = %q, want running", got)
	}
	aborted = true
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusFinished {
		t.Fatalf("after abort: status = %q, want finished", got)
	}
}

// SessionEnd fires for /clear and /exit alike, with the same reason. The pane
// decides: still the agent means /clear, back at a shell means it exited.
func TestCopilotSessionEndClearVsExit(t *testing.T) {
	s, _ := newCopilotTestSession(t, copilotFixture(t, "idle"))
	end := copilotHook("finished", "own-id")
	end.Reason = "user_exit"
	s.UpdateHookStatus(end, true)
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusFinished {
		t.Fatalf("/clear: status = %q, want finished", got)
	}

	s.paneCmdFn = func() string { return "zsh" }
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusError {
		t.Fatalf("/exit: status = %q, want error", got)
	}
}

// One Copilot process can host several conversations, so a hook carrying
// another id (a background session, /resume) must never be adopted while the
// pre-minted owner is live — `r` would resume the wrong conversation.
func TestCopilotForeignSessionIDHookIgnored(t *testing.T) {
	s, _ := newCopilotTestSession(t, copilotFixture(t, "idle"))
	if s.UpdateHookStatus(copilotHook("running", "someone-else"), true) {
		t.Fatal("foreign hook reported a change")
	}
	if got := s.GetClaudeSessionID(); got != "own-id" {
		t.Fatalf("resume id = %q, want own-id", got)
	}
	if !s.UpdateHookStatus(copilotHook("running", "own-id"), true) {
		t.Fatal("owner's hook was dropped")
	}
}

// /new keeps the old conversation open and starts another; /clear ends it and
// starts another. Either way the session follows the conversation the user
// types into — and only that one: the old conversation, still open in the
// background, can't pull it back without a prompt of its own.
func TestCopilotFollowsTheConversationTheUserTypesIn(t *testing.T) {
	s, _ := newCopilotTestSession(t, copilotFixture(t, "idle"))
	s.UpdateHookStatus(copilotHook("running", "own-id"), true)

	prompt := copilotHook("running", "new-id")
	prompt.PromptSessionID = "new-id"
	if !s.UpdateHookStatus(prompt, true) {
		t.Fatal("the conversation the user typed into was not adopted")
	}
	if got := s.GetClaudeSessionID(); got != "new-id" {
		t.Fatalf("resume id = %q, want new-id", got)
	}

	background := copilotHook("finished", "own-id")
	background.PromptSessionID = "new-id"
	if s.UpdateHookStatus(background, true) {
		t.Fatal("the backgrounded old conversation took the session back")
	}
}

// An agent CLI that the user's Copilot runs as a tool inherits FLEET_INSTANCE_ID
// and fires its own prompt hook; only the owning process may move the session.
func TestCopilotNestedAgentPromptCannotTakeOver(t *testing.T) {
	s, _ := newCopilotTestSession(t, copilotFixture(t, "idle"))
	own := copilotHook("running", "own-id")
	own.AgentPID = os.Getpid() // a live owner
	s.UpdateHookStatus(own, true)

	nested := copilotHook("running", "nested-id")
	nested.PromptSessionID, nested.AgentPID = "nested-id", 300
	if s.UpdateHookStatus(nested, true) {
		t.Fatal("a nested agent's prompt took the session over")
	}
	if got := s.GetClaudeSessionID(); got != "own-id" {
		t.Fatalf("resume id = %q, want own-id", got)
	}

	newConv := copilotHook("running", "new-id")
	newConv.PromptSessionID, newConv.AgentPID = "new-id", os.Getpid()
	if !s.UpdateHookStatus(newConv, true) {
		t.Fatal("/new in the owning process was not followed")
	}
}

// /exit drops the pane to a shell; a Copilot the user then starts there by hand
// is the session now, though its pid differs from the dead owner's.
func TestCopilotAdoptsAManualRelaunchAfterExit(t *testing.T) {
	s, _ := newCopilotTestSession(t, copilotFixture(t, "idle"))
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	own := copilotHook("finished", "own-id")
	own.AgentPID = dead.Process.Pid // exited and reaped
	s.UpdateHookStatus(own, true)

	relaunched := copilotHook("running", "manual-id")
	relaunched.PromptSessionID, relaunched.AgentPID = "manual-id", os.Getpid()
	if !s.UpdateHookStatus(relaunched, true) {
		t.Fatal("the hand-started Copilot was not adopted")
	}
	if got := s.GetClaudeSessionID(); got != "manual-id" {
		t.Fatalf("resume id = %q, want manual-id", got)
	}
}

// fleet closed across a /clear or /new: the status file still says which
// conversation the user last typed into, so the reopened session follows it.
func TestCopilotAdoptsLastPromptedConversationOnReopen(t *testing.T) {
	s, _ := newCopilotTestSession(t, copilotFixture(t, "done"))
	stop := copilotHook("finished", "new-id")
	stop.PromptSessionID = "new-id"
	if !s.UpdateHookStatus(stop, false) {
		t.Fatal("the last-prompted conversation was not adopted")
	}
	if got := s.GetClaudeSessionID(); got != "new-id" {
		t.Fatalf("resume id = %q, want new-id", got)
	}
}

func TestCopilotFirstLaunchMintsIDAndIsNotAResume(t *testing.T) {
	s := &Session{Agent: agent.Copilot}
	if s.launchWillResumeLocked() {
		t.Fatal("first launch reported as a resume")
	}
	id := s.ClaudeSessionID
	if len(id) != 36 {
		t.Fatalf("minted id = %q, want a UUID", id)
	}
	if !strings.Contains(s.buildAgentCmd(), "--session-id="+id) {
		t.Fatalf("launch command %q does not carry the minted id", s.buildAgentCmd())
	}
	if !s.launchWillResumeLocked() || s.ClaudeSessionID != id {
		t.Fatal("relaunch must resume the same id")
	}

	c := &Session{Agent: agent.Claude}
	if c.launchWillResumeLocked() || c.ClaudeSessionID != "" {
		t.Fatal("Claude must not get a pre-minted id")
	}
}

func TestCopilotEventsEndInAbort(t *testing.T) {
	cases := map[string]struct {
		lines []string
		want  bool
	}{
		"ctrl+c mid-tool": {[]string{
			`{"type":"user.message","data":{}}`,
			`{"type":"tool.execution_start","data":{}}`,
			`{"type":"assistant.turn_end","data":{"turnId":"0"}}`,
			`{"type":"abort","data":{"reason":"user_initiated"}}`,
			`{"type":"session.usage_checkpoint","data":{}}`,
			`{"type":"hook.end","data":{}}`,
		}, true},
		"normal end": {[]string{
			`{"type":"user.message","data":{}}`,
			`{"type":"assistant.turn_end","data":{"turnId":"1"}}`,
			`{"type":"session.usage_checkpoint","data":{}}`,
		}, false},
		"new prompt after abort": {[]string{
			`{"type":"abort","data":{}}`,
			`{"type":"user.message","data":{}}`,
		}, false},
		"between steps": {[]string{
			`{"type":"assistant.turn_end","data":{"turnId":"0"}}`,
			`{"type":"assistant.turn_start","data":{"turnId":"1"}}`,
			`{"type":"model.model_call_started","data":{}}`,
		}, false},
		"garbage": {[]string{`not json`, `{"type":`}, false},
	}
	for name, c := range cases {
		path := filepath.Join(t.TempDir(), "events.jsonl")
		if err := os.WriteFile(path, []byte(strings.Join(c.lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := copilotEventsEndInAbort(path); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
	if copilotEventsEndInAbort(filepath.Join(t.TempDir(), "missing.jsonl")) {
		t.Error("a missing file must be no signal")
	}
}

// The window is a tail; a cut first line must be skipped, not misparsed.
func TestCopilotEventsEndInAbortLargeFile(t *testing.T) {
	big := `{"type":"model.messages_snapshot","data":{"x":"` + strings.Repeat("a", copilotEventsTail) + `"}}`
	path := filepath.Join(t.TempDir(), "events.jsonl")
	content := big + "\n" + `{"type":"abort","data":{}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if !copilotEventsEndInAbort(path) {
		t.Fatal("trailing abort not found past a large line")
	}
}

// Only the live bottom region counts: a menu footer that has scrolled up (the
// prompt was answered, output followed) must not read as waiting.
func TestCopilotPaneDetectorsIgnoreScrollback(t *testing.T) {
	pane := copilotFixture(t, "permission") + strings.Repeat("output line\n", 20) + copilotFixture(t, "idle")
	if copilotPaneWaiting(pane) {
		t.Error("a permission footer 20+ lines up read as waiting")
	}
}

// Esc at a permission prompt fires no hook either, so a stuck "waiting" hook
// must settle the same way a stuck "running" one does.
func TestCopilotAbortedWhileWaitingSettlesFinished(t *testing.T) {
	s, _ := newCopilotTestSession(t, copilotFixture(t, "done"))
	s.copilotAbortedFn = func() bool { return true }
	s.UpdateHookStatus(copilotHook("waiting", "own-id"), true)
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusFinished {
		t.Fatalf("status = %q, want finished", got)
	}
}

// The real path: $COPILOT_HOME/session-state/<id>/events.jsonl, no seam.
func TestCopilotTurnAbortedReadsCopilotHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	dir := filepath.Join(home, "session-state", "own-id")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	events := `{"type":"abort","data":{}}` + "\n" + `{"type":"system.message","data":{}}` + "\n" + `{"type":"model.turn_ended","data":{}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Session{Agent: agent.Copilot, ClaudeSessionID: "own-id"}
	if !s.copilotTurnAborted() {
		t.Fatal("abort followed by system/model bookkeeping not detected")
	}
}

// `r` kills the old Copilot, whose shutdown can still fire a SessionEnd after
// the hook state was cleared — same conversation id as the new process, so only
// the pid separates it. Accepted, it plus the still-shell new pane read as dead.
func TestRelaunchDropsTheKilledAgentsDeathRattle(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // clearHookState removes the hook file under HOME
	s, _ := newCopilotTestSession(t, copilotFixture(t, "idle"))
	running := copilotHook("running", "own-id")
	running.AgentPID = 100
	s.UpdateHookStatus(running, true)

	s.clearHookState() // what Restart / RespawnClaude / Suspend do before relaunching
	s.SetStatus(StatusIdle)
	s.paneCmdFn = func() string { return "zsh" } // new pane still typing the launch command

	rattle := copilotHook("finished", "own-id")
	rattle.Reason, rattle.AgentPID = "user_exit", 100
	if s.UpdateHookStatus(rattle, true) {
		t.Fatal("the killed process's SessionEnd was accepted")
	}
	s.UpdateStatus()
	if got := s.GetStatus(); got == StatusError {
		t.Fatal("restart flashed error from the old process's hook")
	}

	fresh := copilotHook("running", "own-id")
	fresh.AgentPID = 200
	if !s.UpdateHookStatus(fresh, true) {
		t.Fatal("the relaunched process's hook was dropped")
	}
}

// A relaunch waits for the old Copilot to exit — a new one that starts while it
// is still shutting down opens on "Session in use" instead of the conversation.
func TestAwaitRetiredCopilotWaitsForExit(t *testing.T) {
	cmd := exec.Command(fakeCopilotBinary(t), "0.3")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }() // reap it, or a zombie reads as alive
	s := &Session{Agent: agent.Copilot}
	start := time.Now()
	s.awaitRetiredCopilot(cmd.Process.Pid, false)
	if agentProcessAlive(cmd.Process.Pid) {
		t.Fatal("returned while the old process was still running")
	}
	if time.Since(start) > retiredExitTimeout {
		t.Fatal("waited past the timeout")
	}
}

// fakeCopilotBinary is sleep under Copilot's name, which isCopilotProcess checks.
func fakeCopilotBinary(t *testing.T) string {
	t.Helper()
	src, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copilot")
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return dst
}

// A pid recycled to another process must not hold up the relaunch.
func TestAwaitRetiredCopilotIgnoresAnotherProcess(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	s := &Session{Agent: agent.Copilot}
	start := time.Now()
	s.awaitRetiredCopilot(cmd.Process.Pid, false)
	if time.Since(start) > time.Second {
		t.Fatal("waited on a process that isn't Copilot's")
	}
}

// The pid comes from a hook and may have been recycled: stop must never signal
// a process that isn't Copilot's.
func TestAwaitRetiredCopilotNeverSignalsAnotherProcess(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	s := &Session{Agent: agent.Copilot}
	s.awaitRetiredCopilot(cmd.Process.Pid, true)
	if !agentProcessAlive(cmd.Process.Pid) {
		t.Fatal("signalled a non-Copilot process")
	}
}

// While a relaunch is in flight the old tmux is gone (Restart waits up to 5s for
// a busy Copilot to exit after the kill); the row must read starting, not crashed.
func TestRelaunchInFlightIsNotACrash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, mock := newCopilotTestSession(t, copilotFixture(t, "working"))
	s.SetStatus(StatusRunning)
	s.beginRelaunch()
	mock.tmuxGone = true
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusStarting {
		t.Fatalf("status during relaunch = %q, want starting", got)
	}
	s.endRelaunch()
	s.mu.Lock()
	s.deathRecorded = true // keep the real dump writer (async, under HOME) out of the test
	s.mu.Unlock()
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusError {
		t.Fatalf("status after relaunch with tmux gone = %q, want error", got)
	}
}

// The death-rattle drop is for every agent: Claude's killed process fires a
// SessionEnd(dead) under the same conversation id the relaunch resumes.
func TestRelaunchDropsClaudesDeathRattle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := &Session{ID: "test-claude", Agent: agent.Claude, ClaudeSessionID: "conv", paneCapturer: &mockPane{}}
	s.UpdateHookStatus(&HookStatus{Status: "running", SessionID: "conv", AgentPID: 100, UpdatedAt: time.Now()}, true)
	s.clearHookState()
	if s.UpdateHookStatus(&HookStatus{Status: "dead", SessionID: "conv", AgentPID: 100, UpdatedAt: time.Now()}, true) {
		t.Fatal("the killed Claude's SessionEnd was accepted")
	}
	if !s.UpdateHookStatus(&HookStatus{Status: "finished", SessionID: "conv", AgentPID: 200, UpdatedAt: time.Now()}, true) {
		t.Fatal("the relaunched Claude's hook was dropped")
	}
}

// respawn-pane leaves no gap to wait in, so stop hangs the old Copilot up itself.
func TestAwaitRetiredCopilotStopsTheOldProcess(t *testing.T) {
	cmd := exec.Command(fakeCopilotBinary(t), "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	s := &Session{Agent: agent.Copilot}
	start := time.Now()
	s.awaitRetiredCopilot(cmd.Process.Pid, true)
	if agentProcessAlive(cmd.Process.Pid) {
		t.Fatal("old Copilot still running after stop")
	}
	if time.Since(start) > retiredExitTimeout {
		t.Fatal("waited out the timeout instead of stopping it")
	}
}

// A killed Copilot's sessionEnd arrives as "finished" (Copilot sends the same
// one for /clear), so it must not re-arm the crash dump the death just wrote.
func TestCopilotSessionEndDoesNotRearmCrashDump(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _ := newCopilotTestSession(t, copilotFixture(t, "working"))
	running := copilotHook("running", "own-id")
	running.AgentPID = 100
	s.UpdateHookStatus(running, true)

	s.mu.Lock()
	s.deathRecorded = true // the dump this death already wrote
	s.mu.Unlock()

	end := copilotHook("finished", "own-id")
	end.Reason, end.AgentPID = "user_exit", 100
	s.UpdateHookStatus(end, true)
	s.mu.RLock()
	rearmed := !s.deathRecorded
	s.mu.RUnlock()
	if rearmed {
		t.Fatal("sessionEnd re-armed the crash dump; the next tick writes a second one")
	}
}

// Two relaunches can overlap (r, then Reload All, while a Copilot exits); the
// first to finish must not reopen the window for the second.
func TestOverlappingRelaunchesKeepTheGuard(t *testing.T) {
	s, mock := newCopilotTestSession(t, copilotFixture(t, "working"))
	s.beginRelaunch()
	s.beginRelaunch()
	s.endRelaunch()
	mock.tmuxGone = true
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusStarting {
		t.Fatalf("status with one relaunch still in flight = %q, want starting", got)
	}
}
