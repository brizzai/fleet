package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/debuglog"
)

// Fixtures are real agy 1.5.14 panes (ANSI kept), captured at 120x40 with the
// user's name, email and paths scrubbed.
func antigravityFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "antigravity", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAntigravityPaneDetectors(t *testing.T) {
	cases := []struct {
		fixture          string
		waiting, running bool
	}{
		{"approval_command", true, false},
		{"approval_create", true, false},
		{"approval_edit", true, false},
		{"ask_question", true, false},
		{"trust", true, false},
		{"running", false, true},
		// The parent ended its turn; a subagent is still working.
		{"subagent_wait", false, true},
		{"after_subagent", false, false},
		{"idle", false, false},
		// "? for shortcuts" is gone while a draft is in the box.
		{"draft", false, false},
		// Esc mid-turn: at rest, and no hook says so.
		{"interrupted", false, false},
		// The user browsing /hooks has the same key hints, but nothing is asked.
		{"hooks_picker", false, false},
		// A subagent blocked on an approval: no menu, only a banner above the
		// input box, and the status bar reads idle.
		{"subagent_approval", true, false},
		{"subagents_blocked", true, false},
		// The same approval opened in the Alt+J manager.
		{"subagent_approval_manage", true, false},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			clean := StripANSI(antigravityFixture(t, c.fixture))
			if got := antigravityPaneWaiting(clean); got != c.waiting {
				t.Errorf("waiting = %v, want %v", got, c.waiting)
			}
			if got := antigravityPaneRunning(clean); got != c.running {
				t.Errorf("running = %v, want %v", got, c.running)
			}
			if got := PaneIndicatesWaiting(agent.Antigravity, antigravityFixture(t, c.fixture)); got != c.waiting {
				t.Errorf("PaneIndicatesWaiting = %v, want %v", got, c.waiting)
			}
		})
	}
}

// A reply quoting the prompt footer sits above the input box; only the real
// footer at the bottom may decide.
func TestAntigravityQuotedFooterIsNotAPrompt(t *testing.T) {
	idle := StripANSI(antigravityFixture(t, "idle"))
	i := strings.Index(idle, "> Auto mode")
	if i < 0 {
		t.Fatal("fixture changed: no input box")
	}
	quoted := "  The prompt reads \"↑/↓ Navigate · enter Select\"\n  esc to cancel\n"
	if antigravityPaneWaiting(idle[:i] + quoted + idle[i:]) {
		t.Error("a quoted footer above the input box read as waiting")
	}
}

// `fleet answer` reads the menu. Antigravity marks the highlighted row with ">".
func TestAntigravityMenusParse(t *testing.T) {
	v := ParsePaneView(agent.Antigravity, antigravityFixture(t, "approval_command"))
	if v.Menu == nil {
		t.Fatal("no menu parsed from the command prompt")
	}
	if len(v.Menu.Options) != 4 || v.Menu.Cursor != 0 || v.Menu.Question != "Run this command?" || v.Menu.Kind != "permission" {
		t.Errorf("command menu = %+v", v.Menu)
	}
	if v.Menu.Options[0].Label != "Yes, run command" {
		t.Errorf("first option = %q", v.Menu.Options[0].Label)
	}

	q := ParsePaneView(agent.Antigravity, antigravityFixture(t, "ask_question"))
	if q.Menu == nil || q.Menu.Kind != "question" || len(q.Menu.Options) != 3 {
		t.Fatalf("ask_question menu = %+v", q.Menu)
	}

	trust := ParsePaneView(agent.Antigravity, antigravityFixture(t, "trust"))
	if trust.Menu == nil || len(trust.Menu.Options) != 2 || trust.Menu.Cursor != 0 ||
		trust.Menu.Options[0].Label != "Yes, I trust this folder" {
		t.Fatalf("trust menu = %+v", trust.Menu)
	}
}

func newAntigravityTestSession(t *testing.T, pane string) (*Session, *mockPane) {
	t.Helper()
	debuglog.Init()
	mock := &mockPane{content: pane}
	s := &Session{
		ID:              "test-agy",
		Status:          StatusStarting,
		Agent:           agent.Antigravity,
		ClaudeSessionID: "own-id",
		paneCapturer:    mock,
		paneCmdFn:       func() string { return "agy" },
	}
	return s, mock
}

func agyHook(status, sessionID string) *HookStatus {
	return &HookStatus{Status: status, SessionID: sessionID, UpdatedAt: time.Now()}
}

func TestAntigravityNoHookSettlesIdle(t *testing.T) {
	s, _ := newAntigravityTestSession(t, antigravityFixture(t, "idle"))
	if got := s.initialRunStatus(); got != StatusIdle {
		t.Fatalf("initialRunStatus = %q, want idle", got)
	}
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusIdle {
		t.Fatalf("status = %q, want idle", got)
	}
}

// No hook fires on a permission prompt; the pane must show it, and the
// working footer after approval must show running again.
func TestAntigravityPromptReadsWaitingUnderARunningHook(t *testing.T) {
	s, mock := newAntigravityTestSession(t, antigravityFixture(t, "approval_command"))
	s.UpdateHookStatus(agyHook("running", "own-id"), true)
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusWaiting {
		t.Fatalf("at the prompt: status = %q, want waiting", got)
	}
	mock.content = antigravityFixture(t, "running")
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusRunning {
		t.Fatalf("after approving: status = %q, want running", got)
	}
}

// Esc mid-turn fires nothing, so the hook stays running. An at-rest pane ends
// it — but only once it has been seen twice, some time apart.
func TestAntigravityInterruptedTurnSettlesAfterConfirmation(t *testing.T) {
	s, _ := newAntigravityTestSession(t, antigravityFixture(t, "interrupted"))
	s.UpdateHookStatus(agyHook("running", "own-id"), true)
	s.SetStatus(StatusRunning)

	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusRunning {
		t.Fatalf("one at-rest frame demoted the session to %q", got)
	}
	s.mu.Lock()
	s.paneFinishedFirstAt = time.Now().Add(-paneFinishedConfirmDelay)
	s.mu.Unlock()
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusFinished {
		t.Fatalf("interrupted turn = %q, want finished", got)
	}
}

// A capture that fails says nothing about the pane. Two failures in a row,
// far enough apart to satisfy the confirmation, must not end a running turn.
func TestAntigravityFailedCaptureNeverSettlesARunningTurn(t *testing.T) {
	s, mock := newAntigravityTestSession(t, antigravityFixture(t, "interrupted"))
	s.UpdateHookStatus(agyHook("running", "own-id"), true)
	s.SetStatus(StatusRunning)
	mock.captureErr = errors.New("capture-pane timed out")
	for range 3 {
		s.mu.Lock()
		s.paneFinishedFirstAt = time.Now().Add(-time.Hour)
		s.mu.Unlock()
		s.UpdateStatus()
	}
	if got := s.GetStatus(); got != StatusRunning {
		t.Fatalf("status after failed captures = %q, want running", got)
	}
	if _, pending := s.PaneDemotionPendingSince(); pending {
		t.Error("a failed capture counted toward the at-rest confirmation")
	}
}

// A working frame between two at-rest ones breaks the run; it must not be
// bridged into a confirmation.
func TestAntigravityRunningFrameResetsTheConfirmation(t *testing.T) {
	s, mock := newAntigravityTestSession(t, antigravityFixture(t, "interrupted"))
	s.UpdateHookStatus(agyHook("running", "own-id"), true)
	s.SetStatus(StatusRunning)
	s.UpdateStatus()
	mock.content = antigravityFixture(t, "running")
	s.UpdateStatus()
	mock.content = antigravityFixture(t, "interrupted")
	s.mu.Lock()
	first := s.paneFinishedFirstAt
	s.mu.Unlock()
	if !first.IsZero() {
		t.Fatal("a running frame left a half-built confirmation in place")
	}
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusRunning {
		t.Fatalf("status = %q after one at-rest frame, want running", got)
	}
}

// agy has sent a turn's final Stop with fullyIdle=false after every subagent
// finished, so the hook says running (BackgroundWait) over an idle pane. The
// pane is right; the session must settle rather than run forever.
func TestAntigravityStaleBackgroundWaitSettles(t *testing.T) {
	s, _ := newAntigravityTestSession(t, antigravityFixture(t, "after_subagent"))
	s.UpdateHookStatus(agyHook("running", "own-id"), true)
	s.SetStatus(StatusRunning)
	for range 3 {
		s.mu.Lock()
		if !s.paneFinishedFirstAt.IsZero() {
			s.paneFinishedFirstAt = time.Now().Add(-time.Hour)
		}
		s.mu.Unlock()
		s.UpdateStatus()
	}
	if got := s.GetStatus(); got != StatusFinished {
		t.Fatalf("background wait over an idle pane = %q, want finished", got)
	}
}

// Subagents that really are working keep the session running: their dock rows
// survive a pane too narrow for the status bar's subagent count (measured at
// 40 columns, where agy drops the count).
func TestAntigravityNarrowPaneDockRowsReadRunning(t *testing.T) {
	narrow := strings.Join([]string{
		"  I delegated it and will report back.",
		"────────────────────────────────────────",
		">",
		"────────────────────────────────────────",
		"  ● Agent(self: Shell Runner 2)  . · 19s",
		"  ● Agent(self: Shell Runner 1)  . · 19s",
		"────────────────────────────────────────",
		"? for shortcuts  Gemini 3.8 Flash · high",
	}, "\n")
	if !antigravityPaneRunning(narrow) {
		t.Error("live subagents in a narrow pane read as at rest")
	}
	if antigravityPaneRunning(StripANSI(antigravityFixture(t, "after_subagent"))) {
		t.Error("finished subagents read as running")
	}
}

// A reply quoting a dock row sits above the input box; only the dock below it
// says a subagent is live.
func TestAntigravityQuotedDockRowIsNotALiveSubagent(t *testing.T) {
	pane := StripANSI(antigravityFixture(t, "after_subagent"))
	// The reply's own output line, not the "echo zzdone" in the prose above it.
	i := strings.LastIndex(pane, "zzdone")
	quoted := pane[:i] + "● Agent(example)" + pane[i+len("zzdone"):]
	if i < 0 || !strings.Contains(quoted, "\n    ● Agent(example)\n") {
		t.Fatal("fixture no longer holds the reply line this test rewrites")
	}
	if antigravityPaneRunning(quoted) {
		t.Error("a reply quoting a dock row read as a live subagent")
	}
}

// Y sends the key that approves what is on screen: Enter for agy's own prompts,
// Ctrl+K for a subagent's banner (Enter would submit the empty input box).
func TestAntigravityApproveKey(t *testing.T) {
	for fixture, want := range map[string]string{
		"approval_command":  "Enter",
		"approval_edit":     "Enter",
		"ask_question":      "Enter",
		"subagent_approval": "C-k",
		"subagents_blocked": "C-k",
		// Only the banner is a Ctrl+K approval; `fleet answer` relies on that
		// to tell it apart from the other waiting screens.
		"trust":                    "Enter",
		"subagent_approval_manage": "Enter",
	} {
		if got := AntigravityApproveKey(antigravityFixture(t, fixture)); got != want {
			t.Errorf("%s: approve key = %q, want %q", fixture, got, want)
		}
	}
}

func TestAntigravityFinishedHookAtRestIsFinished(t *testing.T) {
	s, _ := newAntigravityTestSession(t, antigravityFixture(t, "idle"))
	s.UpdateHookStatus(agyHook("finished", "own-id"), true)
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusFinished {
		t.Fatalf("status = %q, want finished", got)
	}
}

// /exit drops the pane to the launch shell with exit 0 and no hook.
func TestAntigravityExitAtAShellIsDead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _ := newAntigravityTestSession(t, "user@host ~ % \n")
	hs := agyHook("finished", "own-id")
	hs.AgentPID = 4242
	s.UpdateHookStatus(hs, true)
	s.SetStatus(StatusFinished)
	s.paneCmdFn = func() string { return "zsh" }

	s.ownerAliveFn = func() bool { return true } // Ctrl+Z: agy is alive under the shell
	s.UpdateStatus()
	if got := s.GetStatus(); got == StatusError {
		t.Fatal("a live agy under a shell pane read as dead")
	}
	s.ownerAliveFn = func() bool { return false }
	s.UpdateStatus()
	if got := s.GetStatus(); got != StatusError {
		t.Fatalf("exited agy at a shell = %q, want error", got)
	}
}

// /clear starts a new conversation in the same process: adopt it. An `agy -p`
// a tool call runs is another process with the owner still alive: don't.
func TestAntigravityAdoptsOnlyItsOwnProcessesConversations(t *testing.T) {
	s, _ := newAntigravityTestSession(t, antigravityFixture(t, "idle"))
	owner := agyHook("finished", "own-id")
	owner.AgentPID = os.Getpid() // alive
	s.UpdateHookStatus(owner, true)

	nested := agyHook("running", "nested-id")
	nested.AgentPID = os.Getppid()
	s.UpdateHookStatus(nested, true)
	if got := s.GetClaudeSessionID(); got != "own-id" {
		t.Fatalf("a nested agy's conversation took the session: id = %q", got)
	}

	cleared := agyHook("running", "after-clear")
	cleared.AgentPID = os.Getpid()
	s.UpdateHookStatus(cleared, true)
	if got := s.GetClaudeSessionID(); got != "after-clear" {
		t.Fatalf("/clear in the same process not followed: id = %q", got)
	}
}

func TestParseAntigravityTitle(t *testing.T) {
	cases := map[string]string{
		`title:"Request To Reply Pong"`:            "Request To Reply Pong",
		"title:\"spike-title-xyz\"\n":              "spike-title-xyz",
		`title:"say \"hi\""`:                       `say "hi"`,
		`title:"caf\303\251"`:                      "café", // non-ASCII as octal bytes
		`title:"Fix User\'s Login"`:                "Fix User's Login",
		`title:"Fix User's Login"`:                 "Fix User's Login",
		`title:"back\\"`:                           `back\`, // an escaped backslash, not \'
		`title:"a\\\'b"`:                           `a\'b`,
		`other:"x"`:                                "",
		`title:"two\nlines"`:                       "",
		`title:"` + strings.Repeat("a", 200) + `"`: "",
	}
	for in, want := range cases {
		if got := parseAntigravityTitle(in); got != want {
			t.Errorf("parseAntigravityTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// The id comes from a hook and becomes a path; only a UUID may.
func TestReadAntigravitySessionNameRefusesNonUUIDs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "annotations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "aa184a65-adb5-41d6-8d1c-28f11eeaeeb7"
	if err := os.WriteFile(filepath.Join(dir, id+".pbtxt"), []byte(`title:"Pong"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadAntigravitySessionName(id); got != "Pong" {
		t.Errorf("title = %q, want Pong", got)
	}
	if got := ReadAgentSessionName(agent.Antigravity, id, ""); got != "Pong" {
		t.Errorf("ReadAgentSessionName = %q, want Pong", got)
	}
	if got := ReadAntigravitySessionName("../annotations/" + id); got != "" {
		t.Errorf("a path-shaped id was read: %q", got)
	}
}

// The trust JSON carries the realpath (agy shows /tmp as /private/tmp) and
// travels in the session env, never the typed command.
func TestAntigravitySessionEnvCarriesTrust(t *testing.T) {
	debuglog.Init()
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	s := &Session{ID: "x", Agent: agent.Antigravity, ProjectPath: link}
	want := agent.AntigravitySettingsEnvVar + "=" + agent.AntigravitySettings(real)
	found := false
	for _, kv := range s.sessionEnv() {
		found = found || kv == want
	}
	if !found {
		t.Errorf("session env lacks %q: %q", want, s.sessionEnv())
	}
	if strings.Contains(s.buildAgentCmd(), dir) {
		t.Errorf("project path leaked into the command: %q", s.buildAgentCmd())
	}
	for _, kv := range (&Session{ID: "y", Agent: agent.Claude, ProjectPath: dir}).sessionEnv() {
		if strings.HasPrefix(kv, agent.AntigravitySettingsEnvVar+"=") {
			t.Error("a Claude session carries Antigravity's settings")
		}
	}
}
