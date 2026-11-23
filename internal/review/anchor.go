package review

// RowSpan is a half-open range of stream rows, [From, To).
type RowSpan struct {
	From, To int
	// Exact is false when nothing in the file's diff overlapped the asked
	// lines and the span fell back to the file's header row: being sent to the
	// right file is the substance of an anchor, the exact rows a convenience.
	Exact bool
}

// Len is how many rows the span holds.
func (r RowSpan) Len() int { return max(r.To-r.From, 0) }

// Side names which file a line range counts in.
const (
	SideNew = "new"
	SideOld = "old"
)

// BindRange resolves a line range in one file to the stream rows that show it:
// every hunk of path whose lines on side overlap [start, end], from the first
// such hunk's header to the last one's end — together with any context that
// expansion has revealed directly around them and the fold markers bounding
// it, so a view rendering just this span can offer "expand" on its edges.
//
// side is SideNew or SideOld (anything else reads as new — that is the side a
// reviewer is told to cite). end below start is read as a single line.
//
// The model is answering about a diff, not reading the stream, so a range may
// not land on any hunk. A file that is in the change but whose lines are not
// binds to its header row with Exact false. A path not in the change at all
// returns ok false: there is nowhere honest to send you.
//
// Row indexes go stale on Doc.Expand / ExpandRows; bind again afterwards.
func BindRange(d *Doc, path, side string, start, end int) (span RowSpan, ok bool) {
	if d == nil {
		return RowSpan{}, false
	}
	return bindRange(d.Stream, path, side, start, end)
}

func bindRange(s Stream, path, side string, start, end int) (RowSpan, bool) {
	header := -1
	for i, f := range s.Files {
		if f == path && i < len(s.FileStarts) {
			header = s.FileStarts[i]
			break
		}
	}
	if header < 0 {
		return RowSpan{}, false
	}
	if end < start {
		end = start
	}
	lineOf := func(l Line) int {
		if side == SideOld {
			return l.Old
		}
		return l.New
	}

	first, last := -1, -1
	for hi := range s.HunkStarts {
		from, to := s.HunkStarts[hi], s.HunkEnds[hi]
		if s.Lines[from].File != path {
			continue
		}
		// The hunk's extent on this side, so a range that falls between two
		// of its rows (say, onto a deletion when asking about the new side)
		// still lands on the hunk that contains it.
		lo, hi2 := 0, 0
		for i := from; i < to; i++ {
			l := s.Lines[i]
			if !l.Kind.IsCode() || l.Expanded {
				continue
			}
			n := lineOf(l)
			if n == 0 {
				continue
			}
			if lo == 0 || n < lo {
				lo = n
			}
			if n > hi2 {
				hi2 = n
			}
		}
		if lo == 0 || start > hi2 || end < lo {
			continue
		}
		if first < 0 {
			first = hi
		}
		last = hi
	}
	if first < 0 {
		return RowSpan{From: header, To: header + 1}, true
	}

	from, to := s.HunkStarts[first], s.HunkEnds[last]
	// Grow over revealed context and the fold marker beyond it, on each side.
	// One marker at most per side: past it lies the far half of the gap, which
	// belongs to the neighbouring hunk.
	for from > 0 && s.Lines[from-1].File == path && s.Lines[from-1].Expanded {
		from--
	}
	if from > 0 && s.Lines[from-1].Kind == LineExpand && s.Lines[from-1].File == path {
		from--
	}
	for to < len(s.Lines) && s.Lines[to].File == path && s.Lines[to].Expanded {
		to++
	}
	if to < len(s.Lines) && s.Lines[to].Kind == LineExpand && s.Lines[to].File == path {
		to++
	}
	return RowSpan{From: from, To: to, Exact: true}, true
}

// HunkAt returns the index into HunkStarts of the hunk holding row i, or -1.
func (s Stream) HunkAt(i int) int {
	for h := range s.HunkStarts {
		if i >= s.HunkStarts[h] && i < s.HunkEnds[h] {
			return h
		}
	}
	return -1
}
