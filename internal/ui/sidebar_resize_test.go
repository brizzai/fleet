package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/config"
)

// resizeTestHome is a dual-layout frame with the sidebar border at column 64
// (a 65-wide sidebar) on a 200-column terminal.
func resizeTestHome(t *testing.T) *Home {
	t.Setenv("HOME", t.TempDir()) // Save writes ~/.config/fleet/config.json
	h := adoptTestHome(t)
	h.width = 200
	h.sidebarWidth = 65
	h.layout.sidebarEdge = mouseRect{x: 64, y: sidebarContentTop - 1, w: 2, h: 22}
	return h
}

func TestSidebarResizeKeys(t *testing.T) {
	h := resizeTestHome(t)

	h.Update(tea.KeyPressMsg{Code: ']', Text: "]"})
	if h.sidebarWidth != 69 || h.cfg.SidebarWidth != 69 {
		t.Fatalf("] → width %d, saved %d; want 69 both", h.sidebarWidth, h.cfg.SidebarWidth)
	}
	if !h.sidebarDirty {
		t.Error("] must mark the sidebar dirty so focus mode drops its cached panel")
	}
	if saved := config.Load(); saved.SidebarWidth != 69 {
		t.Errorf("config on disk: width %d, want 69", saved.SidebarWidth)
	}

	h.layout.sidebarEdge.x = 68 // the frame after the resize
	h.Update(tea.KeyPressMsg{Code: '[', Text: "["})
	if h.sidebarWidth != 65 {
		t.Errorf("[ → width %d, want 65", h.sidebarWidth)
	}
}

func TestSidebarResizeKeysStopAtTheCaps(t *testing.T) {
	h := resizeTestHome(t)
	h.layout.sidebarEdge.x = config.SidebarWidthMin - 1
	h.sidebarWidth = config.SidebarWidthMin
	h.Update(tea.KeyPressMsg{Code: '[', Text: "["})
	if h.sidebarWidth != config.SidebarWidthMin {
		t.Errorf("[ at the floor → %d, want %d", h.sidebarWidth, config.SidebarWidthMin)
	}

	h.layout.sidebarEdge.x = h.sidebarMaxWidth() - 1
	h.sidebarWidth = h.sidebarMaxWidth()
	h.Update(tea.KeyPressMsg{Code: ']', Text: "]"})
	if h.sidebarWidth != h.sidebarMaxWidth() {
		t.Errorf("] at the 45%% cap → %d, want %d", h.sidebarWidth, h.sidebarMaxWidth())
	}
}

func TestSidebarResizeKeysIgnoredOutsideDualLayout(t *testing.T) {
	h := resizeTestHome(t)
	h.layout.sidebarEdge = mouseRect{} // stacked/single leave no edge
	h.Update(tea.KeyPressMsg{Code: ']', Text: "]"})
	if h.sidebarWidth != 65 || h.cfg.SidebarWidth != 0 {
		t.Errorf("] without a dual layout changed width to %d (saved %d)", h.sidebarWidth, h.cfg.SidebarWidth)
	}
}

func TestSidebarBorderDrag(t *testing.T) {
	h := resizeTestHome(t)
	cursor := h.cursor

	h.Update(tea.MouseClickMsg{X: 65, Y: 10, Button: tea.MouseLeft}) // the gap column
	if !h.draggingSidebar {
		t.Fatal("click on the border strip did not start a drag")
	}
	h.Update(tea.MouseMotionMsg{X: 79, Y: 10, Button: tea.MouseLeft})
	if h.sidebarWidth != 80 || !h.sidebarDirty {
		t.Errorf("drag to column 79 → width %d dirty %v; want 80 true", h.sidebarWidth, h.sidebarDirty)
	}
	if h.cfg.SidebarWidth != 0 {
		t.Error("motion saved the config; only the release should")
	}
	h.Update(tea.MouseMotionMsg{X: 199, Y: 10, Button: tea.MouseLeft})
	if h.sidebarWidth != h.sidebarMaxWidth() {
		t.Errorf("drag past the cap → %d, want %d", h.sidebarWidth, h.sidebarMaxWidth())
	}
	h.Update(tea.MouseReleaseMsg{X: 199, Y: 10, Button: tea.MouseLeft})
	if h.draggingSidebar || h.cfg.SidebarWidth != h.sidebarMaxWidth() {
		t.Errorf("release: dragging %v saved %d; want false %d", h.draggingSidebar, h.cfg.SidebarWidth, h.sidebarMaxWidth())
	}
	if h.cursor != cursor {
		t.Error("a border drag moved the sidebar cursor")
	}

	// After release, motion is back to being ignored.
	h.Update(tea.MouseMotionMsg{X: 40, Y: 10, Button: tea.MouseLeft})
	if h.sidebarWidth != h.sidebarMaxWidth() {
		t.Error("motion after release still resized the sidebar")
	}
}
