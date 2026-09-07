package ui

import (
	"errors"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/brizzai/fleet/internal/review"
)

func demoTour() *review.Tour {
	return &review.Tour{
		Summary: "Adds a reader for pull requests.",
		Steps: []review.Step{
			{
				Title:   "How a diff becomes rows",
				Brief:   "Everything downstream walks one flattening, so this is where to start.",
				Diagram: "gh api ──▶ PRFile[] ──▶ Doc ──▶ Stream",
			},
			{
				Title: "The new data model",
				Brief: "One owner for patches, expansions and comments. Row indexes only mean anything against one flattening.",
				Anchors: []review.Anchor{
					{File: "internal/session/session.go", Line: 801, Note: "Doc owns all three"},
					{File: "internal/github/errors.go", Line: 1, Note: "what it flattens to"},
				},
			},
			{Title: "Two colour channels", Brief: "Background says added, foreground says what the code is."},
		},
		Skipped: []string{"go.sum"},
	}
}

func readerWithTour(t *testing.T, w, h int) *ReaderDialog {
	t.Helper()
	d := newDemoReader(t, w, h)
	d.SetTour(demoTour(), nil)
	return d
}

// `t` is the only key the tour adds, and pressing it takes the keyboard with it:
// you asked to be led, so the panel that leads gets the arrows.
func TestTourTakesTheKeyboardWhenItOpens(t *testing.T) {
	d := readerWithTour(t, 160, 40)
	if d.tourMode {
		t.Fatal("the tour is showing before it was asked for")
	}
	readerPress(t, d, "t")
	if !d.tourMode || !d.treeFocus {
		t.Fatalf("t left tourMode=%v treeFocus=%v — both should be on", d.tourMode, d.treeFocus)
	}
	if !strings.Contains(d.View(), "Tour") {
		t.Error("the panel is not titled Tour")
	}
	readerPress(t, d, "t")
	if d.tourMode {
		t.Error("t did not toggle back to the file list")
	}
}

// A key that toasts "no tour" has taught you nothing, so it says what is
// actually happening instead.
func TestTourKeySaysWhyThereIsNone(t *testing.T) {
	d := newDemoReader(t, 160, 40)
	readerPress(t, d, "t")
	if d.tourMode {
		t.Fatal("opened a tour that does not exist")
	}
	if !strings.Contains(d.toast, "no tour") {
		t.Errorf("toast %q does not say there is no tour", d.toast)
	}

	d.TourWorking()
	readerPress(t, d, "t")
	if !strings.Contains(d.toast, "still reading") {
		t.Errorf("toast %q does not say the call is in flight", d.toast)
	}

	d2 := newDemoReader(t, 160, 40)
	d2.SetTour(nil, errors.New("claude is not installed"))
	readerPress(t, d2, "t")
	if !strings.Contains(d2.toast, "not installed") {
		t.Errorf("toast %q loses the reason", d2.toast)
	}
}

// Only the selected step shows its stops. Five steps with four anchors each is
// twenty rows in a quarter-width panel, which is a file tree again.
func TestOnlyTheSelectedStepShowsItsStops(t *testing.T) {
	d := readerWithTour(t, 160, 40)
	readerPress(t, d, "t")

	// Step 1 has no anchors, so the route is three rows.
	if len(d.tourItems) != 3 {
		t.Fatalf("route has %d rows, want 3 step rows", len(d.tourItems))
	}
	readerPress(t, d, "down") // onto step 2, which has two
	if len(d.tourItems) != 5 {
		t.Fatalf("selecting a step with two stops gave %d rows, want 5", len(d.tourItems))
	}
	if it := d.tourItems[d.tourCur]; it.step != 1 || it.anchor != -1 {
		t.Errorf("selection is %+v, want step 2's own row", it)
	}
	readerPress(t, d, "down") // its first stop
	if it := d.tourItems[d.tourCur]; it.anchor != 0 {
		t.Errorf("selection is %+v, want the first stop", it)
	}
}

// Walking the route moves the diff, which is the entire relationship: the tour
// leads, the diff follows — the same one the file tree already has.
func TestWalkingTheRouteMovesTheDiff(t *testing.T) {
	d := readerWithTour(t, 160, 40)
	readerPress(t, d, "t")
	before := d.cursor
	readerPress(t, d, "down") // step 2, which has anchors
	if d.cursor == before {
		t.Fatal("the diff did not follow the route")
	}
	if got := d.doc.Stream.FileAt(d.streamAt(d.cursor)); got != "internal/session/session.go" {
		t.Errorf("the diff is showing %q, want the step's first anchor's file", got)
	}
}

// ⏎ on a stop hands the keyboard back, exactly as it does on a file in the tree.
func TestEnterOnAStopReturnsToTheDiff(t *testing.T) {
	d := readerWithTour(t, 160, 40)
	readerPress(t, d, "t")
	readerPress(t, d, "down")  // step 2
	readerPress(t, d, "enter") // → its first stop
	if it := d.tourItems[d.tourCur]; it.anchor != 0 {
		t.Fatalf("enter on a step went to %+v, want its first stop", it)
	}
	if !d.treeFocus {
		t.Fatal("enter on a step gave the keyboard away too early")
	}
	readerPress(t, d, "enter")
	if d.treeFocus {
		t.Error("enter on a stop did not hand the keyboard to the diff")
	}
}

// The band explains where you are and comes out of the diff's rows, never on
// top of them: the panel promises exactly its height, and a band that added to
// it would push the footer off the screen.
func TestTourKeepsTheScreenExact(t *testing.T) {
	for _, tc := range []struct{ w, h int }{{160, 40}, {124, 30}, {80, 24}, {200, 40}} {
		d := readerWithTour(t, tc.w, tc.h)
		readerPress(t, d, "t")
		for _, step := range []string{"", "down", "down"} {
			if step != "" {
				readerPress(t, d, step)
			}
			lines := strings.Split(d.View(), "\n")
			if len(lines) != tc.h {
				t.Errorf("%dx%d: %d rows, want %d", tc.w, tc.h, len(lines), tc.h)
				break
			}
			for i, l := range lines {
				if got := lipgloss.Width(l); got != tc.w {
					t.Errorf("%dx%d: row %d is %d columns, want %d", tc.w, tc.h, i, got, tc.w)
					break
				}
			}
		}
	}
}

// A step with nowhere to send you takes the wide side of the screen, because a
// diagram needs it and because there is no code to show beside it.
func TestAStepWithNoStopsTakesTheDiffPanel(t *testing.T) {
	d := readerWithTour(t, 160, 40)
	readerPress(t, d, "t")
	out := d.View()
	if !strings.Contains(out, "PRFile[]") {
		t.Error("the opening step's diagram is not on screen")
	}
	if !strings.Contains(out, "step 1/3") {
		t.Error("the panel does not say where in the route you are")
	}
}

// A stale tour survives a failed regeneration: a route built against the
// previous commit still leads somewhere, and blanking it costs more than the
// staleness does.
func TestAFailedRegenerationKeepsTheOldTour(t *testing.T) {
	d := readerWithTour(t, 160, 40)
	d.SetTour(nil, errors.New("claude timed out"))
	if d.tour == nil {
		t.Fatal("a failed refresh threw away a working tour")
	}
	readerPress(t, d, "t")
	if !d.tourMode {
		t.Error("the surviving tour is no longer reachable")
	}
}
