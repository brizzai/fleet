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
