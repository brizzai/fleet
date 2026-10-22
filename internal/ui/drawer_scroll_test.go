package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/shell"
)

// drawerScrollHome has an open drawer on one shell, its body at (71, 31) 128×12.
func drawerScrollHome(t *testing.T) (*Home, *shell.Shell) {
	h, _ := scrollTestHome(t)
	sh := shell.New("sh", "/tmp/drawer-scroll", "")
	h.shells = []*shell.Shell{sh}
	h.drawerMode = drawerTyping
	h.drawerRepo = "/tmp/drawer-scroll"
	h.drawerProgress = 1
	h.drawerInnerH = 12
	h.noteDrawerEdge(70, 30, 130, 14)
	if h.activeShell() != sh {
		t.Fatal("precondition: the shell should be active")
	}
	return h, sh
}

func drawerWheel(h *Home, up bool) tea.Cmd {
	b := tea.MouseWheelDown
	if up {
		b = tea.MouseWheelUp
	}
	_, cmd := h.Update(tea.MouseWheelMsg{X: 100, Y: 35, Button: b})
	return cmd
}

func TestDrawerWheelScrollsShellHistory(t *testing.T) {
	h, sh := drawerScrollHome(t)

	if cmd := drawerWheel(h, true); cmd == nil || h.drawerScroll.offset != 3 || !h.drawerScroll.inFlight {
		t.Fatalf("wheel up: offset %d inFlight %v, want 3 and a capture running", h.drawerScroll.offset, h.drawerScroll.inFlight)
	}
	if h.previewScroll.offset != 0 {
		t.Error("the drawer wheel must not scroll the preview")
	}
	h.Update(previewScrollMsg{drawer: true, id: sh.TmuxName(), want: 3, offset: 3, history: 50, content: "older output"})
	out := h.renderDrawer(130, 14)
	if !strings.Contains(out, "older output") || !strings.Contains(out, "▐") {
		t.Errorf("scrolled drawer should show the captured history and a scrollbar:\n%s", out)
	}

	drawerWheel(h, false)
	if h.drawerScroll.offset != 0 || h.drawerScroll.content != "" {
		t.Errorf("wheel back down: offset %d, want live", h.drawerScroll.offset)
	}
}

func TestDrawerScrollDropsAnotherShellsCapture(t *testing.T) {
	h, _ := drawerScrollHome(t)
	drawerWheel(h, true)
	h.Update(previewScrollMsg{drawer: true, id: "fleetsh_other", want: 3, offset: 3, history: 50, content: "wrong"})
	if h.drawerScroll.content != "" {
		t.Error("a capture for a shell no longer active must not land")
	}
}

func TestDrawerKeyReturnsToLive(t *testing.T) {
	h, sh := drawerScrollHome(t)
	drawerWheel(h, true)
	h.Update(previewScrollMsg{drawer: true, id: sh.TmuxName(), want: 3, offset: 3, history: 50, content: "older"})
	h.handleTypingKey(tea.KeyPressMsg{Code: '`', Text: "`"}) //nolint:errcheck // state is the assertion
	if h.drawerScroll.offset != 0 || h.drawerScroll.content != "" {
		t.Error("a key in the drawer must return it to live")
	}
}
