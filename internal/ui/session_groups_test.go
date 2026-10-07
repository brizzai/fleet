package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/session"
)

func localOrigin(repo string) string { return "local:" + filepath.Base(repo) }

func groupedSession(t *testing.T, title, path, group string) *session.Session {
	t.Helper()
	s := session.NewSession(title, path)
	s.SetGroupID(group)
	return s
}

// countSessionRows counts how many rows show session s.
func countSessionRows(items []SidebarItem, s *session.Session) int {
	n := 0
	for _, it := range items {
		if it.Session != nil && it.Session.ID == s.ID {
			n++
		}
	}
	return n
}

// A grouped session lives only in its group's section (one row per session),
// the group sits above a "repos" divider, and a pin whose only sessions are
// grouped doesn't come back at the top level as an empty ghost.
func TestBuildFlatItemsGroupsSessionsAboveTheRepos(t *testing.T) {
	lead := groupedSession(t, "lead", "/r/svc-a", "g1")
	child := groupedSession(t, "child", "/r/svc-b", "g1")
	loose := session.NewSession("loose", "/r/svc-a")
	pinned := map[string]bool{"/r/svc-a": true, "/r/svc-b": true, "/r/svc-c": true}

	items := BuildFlatItems([]*session.Session{lead, child, loose}, nil,
		[]SidebarGroup{{ID: "g1", Label: "BRZ-12 · fix login"}},
		map[string]bool{}, "", pinned, nil, nil, time.Time{}, localOrigin, nil)

	if !items[0].IsGroupHeader || items[0].GroupLabel != "BRZ-12 · fix login" || items[0].SessionCount != 2 {
		t.Fatalf("first row = %+v, want the group header holding 2 sessions", items[0])
	}
	for _, s := range []*session.Session{lead, child, loose} {
		if n := countSessionRows(items, s); n != 1 {
			t.Fatalf("%s renders %d times, want exactly once", s.Title, n)
		}
	}

	divider := -1
	for i, it := range items {
		if it.IsSpacer && it.Divider == "repos" {
			divider = i
		}
	}
	if divider < 0 {
		t.Fatal("no repos divider between the groups and the origin trees")
	}
	for i, it := range items {
		inGroup := i < divider
		if it.IsSpacer {
			continue
		}
		if inGroup && !it.IsGroupHeader && (it.GroupID != "g1" || it.Depth != 1) {
			t.Fatalf("row %d inside the group has GroupID=%q Depth=%d", i, it.GroupID, it.Depth)
		}
		if !inGroup && (it.GroupID != "" || it.Depth != 0) {
			t.Fatalf("top-level row %d carries GroupID=%q Depth=%d", i, it.GroupID, it.Depth)
		}
		if it.Session == lead || it.Session == child {
			if !inGroup {
				t.Fatalf("grouped session %s rendered at the top level", it.Session.Title)
			}
		}
	}

	var top []string
	for _, it := range items[divider:] {
		if it.IsCheckoutHeader {
			top = append(top, it.RepoPath)
		}
	}
	if strings.Join(top, ",") != "/r/svc-a,/r/svc-c" {
		t.Fatalf("top-level checkouts = %v, want svc-a (has an ungrouped session) and svc-c (pin), not the grouped-only svc-b", top)
	}
}

// With no groups the sidebar is exactly what it was: no divider, no depth.
func TestBuildFlatItemsWithoutGroupsIsUnchanged(t *testing.T) {
	// A dangling id (its group dissolved) renders at the top level too.
	s := groupedSession(t, "orphan", "/r/svc-a", "gone")
	items := BuildFlatItems([]*session.Session{s}, nil, nil, map[string]bool{}, "", nil, nil, nil, time.Time{}, localOrigin, nil)
	for _, it := range items {
		if it.IsGroupHeader || it.Divider != "" || it.Depth != 0 || it.GroupID != "" {
			t.Fatalf("ungrouped sidebar grew group structure: %+v", it)
		}
	}
	if countSessionRows(items, s) != 1 {
		t.Fatal("session with a dangling group id disappeared")
	}
}

// Folding a checkout inside a group must not fold the same checkout at the
// top level — the keys are scoped by group.
func TestGroupFoldKeysAreScoped(t *testing.T) {
	grouped := groupedSession(t, "grouped", "/r/svc-a", "g1")
	loose := session.NewSession("loose", "/r/svc-a")
	expanded := map[string]bool{scopedKey("g1", "/r/svc-a"): false}
	items := BuildFlatItems([]*session.Session{grouped, loose}, nil, []SidebarGroup{{ID: "g1", Label: "g"}},
		expanded, "", nil, nil, nil, time.Time{}, localOrigin, nil)
	if countSessionRows(items, grouped) != 0 {
		t.Fatal("folded checkout inside the group still shows its session")
	}
	if countSessionRows(items, loose) != 1 {
		t.Fatal("folding the group's copy of the checkout folded the top-level one too")
	}

	// And the group's own key folds the whole section.
	items = BuildFlatItems([]*session.Session{grouped}, nil, []SidebarGroup{{ID: "g1", Label: "g"}},
		map[string]bool{GroupExpandKey("g1"): false}, "", nil, nil, nil, time.Time{}, localOrigin, nil)
	if len(items) != 1 || !items[0].IsGroupHeader || items[0].Expanded {
		t.Fatalf("collapsed group rendered %d rows, want just its header", len(items))
	}
}

func TestGroupSnoozeMutesOnlyItsMembers(t *testing.T) {
	grouped := groupedSession(t, "grouped", "/r/svc-a", "g1")
	loose := session.NewSession("loose", "/r/svc-a")
	now := time.Now()
	snoozes := map[string]time.Time{GroupExpandKey("g1"): now.Add(time.Hour)}
	if !snoozeState(grouped, "g1", "local:svc-a", "/r/svc-a", snoozes, now).Muted {
		t.Fatal("group snooze didn't mute its member")
	}
	if snoozeState(loose, "", "local:svc-a", "/r/svc-a", snoozes, now).Muted {
		t.Fatal("group snooze muted a session outside the group")
	}
	// A checkout snoozed inside the group stays scoped there too.
	snoozes = map[string]time.Time{scopedKey("g1", "/r/svc-a"): now.Add(time.Hour)}
	if snoozeState(loose, "", "local:svc-a", "/r/svc-a", snoozes, now).Muted {
		t.Fatal("snoozing the group's copy of a checkout muted the top-level one")
	}
}

func TestIsCheckoutExpandKey(t *testing.T) {
	for key, want := range map[string]bool{
		"/r/svc-a":                     true,
		"origin:github.com/a/b":        false,
		"group:g1":                     false,
		"group:g1|/r/svc-a":            true,
		"group:g1|origin:local:svc-a":  false,
		scopedKey("g1", "/r/a|weird"):  true,
		scopedKey("", "origin:x"):      false,
		scopedKey("g1", "origin:a|b"):  false,
		scopedKey("g1", "/r/svc-b/x"):  true,
		GroupExpandKey("abc-12345678"): false,
	} {
		if got := isCheckoutExpandKey(key); got != want {
			t.Errorf("isCheckoutExpandKey(%q) = %v, want %v", key, got, want)
		}
	}
}

// The parent is a session this TUI already holds; a `fleet wt` child run from
// its pane moves it into a new group in SQLite. Adoption never revisits known
// rows, so the group sync is what moves it on screen.
func TestGroupSyncMovesAKnownParentIntoItsNewGroup(t *testing.T) {
	h := adoptTestHome(t)
	row := storeSession(t, h, "parent", "/tmp/grp-parent")
	h.handleAdoptSessions(adoptSessionsMsg{sessions: []*session.Session{session.FromRow(row)}})
	h.cursor = (contextMenuTarget{sessionID: row.ID}).find(h.flatItems)
	if h.cursor < 0 {
		t.Fatal("adopted parent not in the sidebar")
	}

	// What `fleet wt` does from inside the parent's pane.
	gid, created, ok, err := h.storage.EnsureLeadGroup(row.ID, time.Now())
	if err != nil || !ok || !created {
		t.Fatalf("EnsureLeadGroup: %v ok=%v created=%v", err, ok, created)
	}

	sync := func() groupSyncMsg {
		rows, _ := h.storage.LoadSessions()
		groups, _ := h.storage.LoadGroups()
		return groupSyncMsg{gen: h.groupGen.Load(), membership: session.SessionGroupIDs(rows), groups: groups}
	}

	// A reply read before a local change landed is dropped, not applied.
	stale := sync()
	h.markGroupsChanged()
	h.handleGroupSync(stale)
	if h.sessionByID[row.ID].GroupID() != "" {
		t.Fatal("a stale sync reply was applied")
	}

	h.handleGroupSync(sync())
	if got := h.sessionByID[row.ID].GroupID(); got != gid {
		t.Fatalf("parent group = %q, want %q", got, gid)
	}
	if len(h.flatItems) == 0 || !h.flatItems[0].IsGroupHeader || h.flatItems[0].GroupLabel != "parent" {
		t.Fatalf("sidebar doesn't lead with the parent's group: %+v", h.flatItems)
	}
	if got := h.flatItems[h.cursor]; got.Session == nil || got.Session.ID != row.ID {
		t.Fatalf("cursor didn't follow the parent into its group: %+v", got)
	}
}

func TestGroupLabelPrecedence(t *testing.T) {
	h := adoptTestHome(t)
	lead := session.NewSession("Fix the login flow", "/tmp/grp-label")
	h.sessions = []*session.Session{lead}
	h.rebuildSessionMap()

	if got := h.groupLabel(&session.Group{ID: "g", Name: "BRZ-12"}); got != "BRZ-12" {
		t.Fatalf("named group label = %q", got)
	}
	if got := h.groupLabel(&session.Group{ID: "g", LeadSessionID: lead.ID}); got != "Fix the login flow" {
		t.Fatalf("lead-derived label = %q", got)
	}
	lead.SetGroupID("g")
	if got := h.groupLabel(&session.Group{ID: "g"}); got != "group · 1 session" {
		t.Fatalf("leaderless label = %q", got)
	}
}

// A header delete never reaches past the tree it was pressed in: the top-level
// checkout header leaves the grouped session in the same checkout alone, and
// keeps the directory that session still runs in.
func TestHeaderDeleteStaysInItsScope(t *testing.T) {
	h := adoptTestHome(t)
	g := &session.Group{ID: "g1", CreatedAt: time.Now()}
	if err := h.storage.CreateGroup(g); err != nil {
		t.Fatal(err)
	}
	h.sessionGroups = []*session.Group{g}

	grouped := groupedSession(t, "grouped", "/tmp/grp-scope", "g1")
	loose := session.NewSession("loose", "/tmp/grp-scope")
	for _, s := range []*session.Session{grouped, loose} {
		if err := h.storage.SaveSession(s.ToRow()); err != nil {
			t.Fatal(err)
		}
	}
	h.sessions = []*session.Session{grouped, loose}
	h.rebuildSessionMap()

	h.deferDeleteRepo(repoDeleteMsg{repoPath: session.GetRepoRoot(loose.ProjectPath), destroyWorkspace: true, scope: ""})

	if _, ok := h.sessionByID[grouped.ID]; !ok {
		t.Fatal("top-level delete removed a grouped session")
	}
	if _, ok := h.sessionByID[loose.ID]; ok {
		t.Fatal("top-level delete left its own session")
	}
	if len(h.pendingDeletes) != 1 || h.pendingDeletes[0].DestroyWS {
		t.Fatalf("pending = %+v, want one delete that keeps the directory", h.pendingDeletes)
	}

	// Undo restores it, and the empty-group collector leaves a group alone
	// while an undoable delete still points at it.
	h.undoDelete()
	if _, ok := h.sessionByID[loose.ID]; !ok {
		t.Fatal("undo didn't restore the session")
	}
}

func TestEmptyGroupSurvivesItsUndoWindow(t *testing.T) {
	h := adoptTestHome(t)
	g := &session.Group{ID: "g1", CreatedAt: time.Now().Add(-time.Hour)}
	if err := h.storage.CreateGroup(g); err != nil {
		t.Fatal(err)
	}
	h.sessionGroups = []*session.Group{g}
	only := groupedSession(t, "only", "/tmp/grp-undo", "g1")
	if err := h.storage.SaveSession(only.ToRow()); err != nil {
		t.Fatal(err)
	}
	h.sessions = []*session.Session{only}
	h.rebuildSessionMap()

	h.deferDelete(sessionDeleteMsg{id: only.ID})
	h.collectEmptyGroups()
	if h.groupByID("g1") == nil {
		t.Fatal("group collected while its last member's delete was still undoable")
	}
	h.undoDelete()
	if got := h.sessionByID[only.ID]; got == nil || got.GroupID() != "g1" {
		t.Fatal("undo didn't restore the session into its group")
	}

	// Once nothing references it, it goes.
	h.deferDelete(sessionDeleteMsg{id: only.ID})
	h.pendingDeletes = nil
	h.collectEmptyGroups()
	if h.groupByID("g1") != nil {
		t.Fatal("empty group outlived its last member")
	}
}

// Creation follows the cursor's group, and the captured group applies to
// exactly one create.
func TestCreateGroupIsConsumedOnce(t *testing.T) {
	h := adoptTestHome(t)
	h.sessionGroups = []*session.Group{{ID: "g1"}}
	h.flatItems = []SidebarItem{{IsRepoHeader: true, IsGroupHeader: true, GroupID: "g1", GroupLabel: "g"}}
	h.cursor = 0
	h.beginCreate()
	if got := h.takeCreateGroup(); got != "g1" {
		t.Fatalf("first create got %q, want g1", got)
	}
	if got := h.takeCreateGroup(); got != "" {
		t.Fatalf("second create got %q, want nothing", got)
	}
}

// Every row of the group header's menu and of the move picker must be handled
// by dispatchCommand, or it is a dead click.
func TestGroupMenusAreDispatchable(t *testing.T) {
	known := dispatchCommandIDs(t)
	h := menuHome(SidebarItem{IsRepoHeader: true, IsGroupHeader: true, GroupID: "g1", GroupLabel: "g"})
	_, items := h.buildContextMenuItems()
	if len(items) == 0 {
		t.Fatal("group header has no menu")
	}
	for _, it := range items {
		if !known[it.ID] {
			t.Errorf("group menu entry %q emits unhandled id %q", it.Label, it.ID)
		}
	}

	s := session.NewSession("s", "/tmp/grp-menu")
	h = menuHome(sessionRow(s), s)
	h.sessionGroups = []*session.Group{{ID: "g1", Name: "one"}}
	h.rebuildSessionMap()
	h.contextMenu = NewContextMenuDialog()
	h.openGroupPicker()
	for _, it := range h.contextMenu.items {
		if strings.HasPrefix(it.ID, moveToGroupPrefix) {
			continue // handled ahead of the switch, by prefix
		}
		if !known[it.ID] {
			t.Errorf("move picker entry %q emits unhandled id %q", it.Label, it.ID)
		}
	}
}
