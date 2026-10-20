package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/control"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/session"
)

func permissionMenu() *session.Menu {
	return &session.Menu{
		Kind:     "permission",
		Question: "Do you want to proceed?",
		Context:  []string{"Bash command", "Write fleet-test to note.txt", "echo fleet-test > note.txt"},
		Subject:  "echo fleet-test > note.txt",
		Options: []session.MenuOption{
			{N: 1, Label: "Yes"},
			{N: 2, Label: "Yes, and always allow access to"},
			{N: 3, Label: "No, and tell Claude what to do differently (esc)"},
		},
		Cursor: 0,
	}
}

func TestPickOption(t *testing.T) {
	m := permissionMenu()
	for _, tt := range []struct {
		choice string
		want   int
	}{
		{"1", 0}, {"3", 2}, {"yes", 0}, {"Y", 0}, {"approve", 0},
		{"no", 2}, {"deny", 2}, {" NO ", 2},
	} {
		got, err := pickOption(m, tt.choice)
		if err != nil || got != tt.want {
			t.Errorf("pickOption(%q) = %d, %v; want %d", tt.choice, got, err, tt.want)
		}
	}
	for _, bad := range []string{"4", "0", "maybe", ""} {
		if _, err := pickOption(m, bad); err == nil {
			t.Errorf("pickOption(%q) should fail", bad)
		}
	}

	// yes/no only map onto labels that open with the word: "Nobody" is not No.
	q := &session.Menu{Options: []session.MenuOption{{N: 1, Label: "Nobody"}, {N: 2, Label: "Yesterday"}}}
	for _, c := range []string{"no", "yes"} {
		if _, err := pickOption(q, c); err == nil || !strings.Contains(err.Error(), "pick one by number") {
			t.Errorf("pickOption(%q) on a menu with no yes/no row: err = %v", c, err)
		}
	}
}

func TestMenuMentionsAndMultiSelect(t *testing.T) {
	m := permissionMenu()
	if !menuMentions(m, "NOTE.TXT") || !menuMentions(m, "proceed") || menuMentions(m, "rm -rf") {
		t.Error("menuMentions should search the question and context, case-insensitively")
	}
	if isMultiSelect(m) {
		t.Error("a permission menu is not multi-select")
	}
	cb := &session.Menu{Options: []session.MenuOption{{N: 1, Label: "[ ] Users"}, {N: 2, Label: "[✔] Orgs"}}}
	if !isMultiSelect(cb) {
		t.Error("checkbox rows are multi-select")
	}
}

func TestMenuContextSummary(t *testing.T) {
	if got := menuContextSummary(permissionMenu()); got != "Bash command: echo fleet-test > note.txt" {
		t.Errorf("summary = %q", got)
	}
	noSubject := &session.Menu{Context: []string{"Create file", "../tmp/a.json", "1 hello"}}
	if got := menuContextSummary(noSubject); got != "Create file: ../tmp/a.json" {
		t.Errorf("summary = %q", got)
	}
}

func TestParseVerbArgs(t *testing.T) {
	t.Run("answer takes a session and a choice, flags either side", func(t *testing.T) {
		o, err := parseVerbArgs(verbs["answer"], []string{"fix-login", "2", "--expect", "note.txt"})
		if err != nil || o.selector != "fix-login" || o.arg != "2" || o.expect != "note.txt" {
			t.Fatalf("o = %+v, err = %v", o, err)
		}
		o, err = parseVerbArgs(verbs["answer"], []string{"-expect=x", "fix-login", "yes"})
		if err != nil || o.arg != "yes" || o.expect != "x" {
			t.Fatalf("o = %+v, err = %v", o, err)
		}
	})
	t.Run("missing and extra arguments", func(t *testing.T) {
		for _, tt := range []struct {
			verb string
			args []string
			want string
		}{
			{"answer", []string{"fix-login"}, "missing choice"},
			{"answer", nil, "missing session"},
			{"interrupt", nil, "missing session"},
			{"submit", []string{"a", "b"}, "unexpected argument"},
		} {
			if _, err := parseVerbArgs(verbs[tt.verb], tt.args); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%s %v: err = %v, want %q", tt.verb, tt.args, err, tt.want)
			}
		}
	})
	t.Run("only the verbs that can force accept -force", func(t *testing.T) {
		if _, err := parseVerbArgs(verbs["interrupt"], []string{"-force", "s"}); err != nil {
			t.Errorf("interrupt -force: %v", err)
		}
		if _, err := parseVerbArgs(verbs["submit"], []string{"-force", "s"}); err == nil {
			t.Error("submit has no -force; it must not be silently accepted")
		}
		if o, err := parseVerbArgs(verbs["restart"], []string{"s", "-fresh"}); err != nil || !o.fresh {
			t.Errorf("restart -fresh: %+v, %v", o, err)
		}
	})
}

func TestBusyGuard(t *testing.T) {
	for _, tt := range []struct {
		pane   control.PaneState
		status session.Status
		want   bool
	}{
		{control.PaneAlive, session.StatusRunning, true},
		{control.PaneAlive, session.StatusWaiting, true},
		{control.PaneAlive, session.StatusFinished, false},
		{control.PaneShell, session.StatusRunning, false}, // exited: nothing to interrupt
		{control.PaneGone, session.StatusError, false},
	} {
		if got := busy(&control.Observation{Pane: tt.pane, Status: tt.status}); got != tt.want {
			t.Errorf("busy(%s, %s) = %v, want %v", tt.pane, tt.status, got, tt.want)
		}
	}
}

func TestRequireLivePane(t *testing.T) {
	row := &session.SessionRow{Title: "s"}
	if err := requireLivePane(&control.Observation{Row: row, Pane: control.PaneAlive}); err != nil {
		t.Errorf("alive pane refused: %v", err)
	}
	for pane, want := range map[control.PaneState]string{
		control.PaneSuspended: "fleet resume",
		control.PaneGone:      "fleet restart",
		control.PaneDead:      "fleet restart",
		control.PaneShell:     "would go to the shell",
	} {
		err := requireLivePane(&control.Observation{Row: row, Pane: pane, PaneCmd: "zsh"})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to mention %q", pane, err, want)
		}
	}
}

func TestResolveSessionExact(t *testing.T) {
	rows := testRows()
	// "Doc" is a title substring of one session: send would match it, remove
	// must refuse.
	if got, err := resolveSession(rows, nil, "Doc", noBranch); err != nil || got.Title != "Docs" {
		t.Fatalf("fuzzy resolve: %v, %v", got, err)
	}
	if _, err := resolveSessionExact(rows, nil, "Doc", noBranch); err == nil {
		t.Error("resolveSessionExact matched a title substring")
	}
	if got, err := resolveSessionExact(rows, nil, "cccc", noBranch); err != nil || got.Title != "Docs" {
		t.Errorf("id prefix: %v, %v", got, err)
	}
}

func TestWaitPredicates(t *testing.T) {
	ob := func(p control.PaneState, s session.Status) *control.Observation {
		return &control.Observation{Pane: p, Status: s}
	}
	running := ob(control.PaneAlive, session.StatusRunning)
	waiting := ob(control.PaneAlive, session.StatusWaiting)
	finished := ob(control.PaneAlive, session.StatusFinished)
	idle := ob(control.PaneAlive, session.StatusIdle)
	exited := ob(control.PaneShell, session.StatusRunning)

	cases := []struct {
		until   string
		ob      *control.Observation
		matches bool
		blocked bool
		exited  bool
	}{
		{"done", running, false, false, false},
		{"done", waiting, true, false, false},
		{"done", finished, true, false, false},
		{"done", exited, true, false, false},
		{"idle", finished, true, false, false},
		{"idle", idle, true, false, false},
		{"idle", waiting, false, true, false},
		{"idle", exited, false, false, true},
		{"waiting", waiting, true, false, false},
		{"waiting", running, false, false, false},
		{"running", running, true, false, false},
		{"running", waiting, false, true, false},
	}
	for _, c := range cases {
		if got := waitMatches(c.until, c.ob); got != c.matches {
			t.Errorf("waitMatches(%s, %s/%s) = %v", c.until, c.ob.Pane, c.ob.Status, got)
		}
		if got := waitBlocked(c.until, c.ob); got != c.blocked {
			t.Errorf("waitBlocked(%s, %s/%s) = %v", c.until, c.ob.Pane, c.ob.Status, got)
		}
		if got := waitExited(c.until, c.ob); got != c.exited {
			t.Errorf("waitExited(%s, %s/%s) = %v", c.until, c.ob.Pane, c.ob.Status, got)
		}
	}
}

// A matching state counts only on evidence it belongs to the turn being waited
// on — otherwise `fleet send -wait` returns the previous turn's reply.
func TestWaitCounts(t *testing.T) {
	since := time.Unix(1_000_005, 700_000_000) // a send at :05.7
	sinceSec := since.Truncate(time.Second)
	hook := func(event string, at int64) *control.Observation {
		return &control.Observation{Pane: control.PaneAlive, Status: session.StatusFinished, HookEvent: event, HookAt: time.Unix(at, 0)}
	}

	for _, c := range []struct {
		name    string
		ob      *control.Observation
		moved   bool
		settled bool
		want    bool
	}{
		{"a Stop in a later second ends the turn", hook("Stop", 1_000_009), false, false, true},
		// Stamped :05 — it may be from :05.1, before the send.
		{"a Stop in the send's own second is not evidence", hook("Stop", 1_000_005), false, false, false},
		{"an older Stop is not evidence", hook("Stop", 1_000_001), false, false, false},
		{"the turn's own prompt is not its end", hook("UserPromptSubmit", 1_000_007), false, false, false},
		{"seen moving, then done", hook("Stop", 1_000_001), true, false, true},
		// A wake or a launch with a prompt: Restart shows running, then the
		// agent fires SessionStart before it reads the prompt.
		{"SessionStart after a wake is not the turn ending", hook("SessionStart", 1_000_007), false, false, false},
		{"SessionStart after the relaunch showed running", hook("SessionStart", 1_000_007), true, false, false},
		{"settle counts anything", hook("SessionStart", 1_000_007), true, true, true},
		{"no hook at all waits for settle", &control.Observation{Pane: control.PaneAlive, Status: session.StatusIdle}, false, false, false},
	} {
		if got := waitCounts(c.ob, c.moved, sinceSec, c.settled); got != c.want {
			t.Errorf("%s: waitCounts = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseWaitArgs(t *testing.T) {
	o, err := parseWaitArgs([]string{"a", "b", "--until", "idle", "--timeout", "90s", "--any"})
	if err != nil || len(o.selectors) != 2 || o.until != "idle" || o.timeout != 90*time.Second || !o.any {
		t.Fatalf("o = %+v, err = %v", o, err)
	}
	if o, _ := parseWaitArgs([]string{"a"}); o.until != "done" {
		t.Errorf("default -until = %q, want done", o.until)
	}
	for _, args := range [][]string{nil, {"a", "-until", "forever"}, {"a", "-timeout", "0s"}} {
		if _, err := parseWaitArgs(args); err == nil {
			t.Errorf("parseWaitArgs(%v) should fail", args)
		}
	}
}

func TestParseStatusAndReadArgs(t *testing.T) {
	o, err := parseStatusArgs([]string{"--waiting", "a", "--json", "b"})
	if err != nil || !o.waiting || !o.json || len(o.selectors) != 2 {
		t.Fatalf("status: %+v, %v", o, err)
	}
	r, err := parseReadArgs([]string{"a", "--lines", "50"})
	if err != nil || r.selector != "a" || r.lines != 50 || r.turns != 3 {
		t.Fatalf("read: %+v, %v", r, err)
	}
	for _, args := range [][]string{nil, {"a", "b"}, {"a", "-turns", "0"}} {
		if _, err := parseReadArgs(args); err == nil {
			t.Errorf("parseReadArgs(%v) should fail", args)
		}
	}
}

func TestFormatting(t *testing.T) {
	for d, want := range map[time.Duration]string{
		45 * time.Second:            "45s",
		12 * time.Minute:            "12m",
		3*time.Hour + 5*time.Minute: "3h5m",
		30 * time.Hour:              "1d6h",
		12 * 24 * time.Hour:         "12d",
	} {
		if got := humanDur(d); got != want {
			t.Errorf("humanDur(%v) = %q, want %q", d, got, want)
		}
	}
	if got := clip("line one\nline two", 100); got != "line one ⏎ line two" {
		t.Errorf("clip multi-line = %q", got)
	}
	if got := clip("abcdefghij", 5); got != "abcd…" {
		t.Errorf("clip = %q", got)
	}
	if got := hexID("1373c4f5-1792494994"); got != "1373c4f5" {
		t.Errorf("hexID = %q", got)
	}
	for pr, want := range map[*github.PR]string{
		{Number: 1, State: "MERGED"}:                           "#1(merged)",
		{Number: 2, State: "OPEN", CIStatus: "FAILURE"}:        "#2(ci-failed)",
		{Number: 3, State: "OPEN", ReviewDecision: "APPROVED"}: "#3(approved)",
		{Number: 4, State: "OPEN", IsDraft: true}:              "#4(draft)",
		{Number: 5, State: "OPEN", UnresolvedThreads: 2}:       "#5(2-threads)",
	} {
		if got := prShort(pr); got != want {
			t.Errorf("prShort = %q, want %q", got, want)
		}
	}
}

// A pane back at a shell reads as exited whatever the pipeline last said: that
// is the fact deciding the next move.
func TestStatusWord(t *testing.T) {
	if got := statusWord(&control.Observation{Pane: control.PaneShell, Status: session.StatusRunning}); got != "exited" {
		t.Errorf("statusWord = %q, want exited", got)
	}
	if got := statusWord(&control.Observation{Pane: control.PaneAlive, Status: session.StatusWaiting}); got != "waiting" {
		t.Errorf("statusWord = %q, want waiting", got)
	}
}

// The transcript guard must check the conversation the relaunch will resume,
// not the one status reads: those differ when the hooks moved to a conversation
// with no transcript yet while the row still names an older one that has one.
func TestPrepareLaunchChecksTheResumedConversation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	old := session.ClaudeTranscriptPath("old-conv", project)
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mk := func(resume string) *control.Observation {
		row := &session.SessionRow{ID: "s1", Title: "t", Agent: "claude", ProjectPath: project, ClaudeSessionID: "old-conv"}
		s := session.FromRow(row)
		s.ClaudeSessionID = resume
		return &control.Observation{Row: row, Session: s, Agent: agent.Claude}
	}

	if err := prepareLaunch(mk("old-conv"), false); err != nil {
		t.Errorf("resuming a conversation with a transcript: %v", err)
	}
	if err := prepareLaunch(mk("new-conv"), false); err == nil || !strings.Contains(err.Error(), "new-conv") {
		t.Errorf("resuming one without: err = %v, want a refusal naming new-conv", err)
	}
	// -fresh drops the id in memory only; the row keeps it until a launch works.
	ob := mk("new-conv")
	if err := prepareLaunch(ob, true); err != nil || ob.Session.ClaudeSessionID != "" || ob.Row.ClaudeSessionID != "old-conv" {
		t.Errorf("fresh: err = %v, memory = %q, row = %q", err, ob.Session.ClaudeSessionID, ob.Row.ClaudeSessionID)
	}
}
