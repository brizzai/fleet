// Package review turns a changeset's patches into the flat line stream a diff
// view scrolls: syntax-highlighted, word-diffed, with the unchanged code
// between hunks folded and expandable from the worktree on disk.
//
// Flat on purpose. A diff is read as a pager, not a file browser: you fall down
// the whole changeset and jump past what you do not care about. There is a
// scroll position and there are jump targets (files, hunks), and that is the
// entire model — which is also what lets a caller render just one slice of it,
// the way the self-review stop card shows only the hunks one decision is about.
//
// Two colour languages meet on a row and never share a channel: the background
// says added or deleted, the foreground says what the code is. See Token for
// the second and internal/ui/design_review.go for both.
//
// This package imports nothing else from fleet. Callers convert whatever their
// source of patches speaks (git, a forge API) into File.
package review

import (
	"strconv"
	"strings"
)

// File is one changed file and its unified diff.
type File struct {
	Path     string
	Previous string // the old path, set on a rename
	// Status is added, modified, removed, renamed, copied or changed — git's
	// letters spelled out, which is also the forge vocabulary.
	Status    string
	Additions int
	Deletions int
	// Patch is the unified diff for this file, starting at its first "@@"
	// (anything before it is ignored). Empty for a binary, for a pure rename
	// and for a file the caller declined to diff because it was too large —
	// all real states the view has to render rather than treat as absent.
	Patch string
	// Binary says the empty Patch is because the file is not text, so the
	// view can say "binary file" instead of guessing between the reasons.
	Binary bool
}

// LineKind says how a row renders.
type LineKind int

const (
	LineContext    LineKind = iota // unchanged code
	LineAdd                        // +
	LineDel                        // −
	LineFileHeader                 // the file's name and counts
	LineHunkHeader                 // @@ … @@
	LineBlank                      // spacing between files
	LineNote                       // "binary file", "file deleted"
	LineExpand                     // "⋯ 31 lines hidden" — a foldable gap
)

// IsCode reports whether a row carries a line of either side's file — the
// only rows a range can anchor to, and the only ones search looks at.
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
	// (a deletion has no new-side number, an addition no old-side).
	New int
	Old int

	// Toks is the syntax breakdown of Text. Empty on rows that carry no code.
	Toks []Token

	// Marks are the runes that changed against this row's counterpart, for
	// word-level highlighting. Empty when the two lines were too dissimilar to
	// be one edit, and on rows with no counterpart at all.
	Marks []Span

	// Expanded marks a context row that came out of the worktree rather than
	// out of the patch. It renders identically; the flag exists so an anchor
	// never binds to it — the reviewer was shown the patch, not these lines.
	Expanded bool

	// Gap is set on LineExpand and is the fold this row opens.
	Gap *Gap
}

// Stream is a whole changeset, flattened.
//
// Row indexes are only meaningful against one flattening: Doc.Expand rebuilds
// the stream, so anything holding an index (a bound range, a cursor) must be
// re-derived afterwards.
type Stream struct {
	Lines []Line
	// FileStarts indexes each file's header row, in order, so next/previous
	// file is a lookup rather than a scan.
	FileStarts []int
	// HunkStarts indexes every hunk header, for next/previous hunk.
	HunkStarts []int
	// HunkEnds is aligned with HunkStarts: one past each hunk's last patch
	// row. Context revealed by expansion sits outside it.
	HunkEnds []int
	// Files is the paths in stream order, aligned with FileStarts.
	Files []string
}

// Build flattens files into one stream with no worktree behind it, so no
// context can be expanded. NewDoc is the full path; this is what the tests and
// any caller that only wants the rows use.
func Build(files []File) Stream {
	return NewDoc(files, "").Stream
}

func fileHeaderText(f File) string {
	name := f.Path
	if f.Status == "renamed" && f.Previous != "" {
		name = f.Previous + " → " + f.Path
	}
	return name + "  +" + strconv.Itoa(f.Additions) + " −" + strconv.Itoa(f.Deletions)
}

func noPatchReason(f File) string {
	switch {
	case f.Binary:
		return "  binary file"
	case f.Status == "removed":
		return "  file deleted"
	case f.Status == "renamed":
		return "  renamed, no content change"
	default:
		return "  no diff shown — too large to render"
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
// when there is none — so "next hunk" at the last hunk holds still rather than
// wrapping into a file you already read.
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
// act on.
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
