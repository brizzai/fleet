package ui

import (
	"fmt"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/area"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
)

// areaKey is the key that cycles the visible area (see internal/area).
const areaKey = "v"

// areaState is this process's view of the configured areas. The current area is
// a per-window view filter and is never persisted: two fleet windows share one
// database, and one window switching must not move the other.
type areaState struct {
	matcher *area.Matcher
	current string // area.All ("") = no filter

	mu      sync.Mutex
	cache   map[string]string    // session/checkout path → owning area
	folders map[string][2]string // origin main checkout → (area, folder group)
}

// SetArea selects the area this window opens on. name must be one the config
// defines (cmd/fleet validates it); "" shows everything.
func (h *Home) SetArea(name string) {
	h.areas.current = name
	h.sidebarDirty = true
}

// areaEnabled reports whether any area is configured. With none, every other
// area method is inert and the sidebar behaves as it did before areas existed.
func (h *Home) areaEnabled() bool {
	return h.areas.matcher.Enabled()
}

// areaMatcher builds the matcher lazily from the config so tests that swap
// h.cfg see their own areas.
func (h *Home) initAreas() {
	h.areas.matcher = area.New(h.cfg.Areas, "")
	h.areas.cache = make(map[string]string)
}

// areaOfPath returns the area owning a session or checkout at path, memoized:
// the fallback asks git for the main worktree, which must not run per frame.
func (h *Home) areaOfPath(path string) string {
	h.areas.mu.Lock()
	defer h.areas.mu.Unlock()
	if a, ok := h.areas.cache[path]; ok {
		return a
	}
	a := h.areas.matcher.Of(path, func() string { return git.GetMainWorktreePath(path) })
	h.areas.cache[path] = a
	return a
}

// areaFiltering reports whether the sidebar is currently narrowed to one area.
func (h *Home) areaFiltering() bool {
	return h.areaEnabled() && h.areas.current != area.All
}

func (h *Home) areaShowsPath(path string) bool {
	return !h.areaFiltering() || area.Visible(h.areas.current, h.areaOfPath(path))
}

// areaScoped narrows the inputs of BuildFlatItems to the current area. With no
// areas, or the "all" view, it returns its arguments untouched.
func (h *Home) areaScoped() ([]*session.Session, []*PendingWorkspace, map[string]bool) {
	if !h.areaFiltering() {
		return h.sessions, h.pendingWorkspaces, h.pinnedRepos
	}
	return filterByArea(h.sessions, h.pendingWorkspaces, h.pinnedRepos, h.areaShowsPath)
}

// filterByArea keeps the sessions, pending workspaces and pinned repos whose
// path passes shows. Pinned repos are filtered too, so an empty repo from
// another area doesn't linger as a bare header.
func filterByArea(sessions []*session.Session, pending []*PendingWorkspace, pinned map[string]bool, shows func(path string) bool) ([]*session.Session, []*PendingWorkspace, map[string]bool) {
	var ss []*session.Session
	for _, s := range sessions {
		if shows(s.ProjectPath) {
			ss = append(ss, s)
		}
	}
	var pw []*PendingWorkspace
	for _, p := range pending {
		if p != nil && shows(p.RepoPath) {
			pw = append(pw, p)
		}
	}
	var pins map[string]bool
	if len(pinned) > 0 {
		pins = make(map[string]bool, len(pinned))
		for repo, v := range pinned {
			if shows(repo) {
				pins[repo] = v
			}
		}
	}
	return ss, pw, pins
}

// areaLabel is the name shown for the current view.
func (h *Home) areaLabel() string {
	if h.areas.current == area.All {
		return "all"
	}
	return h.areas.current
}

// sidebarTitle is the Sessions panel title: the current area, then a dim
// summary of attention waiting in the other areas, so nothing is forgotten
// behind the filter. Plain "Sessions" when no areas are configured.
func (h *Home) sidebarTitle() string {
	if !h.areaEnabled() {
		return "Sessions"
	}
	title := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("Sessions · " + h.areaLabel())
	if other := h.otherAreasSummary(); other != "" {
		title += lipgloss.NewStyle().Foreground(ColorTextDim).Render("  " + other)
	}
	return title
}

// otherAreasSummary counts waiting/finished sessions (the Space-jump criterion,
// snoozed ones excluded) in areas the sidebar is not showing, e.g. "pessoal ●2".
func (h *Home) otherAreasSummary() string {
	if !h.areaFiltering() {
		return ""
	}
	counts := make(map[string]int)
	for _, s := range h.sessions {
		if h.snoozeMuted[s.ID] {
			continue
		}
		if st := s.GetStatus(); st != session.StatusWaiting && st != session.StatusFinished {
			continue
		}
		if a := h.areaOfPath(s.ProjectPath); a != area.All && a != h.areas.current {
			counts[a]++
		}
	}
	var parts []string
	for _, name := range h.areas.matcher.Names() {
		if n := counts[name]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s ●%d", name, n))
		}
	}
	return strings.Join(parts, " ")
}

// cycleArea moves to the next area (… → all → first) and keeps the cursor on
// the same row when it is still visible, else on the first visible one.
func (h *Home) cycleArea() tea.Cmd {
	if !h.areaEnabled() {
		h.setInfo("No areas configured — add \"areas\" to config.json")
		return nil
	}
	var keepID, keepRepo string
	if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		it := h.flatItems[h.cursor]
		if it.Session != nil {
			keepID = it.Session.ID
		} else if it.IsCheckoutHeader {
			keepRepo = it.RepoPath
		}
	}
	h.areas.current = h.areas.matcher.Next(h.areas.current)
	h.rebuildFlatItems()
	h.cursor = 0
	for i, it := range h.flatItems {
		if (keepID != "" && it.Session != nil && it.Session.ID == keepID) ||
			(keepRepo != "" && it.IsCheckoutHeader && it.RepoPath == keepRepo) {
			h.cursor = i
			break
		}
	}
	h.clampCursor()
	h.syncViewport()
	h.setInfo("Area: " + h.areaLabel())
	return h.fetchPreviewForSelected()
}

// renderSidebarView renders the sidebar, with an area-specific empty state when
// the current area simply has nothing in it.
func (h *Home) renderSidebarView(gitInfo map[string]*git.RepoInfo, width, height int) string {
	if len(h.flatItems) == 0 && h.areaFiltering() && h.filterText == "" {
		return DimStyle.Render("  No sessions in area " + h.areas.current)
	}
	return RenderSidebar(h.flatItems, h.sessions, gitInfo, h.slotBindings, h.cursor, h.viewOffset, width, height, !h.drawerHasFocus())
}

// noteHiddenNewSession tells the user when a session just created (or adopted
// from `fleet add`) belongs to an area this window is not showing, instead of
// letting it vanish silently.
func (h *Home) noteHiddenNewSession(s *session.Session) {
	if h.areaShowsPath(s.ProjectPath) {
		return
	}
	h.setInfo(fmt.Sprintf("Created in area %s — press %s to switch", h.areaOfPath(s.ProjectPath), areaKey))
}
