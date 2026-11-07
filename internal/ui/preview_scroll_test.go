package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/session"
)

// scrollTestHome has one selected session and a 30×3 preview at (10, 2).
func scrollTestHome(t *testing.T) (*Home, *session.Session) {
	h := selectTestHome(t)
	s := session.NewSession("s", "/tmp/scroll-test")
	h.sessions = []*session.Session{s}
	h.sessionByID = map[string]*session.Session{s.ID: s}
	h.flatItems = []SidebarItem{{Session: s}}
	h.cursor = 0
	if h.selectedSession() != s {
		t.Fatal("precondition: the session should be selected")
	}
	return h, s
}

func wheel(h *Home, up bool) tea.Cmd {
	b := tea.MouseWheelDown
	if up {
		b = tea.MouseWheelUp
	}
	_, cmd := h.Update(tea.MouseWheelMsg{X: 15, Y: 3, Button: b})
	return cmd
}

func TestPreviewWheelScrollsIntoHistory(t *testing.T) {
	h, s := scrollTestHome(t)

	if cmd := wheel(h, true); cmd == nil || h.previewScroll.offset != 3 || !h.previewScroll.inFlight {
		t.Fatalf("wheel up: offset %d inFlight %v, want 3 and a capture running", h.previewScroll.offset, h.previewScroll.inFlight)
	}
	wheel(h, true) // a second notch while the first capture is still running
	if h.previewScroll.offset != 6 {
		t.Fatalf("second notch: offset %d, want 6", h.previewScroll.offset)
	}

	// The first capture lands for offset 3; the user is at 6 by now.
	_, cmd := h.Update(previewScrollMsg{id: s.ID, want: 3, offset: 3, history: 100, content: "old"})
	if cmd == nil || h.previewScroll.content != "" {
		t.Fatal("a stale capture must be dropped and a new one requested")
	}
	h.Update(previewScrollMsg{id: s.ID, want: 6, offset: 6, history: 100, content: "history"})
	if _, content := h.selectedPreview(); content != "history" {
		t.Errorf("preview shows %q, want the scrolled capture", content)
	}

	h.Update(previewMsg{sessionID: s.ID, content: "live"})
	if _, content := h.selectedPreview(); content != "history" {
		t.Error("a live refresh replaced the scrolled view")
	}

	h.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if h.previewScroll.offset != 0 {
		t.Error("a key press did not return the preview to the bottom")
	}
}

func TestPreviewWheelClampsToHistory(t *testing.T) {
	h, s := scrollTestHome(t)
	wheel(h, true)
	h.Update(previewScrollMsg{id: s.ID, want: 3, offset: 2, history: 2, content: "top"})
	if h.previewScroll.offset != 2 {
		t.Fatalf("offset %d, want it clamped to the 2 history lines", h.previewScroll.offset)
	}
	if cmd := wheel(h, true); cmd != nil || h.previewScroll.offset != 2 {
		t.Error("wheel up at the top of history should do nothing")
	}
	wheel(h, false)
	if h.previewScroll.offset != 0 {
		t.Errorf("wheel down to the bottom: offset %d, want 0", h.previewScroll.offset)
	}
	if cmd := wheel(h, false); cmd != nil {
		t.Error("wheel down at the bottom should do nothing")
	}
}

func TestPreviewScrollbarOnlyWhileScrolled(t *testing.T) {
	h, _ := scrollTestHome(t)
	plain := strings.Join(h.previewLines, "\n")
	if got := paintScrollbar(h.previewScroll, plain, 30); got != plain {
		t.Error("scrollbar drawn at the live bottom")
	}
	h.previewScroll = previewScrollState{offset: 5, history: 20, content: "x"}
	painted := paintScrollbar(h.previewScroll, plain, 30)
	if !strings.Contains(painted, "▐") {
		t.Fatal("no scrollbar thumb while scrolled")
	}
	for i, line := range strings.Split(painted, "\n") {
		if w := ansi.StringWidth(line); w != 30 {
			t.Errorf("row %d is %d wide with the scrollbar, want 30", i, w)
		}
	}
}

func TestExpandTabsPadsToTabStops(t *testing.T) {
	if got := expandTabs("ab\tc\n\x1b[1mx\x1b[0m\ty"); got != "ab      c\n\x1b[1mx\x1b[0m       y" {
		t.Errorf("expandTabs = %q", got)
	}
}
