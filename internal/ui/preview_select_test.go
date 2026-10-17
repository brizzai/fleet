package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/session"
)

// selectTestHome has a 30×3 preview whose text starts at screen cell (12, 2),
// past the two-column indent.
func selectTestHome(t *testing.T) *Home {
	h := adoptTestHome(t)
	inner := strings.Join([]string{
		"  \x1b[32mhello world\x1b[0m" + strings.Repeat(" ", 17),
		"  second line" + strings.Repeat(" ", 17),
		"  third" + strings.Repeat(" ", 23),
	}, "\n")
	h.notePreview(inner, nil, 10, 2, 30, 3)
	return h
}

func drag(h *Home, x0, y0, x1, y1 int) {
	h.Update(tea.MouseClickMsg{X: x0, Y: y0, Button: tea.MouseLeft})
	h.Update(tea.MouseMotionMsg{X: x1, Y: y1, Button: tea.MouseLeft})
}

func TestPreviewSelectionText(t *testing.T) {
	for _, tc := range []struct {
		name           string
		x0, y0, x1, y1 int
		want           string
	}{
		{"one row", 12, 2, 16, 2, "hello"},
		{"across rows", 18, 2, 17, 3, "world\nsecond"},
		{"backwards", 17, 3, 18, 2, "world\nsecond"},
		{"overshoot clamps to the pane", 12, 3, 99, 99, "second line\nthird"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := selectTestHome(t)
			drag(h, tc.x0, tc.y0, tc.x1, tc.y1)
			if got := h.selectedPreviewText(); got != tc.want {
				t.Errorf("selected %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPreviewSelectionHighlightKeepsWidth(t *testing.T) {
	h := selectTestHome(t)
	drag(h, 14, 2, 15, 3)
	plain := strings.Join(h.previewLines, "\n")
	painted := h.notePreview(plain, nil, 10, 2, 30, 3)
	if painted == plain {
		t.Fatal("selection was not painted")
	}
	for i, line := range strings.Split(painted, "\n") {
		if w := ansi.StringWidth(line); w != 30 {
			t.Errorf("row %d is %d wide after highlighting, want 30", i, w)
		}
		if ansi.Strip(line) != ansi.Strip(h.previewLines[i]) {
			t.Errorf("row %d text changed: %q", i, ansi.Strip(line))
		}
	}
}

func TestPreviewSelectionLifecycle(t *testing.T) {
	h := selectTestHome(t)

	h.Update(tea.MouseClickMsg{X: 12, Y: 2, Button: tea.MouseLeft})
	h.Update(tea.MouseReleaseMsg{X: 12, Y: 2, Button: tea.MouseLeft})
	if h.previewSel.active() {
		t.Error("a click without a drag left a selection behind")
	}

	drag(h, 12, 2, 16, 2)
	h.Update(previewMsg{sessionID: "x", content: "new output"})
	if _, ok := h.previewCache["x"]; ok {
		t.Error("the preview refreshed under an active selection")
	}

	_, cmd := h.Update(tea.MouseReleaseMsg{X: 16, Y: 2, Button: tea.MouseLeft})
	if cmd != nil || !h.previewSel.shown {
		t.Fatal("release should keep the highlight and copy nothing")
	}

	_, cmd = h.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil || h.quitting {
		t.Fatal("Ctrl+C with a selection should copy, not quit")
	}
	if h.previewSel.active() {
		t.Error("copying did not clear the selection")
	}

	drag(h, 12, 2, 16, 2)
	h.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if h.previewSel.active() {
		t.Error("a key press did not clear the selection")
	}
	h.Update(previewMsg{sessionID: "x", content: "new output"})
	if h.previewCache["x"] != "new output" {
		t.Error("the preview did not resume refreshing after the selection cleared")
	}
}

func TestCtrlCWithoutSelectionStillQuits(t *testing.T) {
	h := selectTestHome(t)
	h.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !h.quitting {
		t.Error("Ctrl+C with nothing selected must keep quitting fleet")
	}
}

func TestPreviewResizeOnlyWhenTheFitChanges(t *testing.T) {
	h := selectTestHome(t)
	s := session.NewSession("s", "/tmp/fit-test")

	h.layout.previewFit = [2]int{}
	h.previewResize(s)
	if _, sent := h.previewSizes[s.ID]; sent {
		t.Error("resized before any preview was drawn")
	}

	h.layout.previewFit = [2]int{100, 30}
	h.previewResize(s)
	if h.previewSizes[s.ID] != [2]int{100, 30} {
		t.Fatalf("first fit not sent: %v", h.previewSizes[s.ID])
	}

	h.previewSizes[s.ID] = [2]int{1, 1} // marker: a repeat call must not overwrite it
	h.layout.previewFit = [2]int{1, 1}
	h.previewResize(s)
	h.layout.previewFit = [2]int{90, 30} // sidebar got wider
	h.previewResize(s)
	if h.previewSizes[s.ID] != [2]int{90, 30} {
		t.Errorf("new fit not sent after the preview narrowed: %v", h.previewSizes[s.ID])
	}
}
