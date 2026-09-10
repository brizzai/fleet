package ui

import (
	"errors"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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
	if d.tourMode() {
		t.Fatal("the tour is showing before it was asked for")
	}
	readerPress(t, d, "t")
	if !d.tourMode() || !d.treeFocus {
		t.Fatalf("t left tourMode=%v treeFocus=%v — both should be on", d.tourMode(), d.treeFocus)
	}
	if !strings.Contains(d.View(), "Tour") {
		t.Error("the panel is not titled Tour")
	}
	readerPress(t, d, "t")
	if d.tourMode() {
		t.Error("t did not toggle back to the file list")
	}
}

// A key that toasts "no tour" has taught you nothing, so it says what is
// actually happening instead.
func TestTourKeySaysWhyThereIsNone(t *testing.T) {
	d := newDemoReader(t, 160, 40)
	readerPress(t, d, "t")
	if d.tourMode() {
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
	if !d.tourMode() {
		t.Error("the surviving tour is no longer reachable")
	}
}

// The narration used to wrap to the diff panel's full width — about 190 columns
// on a wide terminal, roughly three times the measure prose stays readable at.
// That is what turned a short paragraph into one endless line.
func TestTourBandStaysAtAReadingMeasure(t *testing.T) {
	d := readerWithTour(t, 240, 40)
	readerPress(t, d, "t")
	readerPress(t, d, "down") // a step with anchors, so the band renders over code

	band := d.tourBand(d.diffWidth() - 2)
	if len(band) == 0 {
		t.Fatal("no band rendered")
	}
	for i, l := range band {
		if w := lipgloss.Width(l); w > tourBandMeasure+4 {
			t.Errorf("band row %d is %d columns, over the %d measure (+ borders) — "+
				"the box must stop where the text stops, not span the panel",
				i, w, tourBandMeasure)
			break
		}
	}
	// And it must genuinely wrap rather than truncate: the words all survive.
	joined := strings.Join(band, " ")
	for _, want := range []string{"One owner", "flattening"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the band lost %q — it should wrap, not cut", want)
		}
	}
}

// The panel is capped for filenames at 34, which made every step title wrap.
func TestTourWidensTheLeftPanel(t *testing.T) {
	d := readerWithTour(t, 240, 40)
	files := d.treeWidth()
	readerPress(t, d, "t")
	tour := d.treeWidth()
	if tour <= files {
		t.Errorf("tour panel is %d columns, file tree is %d — a step title is a "+
			"sentence, and at the file-tree width every one of them wrapped", tour, files)
	}
	if tour != tourPanelWidth {
		t.Errorf("tour panel is %d, want the %d cap at this width", tour, tourPanelWidth)
	}
}

// The digits are the whole point of the bar, and one of them must refuse: the
// tour is genuinely absent for the first half-minute of every review, and a
// digit that silently does nothing reads as an unbound key.
func TestTabsSwitchAndTourRefusesUntilItExists(t *testing.T) {
	d := newDemoReader(t, 160, 40)
	if d.tab != tabDiff {
		t.Fatalf("opened on tab %v — the diff is what you came for; the tour "+
			"takes tens of seconds and would be an empty panel", d.tab)
	}

	readerPress(t, d, "1")
	if d.tab != tabDiff {
		t.Error("switched to a tour that does not exist")
	}
	if !strings.Contains(d.toast, "no tour") {
		t.Errorf("toast %q does not say why 1 did nothing", d.toast)
	}

	readerPress(t, d, "3")
	if d.tab != tabSession {
		t.Fatalf("3 landed on %v, want the session tab", d.tab)
	}
	readerPress(t, d, "2")
	if d.tab != tabDiff {
		t.Fatalf("2 landed on %v, want the diff", d.tab)
	}

	d.SetTour(demoTour(), nil)
	readerPress(t, d, "1")
	if d.tab != tabTour || !d.treeFocus {
		t.Errorf("with a tour present, 1 gave tab=%v focus=%v — the route should "+
			"take the keyboard, since being led is why you pressed it", d.tab, d.treeFocus)
	}
}

// Every tab must still fill the screen exactly, including the one holding a
// terminal and the one offering to start one.
func TestEveryTabKeepsTheScreenExact(t *testing.T) {
	for _, tc := range []struct{ w, h int }{{160, 40}, {124, 30}, {80, 24}} {
		d := readerWithTour(t, tc.w, tc.h)
		for _, tab := range []string{"1", "2", "3"} {
			readerPress(t, d, tab)
			lines := strings.Split(d.View(), "\n")
			if len(lines) != tc.h {
				t.Errorf("%dx%d tab %s: %d rows, want %d", tc.w, tc.h, tab, len(lines), tc.h)
				break
			}
			for i, l := range lines {
				if got := lipgloss.Width(l); got != tc.w {
					t.Errorf("%dx%d tab %s: row %d is %d columns, want %d",
						tc.w, tc.h, tab, i, got, tc.w)
					break
				}
			}
		}
	}
}

// Most reviews have no agent, so the third tab is usually an offer. Enter there
// must ask to start one rather than silently doing nothing.
func TestSessionTabOffersToStartOne(t *testing.T) {
	d := readerWithTour(t, 160, 40)
	readerPress(t, d, "3")
	if !strings.Contains(d.View(), "No agent on this review") {
		t.Error("the empty session tab does not say what it is empty of")
	}
	cmd := readerPress(t, d, "enter")
	if cmd == nil {
		t.Fatal("enter on an empty session tab did nothing")
	}
	msg, ok := cmd().(reviewSessionMsg)
	if !ok || !msg.start {
		t.Errorf("enter produced %#v, want a request to START a session", cmd())
	}
	// And it must not fire twice while the first is still coming up.
	if second := readerPress(t, d, "enter"); second != nil {
		t.Error("a second enter asked for another session while one was starting")
	}
}

// A step's brief is prose written by the same model in the same voice as the
// pull request description beside it. Rendering one properly and the other as
// flat text made the tour look like a draft of the page next to it.
func TestTourOverviewRendersLikeTheDescription(t *testing.T) {
	d := newDemoReader(t, 160, 40)
	st := &review.Step{
		Title:   "Boot work becomes deploy work",
		Brief:   "Every sweep moves out of `boot` and into one process that will **assert** them.",
		Diagram: "build ──▶ migrate job ──▶ deploy",
	}
	out := strings.Join(d.renderTourOverview(st, 100, 20), "\n")

	for _, marker := range []string{"`", "**"} {
		if strings.Contains(out, marker) {
			t.Errorf("the brief kept its %q markers — it is not going through the "+
				"renderer the description uses:\n%s", marker, out)
		}
	}
	if !strings.Contains(out, "boot") || !strings.Contains(out, "assert") {
		t.Error("the brief lost text")
	}
	// The diagram is a code block: the rule down its left edge separates a
	// drawing from the prose above it without needing a caption to say so.
	// Compared with the escapes stripped — the rule and the text are separately
	// styled, so the two never sit next to each other as literal bytes.
	if plain := ansi.Strip(out); !strings.Contains(plain, "│ build ──▶ migrate job") {
		t.Errorf("the diagram is not rendered as a code block:\n%s", plain)
	}
}

// The band above the code carries the same prose, so it needs the same
// treatment — and the same style-then-wrap order, or emphasis around a phrase
// longer than the measure strands its markers on two lines.
func TestTourBandRendersInlineMarkdown(t *testing.T) {
	d := readerWithTour(t, 200, 40)
	d.tour.Steps[1].Brief = "It replaces `skip_migrations` with a **mode**."
	readerPress(t, d, "t")
	readerPress(t, d, "down")

	band := strings.Join(d.tourBand(d.diffWidth()-2), "\n")
	if strings.Contains(band, "`") || strings.Contains(band, "**") {
		t.Errorf("the band kept its markers:\n%s", band)
	}
	if !strings.Contains(band, "skip_migrations") {
		t.Error("the band lost the identifier")
	}
}
