package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/config"
)

// A drawer whose top border is row 30 and bottom border row 43 (12 body rows),
// with room for at most 35.
func drawerDragHome(t *testing.T) *Home {
	t.Setenv("HOME", t.TempDir())
	h := adoptTestHome(t)
	h.drawerHeight = 12
	h.layout.drawerEdge = mouseRect{x: 70, y: 30, w: 130, h: 1}
	h.layout.drawerBottom = 43
	h.layout.drawerMaxRows = 35
	return h
}

func TestDrawerDragResizes(t *testing.T) {
	h := drawerDragHome(t)
	h.Update(tea.MouseClickMsg{X: 100, Y: 30, Button: tea.MouseLeft})
	if !h.draggingDrawer {
		t.Fatal("press on the drawer's top border must start a drag")
	}
	h.Update(tea.MouseMotionMsg{X: 100, Y: 20, Button: tea.MouseLeft})
	if h.drawerHeight != 22 {
		t.Errorf("drag up 10 rows → %d body rows, want 22", h.drawerHeight)
	}
	h.Update(tea.MouseMotionMsg{X: 100, Y: 0, Button: tea.MouseLeft})
	if h.drawerHeight != 35 {
		t.Errorf("drag past the top → %d, want the 35-row room", h.drawerHeight)
	}
	h.Update(tea.MouseMotionMsg{X: 100, Y: 42, Button: tea.MouseLeft})
	if h.drawerHeight != config.DrawerHeightMin {
		t.Errorf("drag onto the bottom → %d, want min %d", h.drawerHeight, config.DrawerHeightMin)
	}
	h.Update(tea.MouseMotionMsg{X: 100, Y: 33, Button: tea.MouseLeft})
	h.Update(tea.MouseReleaseMsg{X: 100, Y: 33, Button: tea.MouseLeft})
	if h.draggingDrawer {
		t.Error("release must end the drag")
	}
	if saved := config.Load(); saved.DrawerHeight != 9 {
		t.Errorf("saved drawer_height %d, want 9", saved.DrawerHeight)
	}
}

func TestDrawerMotionWithoutDragDoesNothing(t *testing.T) {
	h := drawerDragHome(t)
	h.Update(tea.MouseMotionMsg{X: 100, Y: 20})
	if h.drawerHeight != 12 {
		t.Errorf("hover moved the drawer to %d rows", h.drawerHeight)
	}
}
