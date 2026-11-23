package review

import "testing"

func TestBindRangeLandsOnTheHunk(t *testing.T) {
	d := NewDoc([]File{{Path: "demo.go", Patch: twoHunks}}, "")
	span, ok := BindRange(d, "demo.go", SideNew, addedLine, addedLine)
	if !ok || !span.Exact {
		t.Fatalf("span=%+v ok=%v", span, ok)
	}
	s := d.Stream
	if span.From != s.HunkStarts[1] || span.To != s.HunkEnds[1] {
		t.Errorf("span %+v, want the second hunk [%d,%d)", span, s.HunkStarts[1], s.HunkEnds[1])
	}
	var found bool
	for i := span.From; i < span.To; i++ {
		if s.Lines[i].Kind == LineAdd && s.Lines[i].New == addedLine {
			found = true
		}
	}
	if !found {
		t.Error("the span does not hold the asked line")
	}
}

func TestBindRangeCoversEveryOverlappingHunk(t *testing.T) {
	d := NewDoc([]File{{Path: "demo.go", Patch: twoHunks}}, "")
	span, ok := BindRange(d, "demo.go", SideNew, 1, 200)
	s := d.Stream
	if !ok || span.From != s.HunkStarts[0] || span.To != s.HunkEnds[1] {
		t.Errorf("span %+v, want both hunks [%d,%d)", span, s.HunkStarts[0], s.HunkEnds[1])
	}
}

// The old side counts in old line numbers: the deleted `b := 2` sits at old
// line 80, which on the new side is the context row before the addition.
func TestBindRangeOldSide(t *testing.T) {
	d := NewDoc([]File{{Path: "demo.go", Patch: twoHunks}}, "")
	span, ok := BindRange(d, "demo.go", SideOld, 80, 80)
	if !ok || !span.Exact || span.From != d.Stream.HunkStarts[1] {
		t.Errorf("old-side bind = %+v ok=%v", span, ok)
	}
}

func TestBindRangeFallsBackToTheFileHeader(t *testing.T) {
	d := NewDoc([]File{{Path: "demo.go", Patch: twoHunks}}, "")
	span, ok := BindRange(d, "demo.go", SideNew, 40, 45)
	if !ok || span.Exact {
		t.Fatalf("a line between hunks should fall back, got %+v ok=%v", span, ok)
	}
	if span.From != d.Stream.FileStarts[0] || span.Len() != 1 {
		t.Errorf("fallback span %+v, want the header row", span)
	}
}

func TestBindRangeRefusesAFileNotInTheDiff(t *testing.T) {
	d := NewDoc([]File{{Path: "demo.go", Patch: twoHunks}}, "")
	if _, ok := BindRange(d, "other.go", SideNew, 1, 1); ok {
		t.Error("bound a path the diff does not contain")
	}
	if _, ok := BindRange(nil, "demo.go", SideNew, 1, 1); ok {
		t.Error("bound against a nil doc")
	}
}

// A bound span carries the fold markers on its edges, so a view showing only
// this span can still offer "expand" — and after expanding, a rebind grows to
// hold the revealed context.
func TestBindRangeTakesFoldsAndGrowsAfterExpand(t *testing.T) {
	wt := worktreeWith(t, "demo.go", 140)
	d := NewDoc([]File{{Path: "demo.go", Patch: twoHunks}}, wt)
	span, _ := BindRange(d, "demo.go", SideNew, addedLine, addedLine)
	s := d.Stream
	if s.Lines[span.From].Kind != LineExpand || s.Lines[span.To-1].Kind != LineExpand {
		t.Fatalf("span edges are %v / %v, want fold markers",
			s.Lines[span.From].Kind, s.Lines[span.To-1].Kind)
	}
	before := span.Len()
	if !d.ExpandRows(span.From, span.To, false) {
		t.Fatal("nothing to expand inside the span")
	}
	grown, _ := BindRange(d, "demo.go", SideNew, addedLine, addedLine)
	if grown.Len() <= before {
		t.Errorf("rebound span %d rows, was %d — expansion should have grown it", grown.Len(), before)
	}
	// Never bound onto revealed context: that came from disk, not the patch.
	for i := grown.From; i < grown.To; i++ {
		l := d.Stream.Lines[i]
		if l.Expanded && l.New == addedLine {
			t.Error("the asked line resolved to an expanded row")
		}
	}
}
