package ui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/lipgloss"
)

// Glyphs used across the clean tree.
const (
	branchIcon = ""   // checkout branch icon
	guideGlyph = "│ " // left guide line down each checkout
)

// SidebarItem represents a flattened row for cursor navigation.
//
// IsRepoHeader is the umbrella flag for "any non-session row" — it stays true
// for origin headers, checkout headers, and idle-fold placeholders so existing
// "this is not a session" guards keep working. IsOriginHeader / IsCheckoutHeader
// / IsIdleFold pick the specific render path.
type SidebarItem struct {
	IsRepoHeader     bool
	IsOriginHeader   bool
	IsCheckoutHeader bool
	IsIdleFold       bool

	OriginKey   string // origin grouping key (github org/repo or local:basename)
	OriginLabel string // human-readable origin label
	RepoPath    string // checkout (repo root) — also set on session/pending/idle-fold

	Expanded     bool
	SessionCount int // header: total sessions in group
	IdleCount    int // IsIdleFold: number of folded idle sessions
	Session      *session.Session
	IsLast       bool // retained for layout decisions
	Pending      *PendingWorkspace
}

// RepoGroupInfo holds status counts for a checkout (used by other call sites).
type RepoGroupInfo struct {
	SessionCount int
	StatusCounts map[session.Status]int
}

// OriginOf maps a repo root to its origin key. Callers normally read this from
// a cached RepoInfo; the lookup falls back to "local:<basename>" when unknown.
type OriginOf func(repoRoot string) string

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

// OriginExpandKey returns the map key used for a given origin.
func OriginExpandKey(originKey string) string { return "origin:" + originKey }

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
// originOf maps each repo root to its origin key. idleFolded[checkoutPath]
// collapses that checkout's idle sessions into a single "+ N idle" row.
func BuildFlatItems(
	sessions []*session.Session,
	pending []*PendingWorkspace,
	expanded map[string]bool,
	filter string,
	pinnedRepos map[string]bool,
	originOf OriginOf,
	idleFolded map[string]bool,
) []SidebarItem {
	if originOf == nil {
		originOf = func(string) string { return "" }
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
		return strings.ToLower(labelForOrigin(originKeys[i])) < strings.ToLower(labelForOrigin(originKeys[j]))
	})

	lowerFilter := strings.ToLower(filter)
	var items []SidebarItem

	for _, origin := range originKeys {
		repos := originCheckouts[origin]
		sort.Strings(repos)

		// Filter / build the checkout rows first so we can skip empty origins
		// and compute an accurate session count for the origin header.
		type checkoutBlock struct {
			repo     string
			sessions []*session.Session
			pending  []*PendingWorkspace
		}
		var blocks []checkoutBlock
		originSessionCount := 0
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
			originSessionCount += len(coSessions)
			blocks = append(blocks, checkoutBlock{repo: repo, sessions: coSessions, pending: coPending})
		}

		if len(blocks) == 0 {
			// Skip an origin whose checkouts have no content (also avoids
			// dangling pinned origins after every session is removed).
			continue
		}

		originExpanded := IsExpanded(expanded, OriginExpandKey(origin))
		items = append(items, SidebarItem{
			IsRepoHeader:   true,
			IsOriginHeader: true,
			OriginKey:      origin,
			OriginLabel:    labelForOrigin(origin),
			Expanded:       originExpanded,
			SessionCount:   originSessionCount,
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
			})

			if !checkoutExpanded {
				continue
			}

			// Determine which sessions render directly vs. fold into "+ N idle".
			var rendered []*session.Session
			idleN := 0
			fold := idleFolded[blk.repo]
			for _, s := range blk.sessions {
				if fold && s.GetStatus() == session.StatusIdle {
					idleN++
					continue
				}
				rendered = append(rendered, s)
			}

			totalChildren := len(rendered) + len(blk.pending)
			if idleN > 0 {
				totalChildren++
			}
			childIdx := 0
			for _, s := range rendered {
				childIdx++
				items = append(items, SidebarItem{
					RepoPath: blk.repo,
					Session:  s,
					IsLast:   childIdx == totalChildren,
				})
			}
			if idleN > 0 {
				childIdx++
				items = append(items, SidebarItem{
					IsRepoHeader: true,
					IsIdleFold:   true,
					RepoPath:     blk.repo,
					OriginKey:    origin,
					IdleCount:    idleN,
					IsLast:       childIdx == totalChildren,
				})
			}
			for _, pw := range blk.pending {
				childIdx++
				items = append(items, SidebarItem{
					RepoPath: blk.repo,
					Pending:  pw,
					IsLast:   childIdx == totalChildren,
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
func RenderSidebar(items []SidebarItem, sessions []*session.Session, gitInfo map[string]*git.RepoInfo, slotBindings map[int]string, cursor, viewOffset, width, height int) string {
	if len(items) == 0 {
		return renderEmptyState(width, height)
	}

	// Invert bindings: session ID -> slot number.
	slotBySession := make(map[string]int, len(slotBindings))
	for slot, id := range slotBindings {
		slotBySession[id] = slot
	}

	var b strings.Builder

	b.WriteString(RenderPanelTitle(" SESSIONS", width))
	b.WriteString("\n")

	visibleHeight := height - 2
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
		b.WriteString(DimStyle.Render(fmt.Sprintf("  ⋮ +%d above", viewOffset)))
		b.WriteString("\n")
	}

	for i := viewOffset; i < visibleEnd; i++ {
		item := items[i]
		switch {
		case item.IsOriginHeader:
			b.WriteString(renderOriginHeader(item, width, i == cursor))
		case item.IsCheckoutHeader:
			b.WriteString(renderCheckoutHeader(item, gitInfo[item.RepoPath], width, i == cursor))
		case item.IsIdleFold:
			b.WriteString(renderIdleFold(item, width, i == cursor))
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
		b.WriteString(DimStyle.Render(fmt.Sprintf("  ⋮ +%d below", below)))
	}

	return b.String()
}

func renderEmptyState(width, height int) string {
	var b strings.Builder
	b.WriteString(RenderPanelTitle(" SESSIONS", width))
	b.WriteString("\n")

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

// renderOriginHeader → " ▾ originLabel                          N"
func renderOriginHeader(item SidebarItem, width int, selected bool) string {
	chevron := "▸"
	if item.Expanded {
		chevron = "▾"
	}
	countStr := fmt.Sprintf("%d", item.SessionCount)

	if selected {
		icon := SessionSelectionPrefix.Render(chevron)
		name := SessionTitleSelStyle.Render(" " + item.OriginLabel + " ")
		count := SessionStatusSelStyle.Render(countStr)
		return fmt.Sprintf(" %s %s %s", icon, name, count)
	}
	icon := DimStyle.Render(chevron)
	name := RepoHeaderStyle.Render(item.OriginLabel)
	count := DimStyle.Render(countStr)
	return fmt.Sprintf(" %s %s %s", icon, name, count)
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
	label := branchIcon + " " + branch

	dirty := ""
	if repoInfo.IsDirty {
		dirty = "*"
	}

	pr := ""
	if repoInfo.PR != nil {
		pr = " " + renderPRBadge(repoInfo.PR, selected)
	}

	chevron := "▸"
	if item.Expanded {
		chevron = "▾"
	}

	if selected {
		icon := SessionSelectionPrefix.Render(chevron)
		branchStyled := SessionTitleSelStyle.Render(" " + label + " ")
		dirtyStyled := ""
		if dirty != "" {
			dirtyStyled = SessionStatusSelStyle.Render(dirty)
		}
		return fmt.Sprintf("   %s %s%s", icon, branchStyled, dirtyStyled) + pr
	}
	icon := DimStyle.Render(chevron)
	branchStyled := BranchStyle.Render(label)
	dirtyStyled := ""
	if dirty != "" {
		dirtyStyled = " " + DirtyStyle.Render(dirty)
	}
	return fmt.Sprintf("   %s %s%s", icon, branchStyled, dirtyStyled) + pr
}

// renderCheckoutHeaderNonGit renders a checkout row for a folder that isn't
// a git repo — just the folder name in dim, no branch glyph or PR badge.
func renderCheckoutHeaderNonGit(item SidebarItem, selected bool) string {
	name := filepath.Base(item.RepoPath)
	chevron := "▸"
	if item.Expanded {
		chevron = "▾"
	}
	if selected {
		icon := SessionSelectionPrefix.Render(chevron)
		nameStyled := SessionTitleSelStyle.Render(" " + name + " ")
		return fmt.Sprintf("   %s %s", icon, nameStyled)
	}
	icon := DimStyle.Render(chevron)
	nameStyled := DimStyle.Render(name)
	return fmt.Sprintf("   %s %s", icon, nameStyled)
}

// renderSessionItem → "  │ <status> title [slot]"  (under a checkout)
func renderSessionItem(s *session.Session, width int, selected bool, slot int) string {
	status := s.GetStatus()
	symbolRaw := StatusSymbolRaw(status)
	title := s.Title

	slotRaw := ""
	if slot >= 0 && slot <= 9 {
		slotRaw = fmt.Sprintf(" [%d]", slot)
	}

	// Reserve: 2 leading spaces + "│ " guide + 1 selection prefix + symbol + space + slot.
	maxTitleLen := width - 11 - len(slotRaw)
	if maxTitleLen < 10 {
		maxTitleLen = 10
	}
	if len(title) > maxTitleLen {
		title = title[:maxTitleLen-1] + "…"
	}

	selPrefix := " "
	var styledSymbol, styledTitle, styledSlot string

	if selected {
		selPrefix = SessionSelectionPrefix.Render("▶")
		styledSymbol = SessionStatusSelStyle.Render(symbolRaw)
		styledTitle = SessionTitleSelStyle.Render(" " + title + " ")
		if slotRaw != "" {
			styledSlot = SessionStatusSelStyle.Render(slotRaw)
		}
	} else {
		styledSymbol = StatusSymbol(status)
		styledTitle = TitleStyleForStatus(status).Render(title)
		if slotRaw != "" {
			styledSlot = SlotBadgeDimStyle.Render(slotRaw)
		}
	}

	guide := BorderGuideStyle.Render(guideGlyph)
	return fmt.Sprintf("  %s%s%s %s%s", guide, selPrefix, styledSymbol, styledTitle, styledSlot)
}

// renderIdleFold → "  │   + N idle"  (under a checkout, in dim)
func renderIdleFold(item SidebarItem, width int, selected bool) string {
	label := fmt.Sprintf("+ %d idle", item.IdleCount)
	selPrefix := " "
	var styled string
	if selected {
		selPrefix = SessionSelectionPrefix.Render("▶")
		styled = SessionTitleSelStyle.Render(" " + label + " ")
	} else {
		styled = DimStyle.Render(label)
	}
	guide := BorderGuideStyle.Render(guideGlyph)
	return fmt.Sprintf("  %s%s  %s", guide, selPrefix, styled)
}

// renderPendingItem renders a "Creating…" phantom under its checkout.
func renderPendingItem(pw *PendingWorkspace, width int, selected bool) string {
	spinner := spinnerFrames[pw.Frame%len(spinnerFrames)]
	title := "Creating \"" + pw.Name + "\"..."

	maxTitleLen := width - 11
	if maxTitleLen < 10 {
		maxTitleLen = 10
	}
	if len(title) > maxTitleLen {
		title = title[:maxTitleLen-1] + "…"
	}

	selPrefix := " "
	var styledSpinner, styledTitle string
	if selected {
		selPrefix = SessionSelectionPrefix.Render("▶")
		styledSpinner = SessionStatusSelStyle.Render(spinner)
		styledTitle = SessionTitleSelStyle.Render(" " + title + " ")
	} else {
		styledSpinner = lipgloss.NewStyle().Foreground(ColorAccent).Render(spinner)
		styledTitle = DimStyle.Render(title)
	}
	guide := BorderGuideStyle.Render(guideGlyph)
	return fmt.Sprintf("  %s%s%s %s", guide, selPrefix, styledSpinner, styledTitle)
}

func renderPRBadge(pr *github.PR, selected bool) string {
	if pr == nil || pr.State == "CLOSED" {
		return ""
	}

	badge := fmt.Sprintf("#%d", pr.Number)

	if pr.State == "MERGED" {
		result := badge + " ⇡"
		if selected {
			return SessionStatusSelStyle.Render(result)
		}
		return PRMergedStyle.Render(result)
	}

	ciFail := pr.CIStatus == "FAILURE"
	changesReq := pr.ReviewDecision == "CHANGES_REQUESTED"
	approved := pr.ReviewDecision == "APPROVED"
	ciPass := pr.CIStatus == "SUCCESS"
	hasThreads := pr.UnresolvedThreads > 0
	hasConflicts := pr.HasConflicts

	var icons string
	style := PRPendingStyle

	if ciFail || changesReq || hasThreads || hasConflicts {
		style = PRFailStyle
		if ciFail {
			icons += "✕"
		}
		if hasConflicts {
			icons += "⚠"
		}
		if changesReq || hasThreads {
			icons += "↩"
		}
	} else if approved && ciPass {
		style = PROpenStyle
		icons = "✓"
	}

	result := badge
	if icons != "" {
		result += " " + icons
	}
	if selected {
		return SessionStatusSelStyle.Render(result)
	}
	return style.Render(result)
}

// NextSelectableItem advances the cursor; every row is selectable.
func NextSelectableItem(items []SidebarItem, current, direction int) int {
	next := current + direction
	if next >= 0 && next < len(items) {
		return next
	}
	return current
}

// FirstSelectableItem returns the first row index.
func FirstSelectableItem(items []SidebarItem) int { return 0 }
