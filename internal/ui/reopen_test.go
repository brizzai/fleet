package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/session"
)

func TestCloseNeedsYesOnlyForWorkInFlight(t *testing.T) {
	for st, want := range map[session.Status]bool{
		session.StatusRunning: true, session.StatusWaiting: true,
		session.StatusIdle: false, session.StatusFinished: false, session.StatusError: false,
		session.StatusSuspended: false, session.StatusStarting: false,
	} {
		if got := closeNeedsYes(st); got != want {
			t.Errorf("closeNeedsYes(%s) = %v, want %v", st, got, want)
		}
	}
}

func TestReopenCheck(t *testing.T) {
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	tr := func(string, string) bool { return true }
	noTr := func(string, string) bool { return false }
	never := func(string, string) bool { return false }
	base := session.SessionRow{ID: "a", ProjectPath: "/p", Agent: "claude", ClaudeSessionID: "conv"}

	cases := []struct {
		name       string
		row        session.SessionRow
		open       func(string, string) bool
		dir        func(string) bool
		tr         func(string, string) bool
		ok, dup    bool
		reasonPart string
	}{
		{"ok", base, never, yes, tr, true, false, ""},
		{"same row open", base, func(id, _ string) bool { return id == "a" }, yes, tr, false, true, ""},
		{"same conversation open under another row", base, func(_, c string) bool { return c == "conv" }, yes, tr, false, true, ""},
		{"never prompted: no conversation id", session.SessionRow{ID: "a", ProjectPath: "/p", Agent: "claude"}, never, yes, tr, false, false, "nothing to resume"},
		{"folder gone", base, never, no, tr, false, false, "no longer exists"},
		{"claude without transcript", base, never, yes, noTr, false, false, "no transcript"},
		{"codex is not held to a claude transcript", session.SessionRow{ID: "a", ProjectPath: "/p", Agent: "codex", ClaudeSessionID: "conv"}, never, yes, noTr, true, false, ""},
	}
	for _, c := range cases {
		v := reopenCheck(c.row, c.open, c.dir, c.tr)
		if v.ok != c.ok || v.alreadyOpen != c.dup || !strings.Contains(v.reason, c.reasonPart) {
			t.Errorf("%s: got %+v, want ok=%v dup=%v reason~%q", c.name, v, c.ok, c.dup, c.reasonPart)
		}
	}
}

// closedHome returns a Home holding one Claude session with a real folder and a
// transcript on disk, so a reopen has everything it needs.
func closedHome(t *testing.T) (h *Home, id, dir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	h = adoptTestHome(t)
	dir = filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := session.NewSession("my task", dir)
	s.ClaudeSessionID = "conv-1"
	s.Account = "me@x.com"
	row := s.ToRow()
	if err := h.storage.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	h.handleAdoptSessions(adoptSessionsMsg{
		sessions:  []*session.Session{session.FromRow(row)},
		repoRoots: map[string]string{row.ID: dir},
	})
	tp := session.ClaudeTranscriptPath("conv-1", dir)
	if err := os.MkdirAll(filepath.Dir(tp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tp, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return h, row.ID, dir
}

func TestReopenAfterUndoWindowRestoresRowOnce(t *testing.T) {
	h, id, _ := closedHome(t)
	h.deferDelete(sessionDeleteMsg{id: id})
	if _, ok := h.sessionByID[id]; ok {
		t.Fatal("precondition: delete left the session in memory")
	}
	if c, _ := h.storage.LoadClosed(); len(c) != 1 {
		t.Fatalf("closed list = %d entries, want 1", len(c))
	}
	h.pendingDeletes = nil // the 5s undo window has passed

	_, cmd := h.reopenLastClosed()
	s, ok := h.sessionByID[id]
	if !ok {
		t.Fatalf("session not back in the sidebar; toast=%q", h.infoMsg)
	}
	if cmd == nil {
		t.Fatal("reopen returned no relaunch command")
	}
	if s.ClaudeSessionID != "conv-1" || s.Account != "me@x.com" {
		t.Fatalf("conversation/account not restored: conv=%q account=%q", s.ClaudeSessionID, s.Account)
	}
	if !strings.Contains(h.infoMsg, "me@x.com") || !strings.Contains(h.infoMsg, "proj") {
		t.Fatalf("message doesn't say what came back: %q", h.infoMsg)
	}
	if rows, _ := h.storage.LoadSessions(); len(rows) != 1 {
		t.Fatalf("db rows = %d, want 1", len(rows))
	}

	// Second U: nothing left, and no second copy of the session.
	n := len(h.sessions)
	h.reopenLastClosed()
	if len(h.sessions) != n {
		t.Fatalf("reopening twice duplicated the session: %d -> %d", n, len(h.sessions))
	}
	if h.infoMsg != "No closed session to reopen" {
		t.Fatalf("empty list should read as a notice, got %q", h.infoMsg)
	}
}

func TestReopenInsideUndoWindowIsUndo(t *testing.T) {
	h, id, _ := closedHome(t)
	h.deferDelete(sessionDeleteMsg{id: id})
	_, cmd := h.reopenLastClosed()
	if _, ok := h.sessionByID[id]; !ok {
		t.Fatal("undo window: session not restored")
	}
	if cmd != nil {
		t.Fatal("undo window must not relaunch the agent: its tmux is still alive")
	}
	if c, _ := h.storage.LoadClosed(); len(c) != 0 {
		t.Fatalf("undone delete still listed as closed: %d", len(c))
	}
}

func TestReopenWithoutTranscriptWarnsAndDrops(t *testing.T) {
	h, id, _ := closedHome(t)
	h.deferDelete(sessionDeleteMsg{id: id})
	h.pendingDeletes = nil
	// Claude never wrote a transcript: remove it.
	c, _ := h.storage.LoadClosed()
	_ = os.Remove(session.ClaudeTranscriptPath(c[0].Row.ClaudeSessionID, c[0].Row.ProjectPath))

	_, cmd := h.reopenLastClosed()
	if cmd != nil {
		t.Fatal("must not relaunch a conversation that does not exist")
	}
	if _, ok := h.sessionByID[id]; ok {
		t.Fatal("unrecoverable session was put back in the sidebar")
	}
	if !strings.Contains(h.infoMsg, "no transcript") {
		t.Fatalf("no clear warning: %q", h.infoMsg)
	}
	if c, _ := h.storage.LoadClosed(); len(c) != 0 {
		t.Fatal("dead entry kept; it would block every later U")
	}
}

func TestConfirmYesOnlyDeclinesOnEnter(t *testing.T) {
	var d ConfirmDialog
	fired := false
	d.ShowDanger("t", "s", nil, func() tea.Msg { fired = true; return nil })
	d.RequireYes()
	if _, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil || fired || d.IsVisible() {
		t.Fatalf("Enter must decline: cmd=%v fired=%v visible=%v", cmd != nil, fired, d.IsVisible())
	}

	d.ShowDanger("t", "s", nil, func() tea.Msg { fired = true; return nil })
	d.RequireYes()
	_, cmd := d.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("y must confirm")
	}
	cmd()
	if !fired {
		t.Fatal("y did not fire the action")
	}

	// A later ordinary prompt keeps Enter = confirm (the flag must not leak).
	fired = false
	d.ShowDanger("t", "s", nil, func() tea.Msg { fired = true; return nil })
	if _, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil {
		t.Fatal("yes-only leaked into the next prompt")
	}
}

func cursorOn(t *testing.T, h *Home, id string) {
	t.Helper()
	for i, it := range h.flatItems {
		if !it.IsRepoHeader && it.Session != nil && it.Session.ID == id {
			h.cursor = i
			return
		}
	}
	t.Fatal("session row not in the sidebar")
}

// Closing a working session asks for an explicit y; an idle one is the ordinary
// prompt, unchanged.
func TestDeletePromptDefaultsToNoOnlyWhileWorking(t *testing.T) {
	for _, tc := range []struct {
		st       session.Status
		wantYes  bool
		wantWarn bool
	}{
		{session.StatusRunning, true, true},
		{session.StatusWaiting, true, true},
		{session.StatusIdle, false, false},
		{session.StatusFinished, false, false},
	} {
		h, id, _ := closedHome(t)
		h.sessionByID[id].SetStatus(tc.st)
		cursorOn(t, h, id)
		h.confirmDeleteSelected()
		if h.confirmDialog.yesOnly != tc.wantYes {
			t.Errorf("%s: yesOnly = %v, want %v", tc.st, h.confirmDialog.yesOnly, tc.wantYes)
		}
		warned := strings.Contains(strings.Join(h.confirmDialog.details, "\n"), "Still working")
		if warned != tc.wantWarn {
			t.Errorf("%s: working warning = %v, want %v", tc.st, warned, tc.wantWarn)
		}
	}
}
