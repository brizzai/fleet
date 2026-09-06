// Package review turns a pull request's patches into the flat line stream the
// reader scrolls.
//
// Flat on purpose. The reader is a pager, not a file browser: you fall down the
// whole changeset and jump past what you do not care about, the way you read a
// PR on paper. Nothing here is selectable — there is a scroll position and
// there are jump targets, and that is the entire model.
//
// Two colour languages meet on a row and never share a channel: the background
// says added or deleted, the foreground says what the code is. See Token for
// the second and internal/ui/design_review.go for both.
package review

import (
	"strconv"
	"strings"

	"github.com/brizzai/fleet/internal/github"
)

// LineKind says how a row renders and whether it can be commented on.
type LineKind int

const (
	LineContext    LineKind = iota // unchanged code
	LineAdd                        // +
	LineDel                        // −
	LineFileHeader                 // the file's name and counts
	LineHunkHeader                 // @@ … @@
	LineBlank                      // spacing between files
	LineNote                       // "binary file", "too large to diff"
	LineExpand                     // "⋯ 31 lines hidden" — a foldable gap
	LineCommentTop
	LineCommentBody
	LineCommentBottom
)

// IsComment reports whether this row is part of a rendered comment box.
func (k LineKind) IsComment() bool {
	return k == LineCommentTop || k == LineCommentBody || k == LineCommentBottom
}

// IsCode reports whether a row carries a line of either side's file — the only
// rows that can anchor a comment, and the only ones search looks at.
func (k LineKind) IsCode() bool {
	return k == LineContext || k == LineAdd || k == LineDel
}

// Line is one row of the stream.
type Line struct {
	Kind LineKind
	Text string

	// File is the path this row belongs to — set on every row including the
	// headers, so "which file am I in" is answerable from the cursor alone
	// without scanning backwards.
	File string

	// New and Old are the line numbers on each side, 0 where the row has none
	// (a deletion has no new-side number, an addition no old-side). New is
	// what a comment anchors to, because GitHub positions review comments
	// against the head of the PR.
	New int
	Old int

	// Toks is the syntax breakdown of Text. Empty on rows that carry no code.
	Toks []Token

	// Marks are the runes that changed against this row's counterpart, for
	// word-level highlighting. Empty when the two lines were too dissimilar to
	// be one edit, and on rows with no counterpart at all.
	Marks []Span

	// Expanded marks a context row that came out of the review worktree rather
	// than out of GitHub's patch. It renders identically; the flag exists so
	// the reader never offers to comment on one — GitHub can only anchor a
	// review comment inside the diff it was sent.
	Expanded bool

	// Gap is set on LineExpand and is the fold this row opens.
	Gap *Gap

	// Comment is set on the three comment kinds. CommentIdx is the row's
	// position within the box, so the reader can draw a border only on the
	// first and last.
	Comment    *Comment
	CommentIdx int

	// Draft marks the comment box currently being typed into, so it renders
	// with the caret and the focused border rather than as a saved note.
	Draft bool
}

// Stream is a whole changeset, flattened.
type Stream struct {
	Lines []Line
	// FileStarts indexes each file's header row, in order, so `.` and `,` are
	// a lookup rather than a scan.
	FileStarts []int
	// HunkStarts indexes every hunk header, for `]` and `[`.
	HunkStarts []int
	// Files is the paths in stream order, aligned with FileStarts.
	Files []string
}

// Build flattens files into one stream with no worktree behind it, so no
// context can be expanded. NewDoc is the full path; this is what the tests and
// any caller that only wants the rows use.
func Build(files []github.PRFile) Stream {
	return NewDoc(files, "", 60).Stream
}

func fileHeaderText(f github.PRFile) string {
	name := f.Path
	if f.Status == "renamed" && f.Previous != "" {
		name = f.Previous + " → " + f.Path
	}
	return name + "  +" + strconv.Itoa(f.Additions) + " −" + strconv.Itoa(f.Deletions)
}

func noPatchReason(f github.PRFile) string {
	switch f.Status {
	case "removed":
		return "  file deleted"
	case "renamed":
		return "  renamed, no content change"
	default:
		return "  no diff shown — binary, or too large for GitHub to render"
	}
}

// parseHunkHeader reads the starting line numbers out of "@@ -a,b +c,d @@".
//
// Returns (1,1) on anything it cannot read: a wrong number renders a diff with
// slightly off line numbers, where a panic or a dropped hunk loses the code.
func parseHunkHeader(h string) (oldStart, newStart int) {
	o, n, _ := parseHunkRange(h)
	return o, n
}

// parseHunkRange also returns the new side's length, which is what says where
// the gap after this hunk begins.
//
// A hunk with no comma is one line long ("@@ -1 +1 @@"), which is the form that
// makes a naive split return a zero count and put the next gap on top of the
// hunk it follows.
func parseHunkRange(h string) (oldStart, newStart, newCount int) {
	oldStart, newStart, newCount = 1, 1, 1
	body, _, ok := strings.Cut(strings.TrimPrefix(h, "@@ "), " @@")
	if !ok {
		return
	}
	for _, part := range strings.Fields(body) {
		num, count, hasCount := strings.Cut(strings.TrimLeft(part, "-+"), ",")
		v, err := strconv.Atoi(num)
		if err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(part, "-"):
			oldStart = v
		case strings.HasPrefix(part, "+"):
			newStart = v
			newCount = 1
			if hasCount {
				if c, err := strconv.Atoi(count); err == nil {
					newCount = c
				}
			}
		}
	}
	return
}

// NextIndex returns the first jump target strictly after from, or from itself
// when there is none — so `]` at the last hunk holds still rather than wrapping
// into a file you already read.
func NextIndex(targets []int, from int) int {
	for _, t := range targets {
		if t > from {
			return t
		}
	}
	return from
}

// PrevIndex returns the last jump target strictly before from, or from.
func PrevIndex(targets []int, from int) int {
	out := from
	for _, t := range targets {
		if t >= from {
			break
		}
		out = t
	}
	return out
}

// FileAt returns the path the row at i belongs to.
func (s Stream) FileAt(i int) string {
	if i < 0 || i >= len(s.Lines) {
		return ""
	}
	return s.Lines[i].File
}

// Search returns every row index whose code contains needle, case-insensitively.
//
// Code rows only: matching a file header would put a hit on a row you cannot
// act on, and matching a comment you wrote yourself makes `/` find your own
// words instead of the code.
func (s Stream) Search(needle string) []int {
	if needle == "" {
		return nil
	}
	lower := strings.ToLower(needle)
	var hits []int
	for i, l := range s.Lines {
		if l.Kind.IsCode() && strings.Contains(strings.ToLower(l.Text), lower) {
			hits = append(hits, i)
		}
	}
	return hits
}
