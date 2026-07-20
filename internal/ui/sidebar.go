package ui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

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
// for origin headers and checkout headers so existing "this is not a session"
// guards keep working. IsOriginHeader / IsCheckoutHeader pick the specific
// render path.
type SidebarItem struct {
	IsRepoHeader     bool
	IsOriginHeader   bool
	IsCheckoutHeader bool
	IsSpacer         bool // visual gap row between origin groups; not selectable

	OriginKey   string // origin grouping key (github org/repo or local:basename)
	OriginLabel string // human-readable origin label
	RepoPath    string // checkout (repo root) — also set on session/pending

	Expanded      bool
	SessionCount  int                    // origin header: checkouts (repos+worktrees) in group. checkout header: sessions in checkout.
	StatusCounts  map[session.Status]int // header: per-status breakdown for the group (origin or checkout)
	Session       *session.Session
	IsLast        bool // retained for layout decisions
	Pending       *PendingWorkspace
	RemovalFailed bool // checkout header: worktree whose destroy failed (press d to retry)
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

// BuildFlatItems flattens sessions into an origin → checkout → session list.
//
// originOf maps each repo root to its origin key. isWorktreeOf reports whether
// a checkout is a git worktree (so main clones sort before worktrees inside
// an origin).
func BuildFlatItems(
	sessions []*session.Session,
	pending []*PendingWorkspace,
	expanded map[string]bool,
	filter string,
	pinnedRepos map[string]bool,
	failedRemovals map[string]bool,
	originOf OriginOf,
	isWorktreeOf IsWorktreeOf,
) []SidebarItem {
	if originOf == nil {
		originOf = func(string) string { return "" }
	}
	if isWorktreeOf == nil {
		isWorktreeOf = func(string) bool { return false }
	}

	// Collect all checkouts (repo roots) we know about — from sessions,
	// pending workspaces, and pinned repos.
	checkouts := make(map[string]struct{})
	for _, s := range sessions {
		checkouts[session.GetRepoRoot(s.ProjectPath)] = struct{}{}
	}
	for _, pw := range pending {
		checkouts[pw.RepoPath] = struct{}{}
	}
	for repo := range pinnedRepos {
		checkouts[repo] = struct{}{}
	}

	// Bucket checkouts by origin.
	originCheckouts := make(map[string][]string)
	for repo := range checkouts {
		origin := originOf(repo)
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

	lowerFilter := strings.ToLower(filter)
	var items []SidebarItem

	for _, origin := range originKeys {
		repos := originCheckouts[origin]
		// Main repos sort before worktrees within an origin; ties break
		// alphabetically by path. Worktrees without resolved git info fall
		// back into the "main" bucket and will re-sort once info loads.
		sort.Slice(repos, func(i, j int) bool {
			wi, wj := isWorktreeOf(repos[i]), isWorktreeOf(repos[j])
			if wi != wj {
				return !wi // false (main) < true (worktree)
			}
			return repos[i] < repos[j]
		})

		// Filter / build the checkout rows first so we can skip empty origins
		// and compute an accurate session count for the origin header.
		type checkoutBlock struct {
			repo         string
			sessions     []*session.Session
			pending      []*PendingWorkspace
			statusCounts map[session.Status]int
		}
		var blocks []checkoutBlock
		originStatusCounts := make(map[session.Status]int)
		for _, repo := range repos {
			coSessions := sessionsBy[repo]
			coPending := pendingBy[repo]

			if lowerFilter != "" {
				var filtered []*session.Session
				for _, s := range coSessions {
					if strings.Contains(strings.ToLower(s.Title), lowerFilter) {
						filtered = append(filtered, s)
					}
				}
				if len(filtered) == 0 && len(coPending) == 0 {
					continue
				}
				coSessions = filtered
			}
			coCounts := make(map[session.Status]int)
			for _, s := range coSessions {
				st := s.GetStatus()
				coCounts[st]++
				originStatusCounts[st]++
			}
			blocks = append(blocks, checkoutBlock{repo: repo, sessions: coSessions, pending: coPending, statusCounts: coCounts})
		}

		if len(blocks) == 0 {
			// Skip an origin whose checkouts have no content (also avoids
			// dangling pinned origins after every session is removed).
			continue
		}

		// Visual breathing space between origin groups — one blank row
		// before every origin except the first. Marked IsSpacer so cursor
		// nav skips it and it renders as a blank line. Suppressed in compact
		// density, where groups sit flush.
		if len(items) > 0 && SidebarDensity != "compact" {
			items = append(items, SidebarItem{IsSpacer: true})
		}

		originExpanded := IsExpanded(expanded, OriginExpandKey(origin))
		items = append(items, SidebarItem{
			IsRepoHeader:   true,
			IsOriginHeader: true,
			OriginKey:      origin,
			OriginLabel:    labelForOrigin(origin),
			Expanded:       originExpanded,
			SessionCount:   len(blocks),
			StatusCounts:   originStatusCounts,
		})

		if !originExpanded {
			continue
		}

		for _, blk := range blocks {
			checkoutExpanded := IsExpanded(expanded, blk.repo)
			items = append(items, SidebarItem{
				IsRepoHeader:     true,
				IsCheckoutHeader: true,
				OriginKey:        origin,
				RepoPath:         blk.repo,
				Expanded:         checkoutExpanded,
				SessionCount:     len(blk.sessions),
				StatusCounts:     blk.statusCounts,
				RemovalFailed:    failedRemovals[blk.repo],
			})

			if !checkoutExpanded {
				continue
			}

			totalChildren := len(blk.sessions) + len(blk.pending)
			childIdx := 0
			for _, s := range blk.sessions {
				childIdx++
				items = append(items, SidebarItem{
					OriginKey: origin,
					RepoPath:  blk.repo,
					Session:   s,
					IsLast:    childIdx == totalChildren,
				})
			}
			for _, pw := range blk.pending {
				childIdx++
				items = append(items, SidebarItem{
					OriginKey: origin,
					RepoPath:  blk.repo,
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
	visibleHeight := height
	if visibleHeight < 1 {
		visibleHeight = 1
	}

	showAbove := viewOffset > 0
	showBelow := (viewOffset + visibleHeight) < len(items)
	if showAbove {
		visibleHeight--
	}
	if showBelow {
		visibleHeight--
	}
	if visibleHeight < 1 {
		visibleHeight = 1
	}

	visibleEnd := viewOffset + visibleHeight
	if visibleEnd > len(items) {
		visibleEnd = len(items)
	}

	if showAbove {
		b.WriteString(DimStyle.Render(fmt.Sprintf("  … %d more above", viewOffset)))
		b.WriteString("\n")
	}

	for i := viewOffset; i < visibleEnd; i++ {
		item := items[i]
		switch {
		case item.IsSpacer:
			// Blank line between origin groups.
		case item.IsOriginHeader:
			b.WriteString(renderOriginHeader(item, width, i == cursor))
		case item.IsCheckoutHeader:
			b.WriteString(renderCheckoutHeader(item, gitInfo[item.RepoPath], width, i == cursor))
		case item.Pending != nil:
			b.WriteString(renderPendingItem(item.Pending, width, i == cursor))
		default:
			slot := -1
			if item.Session != nil {
				if n, ok := slotBySession[item.Session.ID]; ok {
					slot = n
				}
			}
			b.WriteString(renderSessionItem(item.Session, width, i == cursor, slot))
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

	if selected {
		icon := SessionSelectionPrefix.Render(chevron)
		name := selTitle().Render(" " + item.OriginLabel + " ")
		out := fmt.Sprintf("%s %s", icon, name)
		if countStr != "" {
			out += " " + selStatus().Render(countStr)
		}
		if summary != "" {
			out += "  " + summary
		}
		return out
	}
	icon := DimStyle.Render(chevron)
	name := RepoHeaderStyle.Render(item.OriginLabel)
	out := fmt.Sprintf("%s %s", icon, name)
	if countStr != "" {
		out += " " + DimStyle.Render(countStr)
	}
	if summary != "" {
		out += "  " + summary
	}
	return out
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
	// Persistent marker for a worktree whose destroy failed (part B). Appended
	// to summarySuffix so it shows on both the selected and unselected rows.
	if item.RemovalFailed {
		summarySuffix += "  " + ErrorStyle.Render("✕ removal failed — d to retry")
	}

	if selected {
		icon := SessionSelectionPrefix.Render(chevron)
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
		icon := SessionSelectionPrefix.Render(chevron)
		nameStyled := selTitle().Render(" " + name + " ")
		return fmt.Sprintf("  %s %s", icon, nameStyled) + failMark
	}
	icon := DimStyle.Render(chevron)
	nameStyled := DimStyle.Render(name)
	return fmt.Sprintf("  %s %s", icon, nameStyled) + failMark
}

// renderSessionItem → "  │ <status> <agent> title [slot]"  (under a checkout)
func renderSessionItem(s *session.Session, width int, selected bool, slot int) string {
	status := s.GetStatus()
	symbolRaw := StatusSymbolRaw(status)
	title := s.Title

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
	maxTitleLen := width - reserve - len(slotRaw)
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
		// On the focused suspended row, spell out the resume affordance — this is
		// exactly when Enter is actionable. Unselected rows rely on the dim dot
		// + dimmed title (the row is width-truncated, so no room for a suffix).
		if status == session.StatusSuspended {
			rendered += "  " + DimStyle.Render("suspended · enter to resume")
		}
		return rendered
	}

	styledSymbol := StatusSymbol(status)
	styledTitle := TitleStyleForStatus(status).Render(title)
	styledSlot := ""
	if slotRaw != "" {
		styledSlot = SlotBadgeDimStyle.Render(slotRaw)
	}
	if glyphRaw != "" {
		styledGlyph := AgentGlyphStyle.Render(glyphRaw)
		return fmt.Sprintf("    %s %s %s%s", styledSymbol, styledGlyph, styledTitle, styledSlot)
	}
	return fmt.Sprintf("    %s %s%s", styledSymbol, styledTitle, styledSlot)
}

// Agent glyphs mark which coding agent a session runs — a quiet, dim,
// monochrome sigil that sits between the status dot and the title. Identity is
// carried by shape alone: the status dot keeps the (dynamic) status color, so
// the glyph is always rendered muted via AgentGlyphStyle regardless of status.
const (
	// All glyphs are width-1 and live in well-covered Unicode blocks so they
	// render cleanly in base monospace fonts (Menlo/SF Mono) and stay aligned:
	// ✻ and ✦ are Dingbats; ◇ and △ are Geometric Shapes — the same block as the
	// status dots (●○◐). A hexagon (U+2B21) was tried first but falls back to a
	// wider glyph in those fonts, shifting the title. △ is a clean third shape,
	// distinct from the star and the diamond; ✦ (four-pointed star) is a clean
	// fourth, distinct from ✻'s six-pointed asterisk.
	claudeGlyph   = "✻"
	codexGlyph    = "◇"
	opencodeGlyph = "△"
	cursorGlyph   = "✦"
)

// agentGlyph returns the sigil for a session's agent. An empty or unrecognized
// agent falls back to Claude (the default), so legacy sessions render ✻.
func agentGlyph(t agent.Type) string {
	switch agent.Parse(string(t)) {
	case agent.Codex:
		return codexGlyph
	case agent.OpenCode:
		return opencodeGlyph
	case agent.Cursor:
		return cursorGlyph
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
	if icons == "" {
		return badge
	}
	return badge + " " + icons
}

// prBadgeStyle picks the foreground color carrying the PR's state semantic.
func prBadgeStyle(pr *github.PR) lipgloss.Style {
	if pr == nil {
		return PRPendingStyle
	}
	if pr.State == "MERGED" {
		return PRMergedStyle
	}
	if pr.IsDraft {
		return PRDraftStyle
	}
	ciFail := pr.CIStatus == "FAILURE"
	changesReq := pr.ReviewDecision == "CHANGES_REQUESTED"
	hasThreads := pr.UnresolvedThreads > 0
	hasConflicts := pr.HasConflicts
	if ciFail || changesReq || hasThreads || hasConflicts {
		return PRFailStyle
	}
	if pr.ReviewDecision == "APPROVED" && pr.CIStatus == "SUCCESS" {
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
	if len(items) == 0 {
		return 0
	}
	if current < 0 {
		current = 0
	} else if current >= len(items) {
		current = len(items) - 1
	}
	for i := current + direction; i >= 0 && i < len(items); i += direction {
		if items[i].IsRepoHeader {
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
