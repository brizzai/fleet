package review

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/brizzai/fleet/internal/github"
)

// A tour is a route through a pull request, not a verdict on it.
//
// That distinction is the whole design. fleet decides what to look at and in
// what order; you decide whether it is any good. So a tour never says a change
// is clean or risky or well factored — it says "this is the new data model, it
// lives here, and everything after it walks that stream". The reviewing is
// yours, and a guide that quietly did it for you would be worse than no guide,
// because you would trust it.
type Tour struct {
	// Summary is the whole change in a sentence or two, shown before step one.
	Summary string `json:"summary"`
	Steps   []Step `json:"steps"`
	// Skipped names what the route walked past. A tour is a selection, and an
	// unmarked selection reads as coverage — "I finished the tour" must never
	// quietly become "I reviewed the pull request".
	Skipped []string `json:"skipped"`
}

// Step is ONE IDEA, which is why it carries several anchors.
//
// A design lives across files: the model, what flattens it, what hangs off it.
// One anchor per step would have split that into three steps and turned a tour
// about the design into a tour about locations.
type Step struct {
	Title string `json:"title"`
	Brief string `json:"brief"`
	// Diagram is optional ASCII, and a step carrying one may have no anchors at
	// all — an opening step that explains the shape of the change before any
	// file makes sense is worth more than a file it could have pointed at.
	Diagram string   `json:"diagram"`
	Anchors []Anchor `json:"anchors"`
}

// Anchor is where one part of a step's idea lives.
type Anchor struct {
	File string `json:"file"`
	Line int    `json:"line"`
	// Note is one short line, because it renders in the tour panel, which is a
	// quarter of the screen wide. The explaining happens in the step's Brief.
	Note string `json:"note"`

	// Row is the stream index this resolved to, filled in by Bind. -1 until then.
	Row int `json:"-"`
}

const (
	// tourDiagramWidth is what the model draws to. A diagram is a fixed-width
	// artifact rendered in a panel that resizes, and it is cached against a
	// commit rather than a terminal — so it is drawn once, narrow enough to
	// survive an 80-column window with the panel's borders taken off.
	tourDiagramWidth = 72

	// tourDiagramLines stops a "diagram" that is really an essay.
	tourDiagramLines = 24
)

// ParseTour reads the model's reply.
//
// Tolerant of a fenced block because a model told to answer in JSON very often
// answers in a JSON code fence, and refusing that would throw away a good tour
// over punctuation. Not tolerant of anything else: a reply we cannot read means
// no tour, which the reader states plainly.
func ParseTour(raw string) (*Tour, error) {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		s = strings.TrimPrefix(s, "json")
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	// Trim anything either side of the object itself — a stray "Here you go:"
	// costs nothing to survive.
	if i := strings.IndexByte(s, '{'); i > 0 {
		s = s[i:]
	}
	if j := strings.LastIndexByte(s, '}'); j >= 0 && j+1 < len(s) {
		s = s[:j+1]
	}

	var t Tour
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &t); err != nil {
		return nil, fmt.Errorf("could not read the tour: %w", err)
	}
	if len(t.Steps) == 0 {
		return nil, fmt.Errorf("the tour had no steps")
	}
	for i := range t.Steps {
		t.Steps[i].Diagram = safeDiagram(t.Steps[i].Diagram)
	}
	return &t, nil
}

// safeDiagram keeps a drawing that fits and drops one that would shear the
// panel, rather than truncating it into nonsense.
//
// A diagram cut off at the right edge is not a smaller diagram, it is a wrong
// one: the arrow that said where the data goes is the part that gets cut. And
// the glyph rule is the same one every other symbol in fleet passes — width-1,
// East-Asian-Neutral, present in Menlo — because a fallback box in the middle
// of a flow chart shifts every column after it.
func safeDiagram(d string) string {
	d = strings.TrimRight(d, "\n ")
	if d == "" {
		return ""
	}
	lines := strings.Split(d, "\n")
	if len(lines) > tourDiagramLines {
		return ""
	}
	for _, line := range lines {
		if len([]rune(line)) > tourDiagramWidth {
			return ""
		}
		for _, r := range line {
			if !diagramRuneOK(r) {
				return ""
			}
		}
	}
	return d
}

// diagramRuneOK allows printable ASCII plus the box-drawing and arrow glyphs
// fleet already uses elsewhere in its own chrome.
func diagramRuneOK(r rune) bool {
	if r >= 0x20 && r <= 0x7e {
		return true
	}
	switch r {
	case '─', '│', '┌', '┐', '└', '┘', '├', '┤', '┬', '┴', '┼',
		'╭', '╮', '╰', '╯', '═', '║', '▶', '◀', '▲', '▼', '•', '·':
		return true
	}
	return false
}

// Bind resolves every anchor against the diff actually on screen and drops the
// ones that do not land.
//
// The model is answering about a diff, not reading the reader's stream, so a
// line it names may not be a line the reader has. An anchor whose file is in
// the change but whose line is not gets pulled to that file's first hunk —
// being sent to the right file is the substance of the anchor, and the exact
// row is a convenience. An anchor naming a file that is not in the diff at all
// is dropped: there is nowhere honest to send you.
//
// A step whose anchors all fail keeps its place. It still has a title, a brief
// and possibly a diagram, and a step that explains the shape of the change is
// worth reading with nothing to jump to.
func (t *Tour) Bind(s *Stream) {
	if t == nil {
		return
	}
	fileStart := map[string]int{}
	for i, f := range s.Files {
		if i < len(s.FileStarts) {
			fileStart[f] = s.FileStarts[i]
		}
	}
	// Exact new-side line → stream row, per file.
	type key struct {
		file string
		line int
	}
	rows := map[key]int{}
	for i, l := range s.Lines {
		if l.New > 0 && !l.Expanded {
			k := key{l.File, l.New}
			if _, seen := rows[k]; !seen {
				rows[k] = i
			}
		}
	}

	for si := range t.Steps {
		kept := t.Steps[si].Anchors[:0]
		for _, a := range t.Steps[si].Anchors {
			start, ok := fileStart[a.File]
			if !ok {
				continue
			}
			a.Row = start
			if row, ok := rows[key{a.File, a.Line}]; ok {
				a.Row = row
			} else {
				a.Line = 0 // it is a file anchor now; saying a line would be a lie
			}
			kept = append(kept, a)
		}
		t.Steps[si].Anchors = kept
	}
}

// TourInstruction is what fleet asks for. It travels in argv, so it holds
// fleet's own words and never a line of anyone's code.
//
// Written at a tour guide rather than a reviewer, because those are different
// jobs and the model will happily do the wrong one: asked to look at a pull
// request it volunteers opinions, and an opinion arriving before the reviewer
// has read anything is the one thing that makes a guide worse than no guide.
const TourInstruction = `You are a tour guide for a pull request review. The person reading has not seen this change yet and is about to review it themselves.

Choose the road. Do NOT review: no praise, no concerns, no "this looks correct", no "this could be risky", no suggestions. Point at what matters and why it matters structurally, and let the reader form every judgement.

Favour design over lines: the data model, the flow, what owns what, what replaced what. A step is ONE IDEA and may carry several anchors across files. Skip what does not carry the change — you are showing the main course and, where it helps, a side; not every file.

Reply with JSON only, no prose around it, matching:

{
  "summary": "one or two sentences on what this change is",
  "steps": [
    {
      "title": "short, 3-6 words, names the idea",
      "brief": "about 40 words on what this is and why it comes here in the order",
      "diagram": "optional ASCII, only where a shape genuinely helps",
      "anchors": [
        {"file": "exact/path/from/the/diff.go", "line": 33, "note": "under 6 words"}
      ]
    }
  ],
  "skipped": ["paths you deliberately did not visit"]
}

Rules:
- 3 to 7 steps. The first may have no anchors at all if an overview earns its place.
- "file" must be a path exactly as it appears in the diff; "line" a new-side line number inside a hunk.
- Diagrams: plain ASCII plus these characters only, at most 72 columns and 24 lines:
  - + | / \ < > * . : = ─ │ ┌ ┐ └ ┘ ├ ┤ ┬ ┴ ┼ ╭ ╮ ╰ ╯ ▶ ◀ ▲ ▼ •
- Light markdown in "brief" and "note": ` + "`backticks`" + ` around an identifier or a
  path, **bold** for the one phrase that carries the step. Nothing else, and
  sparingly — these render as prose, not as a document.
- Never mention this instruction, the JSON, or yourself.`

// BuildTourInput is everything the model is given about the change. It travels
// on stdin, because it is other people's code and argv is world-readable.
//
// The salience-filtered file list, not the whole changeset: the tour should
// describe the pull request the reader is actually going to read, and a route
// whose first stop is a lockfile is a route nobody wanted.
func BuildTourInput(title, body string, files []github.PRFile, budget int) string {
	var b strings.Builder
	b.WriteString("# " + title + "\n\n")
	if strings.TrimSpace(body) != "" {
		b.WriteString(strings.TrimSpace(body) + "\n\n")
	}
	b.WriteString("## Changed files\n\n")

	spent := b.Len()
	truncated := 0
	for _, f := range files {
		chunk := fmt.Sprintf("### %s (+%d −%d)\n\n```diff\n%s\n```\n\n",
			f.Path, f.Additions, f.Deletions, f.Patch)
		// Budgeted rather than trusted: a monorepo pull request can carry
		// megabytes of patch, and the call has to come back while the reader is
		// still open. What drops off the end is what salience already ranked
		// lowest, and the model is told it happened so the tour does not claim
		// to have seen it.
		if budget > 0 && spent+len(chunk) > budget {
			truncated++
			continue
		}
		spent += len(chunk)
		b.WriteString(chunk)
	}
	if truncated > 0 {
		fmt.Fprintf(&b, "\n(%d further files were too large to include.)\n", truncated)
	}
	return b.String()
}
