package ui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// SidebarItem represents a flattened row for cursor navigation.
//
// IsRepoHeader is the umbrella flag for "any non-session row" — it stays true
// for every header kind so existing `if item.IsRepoHeader` cursor-skip
// guards keep working. IsGroupHeader / IsOriginHeader / IsCheckoutHeader pick
// the specific kind.
type SidebarItem struct {
	IsRepoHeader     bool
	IsGroupHeader    bool // a session group's own header (top of its section)
	IsOriginHeader   bool
	IsCheckoutHeader bool
	IsSpacer         bool // visual gap row between origin groups; not selectable
	// Divider, on a spacer, renders a dim rule naming the section below it
	// ("repos", between the session groups and the origin trees).
	Divider string

	OriginKey   string // origin grouping key (github org/repo or local:basename)
	OriginLabel string // human-readable origin label
	RepoPath    string // checkout (repo root) — also set on session/pending

	// GroupID is the session group this row sits inside ("" = the top-level
	// origin trees). Set on every row of a group's section, headers included:
	// the same checkout can appear both inside a group and at the top level, so
	// a header's fold/snooze key and its identity are only unique together
	// with it (see scopedKey).
	GroupID    string
	GroupLabel string // group header: the resolved label
	// Depth is the nesting level of the row's tree: 0 at the top level, 1
	// inside a group's section. Each level indents two columns.
	Depth int

	Expanded      bool
	SessionCount  int                    // group header: sessions. origin header: checkouts (repos+worktrees) in group. checkout header: sessions in checkout.
	StatusCounts  map[session.Status]int // header: per-status breakdown for the group (origin or checkout)
	Session       *session.Session
	IsLast        bool // retained for layout decisions
	Pending       *PendingWorkspace
	RemovalFailed bool // checkout header: worktree whose destroy failed (press d to retry)

	// Snooze is the resolved attention-mute for a session row, stamped here by
	// BuildFlatItems so no downstream caller re-derives the precedence rule.
	Snooze snoozeResult
	// GroupSnooze is a header row's OWN snooze deadline (zero = not snoozed).
	// Only the group that holds the snooze renders a countdown — a checkout
	// under a snoozed origin stays silent for the same reason its sessions do.
	GroupSnooze time.Time
}

// SidebarGroup is a session group as the sidebar draws it. The label is
// resolved by the caller, since it reads git and tracker config; the slice
// order is the render order.
type SidebarGroup struct {
	ID    string
	Label string
}

// RepoGroupInfo holds status counts for a checkout (used by other call sites).
type RepoGroupInfo struct {
	SessionCount int
	StatusCounts map[session.Status]int
}

// OriginOf maps a repo root to its origin key. Callers normally read this from
// a cached RepoInfo; the lookup falls back to "local:<basename>" when unknown.
type OriginOf func(repoRoot string) string

// IsWorktreeOf reports whether a checkout is a git worktree (vs. the main
// clone). Used to sort main repos before their worktrees within an origin
// group. Returns false when the repo info isn't cached yet — the row falls
// back to alphabetical order in that case.
type IsWorktreeOf func(repoRoot string) bool

// IsExpanded resolves the expand state for a header key.
//
// origin keys are stored with the "origin:" prefix in the shared map so they
// don't collide with checkout (repo path) keys. Missing entries default to
// expanded — the clean tree shows everything until the user explicitly folds.
func IsExpanded(expanded map[string]bool, key string) bool {
	v, ok := expanded[key]
	if !ok {
		return true
	}
	return v
}

// originExpandPrefix namespaces origin keys in the shared expand map so they
// don't collide with checkout (repo-path) keys.
const originExpandPrefix = "origin:"

// OriginExpandKey returns the map key used for a given origin.
func OriginExpandKey(originKey string) string { return originExpandPrefix + originKey }

// groupExpandPrefix namespaces session-group keys in the same shared map (and
// in collapsed_groups / snoozed_groups, which use the same keyspace).
const groupExpandPrefix = "group:"

// GroupExpandKey returns the map key for a session group's own header.
func GroupExpandKey(groupID string) string { return groupExpandPrefix + groupID }

// scopedKey namespaces an origin or checkout key under the session group it
// renders in. Outside a group it is the key itself, so every existing collapse
// and snooze row keeps meaning what it meant. Inside one it is
// "group:<id>|<key>": the same checkout can appear in a group and at the top
// level at once, and folding it in one place must not fold the other.
func scopedKey(groupID, key string) string {
	if groupID == "" || key == "" {
		return key
	}
	return groupExpandPrefix + groupID + "|" + key
}

// isCheckoutExpandKey reports whether a fold key belongs to a checkout header
// (scoped or not), as opposed to an origin or a session group.
func isCheckoutExpandKey(key string) bool {
	if rest, ok := strings.CutPrefix(key, groupExpandPrefix); ok {
		i := strings.IndexByte(rest, '|')
		if i < 0 {
			return false // a group's own key
		}
		key = rest[i+1:]
	}
	return !strings.HasPrefix(key, originExpandPrefix)
}

// originFoldKey is the fold/snooze key of this row's origin header.
func (it SidebarItem) originFoldKey() string {
	return scopedKey(it.GroupID, OriginExpandKey(it.OriginKey))
}

// checkoutFoldKey is the fold/snooze key of this row's checkout header.
func (it SidebarItem) checkoutFoldKey() string {
	return scopedKey(it.GroupID, it.RepoPath)
}

// labelForOrigin strips the host prefix from an origin key, leaving just the
// short identifier ("brizzai/fleet" → "fleet", "local:scratch" → "scratch").
func labelForOrigin(originKey string) string {
	if rest, ok := strings.CutPrefix(originKey, "local:"); ok {
		return rest
	}
	if i := strings.LastIndex(originKey, "/"); i != -1 {
		return originKey[i+1:]
	}
	return originKey
}

// sessionGroupOf returns the group a session renders in: its stored group when
// that group exists, else "" (a dangling id — its group was dissolved or not
// yet synced — renders at the top level, which is also how every delete and
// fold decision treats it).
func sessionGroupOf(s *session.Session, known map[string]bool) string {
	if id := s.GroupID(); id != "" && known[id] {
		return id
	}
	return ""
}

// BuildFlatItems flattens sessions into the sidebar: session groups first, in
// the order given, each holding its own origin → checkout → session tree; then,
// under a dim "repos" divider, the ordinary origin → checkout → session trees
// of every ungrouped session.
//
// A grouped session lives only in its group — never in both places — so the
// cursor, the Space rotation and the pills each see exactly one row per
// session. A pinned checkout whose sessions are all grouped is likewise not
// repeated at the top level as an empty header.
//
// originOf maps each repo root to its origin key. isWorktreeOf reports whether
// a checkout is a git worktree (so main clones sort before worktrees inside
// an origin).
func BuildFlatItems(
	sessions []*session.Session,
	pending []*PendingWorkspace,
	groups []SidebarGroup,
	expanded map[string]bool,
	filter string,
	pinnedRepos map[string]bool,
	failedRemovals map[string]bool,
	groupSnooze map[string]time.Time,
	now time.Time,
	originOf OriginOf,
	isWorktreeOf IsWorktreeOf,
) []SidebarItem {
	if now.IsZero() {
		now = time.Now()
	}
	if originOf == nil {
		originOf = func(string) string { return "" }
	}
	if isWorktreeOf == nil {
		isWorktreeOf = func(string) bool { return false }
	}

	known := make(map[string]bool, len(groups))
	for _, g := range groups {
		known[g.ID] = true
	}
	groupSessions := make(map[string][]*session.Session)
	groupPending := make(map[string][]*PendingWorkspace)
	var topSessions []*session.Session
	var topPending []*PendingWorkspace
	groupedRepos := make(map[string]bool)
	topRepos := make(map[string]bool)
	for _, s := range sessions {
		if g := sessionGroupOf(s, known); g != "" {
			groupSessions[g] = append(groupSessions[g], s)
			groupedRepos[session.GetRepoRoot(s.ProjectPath)] = true
		} else {
			topSessions = append(topSessions, s)
			topRepos[session.GetRepoRoot(s.ProjectPath)] = true
		}
	}
	for _, pw := range pending {
		if pw.GroupID != "" && known[pw.GroupID] {
			groupPending[pw.GroupID] = append(groupPending[pw.GroupID], pw)
			groupedRepos[pw.RepoPath] = true
		} else {
			topPending = append(topPending, pw)
			topRepos[pw.RepoPath] = true
		}
	}

	t := flatTree{
		expanded: expanded, filter: strings.ToLower(filter), failedRemovals: failedRemovals,
		groupSnooze: groupSnooze, now: now, originOf: originOf, isWorktreeOf: isWorktreeOf,
	}
	var items []SidebarItem

	for _, g := range groups {
		members := groupSessions[g.ID]
		gPending := groupPending[g.ID]
		groupKey := GroupExpandKey(g.ID)
		groupExpanded := IsExpanded(expanded, groupKey)
		origins, counts, visible := t.origins(members, gPending, nil, g.ID)
		if len(origins) == 0 {
			continue // nothing survives the filter, or the group is empty
		}
		if len(items) > 0 && SidebarDensity != "compact" {
			items = append(items, SidebarItem{IsSpacer: true})
		}
		items = append(items, SidebarItem{
			IsRepoHeader:  true,
			IsGroupHeader: true,
			GroupID:       g.ID,
			GroupLabel:    g.Label,
			Expanded:      groupExpanded,
			SessionCount:  visible,
			StatusCounts:  counts,
			GroupSnooze:   groupSnoozeAt(groupSnooze, groupKey, now),
		})
		if groupExpanded {
			items = t.appendOrigins(items, origins, g.ID, 1)
		}
	}

	// Top-level checkouts: those of ungrouped sessions and phantoms, plus pins
	// — except a pin whose only sessions are grouped, which would otherwise
	// show here as an "(empty)" ghost of the checkout the group already holds.
	var pins []string
	for repo := range pinnedRepos {
		if groupedRepos[repo] && !topRepos[repo] {
			continue
		}
		pins = append(pins, repo)
	}
	origins, _, _ := t.origins(topSessions, topPending, pins, "")
	if len(origins) > 0 {
		if len(items) > 0 {
			// The same breathing row every section gets, then the rule.
			if SidebarDensity != "compact" {
				items = append(items, SidebarItem{IsSpacer: true})
			}
			items = append(items, SidebarItem{IsSpacer: true, Divider: "repos"})
		}
		items = t.appendOrigins(items, origins, "", 0)
	}
	return items
}

// flatTree carries what BuildFlatItems' origin-tree pass needs, so the same
// pass can run once per session group and once for the top level.
type flatTree struct {
	expanded       map[string]bool
	filter         string // lower-cased
	failedRemovals map[string]bool
	groupSnooze    map[string]time.Time
	now            time.Time
	originOf       OriginOf
	isWorktreeOf   IsWorktreeOf
}

// treeBlock is one checkout's rows, filtered and resolved.
type treeBlock struct {
	repo         string
	sessions     []*session.Session
	pending      []*PendingWorkspace
	statusCounts map[session.Status]int
	// snooze[i] pairs with sessions[i] — resolved once here so the row
	// renderer and the jump scan read the same answer.
	snooze []snoozeResult
}

// treeOrigin is one origin's checkouts.
type treeOrigin struct {
	key          string
	blocks       []treeBlock
	statusCounts map[session.Status]int
}

// origins buckets sessions, phantoms and extra (pinned) checkouts into sorted
// origins with their filtered checkout blocks. Origins with nothing left after
// filtering are dropped. Returns the status counts and visible-session count
// summed across everything kept (the group header's pill and count).
func (t flatTree) origins(sessions []*session.Session, pending []*PendingWorkspace, extra []string, groupID string) ([]treeOrigin, map[session.Status]int, int) {
	checkouts := make(map[string]struct{})
	for _, s := range sessions {
		checkouts[session.GetRepoRoot(s.ProjectPath)] = struct{}{}
	}
	for _, pw := range pending {
		checkouts[pw.RepoPath] = struct{}{}
	}
	for _, repo := range extra {
		checkouts[repo] = struct{}{}
	}

	// Bucket checkouts by origin.
	originCheckouts := make(map[string][]string)
	for repo := range checkouts {
		origin := t.originOf(repo)
		if origin == "" {
			origin = "local:" + filepath.Base(repo)
		}
		originCheckouts[origin] = append(originCheckouts[origin], repo)
	}

	// Sessions / pending indexed by checkout for fast lookup.
	sessionsBy := session.GroupByRepo(sessions)
	pendingBy := make(map[string][]*PendingWorkspace)
	for _, pw := range pending {
		pendingBy[pw.RepoPath] = append(pendingBy[pw.RepoPath], pw)
	}

	// Sort origins alphabetically by their visible label.
	originKeys := make([]string, 0, len(originCheckouts))
	for k := range originCheckouts {
		originKeys = append(originKeys, k)
	}
	sort.Slice(originKeys, func(i, j int) bool {
		li, lj := strings.ToLower(labelForOrigin(originKeys[i])), strings.ToLower(labelForOrigin(originKeys[j]))
		if li != lj {
			return li < lj
		}
		return originKeys[i] < originKeys[j]
	})

	total := make(map[session.Status]int)
	visible := 0
	var out []treeOrigin
	for _, origin := range originKeys {
		repos := originCheckouts[origin]
		// Main repos sort before worktrees within an origin; ties break
		// alphabetically by path. Worktrees without resolved git info fall
		// back into the "main" bucket and will re-sort once info loads.
		sort.Slice(repos, func(i, j int) bool {
			wi, wj := t.isWorktreeOf(repos[i]), t.isWorktreeOf(repos[j])
			if wi != wj {
				return !wi // false (main) < true (worktree)
			}
			return repos[i] < repos[j]
		})

		// Filter / build the checkout rows first so we can skip empty origins
		// and compute an accurate session count for the origin header.
		var blocks []treeBlock
		originStatusCounts := make(map[session.Status]int)
		for _, repo := range repos {
			coSessions := sessionsBy[repo]
			coPending := pendingBy[repo]

			if t.filter != "" {
				var filtered []*session.Session
				for _, s := range coSessions {
					if strings.Contains(strings.ToLower(s.Title), t.filter) {
						filtered = append(filtered, s)
					}
				}
				if len(filtered) == 0 && len(coPending) == 0 {
					continue
				}
				coSessions = filtered
			}
			coCounts := make(map[session.Status]int)
			coSnooze := make([]snoozeResult, len(coSessions))
			for i, s := range coSessions {
				visible++
				coSnooze[i] = snoozeState(s, groupID, origin, repo, t.groupSnooze, t.now)
				// A muted session is absent from the attention surfaces, so it
				// contributes to no header's pill. Every counter skips together
				// or the headers disagree.
				if coSnooze[i].Muted {
					continue
				}
				st := s.GetStatus()
				coCounts[st]++
				originStatusCounts[st]++
				total[st]++
			}
			blocks = append(blocks, treeBlock{repo: repo, sessions: coSessions, pending: coPending, statusCounts: coCounts, snooze: coSnooze})
		}

		if len(blocks) == 0 {
			// Skip an origin whose checkouts have no content (also avoids
			// dangling pinned origins after every session is removed).
			continue
		}
		out = append(out, treeOrigin{key: origin, blocks: blocks, statusCounts: originStatusCounts})
	}
	return out, total, visible
}

// appendOrigins emits origin → checkout → session rows for one tree. groupID
// scopes every fold and snooze key (see scopedKey); depth indents the rows.
func (t flatTree) appendOrigins(items []SidebarItem, origins []treeOrigin, groupID string, depth int) []SidebarItem {
	for i, o := range origins {
		// Visual breathing space between origin groups — one blank row
		// before every origin except the first of its tree. Marked IsSpacer
		// so cursor nav skips it and it renders as a blank line. Suppressed in
		// compact density, where groups sit flush, and inside a session group,
		// whose section is already set apart by its own header.
		if i > 0 && depth == 0 && SidebarDensity != "compact" {
			items = append(items, SidebarItem{IsSpacer: true})
		}

		originKey := scopedKey(groupID, OriginExpandKey(o.key))
		originExpanded := IsExpanded(t.expanded, originKey)
		items = append(items, SidebarItem{
			IsRepoHeader:   true,
			IsOriginHeader: true,
			OriginKey:      o.key,
			OriginLabel:    labelForOrigin(o.key),
			GroupID:        groupID,
			Depth:          depth,
			Expanded:       originExpanded,
			SessionCount:   len(o.blocks),
			StatusCounts:   o.statusCounts,
			GroupSnooze:    groupSnoozeAt(t.groupSnooze, originKey, t.now),
		})

		if !originExpanded {
			continue
		}

		for _, blk := range o.blocks {
			checkoutKey := scopedKey(groupID, blk.repo)
			checkoutExpanded := IsExpanded(t.expanded, checkoutKey)
			items = append(items, SidebarItem{
				IsRepoHeader:     true,
				IsCheckoutHeader: true,
				OriginKey:        o.key,
				RepoPath:         blk.repo,
				GroupID:          groupID,
				Depth:            depth,
				Expanded:         checkoutExpanded,
				SessionCount:     len(blk.sessions),
				StatusCounts:     blk.statusCounts,
				RemovalFailed:    t.failedRemovals[blk.repo],
				// Only its own snooze — a checkout inside a snoozed origin
				// renders no countdown of its own (the origin owns the clock).
				GroupSnooze: groupSnoozeAt(t.groupSnooze, checkoutKey, t.now),
			})

			if !checkoutExpanded {
				continue
			}

			totalChildren := len(blk.sessions) + len(blk.pending)
			childIdx := 0
			for i, s := range blk.sessions {
				childIdx++
				items = append(items, SidebarItem{
					OriginKey: o.key,
					RepoPath:  blk.repo,
					GroupID:   groupID,
					Depth:     depth,
					Session:   s,
					IsLast:    childIdx == totalChildren,
					Snooze:    blk.snooze[i],
				})
			}
			for _, pw := range blk.pending {
				childIdx++
				items = append(items, SidebarItem{
					OriginKey: o.key,
					RepoPath:  blk.repo,
					GroupID:   groupID,
					Depth:     depth,
					Pending:   pw,
					IsLast:    childIdx == totalChildren,
				})
			}
		}
	}
	return items
}

// CollectGroupInfo gathers status counts for a checkout (used externally).
func CollectGroupInfo(sessions []*session.Session, repoPath string) RepoGroupInfo {
	info := RepoGroupInfo{StatusCounts: make(map[session.Status]int)}
	groups := session.GroupByRepo(sessions)
	for _, s := range groups[repoPath] {
		info.SessionCount++
		info.StatusCounts[s.GetStatus()]++
	}
	return info
}

// sidebarWindow is the slice of rows RenderSidebar actually paints into an
// inner content area `height` rows tall, plus whether it spends a row on each
// scroll indicator. Items[Start:End] are drawn, one row apiece, in order.
//
// It exists so hit-testing a click reads the same window the frame was drawn
// from (see sidebarItemAt in mouse.go) instead of re-deriving it: the two
// indicators each steal a row, so an off-by-one here would land a click on the
// row above or below the one under the pointer — and the `… N more below`
// indicator sits directly on top of the first item the window excludes, which
// is exactly the index a naive `viewOffset + row` would hand back.
type sidebarWindowSpec struct {
	Start, End   int  // items[Start:End] are painted
	Above, Below bool // a scroll indicator occupies the first / last row
}

// sidebarWindow computes the painted window for itemCount rows scrolled to
// viewOffset inside an inner content area `height` rows tall.
func sidebarWindow(itemCount, viewOffset, height int) sidebarWindowSpec {
	visibleHeight := height
	if visibleHeight < 1 {
		visibleHeight = 1
	}

	w := sidebarWindowSpec{Start: viewOffset}
	w.Above = viewOffset > 0
	if w.Above {
		visibleHeight--
	}
	// Below is decided against the budget the upper indicator has already taken,
	// not the full height. Asking first (as this did) reports "nothing below" for
	// a window that is about to lose a row to the `… N more above` line — so with
	// 11 items at offset 1 in 10 rows, item 10 is neither drawn nor announced and
	// the list looks like it ends one short. Harmless while viewOffset came only
	// from syncViewport, which never parked there; the wheel can (scrollSidebar).
	w.Below = (viewOffset + visibleHeight) < itemCount
	if w.Below {
		visibleHeight--
	}
	if visibleHeight < 1 {
		visibleHeight = 1
	}

	w.End = viewOffset + visibleHeight
	if w.End > itemCount {
		w.End = itemCount
	}
	return w
}

// RenderSidebar renders the clean origin → checkout → session tree.
func RenderSidebar(items []SidebarItem, sessions []*session.Session, gitInfo map[string]*git.RepoInfo, slotBindings map[int]string, cursor, viewOffset, width, height int, sidebarFocused bool) string {
	// Mute the selected-row pill when the sidebar doesn't own the keyboard
	// (e.g. the terminal drawer is focused). Render-thread only.
	selectionDimmed = !sidebarFocused
	if len(items) == 0 {
		return renderEmptyState(width, height)
	}

	// Invert bindings: session ID -> slot number.
	slotBySession := make(map[string]int, len(slotBindings))
	for slot, id := range slotBindings {
		slotBySession[id] = slot
	}

	var b strings.Builder

	// Panel title + border are now drawn by RenderBorderedPanel in the caller.
	// `height` here is the inner content height — no title/underline rows to deduct.
	win := sidebarWindow(len(items), viewOffset, height)
	showAbove, showBelow, visibleEnd := win.Above, win.Below, win.End

	if showAbove {
		b.WriteString(DimStyle.Render(fmt.Sprintf("  … %d more above", viewOffset)))
		b.WriteString("\n")
	}

	for i := viewOffset; i < visibleEnd; i++ {
		item := items[i]
		// Rows inside a session group are one tree level deeper: indent them
		// and take the indent out of the width every renderer budgets against.
		indent := strings.Repeat("  ", item.Depth)
		w := width - len(indent)
		b.WriteString(indent)
		switch {
		case item.IsSpacer && item.Divider != "":
			b.WriteString(renderSectionDivider(item.Divider, w))
		case item.IsSpacer:
			// Blank line between origin groups.
		case item.IsGroupHeader:
			b.WriteString(renderGroupHeader(item, w, i == cursor))
		case item.IsOriginHeader:
			b.WriteString(renderOriginHeader(item, w, i == cursor))
		case item.IsCheckoutHeader:
			b.WriteString(renderCheckoutHeader(item, gitInfo[item.RepoPath], w, i == cursor))
		case item.Pending != nil:
			b.WriteString(renderPendingItem(item.Pending, w, i == cursor))
		default:
			slot := -1
			if item.Session != nil {
				if n, ok := slotBySession[item.Session.ID]; ok {
					slot = n
				}
			}
			b.WriteString(renderSessionItem(item, w, i == cursor, slot))
		}
		if i < visibleEnd-1 {
			b.WriteString("\n")
		}
	}

	if showBelow {
		below := len(items) - visibleEnd
		b.WriteString("\n")
		b.WriteString(DimStyle.Render(fmt.Sprintf("  … %d more below", below)))
	}

	return b.String()
}

func renderEmptyState(width, height int) string {
	var b strings.Builder

	if height < 8 {
		b.WriteString(DimStyle.Render("  No sessions — 'a' to create"))
		return b.String()
	}

	icon := lipgloss.NewStyle().Foreground(ColorAccent).Render("⬡")
	title := lipgloss.NewStyle().Bold(true).Foreground(ColorText).Render("No Sessions Yet")
	hint1 := DimStyle.Render("Press 'a' to create one")
	hint2 := DimStyle.Render("Press '?' for help")

	center := func(s string) string {
		w := lipgloss.Width(s)
		pad := (width - w) / 2
		if pad < 0 {
			pad = 0
		}
		return strings.Repeat(" ", pad) + s
	}

	b.WriteString("\n")
	b.WriteString(center(icon) + "\n")
	b.WriteString(center(title) + "\n")
	b.WriteString("\n")
	b.WriteString(center(hint1) + "\n")
	b.WriteString(center(hint2))
	return b.String()
}

// renderStatusSummary builds a compact "N● N◐ N✕ N●" pill from per-status
// counts. Order: errors first (most urgent), then waiting, running, finished.
// Idle is omitted (it's the dominant state — including it would add noise).
// Counts of 1 render as the glyph alone; counts > 1 prefix the number.
// Returns "" if no non-idle sessions are present.
func renderStatusSummary(counts map[session.Status]int) string {
	return renderStatusSummaryOpts(counts, false)
}

// renderStatusSummaryOpts is the configurable form. When alwaysShowCount is
// true, even single-status counts render with the number prefix ("1 ●"
// instead of "●") — used by the global Sessions header where the fleet-wide
// count is the point.
func renderStatusSummaryOpts(counts map[session.Status]int, alwaysShowCount bool) string {
	if len(counts) == 0 {
		return ""
	}
	type chip struct {
		count int
		style lipgloss.Style
		glyph string
	}
	chips := []chip{
		{counts[session.StatusError], StatusErrorStyle, "✕"},
		{counts[session.StatusWaiting], StatusWaitingStyle, "◐"},
		{counts[session.StatusRunning] + counts[session.StatusStarting], StatusRunningStyle, "●"},
		{counts[session.StatusFinished], StatusFinishedStyle, "●"},
	}
	var parts []string
	for _, c := range chips {
		if c.count == 0 {
			continue
		}
		text := c.glyph
		if c.count > 1 || alwaysShowCount {
			text = fmt.Sprintf("%d %s", c.count, c.glyph)
		}
		parts = append(parts, c.style.Render(text))
	}
	if len(parts) == 0 {
		return ""
	}
	sep := DimStyle.Render(" · ")
	return strings.Join(parts, sep)
}

// renderOriginHeader → "▾ originLabel  N  2● 1◐"
// The blank row inserted before each non-first origin (see BuildFlatItems)
// carries the section break on its own; no trailing rule needed.
func renderOriginHeader(item SidebarItem, width int, selected bool) string {
	chevron := chevronGlyph(item.Expanded)
	countStr := ""
	if ShowHeaderCounts {
		countStr = fmt.Sprintf("%d", item.SessionCount)
	}
	summary := ""
	if ShowStatusPills {
		summary = renderStatusSummary(item.StatusCounts)
	}
	// A snoozed group replaces its pill with the countdown: every child is
	// muted, so the pill is empty anyway, and the clock is the useful signal.
	snoozeSuffix := renderGroupSnooze(item.GroupSnooze)
	if snoozeSuffix != "" {
		summary = ""
	}

	if selected {
		icon := SelectionMarker(true).Render(chevron)
		name := selTitle().Render(" " + item.OriginLabel + " ")
		out := fmt.Sprintf("%s %s", icon, name)
		if countStr != "" {
			out += " " + selStatus().Render(countStr)
		}
		if summary != "" {
			out += "  " + summary
		}
		return out + snoozeSuffix
	}
	icon := DimStyle.Render(chevron)
	name := RepoHeaderStyle.Render(item.OriginLabel)
	if !item.GroupSnooze.IsZero() {
		name = DimStyle.Render(item.OriginLabel)
	}
	out := fmt.Sprintf("%s %s", icon, name)
	if countStr != "" {
		out += " " + DimStyle.Render(countStr)
	}
	if summary != "" {
		out += "  " + summary
	}
	return out + snoozeSuffix
}

// renderGroupHeader → "▾ BRZ-12 · Fix the login flow  3  2● 1◐"
//
// Deliberately the origin header's shape with no glyph of its own: every
// candidate marker (◈ ⬡ ⊞) is either East-Asian-Ambiguous or missing from
// Menlo. Position carries it instead — groups sit above the "repos" divider,
// and their children are indented one level. The count is sessions, not
// checkouts: a group is a unit of work, and its size is how many agents are on
// it.
func renderGroupHeader(item SidebarItem, width int, selected bool) string {
	chevron := chevronGlyph(item.Expanded)
	countStr := ""
	if ShowHeaderCounts {
		countStr = fmt.Sprintf("%d", item.SessionCount)
	}
	summary := ""
	if ShowStatusPills {
		summary = renderStatusSummary(item.StatusCounts)
	}
	snoozeSuffix := renderGroupSnooze(item.GroupSnooze)
	if snoozeSuffix != "" {
		summary = ""
	}
	// A group label is a session title, and titles run long; the origin label
	// it sits beside is a repo name, which doesn't. Budget the chevron, the
	// selection padding and the count, and let the pills overflow as the other
	// headers' do.
	maxLabel := width - 6 - len(countStr)
	if maxLabel < 10 {
		maxLabel = 10
	}
	label := ansi.Truncate(item.GroupLabel, maxLabel, "…")

	if selected {
		icon := SelectionMarker(true).Render(chevron)
		out := fmt.Sprintf("%s %s", icon, selTitle().Render(" "+label+" "))
		if countStr != "" {
			out += " " + selStatus().Render(countStr)
		}
		if summary != "" {
			out += "  " + summary
		}
		return out + snoozeSuffix
	}
	name := RepoHeaderStyle.Render(label)
	if !item.GroupSnooze.IsZero() {
		name = DimStyle.Render(label)
	}
	out := fmt.Sprintf("%s %s", DimStyle.Render(chevron), name)
	if countStr != "" {
		out += " " + DimStyle.Render(countStr)
	}
	if summary != "" {
		out += "  " + summary
	}
	return out + snoozeSuffix
}

// renderSectionDivider → "── repos ─────────" — the dim rule between the
// session groups and the origin trees. Shown only when a group exists, so a
// fleet that never groups anything renders exactly as before.
func renderSectionDivider(label string, width int) string {
	head := "── " + label + " "
	fill := width - 1 - ansi.StringWidth(head)
	if fill < 2 {
		fill = 2
	}
	return DimStyle.Render(head + strings.Repeat("─", fill))
}

// renderGroupSnooze renders a header's snooze countdown, or "" when the group
// isn't snoozed. Shared by both header kinds so they can't drift.
func renderGroupSnooze(until time.Time) string {
	if until.IsZero() {
		return ""
	}
	return "  " + DimStyle.Render(SnoozeGlyph+" "+formatSnoozeRemaining(until, time.Now()))
}

// renderCheckoutHeader → "   ⎇ branch * #PR"  for a git checkout,
// or "   <folder>" (no glyph, dim) for a non-git folder.
func renderCheckoutHeader(item SidebarItem, repoInfo *git.RepoInfo, width int, selected bool) string {
	// Non-git fallback: no repoInfo yet, or git returned no branch (not a
	// git repo). Show the folder name without the ⎇ glyph so we don't
	// imply a branch that isn't there.
	if repoInfo == nil || repoInfo.Branch == "" {
		return renderCheckoutHeaderNonGit(item, selected)
	}

	branch := repoInfo.Branch
	if idx := strings.LastIndex(branch, "/"); idx != -1 {
		branch = branch[idx+1:]
	}
	if len(branch) > 22 {
		branch = branch[:19] + "…"
	}
	label := branch

	dirty := ""
	if ShowDirtyIndicator && repoInfo.IsDirty {
		dirty = " *"
	}

	chevron := chevronGlyph(item.Expanded)

	// Worktree distinction: italicize the branch label instead of a `wt·`
	// prefix. Zero extra width; eye picks out worktrees from main clones at
	// the same indent without a noisy left column of repeated chips.
	branchFG := BranchStyle
	if repoInfo.IsWorktreeRepo {
		branchFG = branchFG.Italic(true)
	}
	if !item.GroupSnooze.IsZero() {
		// Drop the branch colour but keep the worktree italic — a snoozed
		// worktree is still a worktree.
		branchFG = branchFG.Foreground(ColorTextDim)
	}

	prBadge := ""
	if ShowPRBadges && repoInfo.PR != nil {
		prBadge = " " + renderPRBadge(repoInfo.PR, selected)
	}

	summary := ""
	if ShowStatusPills {
		summary = renderStatusSummary(item.StatusCounts)
	}
	summarySuffix := ""
	if summary != "" {
		summarySuffix = "  " + summary
	}
	// A snoozed checkout shows its countdown instead of a pill (every child is
	// muted, so the pill is empty anyway).
	if s := renderGroupSnooze(item.GroupSnooze); s != "" {
		summarySuffix = s
	}
	// Persistent marker for a worktree whose destroy failed (part B). Appended
	// to summarySuffix so it shows on both the selected and unselected rows.
	// Deliberately after the snooze suffix: a failed removal is actionable and
	// must never be hidden by a snooze.
	if item.RemovalFailed {
		summarySuffix += "  " + ErrorStyle.Render("✕ removal failed — d to retry")
	}

	if selected {
		icon := SelectionMarker(true).Render(chevron)
		// Selection bg is one contiguous span over title + dirty + PR badge,
		// so the highlighted row reads as a single pill instead of two boxes.
		inner := " " + label + dirty
		if ShowPRBadges {
			if prText := prBadgeText(repoInfo.PR); prText != "" {
				inner += " " + prText
			}
		}
		inner += " "
		return fmt.Sprintf("  %s %s", icon, selTitle().Italic(repoInfo.IsWorktreeRepo).Render(inner)) + summarySuffix
	}
	icon := DimStyle.Render(chevron)
	branchStyled := branchFG.Render(label)
	dirtyStyled := ""
	if dirty != "" {
		dirtyStyled = DirtyStyle.Render(dirty)
	}
	return fmt.Sprintf("  %s %s%s%s", icon, branchStyled, dirtyStyled, prBadge) + summarySuffix
}

// renderCheckoutHeaderNonGit renders a checkout row for a folder that isn't
// a git repo — just the folder name in dim, no branch glyph or PR badge.
func renderCheckoutHeaderNonGit(item SidebarItem, selected bool) string {
	name := filepath.Base(item.RepoPath)
	chevron := chevronGlyph(item.Expanded)
	failMark := ""
	if item.RemovalFailed {
		failMark = "  " + ErrorStyle.Render("✕ removal failed — d to retry")
	}
	if selected {
		icon := SelectionMarker(true).Render(chevron)
		nameStyled := selTitle().Render(" " + name + " ")
		return fmt.Sprintf("  %s %s", icon, nameStyled) + failMark
	}
	icon := DimStyle.Render(chevron)
	nameStyled := DimStyle.Render(name)
	return fmt.Sprintf("  %s %s", icon, nameStyled) + failMark
}

// renderSessionItem → "  │ <status> <agent> title [slot] <snooze>"  (under a checkout)
func renderSessionItem(item SidebarItem, width int, selected bool, slot int) string {
	s := item.Session
	status := s.GetStatus()
	symbolRaw := StatusSymbolRaw(status)
	title := s.Title

	// Snooze suffix. Only a session's OWN snooze renders a countdown — under a
	// snoozed group the header owns the clock, so N children don't repeat the
	// same number N times; they carry a bare marker instead.
	snoozeRaw := ""
	if item.Snooze.Muted {
		snoozeRaw = "  " + SnoozeGlyph
		if item.Snooze.OwnTimer {
			snoozeRaw += " " + formatSnoozeRemaining(item.Snooze.Until, time.Now())
		}
	}

	// Agent glyph (✻/◇) is optional — when shown it occupies a glyph + a space.
	glyphRaw := ""
	if ShowAgentGlyphs {
		glyphRaw = agentGlyph(s.Agent)
	}

	slotRaw := ""
	if ShowSlotBadges && slot >= 0 && slot <= 9 {
		slotRaw = fmt.Sprintf(" [%d]", slot)
	}

	// Reserve: leading indent + status symbol + selection padding + slot, plus
	// the agent glyph and its trailing space when it's shown (2 cells).
	reserve := 13
	if glyphRaw == "" {
		reserve = 11
	}
	// The snooze suffix comes out of the title's budget like the slot badge,
	// rather than bumping `reserve` — it's present on only some rows. Measured
	// with ansi.StringWidth because the marker is a multi-byte rune (len() would
	// over-reserve by 2 and truncate snoozed titles early).
	//
	// Charged on unselected rows only: the selected branch never renders
	// snoozeRaw (it draws its own affordance note *outside* the selection pill,
	// which is unbudgeted), so docking it there shortened the cursor row's title
	// for a suffix it doesn't draw — the title visibly grew when you moved off.
	snoozeCost := 0
	if !selected {
		snoozeCost = ansi.StringWidth(snoozeRaw)
	}
	maxTitleLen := width - reserve - len(slotRaw) - snoozeCost
	if maxTitleLen < 10 {
		maxTitleLen = 10
	}
	// ansi.Truncate is cell-width-aware and rune/ANSI-safe — it won't split a
	// multibyte rune the way byte slicing would, and only appends "…" when it
	// actually truncates.
	title = ansi.Truncate(title, maxTitleLen, "…")

	// glyphSel is "<glyph> " (with trailing space) when shown, else "".
	glyphSel := ""
	if glyphRaw != "" {
		glyphSel = glyphRaw + " "
	}

	// Selection: the inverted-background title carries the "you are here"
	// signal on its own — no leading ▶ arrow (it collided with the chevron
	// glyph used for collapsed headers). Bg fill spans symbol + agent glyph +
	// title + slot in one continuous render so the row reads as a single pill.
	if selected {
		row := " " + symbolRaw + " " + glyphSel + title + slotRaw + " "
		rendered := fmt.Sprintf("   %s", selTitle().Render(row))
		// On the focused row, spell out the affordance. Exactly one note fits
		// before the row overflows, so these are mutually exclusive: suspended
		// wins, because it's the one that makes Enter mean something unusual.
		switch {
		case status == session.StatusSuspended:
			// Unselected rows rely on the dim dot + dimmed title (the row is
			// width-truncated, so no room for a suffix).
			rendered += "  " + DimStyle.Render("suspended · enter to resume")
		case item.Snooze.Muted && item.Snooze.OwnTimer:
			rendered += "  " + DimStyle.Render(fmt.Sprintf("%s snoozed %s · z to wake",
				SnoozeGlyph, formatSnoozeRemaining(item.Snooze.Until, time.Now())))
		case item.Snooze.Muted:
			rendered += "  " + DimStyle.Render(SnoozeGlyph+" sleeping · group snoozed")
		}
		return rendered
	}

	styledSymbol := StatusSymbol(status)
	styledTitle := TitleStyleForStatus(status).Render(title)
	if item.Snooze.Muted {
		// A muted row reads dim whatever its real status is — that's the whole
		// signal. The status dot keeps its shape (the session is still waiting
		// or running; we're just not nagging), it only loses its colour.
		styledSymbol = DimStyle.Render(symbolRaw)
		styledTitle = DimStyle.Render(title)
	}
	styledSlot := ""
	if slotRaw != "" {
		styledSlot = SlotBadgeDimStyle.Render(slotRaw)
	}
	styledSnooze := ""
	if snoozeRaw != "" {
		styledSnooze = DimStyle.Render(snoozeRaw)
	}
	if glyphRaw != "" {
		styledGlyph := AgentGlyphStyle.Render(glyphRaw)
		return fmt.Sprintf("    %s %s %s%s%s", styledSymbol, styledGlyph, styledTitle, styledSlot, styledSnooze)
	}
	return fmt.Sprintf("    %s %s%s%s", styledSymbol, styledTitle, styledSlot, styledSnooze)
}

// Agent glyphs mark which coding agent a session runs — a quiet, dim,
// monochrome sigil that sits between the status dot and the title. Identity is
// carried by shape alone: the status dot keeps the (dynamic) status color, so
// the glyph is always rendered muted via AgentGlyphStyle regardless of status.
const (
	// Both glyphs are width-1 and live in well-covered Unicode blocks so they
	// render cleanly in base monospace fonts (Menlo/SF Mono) and stay aligned:
	// ✻ is Dingbats; ◇ and △ are Geometric Shapes — the same block as the status
	// dots (●○◐). A hexagon (U+2B21) was tried first but falls back to a wider
	// glyph in those fonts, shifting the title. △ is a clean third shape, distinct
	// from the star and the diamond. ⚇ (Misc Symbols, East-Asian-Neutral, in
	// Menlo) reads as Copilot's goggles.
	claudeGlyph   = "✻"
	codexGlyph    = "◇"
	opencodeGlyph = "△"
	copilotGlyph  = "⚇"
)

// agentGlyph returns the sigil for a session's agent. An empty or unrecognized
// agent falls back to Claude (the default), so legacy sessions render ✻.
func agentGlyph(t agent.Type) string {
	switch agent.Parse(string(t)) {
	case agent.Codex:
		return codexGlyph
	case agent.OpenCode:
		return opencodeGlyph
	case agent.Copilot:
		return copilotGlyph
	default:
		return claudeGlyph
	}
}

// renderPendingItem renders a "Creating…" phantom under its checkout.
func renderPendingItem(pw *PendingWorkspace, width int, selected bool) string {
	spinner := spinnerFrames[pw.Frame%len(spinnerFrames)]
	title := "Creating \"" + pw.Name + "\"..."

	maxTitleLen := width - 11
	if maxTitleLen < 10 {
		maxTitleLen = 10
	}
	title = ansi.Truncate(title, maxTitleLen, "…")

	if selected {
		row := " " + spinner + " " + title + " "
		return fmt.Sprintf("   %s", selTitle().Render(row))
	}
	styledSpinner := lipgloss.NewStyle().Foreground(ColorAccent).Render(spinner)
	styledTitle := DimStyle.Render(title)
	return fmt.Sprintf("    %s %s", styledSpinner, styledTitle)
}

// prBadgeText returns the raw badge text ("#N" or "#N ✕↩") without styling;
// callers wrap it in the selection bg or per-state PR color.
func prBadgeText(pr *github.PR) string {
	if pr == nil || pr.State == "CLOSED" {
		return ""
	}
	badge := fmt.Sprintf("#%d", pr.Number)
	if pr.State == "MERGED" {
		return badge + " ⇡"
	}
	if pr.IsDraft {
		// Draft = work-in-progress: dotted-circle prefix marks it, CI failure
		// still surfaces, but review/approval glyphs don't apply to a draft.
		if pr.CIStatus == "FAILURE" {
			return "◌ " + badge + " ✕"
		}
		return "◌ " + badge
	}
	var icons string
	if pr.CIStatus == "FAILURE" {
		icons += "✕"
	}
	if pr.HasConflicts {
		icons += "⚠"
	}
	if pr.ReviewDecision == "CHANGES_REQUESTED" || pr.UnresolvedThreads > 0 {
		icons += "↩"
	}
	if icons == "" && pr.ReviewDecision == "APPROVED" && pr.CIStatus == "SUCCESS" {
		icons = "✓"
	}
	// Nothing to fix: name what the PR is waiting on, so a yellow badge says
	// whether it's CI or a reviewer holding it up.
	if icons == "" {
		if pr.CIStatus == "PENDING" {
			icons += "◔"
		}
		if pr.ReviewDecision == "REVIEW_REQUIRED" {
			icons += "⌕"
		}
	}
	if icons == "" {
		return badge
	}
	return badge + " " + icons
}

// prBadgeVerdict is the state semantic a PR badge carries. prBadgeStyle maps
// it to a colour and the P jump reads it directly, so the two can
// never disagree about which badges are red.
type prBadgeVerdict int

const (
	prBadgePending prBadgeVerdict = iota // open with nothing to fix and not yet mergeable (also: no PR)
	prBadgeHidden                        // closed — the badge renders nothing
	prBadgeMerged
	prBadgeDraft
	prBadgeFail  // red: CI failed, changes requested, unresolved threads, or conflicts
	prBadgeReady // green: approved + CI passed
)

func prVerdict(pr *github.PR) prBadgeVerdict {
	switch {
	case pr == nil:
		return prBadgePending
	case pr.State == "CLOSED":
		return prBadgeHidden
	case pr.State == "MERGED":
		return prBadgeMerged
	case pr.IsDraft:
		return prBadgeDraft
	case pr.CIStatus == "FAILURE" || pr.ReviewDecision == "CHANGES_REQUESTED" || pr.UnresolvedThreads > 0 || pr.HasConflicts:
		return prBadgeFail
	case pr.ReviewDecision == "APPROVED" && pr.CIStatus == "SUCCESS":
		return prBadgeReady
	}
	return prBadgePending
}

// prBadgeStyle picks the foreground color carrying the PR's state semantic.
func prBadgeStyle(pr *github.PR) lipgloss.Style {
	switch prVerdict(pr) {
	case prBadgeMerged:
		return PRMergedStyle
	case prBadgeDraft:
		return PRDraftStyle
	case prBadgeFail:
		return PRFailStyle
	case prBadgeReady:
		return PROpenStyle
	}
	return PRPendingStyle
}

func renderPRBadge(pr *github.PR, selected bool) string {
	text := prBadgeText(pr)
	if text == "" {
		return ""
	}
	if selected {
		return selStatus().Render(text)
	}
	return prBadgeStyle(pr).Render(text)
}

// NextSelectableItem advances the cursor; every row except spacers is
// selectable. Steps repeatedly past spacers in `direction` so j/k feel
// instant across origin-gap rows.
func NextSelectableItem(items []SidebarItem, current, direction int) int {
	next := current + direction
	for next >= 0 && next < len(items) {
		if !items[next].IsSpacer {
			return next
		}
		next += direction
	}
	return current
}

// FirstSelectableItem returns the first non-spacer row index, or 0 if none.
func FirstSelectableItem(items []SidebarItem) int {
	for i, it := range items {
		if !it.IsSpacer {
			return i
		}
	}
	return 0
}

// LastSelectableItem returns the last non-spacer row index, or 0 if none.
func LastSelectableItem(items []SidebarItem) int {
	for i := len(items) - 1; i >= 0; i-- {
		if !items[i].IsSpacer {
			return i
		}
	}
	return 0
}

// NextHeaderItem moves to the nearest header row (origin or checkout) in
// `direction`. From a session row the nearest header above is that session's own
// checkout header, so shift+↑ surfaces the group you're standing in before it
// climbs out of it.
//
// With no header left to reach, it clamps to the edge of the list rather than
// stalling — so the motion doubles as a jump to top/bottom. Headers are never
// spacers and the clamp goes through First/LastSelectableItem, so the cursor
// can't land on a gap row.
//
// `current` is clamped into range before the scan: rebuildFlatItems does not fix
// h.cursor (only syncViewport does), so a shrinking rebuild can leave it past the
// end. Scanning from an out-of-range index would run the loop zero times and fall
// straight through to the edge clamp — sending the cursor to the *opposite* end of
// the list from where NextSelectableItem leaves it in the same state.
func NextHeaderItem(items []SidebarItem, current, direction int) int {
	return nextRowMatching(items, current, direction, func(it SidebarItem) bool {
		return it.IsRepoHeader
	})
}

// NextOriginItem is NextHeaderItem narrowed to origin headers: checkout headers
// are scanned past rather than landed on, so ctrl+shift+↑/↓ moves a whole group
// at a time however many worktrees sit under it. Everything else — the "own
// group first" landing, the edge clamp, the out-of-range guard — is the same
// scan, so the two motions can't disagree about what a stale cursor means.
//
// The edge clamp is kept deliberately, not merely inherited. It bites harder
// here than it does one level down: with a single origin group there is never a
// next origin, so every ctrl+shift+↓ ends in the clamp rather than reaching it
// occasionally. Moving to the bottom of the list is still the wanted answer —
// the motion doubles as go-to-bottom exactly as shift+↓ does, and a key that
// silently did nothing would be worse than one that lands somewhere useful.
//
// "One level up" means the top-level sections: session group headers and the
// origins outside any group. An origin nested inside a group is passed over
// like a checkout, so the motion steps a whole group at a time too.
func NextOriginItem(items []SidebarItem, current, direction int) int {
	return nextRowMatching(items, current, direction, func(it SidebarItem) bool {
		return it.IsGroupHeader || (it.IsOriginHeader && it.Depth == 0)
	})
}

func nextRowMatching(items []SidebarItem, current, direction int, match func(SidebarItem) bool) int {
	if len(items) == 0 {
		return 0
	}
	if current < 0 {
		current = 0
	} else if current >= len(items) {
		current = len(items) - 1
	}
	for i := current + direction; i >= 0 && i < len(items); i += direction {
		if match(items[i]) {
			return i
		}
	}
	if direction > 0 {
		return LastSelectableItem(items)
	}
	return FirstSelectableItem(items)
}

// FirstSessionItem returns the index of the first row whose Session is non-nil,
// or -1 if no real session exists in the list. Used to land the cursor on
// something actionable instead of an origin header on first paint.
func FirstSessionItem(items []SidebarItem) int {
	for i, it := range items {
		if !it.IsRepoHeader && !it.IsSpacer && it.Session != nil {
			return i
		}
	}
	return -1
}

// sameRow reports whether two items are the same sidebar row across rebuilds.
// A header's identity includes the group it renders in: the same checkout can
// sit both inside a session group and at the top level, and those are two
// different rows with independent fold and snooze state.
func sameRow(a, b SidebarItem) bool {
	switch {
	case a.Session != nil || b.Session != nil:
		return a.Session != nil && b.Session != nil && a.Session.ID == b.Session.ID
	case a.Pending != nil || b.Pending != nil:
		return a.Pending != nil && b.Pending != nil && a.Pending.ID == b.Pending.ID
	case a.IsGroupHeader:
		return b.IsGroupHeader && a.GroupID == b.GroupID
	case a.IsOriginHeader:
		return b.IsOriginHeader && a.GroupID == b.GroupID && a.OriginKey == b.OriginKey
	case a.IsCheckoutHeader:
		return b.IsCheckoutHeader && a.GroupID == b.GroupID && a.RepoPath == b.RepoPath
	}
	return false
}
