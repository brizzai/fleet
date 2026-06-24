package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// renderedBoxHeight measures the height of the dialog box inside the centering
// padding produced by lipgloss.Place.
func renderedBoxHeight(view string) int {
	lines := strings.Split(view, "\n")
	first, last := -1, -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) != "" {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	if first == -1 {
		return 0
	}
	return last - first + 1
}

// The help sheet must never exceed the terminal: short panes used to clip the
// top and bottom rows (issue #141).
func TestHelpOverlayNeverOverflows(t *testing.T) {
	sizes := []struct{ w, h int }{
		{120, 45}, {120, 30}, {100, 20}, {80, 24}, {200, 20}, {60, 15},
	}
	for _, s := range sizes {
		ho := NewHelpOverlay()
		ho.SetSize(s.w, s.h)
		ho.Show()
		view := ho.View()
		if bh := renderedBoxHeight(view); bh > s.h {
			t.Errorf("%dx%d: box height %d exceeds terminal height %d", s.w, s.h, bh, s.h)
		}
		for _, ln := range strings.Split(view, "\n") {
			if w := lipgloss.Width(ln); w > s.w {
				t.Errorf("%dx%d: line width %d exceeds terminal width %d", s.w, s.h, w, s.w)
				break
			}
		}
	}
}

// On a short terminal where the sheet must scroll, every binding has to be
// reachable across the scroll range.
func TestHelpOverlayAllBindingsReachable(t *testing.T) {
	ho := NewHelpOverlay()
	ho.SetSize(100, 20)
	ho.Show()
	lay := ho.layout()
	if lay.maxScroll == 0 {
		t.Fatalf("expected the sheet to scroll at 100x20, got maxScroll=0")
	}
	seen := map[string]bool{}
	for s := 0; s <= lay.maxScroll; s++ {
		ho.scroll = s
		v := ho.View()
		for _, e := range lay.entries {
			if strings.Contains(v, e.Desc) {
				seen[e.Desc] = true
			}
		}
	}
	for _, e := range lay.entries {
		if !seen[e.Desc] {
			t.Errorf("binding %q (%s) is never visible while scrolling", e.Desc, e.Key)
		}
	}
}
