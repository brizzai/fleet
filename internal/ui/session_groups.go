package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/proc"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/ticket"
	"github.com/brizzai/fleet/internal/ticketing"
)

// Session groups (issue #341): a cross-repo section of the sidebar holding the
// sessions working on one thing. The common way one forms is with no keypress
// at all — a session running the fleet skill creates worktrees in three other
// services, and `fleet wt` puts each child in its parent's group (see
// resolveLaunchGroup in cmd/fleet). This file is the TUI half: labels, the
// sync with what other processes wrote, and the group-scoped actions.

// knownGroups is the set of group ids this TUI holds. A session whose stored
// group id is not in it renders at the top level (sessionGroupOf).
func (h *Home) knownGroups() map[string]bool {
	out := make(map[string]bool, len(h.sessionGroups))
	for _, g := range h.sessionGroups {
		out[g.ID] = true
	}
	return out
}

// groupByID returns the group with this id, or nil.
func (h *Home) groupByID(id string) *session.Group {
	if id == "" {
		return nil
	}
	for _, g := range h.sessionGroups {
		if g.ID == id {
			return g
		}
	}
	return nil
}

// effectiveGroup is the group a session renders in ("" = top level).
func (h *Home) effectiveGroup(s *session.Session) string {
	if s == nil {
		return ""
	}
	if id := s.GroupID(); id != "" && h.groupByID(id) != nil {
		return id
	}
	return ""
}

// groupMembers returns the sessions rendering in group id.
func (h *Home) groupMembers(id string) []*session.Session {
	if id == "" {
		return nil
	}
	var out []*session.Session
	for _, s := range h.sessions {
		if s.GroupID() == id {
			out = append(out, s)
		}
	}
	return out
}

// revealSession expands every header between the top of the sidebar and this
// session's row: its session group (if any), then its origin and checkout as
// scoped inside that group. Transient, like revealCheckout — it writes the map
// directly so a jump doesn't overwrite a deliberate collapse on disk.
func (h *Home) revealSession(s *session.Session) {
	repo := session.GetRepoRoot(s.ProjectPath)
	g := h.effectiveGroup(s)
	if g != "" {
		h.repoExpanded[GroupExpandKey(g)] = true
	}
	h.repoExpanded[scopedKey(g, OriginExpandKey(h.originOf(repo)))] = true
	h.repoExpanded[scopedKey(g, repo)] = true
}

// cursorGroupID is the session group the cursor's row sits inside, "" at the
// top level. Creation follows it, the same way delete scope follows the cursor.
func (h *Home) cursorGroupID() string {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return ""
	}
	return h.flatItems[h.cursor].GroupID
}

// sidebarGroups resolves every group's label, in sidebar order.
func (h *Home) sidebarGroups() []SidebarGroup {
	if len(h.sessionGroups) == 0 {
		return nil
	}
	out := make([]SidebarGroup, 0, len(h.sessionGroups))
	for _, g := range h.sessionGroups {
		out = append(out, SidebarGroup{ID: g.ID, Label: h.groupLabel(g)})
	}
	return out
}

// groupLabel names a group on its header:
//   - its explicit name, when it has one (`R` on the header, `--group`);
//   - otherwise its lead session's live title, so the label follows
//     auto-naming — prefixed with the ticket the lead's branch carries
//     ("BRZ-12 · Fix the login flow"), which is how a ticket's worth of
//     sessions gets a ticket's name with nothing typed;
//   - with neither, "group · N sessions".
func (h *Home) groupLabel(g *session.Group) string {
	if g.Name != "" {
		return g.Name
	}
	if lead := h.sessionByID[g.LeadSessionID]; lead != nil {
		title := lead.Title
		if id := h.leadTicketID(lead); id != "" && !strings.HasPrefix(strings.ToUpper(title), id) {
			return id + " · " + title
		}
		return title
	}
	n := len(h.groupMembers(g.ID))
	if n == 1 {
		return "group · 1 session"
	}
	return fmt.Sprintf("group · %d sessions", n)
}

// leadTicketID returns the tracker identifier a session's branch carries,
// gated on the repo's configured tracker keys exactly as branch inference is
// everywhere else — ungated, `release-2024-cleanup` reads as RELEASE-2024.
// Cache-only reads: the branch comes from the git snapshot and the keys are
// memoized, since this runs on every sidebar rebuild.
func (h *Home) leadTicketID(s *session.Session) string {
	root, ok := session.LookupRepoRoot(s.ProjectPath)
	if !ok {
		return ""
	}
	info := h.gitInfo()[root]
	if info == nil || info.Branch == "" {
		return ""
	}
	keys, cached := h.ticketKeysByRepo[root]
	if !cached {
		keys = ticketing.Keys(root)
		if h.ticketKeysByRepo == nil {
			h.ticketKeysByRepo = make(map[string][]string)
		}
		h.ticketKeysByRepo[root] = keys
	}
	if len(keys) == 0 {
		return ""
	}
	return ticket.IdentifierFromBranch(info.Branch, keys)
}

// beginCreate records the group the cursor is in as the one the create the
// user is starting will join. Every user-started create calls it — that is
// what keeps createGroup from going stale: a create that was abandoned (a
// dialog dismissed with esc) is overwritten by the next one before anything
// reads it.
func (h *Home) beginCreate() {
	h.createGroup = h.cursorGroupID()
	label := ""
	if g := h.groupByID(h.createGroup); g != nil {
		label = h.groupLabel(g)
	}
	h.newDialog.SetGroupLabel(label)
	h.sessionCreateDialog.SetGroupLabel(label)
	h.worktreeDialog.SetGroupLabel(label)
}

// takeCreateGroup consumes createGroup, so it is applied to exactly one create.
func (h *Home) takeCreateGroup() string {
	g := h.createGroup
	h.createGroup = ""
	return g
}

// pendingGroup returns the group a "Creating…" phantom was started in.
func (h *Home) pendingGroup(id string) string {
	for _, pw := range h.pendingWorkspaces {
		if pw.ID == id {
			return pw.GroupID
		}
	}
	return ""
}

// emptyGroupGrace is how old a group must be before an empty one is collected.
// A CLI launch creates its group a moment before it saves the session that
// joins it; a sweep landing in that gap must not collect it.
const emptyGroupGrace = 30 * time.Second

// moveToGroupPrefix prefixes the ids of the group picker's rows — one per
// existing group, so they can't be case literals in dispatchCommand.
const moveToGroupPrefix = "move_to_group:"

// markGroupsChanged records a local group mutation. Call it AFTER the SQLite
// write: a sync sweep that read SQLite before the write then carries an older
// generation and is dropped, and one that read after it is already fresh.
func (h *Home) markGroupsChanged() { h.groupGen.Add(1) }

// groupContextMenu is the `.` menu on a group header.
func (h *Home) groupContextMenu() (string, []ContextMenuItem) {
	item := h.flatItems[h.cursor]
	expand := "Expand"
	if item.Expanded {
		expand = "Collapse"
	}
	items := []ContextMenuItem{
		{ID: "toggle_group", Label: expand, Shortcut: "⏎", Enabled: true},
		{ID: "new_repo", Label: "New Session in Group…", Shortcut: "n", Key: "n", Enabled: true},
		{ID: "rename", Label: "Rename Group", Shortcut: "R", Key: "R", Enabled: true},
		h.snoozeMenuItem(),
		{ID: "ungroup", Label: "Ungroup (keep sessions)", Enabled: true},
		{ID: "delete_at_cursor", Label: "Delete Group…", Shortcut: "d", Key: "d", Enabled: true},
	}
	return "group: " + item.GroupLabel, items
}

// openGroupPicker offers the groups the selected session can move to, on the
// context menu's own dropdown: every other group, then a new one, then out of
// its group. Each row is an ordinary dispatch id, so the menu's anchoring,
// keys, clicks and target tracking all carry over.
func (h *Home) openGroupPicker() {
	s := h.selectedSession()
	if s == nil {
		return
	}
	current := h.effectiveGroup(s)
	labels := h.sidebarGroups()
	items := make([]ContextMenuItem, 0, len(labels)+2)
	for _, g := range labels {
		it := ContextMenuItem{ID: moveToGroupPrefix + g.ID, Label: g.Label, Enabled: g.ID != current}
		if g.ID == current {
			it.Note = "current"
		}
		items = append(items, it)
	}
	items = append(items, ContextMenuItem{ID: "move_to_new_group", Label: "New Group…", Enabled: true})
	remove := ContextMenuItem{ID: "remove_from_group", Label: "Remove from Group"}
	if current == "" {
		remove.Note = "not in a group"
	} else {
		remove.Enabled = true
	}
	items = append(items, remove)
	h.contextMenuTarget = h.targetForCursor()
	h.contextMenu.SetAnchor(h.contextMenuAnchor())
	h.contextMenu.Show("move: "+s.Title, items)
}

// moveSessionToGroup moves the selected session into group gid ("" = out of
// every group), keeping the cursor on it as its row jumps sections.
func (h *Home) moveSessionToGroup(gid string) {
	s := h.selectedSession()
	if s == nil {
		return
	}
	if gid != "" && h.groupByID(gid) == nil {
		return
	}
	old := h.effectiveGroup(s)
	if old == gid {
		return
	}
	if err := h.storage.SetSessionGroup(s.ID, gid); err != nil {
		h.setError("group_move_failed", fmt.Errorf("move to group failed: %w", err))
		return
	}
	h.retireLeadIfLeaving(s, old)
	s.SetGroupID(gid)
	h.markGroupsChanged()
	h.collectEmptyGroups()
	h.revealSession(s)
	h.rebuildAndFollow(s.ID)
	if gid == "" {
		h.logAction("ungroup session", s.Title, true)
		h.setInfo(fmt.Sprintf("Moved %q out of its group", s.Title))
		return
	}
	h.logAction("move session to group", s.Title, true)
	h.setInfo(fmt.Sprintf("Moved %q to %s", s.Title, h.groupLabel(h.groupByID(gid))))
}

// createGroupForSession starts a new group led by the given session, named
// name ("" = labelled from the session's own title).
func (h *Home) createGroupForSession(sessionID, name string) {
	s := h.sessionByID[sessionID]
	if s == nil {
		return
	}
	g := &session.Group{ID: session.NewGroupID(), Name: name, LeadSessionID: s.ID, CreatedAt: time.Now()}
	if err := h.storage.CreateGroup(g); err != nil {
		h.setError("group_create_failed", fmt.Errorf("create group failed: %w", err))
		return
	}
	if err := h.storage.SetSessionGroup(s.ID, g.ID); err != nil {
		h.setError("group_move_failed", fmt.Errorf("move to group failed: %w", err))
		return
	}
	old := h.effectiveGroup(s)
	h.retireLeadIfLeaving(s, old)
	s.SetGroupID(g.ID)
	h.sessionGroups = append(h.sessionGroups, g)
	h.markGroupsChanged()
	h.collectEmptyGroups()
	h.revealSession(s)
	h.rebuildAndFollow(s.ID)
	h.logAction("new group", s.Title, true)
	h.setInfo(fmt.Sprintf("Started group %s", h.groupLabel(g)))
}

// renameGroup sets a group's explicit name; "" returns it to the label derived
// from its lead.
func (h *Home) renameGroup(id, name string) {
	g := h.groupByID(id)
	if g == nil {
		return
	}
	if err := h.storage.RenameGroup(id, name); err != nil {
		h.setError("group_rename_failed", fmt.Errorf("rename group failed: %w", err))
		return
	}
	g.Name = name
	h.markGroupsChanged()
	h.rebuildFlatItems()
	h.logAction("rename group", name, true)
}

// ungroupAtCursor dissolves the group under the cursor: every member returns to
// its origin tree, and nothing is deleted.
func (h *Home) ungroupAtCursor() {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return
	}
	item := h.flatItems[h.cursor]
	if !item.IsGroupHeader {
		return
	}
	if err := h.storage.DeleteGroup(item.GroupID); err != nil {
		h.setError("group_ungroup_failed", fmt.Errorf("ungroup failed: %w", err))
		return
	}
	for _, s := range h.groupMembers(item.GroupID) {
		s.SetGroupID("")
	}
	h.dropGroup(item.GroupID)
	h.markGroupsChanged()
	h.rebuildFlatItems()
	h.clampCursor()
	h.syncViewport()
	h.logAction("ungroup", item.GroupLabel, true)
	h.setInfo(fmt.Sprintf("Ungrouped %s", item.GroupLabel))
}

// dropGroup forgets a group in memory, along with every fold and snooze row
// keyed under it — a group id is never reused, so they would only pile up.
func (h *Home) dropGroup(id string) {
	kept := h.sessionGroups[:0]
	for _, g := range h.sessionGroups {
		if g.ID != id {
			kept = append(kept, g)
		}
	}
	h.sessionGroups = kept
	prefix := GroupExpandKey(id)
	for key := range h.repoExpanded {
		if key == prefix || strings.HasPrefix(key, prefix+"|") {
			h.clearCollapse(key)
		}
	}
	for key := range h.groupSnooze {
		if key == prefix || strings.HasPrefix(key, prefix+"|") {
			h.clearGroupSnooze(key)
		}
	}
}

// retireLeadIfLeaving freezes a group's label when its lead leaves it (moved
// elsewhere or deleted), so the group doesn't rename itself after whichever
// member survives.
func (h *Home) retireLeadIfLeaving(s *session.Session, groupID string) {
	g := h.groupByID(groupID)
	if g == nil || g.LeadSessionID != s.ID {
		return
	}
	label := h.groupLabel(g)
	if err := h.storage.RetireGroupLead(g.ID, label); err != nil {
		return
	}
	if g.Name == "" {
		g.Name = label
	}
	g.LeadSessionID = ""
	h.markGroupsChanged()
}

// collectEmptyGroups deletes groups no session belongs to any more. Groups a
// pending (undoable) delete still references are kept, so `u` restores a
// session into a group that still exists.
func (h *Home) collectEmptyGroups() {
	keep := make(map[string]bool)
	for _, pd := range h.pendingDeletes {
		if pd.Row != nil && pd.Row.GroupID != "" {
			keep[pd.Row.GroupID] = true
		}
	}
	gone, err := h.storage.DeleteEmptyGroups(time.Now().Add(-emptyGroupGrace), keep)
	if err != nil || len(gone) == 0 {
		return
	}
	for _, id := range gone {
		h.dropGroup(id)
	}
	h.markGroupsChanged()
}

// rebuildAndFollow rebuilds the sidebar and puts the cursor back on a session
// whose row just moved sections.
func (h *Home) rebuildAndFollow(sessionID string) {
	h.rebuildFlatItems()
	if idx := (contextMenuTarget{sessionID: sessionID}).find(h.flatItems); idx >= 0 {
		h.cursor = idx
	}
	h.syncViewport()
}

// sessionsInScope returns the sessions in checkout repo that render in scope —
// the session group scope, or the top-level tree when scope is "". Header
// deletes act on exactly these: the rows the header shows.
func (h *Home) sessionsInScope(repo, scope string) []*session.Session {
	var out []*session.Session
	for _, s := range h.sessions {
		if session.GetRepoRoot(s.ProjectPath) == repo && h.effectiveGroup(s) == scope {
			out = append(out, s)
		}
	}
	return out
}

// countSessionsOutsideScope counts the sessions in checkout repo that render
// somewhere other than scope. Non-zero means the directory is still in use by
// work a scoped delete doesn't reach, so it must not be removed.
func (h *Home) countSessionsOutsideScope(repo, scope string) int {
	n := 0
	for _, s := range h.sessions {
		if session.GetRepoRoot(s.ProjectPath) == repo && h.effectiveGroup(s) != scope {
			n++
		}
	}
	return n
}

// checkoutsInScope is checkoutsForOriginIn narrowed to the checkouts an origin
// header in scope actually shows: inside a group, those of its sessions and
// phantoms; at the top level, those of ungrouped sessions and phantoms plus
// pins — minus a pin whose sessions are all grouped, which BuildFlatItems
// doesn't render there either.
func (h *Home) checkoutsInScope(m map[string]*git.RepoInfo, origin, scope string) []string {
	if scope == "" {
		grouped := make(map[string]bool)
		ungrouped := make(map[string]bool)
		for _, s := range h.sessions {
			repo := session.GetRepoRoot(s.ProjectPath)
			if h.effectiveGroup(s) != "" {
				grouped[repo] = true
			} else {
				ungrouped[repo] = true
			}
		}
		for _, pw := range h.pendingWorkspaces {
			if pw != nil && h.groupByID(pw.GroupID) == nil {
				ungrouped[pw.RepoPath] = true
			}
		}
		var out []string
		for _, repo := range h.checkoutsForOriginIn(m, origin) {
			if grouped[repo] && !ungrouped[repo] {
				continue
			}
			out = append(out, repo)
		}
		return out
	}
	seen := make(map[string]bool)
	var out []string
	add := func(repo string) {
		if repo == "" || seen[repo] || (origin != "" && originOfIn(m, repo) != origin) {
			return
		}
		seen[repo] = true
		out = append(out, repo)
	}
	for _, s := range h.sessions {
		if h.effectiveGroup(s) == scope {
			add(session.GetRepoRoot(s.ProjectPath))
		}
	}
	for _, pw := range h.pendingWorkspaces {
		if pw != nil && pw.GroupID == scope {
			add(pw.RepoPath)
		}
	}
	return out
}

// scopeNoun names a delete's scope in dialog copy.
func scopeNoun(scope string) string {
	if scope == "" {
		return "list"
	}
	return "group"
}

// keptOutsideNote explains why a worktree survives a scoped delete.
func keptOutsideNote(outside int, scope string) string {
	where := "in a session group"
	if scope != "" {
		where = "outside this group"
	}
	return fmt.Sprintf("Directory kept — %d session(s) %s still use it", outside, where)
}

// confirmDeleteGroup handles `d` on a group header: "this ticket is done".
// Every member session is deleted, and each worktree in the group is removed
// from disk (following origin_delete_removes_worktrees, like an origin forget)
// unless a session outside the group still lives in it. Main clones are never
// removed. Checkbox-gated, like the origin forget it mirrors, because it wipes
// several checkouts at once; every session still goes through deferDelete, so
// `u` brings them back one at a time.
func (h *Home) confirmDeleteGroup(item SidebarItem) tea.Cmd {
	gitSnap := h.gitInfo()
	scope := item.GroupID
	checkouts := h.checkoutsInScope(gitSnap, "", scope)
	if len(checkouts) == 0 {
		return nil
	}
	removeWorktrees := h.cfg.GetOriginDeleteRemovesWorktrees()

	var targets []originDeleteTarget
	var destroyDirs []string
	dirty, kept := 0, 0
	for _, repo := range checkouts {
		isWorktree := repoIsWorktreeIn(gitSnap, repo) || h.failedWorktreeRemovals[repo]
		destroy := removeWorktrees && isWorktree
		if destroy && h.countSessionsOutsideScope(repo, scope) > 0 {
			destroy = false
			kept++
		}
		targets = append(targets, originDeleteTarget{repoPath: repo, destroy: destroy, scope: scope})
		if destroy {
			destroyDirs = append(destroyDirs, repo)
			if info := gitSnap[repo]; info != nil && info.IsDirty {
				dirty++
			}
		}
	}

	members := h.groupMembers(scope)
	running := 0
	for _, s := range members {
		switch s.GetStatus() {
		case session.StatusRunning, session.StatusWaiting, session.StatusStarting:
			running++
		}
	}

	details := []string{fmt.Sprintf("Deletes %d session(s) in %d checkout(s)", len(members), len(checkouts))}
	if len(destroyDirs) > 0 {
		details = append(details, fmt.Sprintf("Removes %d worktree director(ies) from disk", len(destroyDirs)))
	} else {
		details = append(details, "No directories removed")
	}
	if kept > 0 {
		details = append(details, fmt.Sprintf("Keeps %d worktree(s) used outside it", kept))
	}
	if dirty > 0 {
		details = append(details, fmt.Sprintf("%d worktree(s) have uncommitted changes", dirty))
	}
	if running > 0 {
		details = append(details, fmt.Sprintf("%d session(s) here are still running", running))
	}
	details = append(details, checkoutPathLines(checkouts)...)
	if len(members) > 0 {
		details = append(details, "Press u to undo within 5s")
	}

	h.logAction("delete group", item.GroupLabel, true)
	h.confirmDialog.ShowDanger("Delete group?", item.GroupLabel, details, func() tea.Msg {
		return originDeleteMsg{targets: targets}
	})
	h.confirmDialog.RequireCheckbox("Yes, delete every session in this group")

	if len(destroyDirs) == 0 {
		return nil
	}
	gen := h.nextHolderScanGen()
	h.confirmDialog.StartScan(gen, "Checking for running processes…")
	editor := h.cfg.GetEditor()
	dirs := destroyDirs
	return func() tea.Msg {
		seen := make(map[int]bool)
		var holders []proc.Holder
		for _, dir := range dirs {
			hs, _ := proc.FindHolders(dir, []string{editor})
			for _, hd := range hs {
				if !seen[hd.PID] {
					seen[hd.PID] = true
					holders = append(holders, hd)
				}
			}
		}
		return worktreeHoldersScannedMsg{gen: gen, holders: holders}
	}
}

// groupSyncMsg carries what SQLite says about groups, read by the sync sweep.
// Adoption only ever brings in rows this TUI has never seen, but grouping
// rewrites rows it already holds: a `fleet wt` child, run from inside a
// session, moves its *parent* into a new group. Without this the parent would
// sit at the top level, apart from the child that was grouped with it, until
// fleet restarted.
type groupSyncMsg struct {
	// gen is groupGen as it stood before SQLite was read; a reply whose gen no
	// longer matches raced a local change and is dropped (markGroupsChanged).
	gen        int64
	membership map[string]string // session id -> group id, every stored row
	groups     []*session.Group
}

// sendGroupSync reports SQLite's group state to the Update goroutine. Runs on
// the worker, beside the adopt and removal sends, from rows already loaded.
func (h *Home) sendGroupSync(gen int64, rows []*session.SessionRow) {
	groups, err := h.storage.LoadGroups()
	if err != nil {
		debuglog.Logger.Error("sync sweep: failed to load session groups", "err", err)
		return
	}
	// Same rendezvous hazard as the adopt send; the next sweep re-reads it.
	if h.isAttaching.Load() {
		return
	}
	h.send(groupSyncMsg{gen: gen, membership: session.SessionGroupIDs(rows), groups: groups})
}

// handleGroupSync mirrors SQLite's groups and memberships into memory.
func (h *Home) handleGroupSync(msg groupSyncMsg) (tea.Model, tea.Cmd) {
	if msg.gen != h.groupGen.Load() {
		return h, nil // read before a local change landed; the next sweep is fresh
	}
	changed := !sameGroups(h.sessionGroups, msg.groups)
	if changed {
		h.sessionGroups = msg.groups
	}
	for _, s := range h.sessions {
		stored, ok := msg.membership[s.ID]
		if !ok {
			// Not saved yet (a create still in flight) or removed — neither is
			// this sweep's to decide.
			continue
		}
		if s.GroupID() != stored {
			s.SetGroupID(stored)
			changed = true
		}
	}
	// `fleet remove` can delete a group's last member from another process.
	if n := len(h.sessionGroups); n > 0 {
		h.collectEmptyGroups()
		changed = changed || len(h.sessionGroups) != n
	}
	if !changed {
		return h, nil
	}
	// Arrives on a timer: keep the cursor on its row by identity, even as that
	// row jumps into a group section.
	target := h.targetForCursor()
	h.rebuildFlatItems()
	if idx := target.find(h.flatItems); idx >= 0 {
		h.cursor = idx
	} else {
		h.clampCursor()
	}
	h.syncViewport()
	return h, nil
}

// sameGroups reports whether two group lists say the same thing, in order.
func sameGroups(a, b []*session.Group) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Name != b[i].Name || a[i].LeadSessionID != b[i].LeadSessionID {
			return false
		}
	}
	return true
}
