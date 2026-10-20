package ui

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/session"
)

func adoptTestHome(t *testing.T) *Home {
	t.Helper()
	storage, err := session.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	h.width, h.height = 120, 40
	return h
}

// storeSession writes a session row straight to SQLite without telling the
// running model about it — what `fleet worktree` does from another process.
func storeSession(t *testing.T, h *Home, title, path string) *session.SessionRow {
	t.Helper()
	s := session.NewSession(title, path)
	row := s.ToRow()
	if err := h.storage.SaveSession(row); err != nil {
		t.Fatalf("save session: %v", err)
	}
	return row
}

func TestUnknownSessionsFindsOnlyNewRows(t *testing.T) {
	rowA := session.NewSession("a", "/tmp/a").ToRow()
	rowB := session.NewSession("b", "/tmp/b").ToRow()
	known := []*session.Session{session.FromRow(rowA)}

	got := unknownSessions([]*session.SessionRow{rowA, rowB}, known, "")
	if len(got) != 1 || got[0].ID != rowB.ID {
		t.Fatalf("got %d sessions, want just %s", len(got), rowB.ID)
	}

	// Nothing new once both are known.
	known = append(known, session.FromRow(rowB))
	if got := unknownSessions([]*session.SessionRow{rowA, rowB}, known, ""); len(got) != 0 {
		t.Fatalf("expected no unknown sessions, got %d", len(got))
	}
}

// The demo filter has to match loadSessions', or a demo-mode TUI adopts back
// the sessions it was launched to hide.
func TestUnknownSessionsRespectsDemoPrefix(t *testing.T) {
	inside := session.NewSession("inside", "/demo/repo").ToRow()
	outside := session.NewSession("outside", "/elsewhere/repo").ToRow()

	got := unknownSessions([]*session.SessionRow{inside, outside}, nil, "/demo")
	if len(got) != 1 || got[0].ID != inside.ID {
		t.Fatalf("got %d sessions, want just the /demo one", len(got))
	}
}

func TestHandleAdoptSessionsAddsAndPins(t *testing.T) {
	h := adoptTestHome(t)
	repo := t.TempDir()
	row := storeSession(t, h, "from-cli", repo)

	h.handleAdoptSessions(adoptSessionsMsg{sessions: []*session.Session{session.FromRow(row)}})

	if _, ok := h.sessionByID[row.ID]; !ok {
		t.Fatal("adopted session missing from sessionByID")
	}
	if len(h.sessions) != 1 {
		t.Fatalf("h.sessions has %d entries, want 1", len(h.sessions))
	}
	repoRoot := session.GetRepoRoot(repo)
	if !h.pinnedRepos[repoRoot] {
		t.Error("adopted session's repo was not pinned")
	}
	pinned, err := h.storage.LoadPinnedRepos()
	if err != nil {
		t.Fatalf("load pinned repos: %v", err)
	}
	var found bool
	for _, p := range pinned {
		if p == repoRoot {
			found = true
		}
	}
	if !found {
		t.Error("pin was not persisted to storage")
	}
}

// The worker diffs against a snapshot taken at cycle start, so it can offer a
// session the Update loop created since. The handler is the only place that
// check is authoritative.
func TestHandleAdoptSessionsSkipsAlreadyKnown(t *testing.T) {
	h := adoptTestHome(t)
	row := storeSession(t, h, "from-cli", t.TempDir())

	msg := adoptSessionsMsg{sessions: []*session.Session{session.FromRow(row)}}
	h.handleAdoptSessions(msg)
	h.handleAdoptSessions(msg) // same row again

	if len(h.sessions) != 1 {
		t.Fatalf("h.sessions has %d entries after a duplicate adopt, want 1", len(h.sessions))
	}
}

// An adopted row arrives on a timer, not because the user asked for it, so it
// must never slide the selection out from under someone mid-keystroke.
func TestHandleAdoptSessionsKeepsCursorOnItsRow(t *testing.T) {
	h := adoptTestHome(t)
	// t.TempDir() hands out .../001, .../002 in call order, and checkouts sort
	// by path — so a session in the first dir renders above one in the second.
	above, below := t.TempDir(), t.TempDir()

	parkedRow := storeSession(t, h, "parked", below)
	h.handleAdoptSessions(adoptSessionsMsg{sessions: []*session.Session{session.FromRow(parkedRow)}})

	idxBefore := indexOfSession(h, parkedRow.ID)
	if idxBefore < 0 {
		t.Fatal("parked session not in the sidebar")
	}
	h.cursor = idxBefore

	// Adopt a session that renders above the parked one, pushing its index down.
	newRow := storeSession(t, h, "adopted", above)
	h.handleAdoptSessions(adoptSessionsMsg{sessions: []*session.Session{session.FromRow(newRow)}})

	idxAfter := indexOfSession(h, parkedRow.ID)
	if idxAfter == idxBefore {
		t.Fatalf("test is vacuous: the adopted row did not shift the parked row (still at %d)", idxBefore)
	}
	if h.cursor != idxAfter {
		t.Fatalf("cursor moved off its row: parked session is at %d, cursor is at %d", idxAfter, h.cursor)
	}
}

// statusWorkerCycle returns early when there are no sessions and no shells.
// The adoption sweep has to run anyway: an empty sidebar is the likeliest place
// to be driving fleet from the shell, and it's the one case where a session
// appearing is the whole point. Regression guard for the bug where the sweep
// sat below that guard and was unreachable.
func TestStatusWorkerCycleAdoptsOnEmptyFleet(t *testing.T) {
	h := adoptTestHome(t)
	storeSession(t, h, "from-cli", t.TempDir())

	if len(h.sessions) != 0 || len(h.shells) != 0 {
		t.Fatalf("precondition: fleet must be empty, got %d sessions / %d shells", len(h.sessions), len(h.shells))
	}

	h.statusWorkerCycle()

	// h.send is a no-op without a running program, so the adopted session can't
	// land in h.sessions here — what's under test is that the sweep was reached
	// at all past the empty-fleet guard.
	if h.lastAdoptSweepAt.IsZero() {
		t.Fatal("adoption sweep never ran on an empty fleet")
	}
}

// A "Creating…" phantom is the row handleWorkspaceCreate auto-selects, so it's
// exactly where the cursor sits while a TUI-side worktree create is in flight —
// the window a shell `fleet worktree` lands in. targetForCursor has to know it,
// or the cursor silently drifts when an adopted row is inserted above.
func TestHandleAdoptSessionsKeepsCursorOnPendingRow(t *testing.T) {
	h := adoptTestHome(t)
	above, pendingRepo := t.TempDir(), t.TempDir()

	h.pinnedRepos[pendingRepo] = true
	pw := &PendingWorkspace{ID: "pending-test", Name: "creating", RepoPath: pendingRepo}
	h.pendingWorkspaces = append(h.pendingWorkspaces, pw)
	h.rebuildFlatItems()

	idxBefore := indexOfPending(h, pw.ID)
	if idxBefore < 0 {
		t.Fatal("phantom row not in the sidebar")
	}
	h.cursor = idxBefore

	newRow := storeSession(t, h, "adopted", above)
	h.handleAdoptSessions(adoptSessionsMsg{
		sessions:  []*session.Session{session.FromRow(newRow)},
		repoRoots: map[string]string{newRow.ID: above},
	})

	idxAfter := indexOfPending(h, pw.ID)
	if idxAfter == idxBefore {
		t.Fatalf("test is vacuous: the adopted row did not shift the phantom (still at %d)", idxBefore)
	}
	if h.cursor != idxAfter {
		t.Fatalf("cursor drifted off the phantom: phantom is at %d, cursor is at %d", idxAfter, h.cursor)
	}
}

// setExpanded persists, so auto-expanding on a 5s timer would overwrite a
// collapse the user chose. Snooze is the sharp case: it collapses its group on
// purpose, and re-opening leaves an expanded group still showing a ☾ countdown.
func TestCanAutoExpandRespectsCollapseAndSnooze(t *testing.T) {
	h := adoptTestHome(t)

	if !h.canAutoExpand("/repo/untouched") {
		t.Error("a group with no explicit state should be expandable")
	}

	h.repoExpanded["/repo/collapsed"] = false
	if h.canAutoExpand("/repo/collapsed") {
		t.Error("a deliberately collapsed group must not be auto-expanded")
	}

	h.repoExpanded["/repo/snoozed"] = false
	h.groupSnooze["/repo/snoozed"] = time.Now().Add(time.Hour)
	if h.canAutoExpand("/repo/snoozed") {
		t.Error("a snoozed group must not be auto-expanded")
	}
}

func indexOfPending(h *Home, id string) int {
	for i, item := range h.flatItems {
		if item.Pending != nil && item.Pending.ID == id {
			return i
		}
	}
	return -1
}

func indexOfSession(h *Home, id string) int {
	for i, item := range h.flatItems {
		if item.Session != nil && item.Session.ID == id {
			return i
		}
	}
	return -1
}

// A session another process removed has no row AND no tmux. Either alone is not
// enough: a session this TUI just created is live before its row lands.
func TestGoneSessionsNeedsNoRowAndNoTmux(t *testing.T) {
	stored := session.NewSession("stored", "/tmp/stored")
	unsavedLive := session.NewSession("unsaved-live", "/tmp/live")
	removed := session.NewSession("removed", "/tmp/removed")
	rows := []*session.SessionRow{stored.ToRow()}
	live := map[string]bool{unsavedLive.ID: true}

	got := goneSessions(rows, []*session.Session{stored, unsavedLive, removed},
		func(s *session.Session) bool { return live[s.ID] })
	if len(got) != 1 || got[0] != removed.ID {
		t.Fatalf("got %v, want just %s", got, removed.ID)
	}
}

func TestMissingDirsFlagsOnlyDeletedPaths(t *testing.T) {
	present := t.TempDir()
	deleted := filepath.Join(t.TempDir(), "worktree-removed")

	got := missingDirs([]string{present, deleted})
	if len(got) != 1 || !got[deleted] {
		t.Fatalf("got %v, want just %s", got, deleted)
	}
}

// `fleet remove` from a shell (#304): the row is gone, so the running TUI must
// drop the session and any slot badge pointing at it.
func TestHandleExternalRemovalsDropsSession(t *testing.T) {
	h := adoptTestHome(t)
	row := storeSession(t, h, "from-cli", t.TempDir())
	h.handleAdoptSessions(adoptSessionsMsg{sessions: []*session.Session{session.FromRow(row)}})
	h.slotBindings = map[int]string{1: row.ID}

	h.handleExternalRemovals(externalRemovalsMsg{sessionIDs: []string{row.ID}})

	if _, ok := h.sessionByID[row.ID]; ok {
		t.Fatal("removed session still in sessionByID")
	}
	if len(h.sessions) != 0 {
		t.Fatalf("h.sessions has %d entries, want 0", len(h.sessions))
	}
	if indexOfSession(h, row.ID) >= 0 {
		t.Fatal("removed session still in the sidebar")
	}
	if _, ok := h.slotBindings[1]; ok {
		t.Error("slot binding to the removed session survived")
	}
}

// A removal arrives on a timer, so it must not slide the selection onto a
// different session.
func TestHandleExternalRemovalsKeepsCursorOnItsRow(t *testing.T) {
	h := adoptTestHome(t)
	above, below := t.TempDir(), t.TempDir()
	removedRow := storeSession(t, h, "removed", above)
	parkedRow := storeSession(t, h, "parked", below)
	h.handleAdoptSessions(adoptSessionsMsg{sessions: []*session.Session{
		session.FromRow(removedRow), session.FromRow(parkedRow),
	}})

	idxBefore := indexOfSession(h, parkedRow.ID)
	if idxBefore < 0 {
		t.Fatal("parked session not in the sidebar")
	}
	h.cursor = idxBefore

	h.handleExternalRemovals(externalRemovalsMsg{sessionIDs: []string{removedRow.ID}})

	idxAfter := indexOfSession(h, parkedRow.ID)
	if idxAfter == idxBefore {
		t.Fatalf("test is vacuous: the removal did not shift the parked row (still at %d)", idxBefore)
	}
	if h.cursor != idxAfter {
		t.Fatalf("cursor moved off its row: parked session is at %d, cursor is at %d", idxAfter, h.cursor)
	}
}

// A pinned worktree deleted from disk (#304) is hidden, not unpinned: the SQLite
// pin survives, so the checkout comes back if its directory does.
func TestHandleExternalRemovalsHidesAndRestoresMissingPin(t *testing.T) {
	h := adoptTestHome(t)
	repo := filepath.Join(t.TempDir(), "worktree")
	if err := h.storage.PinRepo(repo); err != nil {
		t.Fatalf("pin repo: %v", err)
	}
	h.pinnedRepos[repo] = true
	h.rebuildFlatItems()
	if len(h.flatItems) == 0 {
		t.Fatal("precondition: pinned repo not in the sidebar")
	}

	h.handleExternalRemovals(externalRemovalsMsg{
		missingPins: map[string]bool{repo: true},
		pinned:      map[string]bool{repo: true},
	})

	if len(h.flatItems) != 0 {
		t.Fatalf("missing pin still renders %d sidebar rows", len(h.flatItems))
	}
	pinned, err := h.storage.LoadPinnedRepos()
	if err != nil {
		t.Fatalf("load pinned repos: %v", err)
	}
	if !slices.Contains(pinned, repo) {
		t.Error("hiding a missing pin must not unpin it in storage")
	}

	h.handleExternalRemovals(externalRemovalsMsg{pinned: map[string]bool{repo: true}})

	if !h.pinnedRepos[repo] || len(h.flatItems) == 0 {
		t.Fatal("pin did not come back once its directory stopped being missing")
	}
}

// hiddenPinWithSession is a worktree whose directory was deleted outside fleet
// while a session still lives under it: one sweep has hidden its pin, and the
// session keeps its header on screen.
func hiddenPinWithSession(t *testing.T, h *Home) (repo, sessionID string) {
	t.Helper()
	repo = filepath.Join(t.TempDir(), "worktree") // never created
	row := storeSession(t, h, "orphan", repo)
	h.handleAdoptSessions(adoptSessionsMsg{
		sessions:  []*session.Session{session.FromRow(row)},
		repoRoots: map[string]string{row.ID: repo},
	})
	syncPins(h)
	if h.pinnedRepos[repo] || !h.missingPins[repo] {
		t.Fatal("precondition: the missing pin was not hidden")
	}
	return repo, row.ID
}

// syncPins runs a sync sweep's pin half against real storage and disk. It passes
// no sessions, so nothing is asked about tmux.
func syncPins(h *Home) {
	h.handleExternalRemovals(h.buildExternalRemovals(nil, nil))
}

// `d` on the header of a hidden pin unpins it in SQLite; the next sweep must not
// read "dropped out of the missing set" as "the directory is back".
func TestUnpinnedHiddenPinStaysGone(t *testing.T) {
	h := adoptTestHome(t)
	repo, id := hiddenPinWithSession(t, h)

	h.deferDelete(sessionDeleteMsg{id: id, unpinRepo: true, repoPath: repo})
	syncPins(h)

	if h.pinnedRepos[repo] || h.missingPins[repo] {
		t.Fatalf("unpinned pin came back: pinned=%v missing=%v", h.pinnedRepos[repo], h.missingPins[repo])
	}
	if len(h.flatItems) != 0 {
		t.Fatalf("sidebar still renders %d rows for an unpinned, deleted directory", len(h.flatItems))
	}
}

// Undoing that delete re-pins in memory without changing the missing set, so the
// sweep has to reconcile every time rather than only when the set changes.
func TestUndoneUnpinIsHiddenAgain(t *testing.T) {
	h := adoptTestHome(t)
	repo, id := hiddenPinWithSession(t, h)

	h.deferDelete(sessionDeleteMsg{id: id, unpinRepo: true, repoPath: repo})
	h.undoDelete()
	if !h.pinnedRepos[repo] {
		t.Fatal("test is vacuous: undo did not re-pin in memory")
	}
	syncPins(h)

	if h.pinnedRepos[repo] || !h.missingPins[repo] {
		t.Fatalf("undone pin not hidden again: pinned=%v missing=%v", h.pinnedRepos[repo], h.missingPins[repo])
	}
}

// The CLI (`fleet restart`, `fleet resume`, `fleet suspend`) changes a row
// behind an open TUI. Direction is what matters: this TUI's own relaunch also
// leaves memory and row disagreeing for a moment, and must not be "adopted"
// back onto the dead tmux session.
func TestRelaunchedElsewhere(t *testing.T) {
	mk := func(tmux string, status session.Status) (*session.Session, *session.SessionRow) {
		s := session.NewSession("t", "/tmp/t")
		s.TmuxSessionName = tmux
		s.SetStatus(status)
		row := s.ToRow()
		return s, row
	}
	live := map[string]bool{}
	alive := func(name string) bool { return live[name] }

	t.Run("another process relaunched it into a new tmux session", func(t *testing.T) {
		s, row := mk("fleet_old", session.StatusError)
		row.TmuxSession, row.ClaudeSessionID = "fleet_new", "c2"
		live = map[string]bool{"fleet_new": true}
		got := relaunchedElsewhere([]*session.SessionRow{row}, []*session.Session{s}, alive)
		if len(got) != 1 || got[0].tmuxSession != "fleet_new" || got[0].claudeSessionID != "c2" || got[0].suspended ||
			got[0].fromTmux != "fleet_old" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("this TUI's own relaunch, not yet persisted, is left alone", func(t *testing.T) {
		s, row := mk("fleet_new", session.StatusStarting)
		row.TmuxSession = "fleet_old" // the row still names the session just killed
		live = map[string]bool{"fleet_new": true}
		if got := relaunchedElsewhere([]*session.SessionRow{row}, []*session.Session{s}, alive); len(got) != 0 {
			t.Fatalf("got %+v, want nothing", got)
		}
	})

	t.Run("another process suspended it", func(t *testing.T) {
		s, row := mk("fleet_old", session.StatusRunning)
		row.Status = string(session.StatusSuspended)
		live = map[string]bool{}
		got := relaunchedElsewhere([]*session.SessionRow{row}, []*session.Session{s}, alive)
		if len(got) != 1 || !got[0].suspended {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("a row saying suspended while tmux is up is not a suspend", func(t *testing.T) {
		// This TUI resumed it; the row will catch up.
		s, row := mk("fleet_live", session.StatusStarting)
		row.Status = string(session.StatusSuspended)
		live = map[string]bool{"fleet_live": true}
		if got := relaunchedElsewhere([]*session.SessionRow{row}, []*session.Session{s}, alive); len(got) != 0 {
			t.Fatalf("got %+v, want nothing", got)
		}
	})

	t.Run("already suspended in both, or no row", func(t *testing.T) {
		s, row := mk("fleet_old", session.StatusSuspended)
		live = map[string]bool{}
		other, _ := mk("fleet_x", session.StatusError)
		if got := relaunchedElsewhere([]*session.SessionRow{row}, []*session.Session{s, other}, alive); len(got) != 0 {
			t.Fatalf("got %+v, want nothing", got)
		}
	})

	// Suspended here, woken elsewhere (`fleet resume`, a `fleet send` wake): a
	// relaunch like any other. This TUI's own suspend never renames the tmux
	// session, so it can't look like this.
	t.Run("suspended here, woken by another process", func(t *testing.T) {
		s, row := mk("fleet_old", session.StatusSuspended)
		row.Status, row.TmuxSession = string(session.StatusStarting), "fleet_new"
		live = map[string]bool{"fleet_new": true}
		got := relaunchedElsewhere([]*session.SessionRow{row}, []*session.Session{s}, alive)
		if len(got) != 1 || got[0].suspended || got[0].tmuxSession != "fleet_new" {
			t.Fatalf("got %+v", got)
		}
	})
}

// The worker diffs a snapshot; by the time its message is handled this TUI may
// have relaunched the session itself. Adopting the stale change then would point
// memory at the CLI's tmux session and orphan this TUI's own agent.
func TestHandleExternalRelaunchesSkipsStaleChange(t *testing.T) {
	h := adoptTestHome(t)
	s := session.NewSession("t", t.TempDir())
	s.TmuxSessionName = "fleet_old"
	s.SetStatus(session.StatusError)
	h.sessions = []*session.Session{s}
	h.rebuildSessionMap()

	change := externalRelaunch{id: s.ID, fromTmux: "fleet_old", tmuxSession: "fleet_cli", claudeSessionID: "c2"}

	// This TUI restarted it after the snapshot: memory now names fleet_tui.
	s.TmuxSessionName = "fleet_tui"
	h.handleExternalRelaunches(externalRelaunchMsg{changes: []externalRelaunch{change}})
	if s.TmuxSessionName != "fleet_tui" {
		t.Fatalf("tmux = %q, want fleet_tui — a stale change was adopted", s.TmuxSessionName)
	}
	suspend := externalRelaunch{id: s.ID, fromTmux: "fleet_old", suspended: true}
	h.handleExternalRelaunches(externalRelaunchMsg{changes: []externalRelaunch{suspend}})
	if s.GetStatus() == session.StatusSuspended {
		t.Fatal("a stale suspend marked a session this TUI relaunched as suspended")
	}

	// Memory unchanged since the snapshot: adopted.
	s.TmuxSessionName = "fleet_old"
	h.handleExternalRelaunches(externalRelaunchMsg{changes: []externalRelaunch{change}})
	if s.TmuxSessionName != "fleet_cli" {
		t.Fatalf("tmux = %q, want fleet_cli", s.TmuxSessionName)
	}
}

// The worker writes "error" the moment a session's tmux disappears — within one
// fast pass, long before the 5s sweep. For a CLI suspend that write is what
// would erase the record of it, so the worker checks the row first.
func TestUpdateAndPersistStatusAdoptsExternalSuspend(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // a regression writes a crash dump; keep it out of the real one
	h := adoptTestHome(t)
	row := storeSession(t, h, "suspended-from-cli", t.TempDir())
	row.TmuxSession = "fleet_never_existed_" + row.ID
	if err := h.storage.UpdateTmuxSession(row.ID, row.TmuxSession); err != nil {
		t.Fatal(err)
	}
	s := session.FromRow(row)
	s.SetStatus(session.StatusRunning)
	// What `fleet suspend` leaves behind: the row says suspended, tmux is gone.
	if err := h.storage.UpdateStatus(row.ID, string(session.StatusSuspended)); err != nil {
		t.Fatal(err)
	}

	if !h.updateAndPersistStatus(s) {
		t.Fatal("expected a status change")
	}
	if s.GetStatus() != session.StatusSuspended {
		t.Errorf("in-memory status = %s, want suspended", s.GetStatus())
	}
	got, _, _ := h.storage.LoadSessionLaunch(row.ID)
	if got.Status != string(session.StatusSuspended) {
		t.Errorf("row status = %s — the worker overwrote the CLI's suspend", got.Status)
	}
	// The row is checked before UpdateStatus, which would otherwise have read
	// the missing tmux session as a crash: a dump on disk, a session_errored
	// event. The dump is written on a goroutine, so give it a moment to appear.
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(home, ".config", "fleet", "crashes")); err == nil {
		t.Error("a deliberate suspend was recorded as a crash")
	}
}
