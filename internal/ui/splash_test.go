package ui

import (
	"math"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The shine is a single vertical bar only because a rune index in one row is
// the same terminal column as that rune index in every other row. Redraw the
// ASCII art with a ragged row or a double-width rune and nothing panics — the
// bar just goes quietly crooked. Both halves matter: the rune count catches a
// ragged row, the display width catches a wide rune that leaves layout correct
// while skewing the shine.
func TestFleetWordmarkColumnsAlign(t *testing.T) {
	for i, row := range fleetWordmark {
		if n := len([]rune(row)); n != fleetWordmarkWidth {
			t.Errorf("row %d has %d runes, want %d", i, n, fleetWordmarkWidth)
		}
		if w := lipgloss.Width(row); w != fleetWordmarkWidth {
			t.Errorf("row %d renders %d columns, want %d (a wide rune would skew the shine)", i, w, fleetWordmarkWidth)
		}
	}
}

func TestSplashShineCrestSweepsThenRests(t *testing.T) {
	// Frame 0 parks the crest where the falloff contributes exactly zero, so a
	// FLEET_FREEZE_ANIM capture matches the un-shined wordmark byte for byte.
	if got := splashShineCrest(0); got != -splashShineWidth {
		t.Fatalf("frame 0 crest = %v, want the glint parked off the left edge at %v", got, -splashShineWidth)
	}

	prev := splashShineCrest(0)
	inRest, sawRest, restarted := false, false, false
	for f := 1; f <= 200; f++ {
		c := splashShineCrest(f)
		switch {
		case math.IsInf(c, 1):
			// Resting: the sweep that just ended must have carried the glint
			// past the final column, so no pass stops mid-wordmark.
			if !inRest && prev <= float64(fleetWordmarkWidth-1) {
				t.Fatalf("frame %d rests with the crest still at %v — sweep ended early", f, prev)
			}
			inRest, sawRest = true, true
		case inRest:
			restarted, inRest = true, false
		case c <= prev:
			t.Fatalf("frame %d: crest %v did not advance past %v", f, c, prev)
		}
		prev = c
	}
	if !sawRest || !restarted {
		t.Errorf("in 200 frames: rested=%v restarted=%v, want both (the sweep should loop)", sawRest, restarted)
	}
}

// The shine may change color and nothing else — a splash that reflows every
// 80ms is the most visible jitter fleet can ship.
func TestRenderSplashShineIsColorOnly(t *testing.T) {
	// Frames 1 and 3 fall inside one spinner window (splashSpinnerDiv frames
	// per glyph) and one label bucket (splashLabelDwell), so the shine is the
	// only thing that can differ. Both are mid-sweep, so both are lit.
	a := RenderSplash(80, 24, 0.5, 1)
	b := RenderSplash(80, 24, 0.5, 3)

	if ansi.Strip(a) != ansi.Strip(b) {
		t.Error("the shine changed the splash's layout; it must only change color")
	}
	if lipgloss.Width(a) != lipgloss.Width(b) {
		t.Errorf("splash width moved between frames: %d vs %d", lipgloss.Width(a), lipgloss.Width(b))
	}
	if a == b {
		t.Error("frames 0 and 10 render identically; the shine is not animating")
	}
}
