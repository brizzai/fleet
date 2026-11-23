package review

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// expandStep is how many lines one expand reveals on each side of a gap.
// Twenty is GitHub's own step and is about a screenful of context — enough to
// see the enclosing function, few enough that a 600-line gap does not open in
// one keystroke.
const expandStep = 20

// maxExpandSource caps the file we will read out of the worktree. Expansion
// exists to show you the code around a hunk; a two-megabyte generated file has
// no such code, and reading it stalls the keypress.
const maxExpandSource = 2 << 20

// Doc is a reviewable changeset: the patches, and what has been expanded
// around them.
//
// It owns the flattened Stream and rebuilds it whenever an expansion changes
// it. One owner, because the row indexes a view scrolls through are only
// meaningful against a single consistent flattening.
type Doc struct {
	files    []fileState
	worktree string

	// src is the worktree's copy of each changed file, which is what makes
	// expansion a file read rather than a git call. Absent for a file the
	// worktree does not have (a deletion) or refuses (too large, binary, a
	// symlink).
	src map[string][]string

	// srcHL is that same copy, lexed. Expanded context is code and reads as
	// code; leaving it plain while the hunk beside it is coloured makes the
	// revealed lines look like a different kind of thing than the ones they
	// sit between. Whole-file, so its lexer state is the most correct in the
	// document — the hunks' own highlighters see only fragments.
	srcHL map[string]*Highlighter

	Stream Stream
}

type fileState struct {
	f     File
	hunks []hunk
	gaps  []*Gap
	newHL *Highlighter
	oldHL *Highlighter
}

type hunk struct {
	header             string
	newStart, newCount int
	rows               []prow
}

type prow struct {
	kind     LineKind
	text     string
	old, new int
	synIdx   int // line index into this row's side of the highlighter
	marks    []Span
}

// Gap is unchanged code between two hunks, and what it takes to see it.
//
// Top and Bot are the revealed frontiers: lines Lo..Top have been opened from
// above and Bot..Hi from below, so hidden is what remains between them.
type Gap struct {
	File     string
	Lo, Hi   int
	Top, Bot int
}

// Hidden is how many lines are still folded away.
func (g *Gap) Hidden() int {
	n := g.Bot - g.Top - 1
	if n < 0 {
		return 0
	}
	return n
}

// NewDoc parses a changeset. worktree is the checkout the new side of the
// patches describes; it may be empty, in which case context cannot be
// expanded and no expand rows are drawn — the view still works, it just
// cannot offer what it has no bytes for.
//
// The worktree is read as it is on disk NOW. For a self-review of a snapshot
// that is what you want while the agent is idle; if the files moved on since
// the diff was taken, expanded context can disagree with the patch around it,
// which is why an anchor never binds to an expanded row.
func NewDoc(files []File, worktree string) *Doc {
	d := &Doc{worktree: worktree,
		src: map[string][]string{}, srcHL: map[string]*Highlighter{}}

	// Files render in path order — the order a file tree beside the diff is
	// in, so the two never disagree about where you are.
	sorted := append([]File(nil), files...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	for _, f := range sorted {
		if f.Status != "removed" && !f.Binary {
			d.loadSource(f.Path)
		}
		d.files = append(d.files, d.parseFile(f))
	}
	d.Rebuild()
	return d
}

// Files returns the changeset in stream order.
func (d *Doc) Files() []File {
	out := make([]File, len(d.files))
	for i := range d.files {
		out[i] = d.files[i].f
	}
	return out
}

// loadSource reads one file out of the worktree.
func (d *Doc) loadSource(path string) {
	if d.worktree == "" {
		return
	}
	full := filepath.Join(d.worktree, filepath.Clean(path))
	// Refuse a path that climbs out of the worktree. The paths come from a
	// diff of code an agent wrote, and this is the one place they become a
	// filesystem read.
	rel, err := filepath.Rel(d.worktree, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	// Lstat, and regular files only: a diffed symlink's patch is its target
	// path, not the bytes behind it, so following it would show context from
	// a different file — possibly one outside the worktree entirely.
	st, err := os.Lstat(full)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxExpandSource {
		return
	}
	b, err := os.ReadFile(full)
	if err != nil || !utf8.Valid(b) {
		return
	}
	d.src[path] = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	d.srcHL[path] = Highlight(path, string(b))
}

// parseFile turns one unified diff into hunks, then highlights and word-diffs it.
func (d *Doc) parseFile(f File) fileState {
	fs := fileState{f: f}
	if f.Patch == "" {
		return fs
	}

	var cur *hunk
	oldLine, newLine := 0, 0
	for _, line := range strings.Split(f.Patch, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			o, n, count := parseHunkRange(line)
			fs.hunks = append(fs.hunks, hunk{header: line, newStart: n, newCount: count})
			cur = &fs.hunks[len(fs.hunks)-1]
			oldLine, newLine = o, n

		case cur == nil:
			// Patch preamble before the first @@ (diff --git, index, ---/+++).
			// Nothing to anchor it to.

		case strings.HasPrefix(line, "+"):
			cur.rows = append(cur.rows, prow{kind: LineAdd, text: line[1:], new: newLine})
			newLine++

		case strings.HasPrefix(line, "-"):
			cur.rows = append(cur.rows, prow{kind: LineDel, text: line[1:], old: oldLine})
			oldLine++

		case strings.HasPrefix(line, "\\"):
			// "\ No newline at end of file" — real diff output, and it belongs
			// on screen, but it is not a line of either side's file.
			cur.rows = append(cur.rows, prow{kind: LineNote, text: "  " + strings.TrimSpace(line)})

		default:
			// Context. A context row begins with a space, but the last row of a
			// patch can be bare empty rather than " " — reading that as
			// anything else drops a line and shifts every number after it.
			text := strings.TrimPrefix(line, " ")
			cur.rows = append(cur.rows, prow{kind: LineContext, text: text, old: oldLine, new: newLine})
			oldLine++
			newLine++
		}
	}
	// A patch ending in "\n" splits into a trailing empty string, which the
	// default case above reads as one more context row. Drop it when it runs
	// past what the hunk header promised.
	if cur != nil && len(cur.rows) > 0 {
		last := cur.rows[len(cur.rows)-1]
		if last.kind == LineContext && last.text == "" && last.new >= cur.newStart+cur.newCount {
			cur.rows = cur.rows[:len(cur.rows)-1]
		}
	}

	fs.gaps = buildGaps(f.Path, fs.hunks, len(d.src[f.Path]), d.worktree != "" && d.src[f.Path] != nil)
	d.highlightFile(&fs)
	markWordDiffs(&fs)
	return fs
}

// buildGaps finds the unchanged stretches between hunks on the new side.
func buildGaps(path string, hunks []hunk, eof int, haveSource bool) []*Gap {
	if !haveSource || len(hunks) == 0 {
		return nil
	}
	newGap := func(lo, hi int) *Gap {
		if lo > hi {
			return nil
		}
		return &Gap{File: path, Lo: lo, Hi: hi, Top: lo - 1, Bot: hi + 1}
	}
	gaps := make([]*Gap, len(hunks)+1)
	gaps[0] = newGap(1, hunks[0].newStart-1)
	for i := 0; i+1 < len(hunks); i++ {
		gaps[i+1] = newGap(hunks[i].newStart+hunks[i].newCount, hunks[i+1].newStart-1)
	}
	last := hunks[len(hunks)-1]
	gaps[len(hunks)] = newGap(last.newStart+last.newCount, eof)
	return gaps
}

// highlightFile lexes each side of the file once and indexes the tokens by row.
//
// Two passes because a deleted line and an added line are lines of different
// files: highlighting them together would feed the lexer code that never
// existed in either version.
func (d *Doc) highlightFile(fs *fileState) {
	var newSrc, oldSrc []string
	for hi := range fs.hunks {
		for ri := range fs.hunks[hi].rows {
			r := &fs.hunks[hi].rows[ri]
			switch r.kind {
			case LineAdd:
				r.synIdx = len(newSrc)
				newSrc = append(newSrc, r.text)
			case LineDel:
				r.synIdx = len(oldSrc)
				oldSrc = append(oldSrc, r.text)
			case LineContext:
				r.synIdx = len(newSrc)
				newSrc = append(newSrc, r.text)
				oldSrc = append(oldSrc, r.text)
			}
		}
	}
	fs.newHL = Highlight(fs.f.Path, strings.Join(newSrc, "\n"))
	fs.oldHL = Highlight(fs.f.Path, strings.Join(oldSrc, "\n"))
}

// markWordDiffs pairs each run of deletions with the run of additions that
// follows it and marks the runes that changed.
//
// Paired by position within the run, which is what a unified diff's shape
// already implies: git emits the removals of a replaced block together, then
// its additions, in the same order.
func markWordDiffs(fs *fileState) {
	for hi := range fs.hunks {
		rows := fs.hunks[hi].rows
		i := 0
		for i < len(rows) {
			if rows[i].kind != LineDel {
				i++
				continue
			}
			delStart := i
			for i < len(rows) && rows[i].kind == LineDel {
				i++
			}
			addStart := i
			for i < len(rows) && rows[i].kind == LineAdd {
				i++
			}
			n := min(addStart-delStart, i-addStart)
			for k := range n {
				dm, am := WordDiff(rows[delStart+k].text, rows[addStart+k].text)
				rows[delStart+k].marks = dm
				rows[addStart+k].marks = am
			}
		}
	}
}

// Expand opens a gap by one step in both directions and returns whether
// anything moved. all opens it completely.
func (d *Doc) Expand(g *Gap, all bool) bool {
	if g == nil || g.Hidden() == 0 {
		return false
	}
	g.open(all)
	d.Rebuild()
	return true
}

// open moves a gap's frontiers by one step each way, or all the way.
func (g *Gap) open(all bool) {
	if all {
		g.Top, g.Bot = g.Hi, g.Hi+1
		return
	}
	g.Top += expandStep
	g.Bot -= expandStep
	if g.Top >= g.Bot-1 {
		// The two frontiers met. Close the gap outright rather than leaving
		// a marker over zero hidden lines.
		g.Top, g.Bot = g.Hi, g.Hi+1
	}
	if g.Top > g.Hi {
		g.Top = g.Hi
	}
	if g.Bot < g.Lo {
		g.Bot = g.Lo
	}
}

// ExpandRows opens, by one step (or fully, with all), every fold whose marker
// row lies in the stream rows [from, to). It rebuilds once and reports whether
// anything moved. This is the "expand context" key on a slice of the diff:
// bind the slice with BindRange, expand it, then bind again — the rows have
// moved.
func (d *Doc) ExpandRows(from, to int, all bool) bool {
	from, to = max(from, 0), min(to, len(d.Stream.Lines))
	var gaps []*Gap
	for i := from; i < to; i++ {
		if l := d.Stream.Lines[i]; l.Kind == LineExpand && l.Gap != nil && l.Gap.Hidden() > 0 {
			gaps = append(gaps, l.Gap)
		}
	}
	if len(gaps) == 0 {
		return false
	}
	for _, g := range gaps {
		g.open(all)
	}
	d.Rebuild()
	return true
}

// CanExpand reports whether this document has any bytes to expand with.
func (d *Doc) CanExpand() bool { return len(d.src) > 0 }

// Rebuild flattens everything into the Stream a view scrolls.
func (d *Doc) Rebuild() {
	var s Stream
	emit := func(l Line) { s.Lines = append(s.Lines, l) }

	for fi := range d.files {
		fs := &d.files[fi]
		if fi > 0 {
			emit(Line{Kind: LineBlank, File: fs.f.Path})
		}
		s.FileStarts = append(s.FileStarts, len(s.Lines))
		s.Files = append(s.Files, fs.f.Path)
		emit(Line{Kind: LineFileHeader, File: fs.f.Path, Text: fileHeaderText(fs.f)})

		if fs.f.Patch == "" || len(fs.hunks) == 0 {
			emit(Line{Kind: LineNote, File: fs.f.Path, Text: noPatchReason(fs.f)})
			continue
		}

		for hi := range fs.hunks {
			h := &fs.hunks[hi]
			d.emitGap(&s, fs, gapAt(fs.gaps, hi))

			s.HunkStarts = append(s.HunkStarts, len(s.Lines))
			emit(Line{Kind: LineHunkHeader, File: fs.f.Path, Text: h.header})

			for ri := range h.rows {
				r := &h.rows[ri]
				hl := fs.newHL
				if r.kind == LineDel {
					hl = fs.oldHL
				}
				l := Line{
					Kind: r.kind, Text: r.text, File: fs.f.Path,
					New: r.new, Old: r.old, Marks: r.marks,
				}
				if r.kind.IsCode() {
					l.Toks = hl.Line(r.synIdx, r.text)
				}
				emit(l)
			}
			s.HunkEnds = append(s.HunkEnds, len(s.Lines))
		}
		d.emitGap(&s, fs, gapAt(fs.gaps, len(fs.hunks)))
	}
	d.Stream = s
}

func gapAt(gaps []*Gap, i int) *Gap {
	if i < 0 || i >= len(gaps) {
		return nil
	}
	return gaps[i]
}

// emitGap draws whatever of a gap has been revealed, plus the marker row for
// what has not.
func (d *Doc) emitGap(s *Stream, fs *fileState, g *Gap) {
	if g == nil {
		return
	}
	src := d.src[fs.f.Path]
	line := func(n int) {
		if n-1 < 0 || n-1 >= len(src) {
			return
		}
		text := src[n-1]
		s.Lines = append(s.Lines, Line{
			Kind: LineContext, Text: text, File: fs.f.Path,
			New: n, Old: 0, Expanded: true,
			Toks: d.srcHL[fs.f.Path].Line(n-1, text),
		})
	}
	for n := g.Lo; n <= g.Top; n++ {
		line(n)
	}
	if h := g.Hidden(); h > 0 {
		s.Lines = append(s.Lines, Line{
			Kind: LineExpand, File: fs.f.Path, Gap: g,
			Text: fmt.Sprintf("⋯ %s hidden", plural(h, "line")),
		})
	}
	for n := g.Bot; n <= g.Hi; n++ {
		line(n)
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
