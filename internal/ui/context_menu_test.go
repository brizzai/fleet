package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/session"
)

// menuHome builds a Home whose cursor sits on the single given row. Storage-free:
// buildContextMenuItems only reads cursor/flatItems/sessions/gitInfo/cfg.
func menuHome(item SidebarItem, sessions ...*session.Session) *Home {
	h := &Home{
		sessions:     sessions,
		cfg:          &config.Config{},
		repoExpanded: map[string]bool{},
		pinnedRepos:  map[string]bool{},
		flatItems:    []SidebarItem{item},
		cursor:       0,
	}
	gi := map[string]*git.RepoInfo{}
	h.gitInfoCache.Store(&gi)
	return h
}

func (h *Home) setGitInfo(m map[string]*git.RepoInfo) { h.gitInfoCache.Store(&m) }

func findItem(items []ContextMenuItem, id string) (ContextMenuItem, bool) {
	for _, it := range items {
		if it.ID == id {
			return it, true
		}
	}
	return ContextMenuItem{}, false
}

func ids(items []ContextMenuItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

// sessionRow puts a session under the cursor.
func sessionRow(s *session.Session) SidebarItem {
	return SidebarItem{Session: s, RepoPath: session.GetRepoRoot(s.ProjectPath)}
}

// TestContextMenuStateGuardsMatchHandlers: an entry is enabled exactly when the
// handler behind it would actually run. A drifting guard would either dead-click
// (enabled but the handler refuses) or hide a working action behind a dim row.
func TestContextMenuStateGuardsMatchHandlers(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		mutate  func(s *session.Session)
		enabled bool
	}{
		// quickApproveSelected also requires s.IsAlive(), which needs a live tmux
		// pane — so the enabled path isn't reachable from a unit test. The guard
		// mirrors the handler, and these cover the states that actually gate it.
		{"approve disabled while idle", "approve", func(s *session.Session) { s.SetStatus(session.StatusIdle) }, false},
		{"approve disabled while running", "approve", func(s *session.Session) { s.SetStatus(session.StatusRunning) }, false},
		{"approve disabled when the pane is dead", "approve", func(s *session.Session) { s.SetStatus(session.StatusWaiting) }, false},

		{"mark unread disabled while running", "mark_unread", func(s *session.Session) { s.SetStatus(session.StatusRunning) }, false},
		{"mark unread disabled when session never ran", "mark_unread", func(s *session.Session) {
			s.SetStatus(session.StatusIdle) // idle, but no hook has ever fired
		}, false},
		{"restart always available", "restart", func(s *session.Session) { s.SetStatus(session.StatusIdle) }, true},

		{"fork disabled without a resume id", "fork", func(s *session.Session) {
			s.ClaudeSessionID = ""
		}, false},
		{"fork enabled with a resume id", "fork", func(s *session.Session) {
			s.ClaudeSessionID = "abc123"
		}, true},

		{"fork to worktree enabled for claude", "fork_worktree", func(s *session.Session) {
			s.Agent = agent.Claude
			s.ClaudeSessionID = "abc123"
		}, true},
		{"fork to worktree disabled for codex", "fork_worktree", func(s *session.Session) {
			s.Agent = agent.Codex
			s.ClaudeSessionID = "abc123"
		}, false},

		{"suspend enabled when idle with a resume id", "suspend_session", func(s *session.Session) {
			s.SetStatus(session.StatusIdle)
			s.ClaudeSessionID = "abc123"
		}, true},
		{"suspend disabled while running", "suspend_session", func(s *session.Session) {
			s.SetStatus(session.StatusRunning)
			s.ClaudeSessionID = "abc123"
		}, false},
		{"suspend disabled without a resume id", "suspend_session", func(s *session.Session) {
			s.SetStatus(session.StatusIdle)
			s.ClaudeSessionID = ""
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := session.NewSession("s", "/tmp/cm-repo")
			s.Agent = agent.Claude
			tt.mutate(s)

			h := menuHome(sessionRow(s), s)
			_, items := h.buildContextMenuItems()

			it, ok := findItem(items, tt.id)
			if !ok {
				t.Fatalf("session menu has no %q entry; got %v", tt.id, ids(items))
			}
			if it.Enabled != tt.enabled {
				t.Errorf("%q enabled = %v, want %v", tt.id, it.Enabled, tt.enabled)
			}
			// A dimmed row must say why, or it's just a mystery.
			if !it.Enabled && it.Note == "" {
				t.Errorf("%q is disabled but carries no Note explaining why", tt.id)
			}
		})
	}
}

// The title names the row's kind, not just its name — "fleet" alone doesn't say
// whether `d` will forget a repo or remove a worktree. The worktree/repo split
// must agree with the delete label, since they come from the same branch.
func TestContextMenuTitleNamesTheRowKind(t *testing.T) {
	const repo = "/tmp/cm-title"

	s := session.NewSession("my-session", "/tmp/cm-title")
	if title, _ := menuHome(sessionRow(s), s).buildContextMenuItems(); title != "session: my-session" {
		t.Errorf("session title = %q, want %q", title, "session: my-session")
	}

	checkout := SidebarItem{IsRepoHeader: true, IsCheckoutHeader: true, RepoPath: repo}

	// Plain repo.
	h := menuHome(checkout, session.NewSession("s", repo))
	h.setGitInfo(map[string]*git.RepoInfo{repo: {IsWorktreeRepo: false}})
	if title, _ := h.buildContextMenuItems(); title != "repo: cm-title" {
		t.Errorf("repo title = %q, want %q", title, "repo: cm-title")
	}

	// Worktree — title and delete label must agree.
	h = menuHome(checkout)
	h.setGitInfo(map[string]*git.RepoInfo{repo: {IsWorktreeRepo: true}})
	title, items := h.buildContextMenuItems()
	if title != "worktree: cm-title" {
		t.Errorf("worktree title = %q, want %q", title, "worktree: cm-title")
	}
	if it, _ := findItem(items, "delete_at_cursor"); it.Label != "Remove Worktree" {
		t.Errorf("title says worktree but delete says %q", it.Label)
	}

	origin := SidebarItem{IsRepoHeader: true, IsOriginHeader: true, OriginKey: "github.com/acme/x", OriginLabel: "acme/x"}
	if title, _ := menuHome(origin).buildContextMenuItems(); title != "origin: acme/x" {
		t.Errorf("origin title = %q, want %q", title, "origin: acme/x")
	}
}

// A dim row's note is the only thing telling the user why the action is off, so
// it has to name the clause that actually failed — not a plausible-sounding one.
// Every one of these guards is a conjunction, and a constant note contradicts the
// status dot the sidebar draws right next to the row.
func TestContextMenuNotesNameTheRealReason(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		mutate func(s *session.Session)
		want   string
	}{
		{"mark unread, not idle", "mark_unread", func(s *session.Session) {
			s.SetStatus(session.StatusRunning)
		}, "idle only"},
		// It IS idle here, so "idle only" would be a lie.
		{"mark unread, idle but never ran", "mark_unread", func(s *session.Session) {
			s.SetStatus(session.StatusIdle)
		}, "hasn't run yet"},

		{"approve, not waiting", "approve", func(s *session.Session) {
			s.SetStatus(session.StatusIdle)
		}, "not waiting"},
		// It IS waiting (the sidebar shows ◐ waiting) — the pane is just dead.
		{"approve, waiting but pane dead", "approve", func(s *session.Session) {
			s.SetStatus(session.StatusWaiting)
		}, "session not running"},

		{"fork to worktree, wrong agent", "fork_worktree", func(s *session.Session) {
			s.Agent = agent.Codex
			s.ClaudeSessionID = "abc"
		}, "Claude only"},
		// It IS Claude — it just hasn't captured a resume id yet, which is exactly
		// what the `fork` row directly above it says for the same state.
		{"fork to worktree, claude without a resume id", "fork_worktree", func(s *session.Session) {
			s.Agent = agent.Claude
			s.ClaudeSessionID = ""
		}, "no session id yet"},

		{"suspend, wrong status", "suspend_session", func(s *session.Session) {
			s.SetStatus(session.StatusRunning)
			s.ClaudeSessionID = "abc"
		}, "idle or finished only"},
		{"suspend, already suspended", "suspend_session", func(s *session.Session) {
			s.SetStatus(session.StatusSuspended)
			s.ClaudeSessionID = "abc"
		}, "already suspended"},
		// It IS idle — there's just no resume id, so suspending would lose the convo.
		{"suspend, idle without a resume id", "suspend_session", func(s *session.Session) {
			s.SetStatus(session.StatusIdle)
			s.ClaudeSessionID = ""
		}, "no session id yet"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := session.NewSession("s", "/tmp/cm-repo")
			s.Agent = agent.Claude
			tt.mutate(s)

			h := menuHome(sessionRow(s), s)
			_, items := h.buildContextMenuItems()

			it, ok := findItem(items, tt.id)
			if !ok {
				t.Fatalf("no %q entry", tt.id)
			}
			if it.Enabled {
				t.Fatalf("%q is enabled — this case is supposed to be blocked", tt.id)
			}
			if it.Note != tt.want {
				t.Errorf("%q note = %q, want %q", tt.id, it.Note, tt.want)
			}
		})
	}
}

// The menu teaches keybindings, so a row's shortcut has to be the key that
// actually works. handleKey's suspended check sits inside `case "enter"` ABOVE the
// split-mode swap, so Enter resumes in BOTH modes — advertising ⇥ in split mode
// would point at attachSelected(), which has no live tmux for a suspended session.
func TestContextMenuResumeShortcutIsEnterInBothModes(t *testing.T) {
	for _, mode := range []string{"attach", "split"} {
		t.Run(mode, func(t *testing.T) {
			s := session.NewSession("s", "/tmp/cm-repo")
			s.SetStatus(session.StatusSuspended)

			h := menuHome(sessionRow(s), s)
			h.cfg = &config.Config{EnterMode: mode}

			_, items := h.buildContextMenuItems()
			it, ok := findItem(items, "resume")
			if !ok {
				t.Fatalf("no resume entry in %s mode", mode)
			}
			if it.Shortcut != "⏎" {
				t.Errorf("resume shortcut = %q in %s mode, want ⏎ (Tab does not resume)", it.Shortcut, mode)
			}
		})
	}

	// Sanity: the swap still applies to the rows that genuinely follow it.
	live := session.NewSession("s", "/tmp/cm-repo")
	h := menuHome(sessionRow(live), live)
	h.cfg = &config.Config{EnterMode: "split"}
	_, items := h.buildContextMenuItems()
	if it, _ := findItem(items, "attach"); it.Shortcut != "⇥" {
		t.Errorf("split-mode attach shortcut = %q, want ⇥", it.Shortcut)
	}
}

// A suspended session's tmux is gone, so the primary action has to restart it
// with --resume. Routing it through "attach" would just fail.
func TestContextMenuSuspendedSessionOffersResume(t *testing.T) {
	s := session.NewSession("s", "/tmp/cm-repo")
	s.SetStatus(session.StatusSuspended)
	h := menuHome(sessionRow(s), s)

	_, items := h.buildContextMenuItems()

	if _, ok := findItem(items, "attach"); ok {
		t.Error("suspended session offers `attach`, which cannot work — expected `resume`")
	}
	it, ok := findItem(items, "resume")
	if !ok {
		t.Fatalf("suspended session has no `resume` entry; got %v", ids(items))
	}
	if !it.Enabled {
		t.Error("`resume` should be enabled on a suspended session")
	}
}

// Open PR follows the same condition openPRInBrowser enforces: a cached PR URL.
func TestContextMenuOpenPRTracksCachedPR(t *testing.T) {
	s := session.NewSession("s", "/tmp/cm-repo")
	h := menuHome(sessionRow(s), s)

	_, items := h.buildContextMenuItems()
	if it, _ := findItem(items, "open_pr"); it.Enabled {
		t.Error("open_pr enabled with no PR cached")
	}

	repo := session.GetRepoRoot(s.ProjectPath)
	h.setGitInfo(map[string]*git.RepoInfo{repo: {PR: &github.PR{URL: "https://example.test/pr/1"}}})

	_, items = h.buildContextMenuItems()
	if it, _ := findItem(items, "open_pr"); !it.Enabled {
		t.Error("open_pr disabled even though a PR URL is cached")
	}
}

// The delete label has to name what actually happens — the three-way branch
// confirmDeleteHeader takes. "Forget Repo" on a worktree would be a lie.
func TestContextMenuCheckoutDeleteLabel(t *testing.T) {
	const repo = "/tmp/cm-checkout"

	tests := []struct {
		name     string
		worktree bool
		sessions []*session.Session
		want     string
	}{
		{"worktree says remove", true, nil, "Remove Worktree"},
		{"repo with sessions says forget", false, []*session.Session{session.NewSession("s", repo)}, "Forget Repo"},
		{"empty repo says unpin", false, nil, "Unpin Repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := SidebarItem{IsRepoHeader: true, IsCheckoutHeader: true, RepoPath: repo}
			h := menuHome(item, tt.sessions...)
			h.setGitInfo(map[string]*git.RepoInfo{repo: {IsWorktreeRepo: tt.worktree}})

			_, items := h.buildContextMenuItems()
			it, ok := findItem(items, "delete_at_cursor")
			if !ok {
				t.Fatalf("checkout menu has no delete entry; got %v", ids(items))
			}
			if it.Label != tt.want {
				t.Errorf("delete label = %q, want %q", it.Label, tt.want)
			}
		})
	}
}

// A worktree whose removal previously failed still offers "Remove Worktree" so
// the retry path (d again) is reachable from the menu too.
func TestContextMenuFailedWorktreeStillOffersRemove(t *testing.T) {
	const repo = "/tmp/cm-failed-wt"
	item := SidebarItem{IsRepoHeader: true, IsCheckoutHeader: true, RepoPath: repo, RemovalFailed: true}
	h := menuHome(item)
	h.failedWorktreeRemovals = map[string]bool{repo: true}
	// Note: no gitInfo entry, so repoIsWorktree() is false — the failed-removal
	// map is the only thing that can identify this as a worktree.

	_, items := h.buildContextMenuItems()
	it, _ := findItem(items, "delete_at_cursor")
	if it.Label != "Remove Worktree" {
		t.Errorf("delete label = %q, want %q", it.Label, "Remove Worktree")
	}
}

func TestContextMenuOriginRow(t *testing.T) {
	item := SidebarItem{IsRepoHeader: true, IsOriginHeader: true, OriginKey: "github.com/acme/x", OriginLabel: "acme/x"}
	h := menuHome(item)

	_, items := h.buildContextMenuItems()
	want := []string{"toggle_group", "new_worktree", "delete_at_cursor"}
	got := ids(items)
	if len(got) != len(want) {
		t.Fatalf("origin menu = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("origin menu = %v, want %v", got, want)
		}
	}
	if it, _ := findItem(items, "delete_at_cursor"); it.Label != "Forget Origin Group" {
		t.Errorf("origin delete label = %q, want %q", it.Label, "Forget Origin Group")
	}
}

// Rows with nothing to act on produce no menu, which is what makes `.` a silent
// no-op there rather than an empty box.
func TestContextMenuEmptyRowsProduceNothing(t *testing.T) {
	for _, tt := range []struct {
		name string
		item SidebarItem
	}{
		{"spacer", SidebarItem{IsSpacer: true}},
		{"pending workspace", SidebarItem{Pending: &PendingWorkspace{ID: "p1"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := menuHome(tt.item)
			if _, items := h.buildContextMenuItems(); len(items) != 0 {
				t.Errorf("expected no menu on a %s row, got %v", tt.name, ids(items))
			}
		})
	}

	// Empty fleet: cursor points nowhere.
	h := &Home{cfg: &config.Config{}}
	if _, items := h.buildContextMenuItems(); len(items) != 0 {
		t.Errorf("expected no menu on an empty fleet, got %v", ids(items))
	}
}

// TestContextMenuIDsAreDispatchable is the drift guard: dispatchCommand silently
// no-ops on an unknown id, so a renamed action would turn a menu entry into a
// dead click with no error anywhere. Parse dispatchCommand's case literals and
// assert every id the menu can emit is among them.
func TestContextMenuIDsAreDispatchable(t *testing.T) {
	known := dispatchCommandIDs(t)

	// Every context the menu can open in.
	s := session.NewSession("s", "/tmp/cm-repo")
	s.SetStatus(session.StatusSuspended) // reaches the `resume` branch
	homes := []*Home{
		menuHome(sessionRow(s), s),
		menuHome(SidebarItem{IsRepoHeader: true, IsCheckoutHeader: true, RepoPath: "/tmp/cm-repo"}),
		menuHome(SidebarItem{IsRepoHeader: true, IsOriginHeader: true, OriginKey: "o", OriginLabel: "o"}),
	}
	// ...plus the non-suspended session, whose primary action is `attach`.
	live := session.NewSession("live", "/tmp/cm-repo")
	homes = append(homes, menuHome(sessionRow(live), live))

	for _, h := range homes {
		_, items := h.buildContextMenuItems()
		for _, it := range items {
			if it.ID == "" {
				t.Errorf("menu entry %q has no ID", it.Label)
				continue
			}
			if !known[it.ID] {
				t.Errorf("menu entry %q emits id %q, which dispatchCommand does not handle "+
					"(it would silently do nothing)", it.Label, it.ID)
			}
		}
	}
}

// dispatchCommandIDs parses app.go and returns the set of ids dispatchCommand
// has a case for.
func dispatchCommandIDs(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", nil, 0)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}

	out := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "dispatchCommand" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if v, err := strconv.Unquote(lit.Value); err == nil {
					out[v] = true
				}
			}
			return true
		})
	}
	if len(out) == 0 {
		t.Fatal("found no case ids in dispatchCommand — did it move or get renamed?")
	}
	return out
}

// --- end-to-end through the real key router ---

// Drives the whole path a user takes: `.` in handleKey opens the menu, the menu
// swallows keys via routeToModal, its pick comes back as a contextMenuMsg, and
// Update dispatches it into the real handler. Exercises the wiring the unit tests
// above deliberately skip.
func TestContextMenuEndToEndOpensAndDispatches(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cm.db")
	storage, err := session.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	h.width, h.height = 120, 40

	s := session.NewSession("ctx", "/tmp/cm-e2e")
	h.sessions = []*session.Session{s}
	h.flatItems = []SidebarItem{sessionRow(s)}
	h.cursor = 0

	// `.` opens the menu.
	if _, cmd := h.handleKey(key(".")); cmd != nil {
		t.Errorf("opening the menu emitted %#v, want nothing", cmd())
	}
	if !h.contextMenu.IsVisible() {
		t.Fatal("`.` did not open the context menu")
	}
	if !h.modalOpen() {
		t.Error("modalOpen() is false while the context menu is up")
	}

	// While it's open, keys route to the menu — `d` fires Delete Session.
	_, cmd := h.handleKey(key("d"))
	if cmd == nil {
		t.Fatal("`d` inside the menu produced no command")
	}
	msg, ok := cmd().(contextMenuMsg)
	if !ok || msg.id != "delete_at_cursor" {
		t.Fatalf("`d` inside the menu emitted %#v, want contextMenuMsg{delete_at_cursor}", cmd())
	}
	if h.contextMenu.IsVisible() {
		t.Error("menu stayed open after picking an action")
	}

	// The msg dispatches into the real handler, which raises the delete confirm.
	if _, cmd = h.Update(msg); cmd != nil {
		cmd()
	}
	if !h.confirmDialog.IsVisible() {
		t.Error("dispatching delete_at_cursor did not raise the confirm dialog")
	}
}

// The bug this guards: the menu's entries resolve their subject from h.cursor at
// dispatch time, and an ASYNC message can move the cursor while the menu is open
// (handleSessionCreate auto-selects the session it just made; rebuildFlatItems
// runs on the tick). Keys can't do it — routeToModal feeds those to the menu — so
// the original "the cursor can't move" reasoning missed this entirely. Left
// unfixed, `d` on a menu titled `session: A` would confirm deleting session B.
func TestContextMenuDispatchFollowsTargetNotCursor(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cm-target.db")
	storage, err := session.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	h.width, h.height = 120, 40

	a := session.NewSession("alpha", "/tmp/cm-e2e")
	b := session.NewSession("beta", "/tmp/cm-e2e")
	h.sessions = []*session.Session{a, b}
	h.flatItems = []SidebarItem{sessionRow(a), sessionRow(b)}
	h.cursor = 0 // menu opens on alpha

	h.handleKey(key("."))
	if !h.contextMenu.IsVisible() {
		t.Fatal("`.` did not open the menu")
	}

	// Simulate the async move: a session-create result auto-selects the new row.
	h.cursor = 1 // cursor is now on beta, but the menu still says `session: alpha`

	_, cmd := h.handleKey(key("d"))
	if cmd == nil {
		t.Fatal("`d` produced no command")
	}
	msg := cmd().(contextMenuMsg)
	if _, cmd = h.Update(msg); cmd != nil {
		cmd()
	}

	// The confirm must be for alpha — the row the menu was opened on and named.
	if h.cursor != 0 {
		t.Errorf("cursor = %d after dispatch, want 0 (re-pinned to the menu's target)", h.cursor)
	}
	if !h.confirmDialog.IsVisible() {
		t.Fatal("no confirm dialog raised")
	}
	if h.confirmDialog.subject != "alpha" {
		t.Errorf("confirm names %q, want %q", h.confirmDialog.subject, "alpha")
	}
	// The subject is just a label — assert on the id the confirm would actually delete.
	del, ok := h.confirmDialog.onYes().(sessionDeleteMsg)
	if !ok {
		t.Fatalf("confirm emits %#v, want sessionDeleteMsg", h.confirmDialog.onYes())
	}
	if del.id != a.ID {
		t.Errorf("confirm would delete %q (beta), want %q (alpha) — the menu acted on the wrong session", del.id, a.ID)
	}
}

// If the menu's row is deleted out from under it, dispatch must refuse rather than
// silently act on whatever row inherited the cursor index.
func TestContextMenuRefusesWhenTargetIsGone(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cm-gone.db")
	storage, err := session.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	h.width, h.height = 120, 40

	a := session.NewSession("alpha", "/tmp/cm-e2e")
	b := session.NewSession("beta", "/tmp/cm-e2e")
	h.sessions = []*session.Session{a, b}
	h.flatItems = []SidebarItem{sessionRow(a), sessionRow(b)}
	h.cursor = 0

	h.handleKey(key("."))
	_, cmd := h.handleKey(key("d"))
	msg := cmd().(contextMenuMsg)

	// alpha vanishes before the pick lands; beta slides into index 0.
	h.sessions = []*session.Session{b}
	h.flatItems = []SidebarItem{sessionRow(b)}

	if _, cmd = h.Update(msg); cmd != nil {
		cmd()
	}
	if h.confirmDialog.IsVisible() {
		t.Error("dispatched onto a different session after the menu's target disappeared")
	}
}

// `.` on a row with nothing to act on stays silent rather than popping an empty box.
func TestContextMenuKeyIsNoOpOnEmptyRow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cm2.db")
	storage, err := session.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	h.width, h.height = 120, 40
	h.flatItems = []SidebarItem{{IsSpacer: true}}
	h.cursor = 0

	h.handleKey(key("."))
	if h.contextMenu.IsVisible() {
		t.Error("`.` opened a menu on a spacer row")
	}
}

// --- dialog behavior ---

func menuDialog(items []ContextMenuItem) *ContextMenuDialog {
	d := NewContextMenuDialog()
	d.SetSize(120, 40)
	d.SetAnchor(3, 5, 39)
	d.Show("row", items)
	return d
}

func key(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

// Nav must never land on a dimmed row — it's not selectable, so stopping there
// would be a dead end the user has to press through.
func TestContextMenuNavSkipsDisabled(t *testing.T) {
	d := menuDialog([]ContextMenuItem{
		{ID: "a", Label: "A", Enabled: true},
		{ID: "b", Label: "B", Enabled: false},
		{ID: "c", Label: "C", Enabled: true},
	})

	if got := d.items[d.cursor].ID; got != "a" {
		t.Fatalf("cursor opened on %q, want %q", got, "a")
	}
	d, _ = d.Update(key("j"))
	if got := d.items[d.cursor].ID; got != "c" {
		t.Errorf("after j, cursor = %q, want %q (b is disabled)", got, "c")
	}
	d, _ = d.Update(key("k"))
	if got := d.items[d.cursor].ID; got != "a" {
		t.Errorf("after k, cursor = %q, want %q (b is disabled)", got, "a")
	}
	// Nav does not wrap: k at the top stays put.
	d, _ = d.Update(key("k"))
	if got := d.items[d.cursor].ID; got != "a" {
		t.Errorf("k at the top moved to %q, want to stay on %q", got, "a")
	}
}

// The cursor must never *open* on a disabled row either.
func TestContextMenuOpensOnFirstEnabled(t *testing.T) {
	d := menuDialog([]ContextMenuItem{
		{ID: "a", Label: "A", Enabled: false},
		{ID: "b", Label: "B", Enabled: true},
	})
	if got := d.items[d.cursor].ID; got != "b" {
		t.Errorf("cursor opened on %q, want the first enabled row %q", got, "b")
	}
}

func TestContextMenuShortcutPassthrough(t *testing.T) {
	items := []ContextMenuItem{
		{ID: "attach", Label: "Attach", Enabled: true},
		{ID: "delete", Label: "Delete", Shortcut: "d", Key: "d", Enabled: true},
		{ID: "approve", Label: "Approve", Shortcut: "Y", Key: "Y", Enabled: false},
	}

	// An enabled row's own key fires it, even though the cursor is elsewhere.
	d := menuDialog(items)
	d, cmd := d.Update(key("d"))
	if cmd == nil {
		t.Fatal("pressing `d` produced no command")
	}
	msg, ok := cmd().(contextMenuMsg)
	if !ok || msg.id != "delete" {
		t.Fatalf("pressing `d` emitted %#v, want contextMenuMsg{delete}", cmd())
	}
	if d.IsVisible() {
		t.Error("menu stayed open after firing an action")
	}

	// A disabled row's key does nothing — it must not fire the action that the
	// dimmed row is telling the user is unavailable.
	d = menuDialog(items)
	d, cmd = d.Update(key("Y"))
	if cmd != nil {
		t.Errorf("pressing a disabled row's key emitted %#v, want nothing", cmd())
	}
	if !d.IsVisible() {
		t.Error("menu closed on a disabled row's key")
	}

	// An unmatched key is swallowed rather than falling through to the sidebar.
	d, cmd = d.Update(key("z"))
	if cmd != nil {
		t.Errorf("unmatched key emitted %#v, want nothing", cmd())
	}
	if !d.IsVisible() {
		t.Error("menu closed on an unmatched key")
	}
}

func TestContextMenuEnterFiresCursorRow(t *testing.T) {
	d := menuDialog([]ContextMenuItem{
		{ID: "attach", Label: "Attach", Enabled: true},
		{ID: "delete", Label: "Delete", Enabled: true},
	})
	d, _ = d.Update(key("j"))
	_, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter produced no command")
	}
	if msg, ok := cmd().(contextMenuMsg); !ok || msg.id != "delete" {
		t.Fatalf("enter emitted %#v, want contextMenuMsg{delete}", cmd())
	}
}

func TestContextMenuEscCloses(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{
		{Code: tea.KeyEscape},
		key("."),
	} {
		d := menuDialog([]ContextMenuItem{{ID: "a", Label: "A", Enabled: true}})
		d, cmd := d.Update(k)
		if d.IsVisible() {
			t.Errorf("menu stayed open after %v", k)
		}
		if cmd != nil {
			t.Errorf("closing emitted %#v, want nothing", cmd())
		}
	}
}

// The rendered box must never be taller than the space it's budgeted for. The
// original budget counted "2 borders + title + hint" — but there is no hint line,
// and the two `⋮` scroll indicators weren't counted at all, so a scrolled menu on
// a short terminal overran the footer by a row.
func TestContextMenuFitsShortTerminal(t *testing.T) {
	items := make([]ContextMenuItem, 14) // more rows than a short screen can hold
	for i := range items {
		items[i] = ContextMenuItem{ID: "a", Label: "Action", Enabled: true}
	}

	for _, height := range []int{10, 12, 16, 24} {
		bottomLimit := height - 1 // 1-row footer
		d := NewContextMenuDialog()
		d.SetSize(80, height)
		d.SetAnchor(3, 2, bottomLimit)
		d.Show("row", items)

		// Scroll to the middle so BOTH ⋮ indicators render — the worst case.
		for i := 0; i < len(items)/2; i++ {
			d, _ = d.Update(key("j"))
		}

		got := lipgloss.Height(d.View())
		if got > bottomLimit {
			t.Errorf("height=%d: box is %d rows but only %d are available — it overruns the footer",
				height, got, bottomLimit)
		}
	}
}

// The dropdown hangs below its row, and flips above when that would run it into
// the footer — otherwise a menu on the bottom row would render off-screen.
func TestContextMenuPositionFlipsAndClamps(t *testing.T) {
	d := NewContextMenuDialog()
	d.SetSize(80, 40)
	d.Show("row", []ContextMenuItem{{ID: "a", Label: "A", Enabled: true}})

	// Room below: hangs just under the row.
	d.SetAnchor(3, 5, 39)
	if x, y := d.Position(20, 8); x != 3 || y != 6 {
		t.Errorf("with room below: (x,y) = (%d,%d), want (3,6)", x, y)
	}

	// No room below: flips above the row.
	d.SetAnchor(3, 35, 39)
	if _, y := d.Position(20, 8); y != 27 {
		t.Errorf("near the footer: y = %d, want 27 (flipped above row 35)", y)
	}

	// Too wide to sit at the anchor: clamped to the right edge.
	d.SetAnchor(70, 5, 39)
	if x, _ := d.Position(20, 8); x != 60 {
		t.Errorf("wide menu: x = %d, want 60 (clamped to the 80-col screen)", x)
	}

	// Fits nowhere vertically: clamped on-screen rather than pushed negative.
	d.SetAnchor(3, 2, 39)
	if _, y := d.Position(20, 60); y != 0 {
		t.Errorf("oversized menu: y = %d, want 0", y)
	}
}

// The anchor tracks the cursor's actual screen row, including the row
// RenderSidebar spends on its "… N more above" indicator once scrolled. Getting
// this wrong lands the dropdown next to the wrong session.
func TestContextMenuAnchorFollowsScroll(t *testing.T) {
	h := &Home{cfg: &config.Config{}, width: 100, height: 40}

	h.cursor, h.viewOffset = 0, 0
	if _, rowY, _ := h.contextMenuAnchor(); rowY != 2 {
		t.Errorf("unscrolled first row: rowY = %d, want 2 (header + panel border)", rowY)
	}

	h.cursor, h.viewOffset = 3, 0
	if _, rowY, _ := h.contextMenuAnchor(); rowY != 5 {
		t.Errorf("unscrolled 4th row: rowY = %d, want 5", rowY)
	}

	// Scrolled: the cursor is the 1st visible row, but an indicator row sits above it.
	h.cursor, h.viewOffset = 10, 10
	if _, rowY, _ := h.contextMenuAnchor(); rowY != 3 {
		t.Errorf("scrolled: rowY = %d, want 3 (2 + the `… more above` row)", rowY)
	}

	// The bottom limit clears the footer.
	if _, _, bottom := h.contextMenuAnchor(); bottom != 40-h.footerHeight() {
		t.Errorf("bottom limit = %d, want %d", bottom, 40-h.footerHeight())
	}
}
