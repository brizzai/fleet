package ui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/config"
)

// noteDrawerEdge records where renderBody put the drawer: its top border at
// (x, top), outerH rows tall including both borders.
func (h *Home) noteDrawerEdge(x, top, w, outerH int) {
	h.layout.drawerEdge = mouseRect{x: x, y: top, w: w, h: 1}
	h.layout.drawerBottom = top + outerH - 1
}

// dragDrawer follows the pointer while the drawer's top border is held: the
// body is every row between the pointer and the bottom border. The shell pane
// follows on the next syncShellStream, which resizes it to the new body.
func (h *Home) dragDrawer(m tea.Mouse) (tea.Model, tea.Cmd) {
	rows := h.layout.drawerBottom - m.Y - 1
	rows = max(config.DrawerHeightMin, min(rows, h.layout.drawerMaxRows))
	if rows == h.drawerHeight || h.layout.drawerMaxRows < config.DrawerHeightMin {
		return h, nil
	}
	h.drawerHeight = rows
	return h, h.markMouseRepaint()
}

// saveDrawerHeight persists the height once per drag, on release.
func (h *Home) saveDrawerHeight() tea.Cmd {
	h.cfg.DrawerHeight = h.drawerHeight
	if err := h.cfg.Save(); err != nil {
		h.setError("drawer_height_save_failed", fmt.Errorf("could not save drawer height: %w", err))
	}
	return nil
}
