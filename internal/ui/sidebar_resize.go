package ui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/config"
)

// sidebarResizeStep is how many columns one [ or ] press moves the border.
const sidebarResizeStep = 4

// sidebarMaxWidth is the dual layout's cap: the sidebar never takes more than
// 45% of the terminal, whatever sidebar_width asks for.
func (h *Home) sidebarMaxWidth() int {
	return h.width * 45 / 100
}

// setSidebarWidth clamps w to what the dual layout can actually show and
// reports whether the width changed. Storing the visible width rather than a
// wider preference keeps [ / ] and dragging from pushing an invisible value
// past the 45% cap.
func (h *Home) setSidebarWidth(w int) bool {
	w = config.ClampSidebarWidth(w)
	w = max(config.SidebarWidthMin, min(w, h.sidebarMaxWidth()))
	if w == h.sidebarWidth {
		return false
	}
	h.sidebarWidth = w
	h.sidebarDirty = true
	return true
}

// stepSidebarWidth handles [ (narrower) and ] (wider). Only the dual layout has
// a sidebar width to change; stacked and single ignore the keys.
func (h *Home) stepSidebarWidth(key string) tea.Cmd {
	edge := h.layout.sidebarEdge
	if edge.w == 0 {
		return nil
	}
	step := sidebarResizeStep
	if key == "[" {
		step = -step
	}
	if !h.setSidebarWidth(edge.x + 1 + step) {
		return nil
	}
	return h.saveSidebarWidth()
}

// dragSidebar follows the pointer while the border is held. The border column
// sits at width-1, so the pointer's column + 1 is the new width.
func (h *Home) dragSidebar(m tea.Mouse) (tea.Model, tea.Cmd) {
	if !h.setSidebarWidth(m.X + 1) {
		return h, nil
	}
	return h, h.markMouseRepaint()
}

// saveSidebarWidth persists the width once per gesture (a key press or a drag
// release), never per motion event.
func (h *Home) saveSidebarWidth() tea.Cmd {
	h.cfg.SidebarWidth = h.sidebarWidth
	if err := h.cfg.Save(); err != nil {
		h.setError("sidebar_width_save_failed", fmt.Errorf("could not save sidebar width: %w", err))
	}
	return nil
}
