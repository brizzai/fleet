package ui

import (
	"path/filepath"
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

// The drop direction is the mirror of adoption: `fleet remove` deletes the row
// from another process, and the TUI has to stop rendering it without a restart.

// A row missing from the current read is only a removal if it was in the read
// before. That rule is what separates a deleted session from one this TUI just
// created — handleSessionCreateResult appends to h.sessions before SaveSession
// lands, so a sweep can legitimately catch a session in memory whose row isn't
// written yet.
func TestVanishedSessionsNeedsTheRowInAPreviousRead(t *testing.T) {
	rowA := session.NewSession("a", "/tmp/a").ToRow()
	rowB := session.NewSession("b", "/tmp/b").ToRow()
	known := []*session.Session{session.FromRow(rowA), session.FromRow(rowB)}

	// B is in memory and in neither read: brand new, not deleted.
	cur := map[string]bool{rowA.ID: true}
	if got := vanishedSessions(known, cur, map[string]bool{rowA.ID: true}); len(got) != 0 {
		t.Fatalf("a never-persisted session was reported as vanished: %v", got)
	}

	// B was there last read and is gone now: deleted elsewhere.
	prev := map[string]bool{rowA.ID: true, rowB.ID: true}
	got := vanishedSessions(known, cur, prev)
	if len(got) != 1 || got[0] != rowB.ID {
		t.Fatalf("got %v, want just %s", got, rowB.ID)
	}
}

func TestDiffAgainstStorageFindsRemovedSessionAndPin(t *testing.T) {
	h := adoptTestHome(t)
	repo := t.TempDir()
	row := storeSession(t, h, "from-cli", repo)
	h.handleAdoptSessions(adoptSessionsMsg{sessions: []*session.Session{session.FromRow(row)}})
	known := []*session.Session{session.FromRow(row)}

	// First sweep: everything is where it should be, and it seeds the
	// previous-read state the next one diffs against.
	if _, drop, ok := h.diffAgainstStorage(known); !ok || len(drop.ids) != 0 || len(drop.unpinned) != 0 {
		t.Fatalf("first sweep reported a removal: %+v (ok=%v)", drop, ok)
	}

	// What `fleet remove` now does from another process.
	repoRoot := session.GetRepoRoot(repo)
	if err := h.storage.DeleteSession(row.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if err := h.storage.UnpinRepo(repoRoot); err != nil {
		t.Fatalf("unpin repo: %v", err)
	}

	_, drop, ok := h.diffAgainstStorage(known)
	if !ok {
		t.Fatal("diff failed")
	}
	if len(drop.ids) != 1 || drop.ids[0] != row.ID {
		t.Fatalf("got ids %v, want just %s", drop.ids, row.ID)
	}
	if len(drop.unpinned) != 1 || drop.unpinned[0] != repoRoot {
		t.Fatalf("got unpinned %v, want just %s", drop.unpinned, repoRoot)
	}
}

// A session whose SaveSession failed (SQLITE_BUSY happens) exists only in
// memory. It must never be dropped, however many sweeps run: it was never in
// the table, so nothing about it vanished — and pulling a live agent out of the
// sidebar because its row didn't land is the worse of the two failures.
func TestDiffAgainstStorageKeepsAnUnpersistedSession(t *testing.T) {
	h := adoptTestHome(t)
	known := []*session.Session{session.NewSession("never-saved", t.TempDir())}

	for i := 0; i < 3; i++ {
		if _, drop, ok := h.diffAgainstStorage(known); !ok || len(drop.ids) != 0 {
			t.Fatalf("sweep %d dropped a session with no row: %v", i, drop.ids)
		}
	}
}

func TestHandleDropSessionsRemovesSessionAndPin(t *testing.T) {
	h := adoptTestHome(t)
	repo := t.TempDir()
	row := storeSession(t, h, "from-cli", repo)
	h.handleAdoptSessions(adoptSessionsMsg{sessions: []*session.Session{session.FromRow(row)}})
	repoRoot := session.GetRepoRoot(repo)
	h.slotBindings[3] = row.ID

	h.handleDropSessions(dropSessionsMsg{ids: []string{row.ID}, unpinned: []string{repoRoot}})

	if _, ok := h.sessionByID[row.ID]; ok {
		t.Error("dropped session still in sessionByID")
	}
	if len(h.sessions) != 0 {
		t.Errorf("h.sessions has %d entries, want 0", len(h.sessions))
	}
	if h.pinnedRepos[repoRoot] {
		t.Error("pin outlived the session it was created with")
	}
	if _, ok := h.slotBindings[3]; ok {
		t.Error("slot binding still points at a session that is gone")
	}
	if indexOfSession(h, row.ID) >= 0 {
		t.Error("dropped session still rendered in the sidebar")
	}
}

// The sweep can catch an unpin the user has already reversed with `u` — undo
// re-pins the repo and restores its session in one step. A repo with sessions
// renders a header either way, so dropping the pin could only desync the two.
func TestHandleDropSessionsKeepsAPinWithLiveSessions(t *testing.T) {
	h := adoptTestHome(t)
	repo := t.TempDir()
	row := storeSession(t, h, "still-here", repo)
	h.handleAdoptSessions(adoptSessionsMsg{sessions: []*session.Session{session.FromRow(row)}})
	repoRoot := session.GetRepoRoot(repo)

	h.handleDropSessions(dropSessionsMsg{unpinned: []string{repoRoot}})

	if !h.pinnedRepos[repoRoot] {
		t.Error("pin was dropped while a session still lives in that repo")
	}
}

// Like adoption, a drop arrives on a timer rather than because the user pressed
// a key — so a row disappearing elsewhere must not slide the selection.
func TestHandleDropSessionsKeepsCursorOnItsRow(t *testing.T) {
	h := adoptTestHome(t)
	above, below := t.TempDir(), t.TempDir()

	goneRow := storeSession(t, h, "removed-elsewhere", above)
	parkedRow := storeSession(t, h, "parked", below)
	h.handleAdoptSessions(adoptSessionsMsg{sessions: []*session.Session{
		session.FromRow(goneRow), session.FromRow(parkedRow),
	}})

	idxBefore := indexOfSession(h, parkedRow.ID)
	if idxBefore < 0 {
		t.Fatal("parked session not in the sidebar")
	}
	h.cursor = idxBefore

	h.handleDropSessions(dropSessionsMsg{ids: []string{goneRow.ID}})

	idxAfter := indexOfSession(h, parkedRow.ID)
	if idxAfter == idxBefore {
		t.Fatalf("test is vacuous: the dropped row did not shift the parked row (still at %d)", idxBefore)
	}
	if h.cursor != idxAfter {
		t.Fatalf("cursor moved off its row: parked session is at %d, cursor is at %d", idxAfter, h.cursor)
	}
}

// The cursor's own row can be the one that goes away; the clamp has to leave it
// somewhere selectable rather than off the end of the list.
func TestHandleDropSessionsClampsCursorWhenItsRowGoes(t *testing.T) {
	h := adoptTestHome(t)
	repo := t.TempDir()
	row := storeSession(t, h, "removed-elsewhere", repo)
	h.handleAdoptSessions(adoptSessionsMsg{sessions: []*session.Session{session.FromRow(row)}})
	h.cursor = indexOfSession(h, row.ID)

	h.handleDropSessions(dropSessionsMsg{ids: []string{row.ID}, unpinned: []string{session.GetRepoRoot(repo)}})

	if h.cursor < 0 || (len(h.flatItems) > 0 && h.cursor >= len(h.flatItems)) {
		t.Fatalf("cursor %d out of range for %d rows", h.cursor, len(h.flatItems))
	}
}
