package review

import (
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/github"
)

// A model told to answer in JSON very often answers in a JSON code fence, and
// refusing that would throw away a good tour over punctuation.
func TestParseTourSurvivesFencesAndPreamble(t *testing.T) {
	for name, raw := range map[string]string{
		"bare":     `{"summary":"s","steps":[{"title":"t","brief":"b"}]}`,
		"fenced":   "```json\n{\"summary\":\"s\",\"steps\":[{\"title\":\"t\",\"brief\":\"b\"}]}\n```",
		"preamble": "Here is the tour:\n\n{\"summary\":\"s\",\"steps\":[{\"title\":\"t\",\"brief\":\"b\"}]}\n",
	} {
		got, err := ParseTour(raw)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got.Summary != "s" || len(got.Steps) != 1 {
			t.Errorf("%s: parsed %+v", name, got)
		}
	}
}

// A reply with no steps is not a tour, and installing one would put an empty
// panel on screen with nothing saying why.
func TestParseTourRejectsWhatItCannotUse(t *testing.T) {
	for name, raw := range map[string]string{
		"prose":     "I had a look and it seems fine.",
		"no steps":  `{"summary":"s","steps":[]}`,
		"truncated": `{"summary":"s","steps":[{"title":`,
	} {
		if _, err := ParseTour(raw); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A diagram cut off at the right edge is not a smaller diagram, it is a wrong
// one — the arrow saying where the data goes is exactly the part that gets cut.
// So an unsafe drawing is dropped rather than truncated.
func TestUnsafeDiagramsAreDroppedNotTruncated(t *testing.T) {
	keep := "a ──▶ b\n│\n└─ c"
	if got := safeDiagram(keep); got != keep {
		t.Errorf("a valid diagram was altered: %q", got)
	}

	tooWide := strings.Repeat("-", tourDiagramWidth+1)
	if got := safeDiagram(tooWide); got != "" {
		t.Errorf("a %d-column diagram survived: %q", tourDiagramWidth+1, got)
	}
	if got := safeDiagram(strings.Repeat("x\n", tourDiagramLines+1)); got != "" {
		t.Error("a diagram taller than the cap survived")
	}
	// Emoji and CJK are width-2 in some terminals and width-1 in others, which
	// shears every column after them — the same rule the status dots pass.
	for _, bad := range []string{"a 🚀 b", "型 ──▶ b", "a ⟶ b"} {
		if got := safeDiagram(bad); got != "" {
			t.Errorf("diagram with an unsafe glyph survived: %q", got)
		}
	}
}

func tourTestStream(t *testing.T) *Stream {
	t.Helper()
	d := NewDoc([]github.PRFile{{
		Path: "a.go", Status: "modified", Additions: 2,
		Patch: "@@ -1,2 +1,4 @@\n ctx\n+added one\n+added two\n ctx2",
	}}, "", 40)
	return &d.Stream
}

// The model is answering about a diff, not reading the reader's stream, so
// every anchor it names has to be checked against what is actually on screen.
func TestBindResolvesSnapsAndDrops(t *testing.T) {
	s := tourTestStream(t)
	tour := &Tour{Steps: []Step{{
		Title: "one idea",
		Anchors: []Anchor{
			{File: "a.go", Line: 2, Note: "the real line"},
			{File: "a.go", Line: 9999, Note: "not in any hunk"},
			{File: "gone.go", Line: 1, Note: "not in the change at all"},
		},
	}}}
	tour.Bind(s)

	got := tour.Steps[0].Anchors
	if len(got) != 2 {
		t.Fatalf("kept %d anchors, want 2 — the file that is not in the diff has "+
			"nowhere honest to send you", len(got))
	}
	if got[0].Line != 2 || got[0].Row < 0 {
		t.Errorf("the real line did not resolve: %+v", got[0])
	}
	if l := s.Lines[got[0].Row]; l.File != "a.go" || l.New != 2 {
		t.Errorf("anchor row %d is %s:%d, want a.go:2", got[0].Row, l.File, l.New)
	}
	if got[1].Line != 0 {
		t.Errorf("a line that is not in the diff kept its number (%d) — being sent "+
			"to the right file is the substance, but claiming a line is a lie", got[1].Line)
	}
	if got[1].Row != s.FileStarts[0] {
		t.Errorf("unresolvable line snapped to row %d, want the file's start %d",
			got[1].Row, s.FileStarts[0])
	}
}

// A step whose anchors all fail keeps its place: it still has a title, a brief
// and possibly a diagram, and explaining the shape of a change is worth reading
// with nothing to jump to.
func TestBindKeepsAStepWithNoSurvivingAnchors(t *testing.T) {
	tour := &Tour{Steps: []Step{
		{Title: "overview"},
		{Title: "elsewhere", Anchors: []Anchor{{File: "nope.go", Line: 1}}},
	}}
	tour.Bind(tourTestStream(t))
	if len(tour.Steps) != 2 {
		t.Fatalf("Bind dropped a step: %+v", tour.Steps)
	}
	if len(tour.Steps[1].Anchors) != 0 {
		t.Errorf("a dead anchor survived: %+v", tour.Steps[1].Anchors)
	}
}

// The instruction travels in argv, which is world-readable through ps, so it
// must hold fleet's words and never anyone's code. And it has to actually say
// the thing the whole feature rests on.
func TestTourInstructionForbidsReviewing(t *testing.T) {
	for _, want := range []string{"Do NOT review", "no praise", "no concerns", "JSON only"} {
		if !strings.Contains(TourInstruction, want) {
			t.Errorf("the instruction never says %q — a guide that volunteers a "+
				"verdict before you have read anything is worse than no guide", want)
		}
	}
}

// The budget is what keeps a monorepo pull request from being sent whole, and
// what drops off is what salience already ranked lowest.
func TestBuildTourInputStaysWithinItsBudget(t *testing.T) {
	files := make([]github.PRFile, 20)
	for i := range files {
		files[i] = github.PRFile{Path: "f.go", Patch: strings.Repeat("x", 2000)}
	}
	got := BuildTourInput("title", "body", files, 6000)
	if len(got) > 8000 {
		t.Errorf("input is %d bytes against a 6000 budget", len(got))
	}
	if !strings.Contains(got, "too large to include") {
		t.Error("truncation was silent — the tour would describe files it never saw " +
			"without knowing it had not seen them")
	}
	if !strings.Contains(got, "title") || !strings.Contains(got, "body") {
		t.Error("the author's own description did not survive; it is the best " +
			"context available and it is free")
	}
}
