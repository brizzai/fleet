package review

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/brizzai/fleet/internal/github"
)

// expandStep is how many lines one press of ⏎ reveals on each side of a gap.
// Twenty is GitHub's own step and is about a screenful of context — enough to
// see the enclosing function, few enough that a 600-line gap does not open in
// one keystroke.
const expandStep = 20

// maxExpandSource caps the file we will read out of the review worktree.
// Expansion exists to show you the code around a hunk; a two-megabyte generated
// file has no such code, and reading it stalls the keypress.
const maxExpandSource = 2 << 20

// Doc is a reviewable changeset: the patches, what has been expanded around
// them, and the comments written against them.
//
// It owns the flattened Stream and rebuilds it whenever any of those change.
// One owner rather than three, because the row indexes the reader scrolls
// through are only meaningful against a single consistent flattening — a
// comment inserted by one component and an expansion by another would each be
// correct about a stream the other had already invalidated.
type Doc struct {
	files    []fileState
	worktree string
	comments []Comment
	nextID   int
	wrapW    int

	// draft is the comment being typed, rendered in the stream at its anchor
	// exactly like a saved one. Composing in place rather than in a box at the
	// bottom of the screen is the point: you write the comment against the code
	// it is about, with that code still on screen above it.
	draft *Comment

	// src is the worktree's copy of each changed file, which is what makes
	// expansion a file read rather than an API call. Absent for a file the
	// worktree does not have (a deletion) or refuses (too large, binary).
	src map[string][]string

	// srcHL is that same copy, lexed. Expanded context is code and reads as
	// code; leaving it plain while the hunk above it is coloured makes the
	// revealed lines look like a different kind of thing than the ones they
	// sit between. Whole-file, so its lexer state is the most correct in the
	// document — the hunks' own highlighters see only fragments.
	srcHL map[string]*Highlighter

	Stream Stream
}

type fileState struct {
	f     github.PRFile
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

// NewDoc parses a changeset. worktree may be empty, in which case context
// cannot be expanded and no expand rows are drawn — the reader still works, it
// just cannot offer what it has no bytes for.
func NewDoc(files []github.PRFile, worktree string, wrapWidth int) *Doc {
	d := &Doc{worktree: worktree, wrapW: wrapWidth,
		src: map[string][]string{}, srcHL: map[string]*Highlighter{}}

	// Files render in path order. GitHub's own convention, and — more to the
	// point — the file tree beside the diff is in path order, so any other
	// ordering makes the two panels disagree about where you are.
	sorted := append([]github.PRFile(nil), files...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	for _, f := range sorted {
		d.loadSource(f.Path)
		d.files = append(d.files, d.parseFile(f))
	}
	d.Rebuild()
	return d
}

// loadSource reads one file out of the review worktree.
func (d *Doc) loadSource(path string) {
	if d.worktree == "" {
		return
	}
	full := filepath.Join(d.worktree, filepath.Clean(path))
	// Refuse a path that climbs out of the worktree. Paths come from GitHub's
	// API rather than from the user, but a review is other people's data and
	// this is the one place it becomes a filesystem read.
	if rel, err := filepath.Rel(d.worktree, full); err != nil || strings.HasPrefix(rel, "..") {
		return
	}
	st, err := os.Stat(full)
	if err != nil || st.IsDir() || st.Size() > maxExpandSource {
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
func (d *Doc) parseFile(f github.PRFile) fileState {
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
			// Patch preamble before the first @@. Nothing to anchor it to.

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

// SetDraft puts a comment under composition at file:line. Passing nil clears it.
func (d *Doc) SetDraft(c *Comment) {
	d.draft = c
	d.Rebuild()
}

// Draft returns the comment under composition, or nil.
func (d *Doc) Draft() *Comment { return d.draft }

// CommentByID finds a saved comment.
func (d *Doc) CommentByID(id int) (Comment, bool) {
	for _, c := range d.comments {
		if c.ID == id {
			return c, true
		}
	}
	return Comment{}, false
}

// SeedComments installs comments made in an earlier visit to this PR, so
// closing the reader and reopening it does not lose what you wrote.
func (d *Doc) SeedComments(cs []Comment) {
	d.comments = nil
	d.nextID = 0
	for _, c := range cs {
		d.nextID++
		c.ID = d.nextID
		d.comments = append(d.comments, c)
	}
	d.Rebuild()
}

// SetWrapWidth records the width comments wrap to and rebuilds if it changed.
func (d *Doc) SetWrapWidth(w int) {
	if w == d.wrapW || w < 8 {
		return
	}
	d.wrapW = w
	d.Rebuild()
}

// Comments returns every comment, in the order they were written.
func (d *Doc) Comments() []Comment { return d.comments }

// Pending is how many comments have not been submitted.
func (d *Doc) Pending() int {
	n := 0
	for _, c := range d.comments {
		if !c.Sent {
			n++
		}
	}
	return n
}

// MarkSent records that every pending comment has reached GitHub.
//
// The comments stay in the document rather than being cleared: what you just
// sent is the most useful thing to still see while you finish reading, and the
// rows already render a sent comment as read-only.
func (d *Doc) MarkSent() {
	changed := false
	for i := range d.comments {
		if !d.comments[i].Sent {
			d.comments[i].Sent = true
			changed = true
		}
	}
	if changed {
		d.Rebuild()
	}
}

// AddComment files a comment and returns its id.
func (d *Doc) AddComment(c Comment) int {
	d.nextID++
	c.ID = d.nextID
	d.comments = append(d.comments, c)
	d.Rebuild()
	return c.ID
}

// UpdateComment replaces a comment's kind and body.
func (d *Doc) UpdateComment(id int, kind CommentKind, body string) {
	for i := range d.comments {
		if d.comments[i].ID == id {
			d.comments[i].Kind, d.comments[i].Body = kind, body
			d.Rebuild()
			return
		}
	}
}

// DeleteComment removes a comment.
func (d *Doc) DeleteComment(id int) {
	for i := range d.comments {
		if d.comments[i].ID == id {
			d.comments = append(d.comments[:i], d.comments[i+1:]...)
			d.Rebuild()
			return
		}
	}
}

// Expand opens a gap by one step in both directions and returns whether
// anything moved. all opens it completely.
func (d *Doc) Expand(g *Gap, all bool) bool {
	if g == nil || g.Hidden() == 0 {
		return false
	}
	if all {
		g.Top, g.Bot = g.Hi, g.Hi+1
	} else {
		g.Top += expandStep
		g.Bot -= expandStep
		if g.Top >= g.Bot-1 {
			// The two frontiers met. Close the gap outright rather than leaving
			// a marker over zero hidden lines.
			g.Top, g.Bot = g.Hi, g.Hi+1
		}
	}
	if g.Top > g.Hi {
		g.Top = g.Hi
	}
	if g.Bot < g.Lo {
		g.Bot = g.Lo
	}
	d.Rebuild()
	return true
}

// CanExpand reports whether this document has any bytes to expand with.
func (d *Doc) CanExpand() bool { return len(d.src) > 0 }

// Rebuild flattens everything into the Stream the reader scrolls.
func (d *Doc) Rebuild() {
	var s Stream
	byAnchor := map[string][]Comment{}
	for _, c := range d.comments {
		k := fmt.Sprintf("%s:%d", c.File, c.Line)
		byAnchor[k] = append(byAnchor[k], c)
	}

	emit := func(l Line) { s.Lines = append(s.Lines, l) }
	emitComments := func(file string, line int) {
		anchored := byAnchor[fmt.Sprintf("%s:%d", file, line)]
		// The draft renders last, under any saved comment on the same line, so
		// a reply reads in the order it was written.
		if d.draft != nil && d.draft.File == file && d.draft.Line == line {
			anchored = append(append([]Comment(nil), anchored...), *d.draft)
		}
		for _, c := range anchored {
			draft := d.draft != nil && c.ID == 0 && c.File == d.draft.File && c.Line == d.draft.Line
			for i, row := range d.commentRows(c) {
				row.File = file
				row.New = line
				row.CommentIdx = i
				row.Draft = draft
				emit(row)
			}
		}
	}

	for fi := range d.files {
		fs := &d.files[fi]
		if fi > 0 {
			emit(Line{Kind: LineBlank, File: fs.f.Path})
		}
		s.FileStarts = append(s.FileStarts, len(s.Lines))
		s.Files = append(s.Files, fs.f.Path)
		emit(Line{Kind: LineFileHeader, File: fs.f.Path, Text: fileHeaderText(fs.f)})

		if fs.f.Patch == "" {
			emit(Line{Kind: LineNote, File: fs.f.Path, Text: noPatchReason(fs.f)})
			continue
		}

		for hi := range fs.hunks {
			h := &fs.hunks[hi]
			d.emitGap(&s, fs, gapAt(fs.gaps, hi), emitComments)

			s.HunkStarts = append(s.HunkStarts, len(s.Lines))
			emit(Line{Kind: LineHunkHeader, File: fs.f.Path, Text: h.header})

			for ri := range h.rows {
				r := &h.rows[ri]
				hl := fs.newHL
				if r.kind == LineDel {
					hl = fs.oldHL
				}
				emit(Line{
					Kind: r.kind, Text: r.text, File: fs.f.Path,
					New: r.new, Old: r.old, Marks: r.marks,
					Toks: hl.Line(r.synIdx, r.text),
				})
				if r.new > 0 {
					emitComments(fs.f.Path, r.new)
				}
			}
		}
		d.emitGap(&s, fs, gapAt(fs.gaps, len(fs.hunks)), emitComments)
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
func (d *Doc) emitGap(s *Stream, fs *fileState, g *Gap, emitComments func(string, int)) {
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
		emitComments(fs.f.Path, n)
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

// commentRows renders one comment as the rows it occupies in the stream.
func (d *Doc) commentRows(c Comment) []Line {
	body := wrapBody(c.Body, d.wrapW)
	rows := make([]Line, 0, len(body)+2)
	rows = append(rows, Line{Kind: LineCommentTop, Comment: &c})
	for _, b := range body {
		rows = append(rows, Line{Kind: LineCommentBody, Text: b, Comment: &c})
	}
	rows = append(rows, Line{Kind: LineCommentBottom, Comment: &c})
	return rows
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
