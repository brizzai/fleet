package review

import (
	"testing"

	"github.com/brizzai/fleet/internal/github"
)

const patch = `@@ -102,6 +102,10 @@ func NewConversationSpan(
 	return span
 }
 
+type ConversationEvent struct {
+	TraceId string
+}
+
 // ConversationFilters represents filtering options
 type ConversationFilters struct {`

func TestBuildLineNumbers(t *testing.T) {
	s := Build([]github.PRFile{{Path: "conv.go", Additions: 4, Patch: patch}})

	// The header, then the hunk header, then rows.
	if len(s.FileStarts) != 1 || len(s.HunkStarts) != 1 {
		t.Fatalf("starts: files=%v hunks=%v", s.FileStarts, s.HunkStarts)
	}

	var adds []int
	for _, l := range s.Lines {
		if l.Kind == LineAdd {
			adds = append(adds, l.New)
		}
	}
	// Hunk starts at new line 102. Three context rows first — `return span`,
	// `}` and the blank line between — so 102,103,104, and the additions run
	// 105..108. The blank context row is the one easy to miscount.
	want := []int{105, 106, 107, 108}
	if len(adds) != len(want) {
		t.Fatalf("got %d additions %v, want %v", len(adds), adds, want)
	}
	for i := range want {
		if adds[i] != want[i] {
			t.Errorf("addition %d at new line %d, want %d", i, adds[i], want[i])
		}
	}
}

func TestNoPatchIsNotSilent(t *testing.T) {
	s := Build([]github.PRFile{{Path: "logo.png", Status: "modified"}})
	found := false
	for _, l := range s.Lines {
		if l.Kind == LineNote {
			found = true
		}
	}
	if !found {
		t.Error("a file with no patch must say so, not render as an empty gap")
	}
}

func TestJumpTargetsDoNotWrap(t *testing.T) {
	targets := []int{5, 12, 30}
	if got := NextIndex(targets, 30); got != 30 {
		t.Errorf("next from last = %d, want it to hold at 30", got)
	}
	if got := PrevIndex(targets, 5); got != 5 {
		t.Errorf("prev from first = %d, want it to hold at 5", got)
	}
	if got := NextIndex(targets, 6); got != 12 {
		t.Errorf("next from 6 = %d, want 12", got)
	}
}

func TestHunkHeaderParse(t *testing.T) {
	o, n := parseHunkHeader("@@ -102,6 +102,28 @@ func NewConversationSpan(")
	if o != 102 || n != 102 {
		t.Errorf("got old=%d new=%d", o, n)
	}
	o, n = parseHunkHeader("@@ -1 +1 @@")
	if o != 1 || n != 1 {
		t.Errorf("single-line hunk: old=%d new=%d", o, n)
	}
	// Garbage must degrade, never panic or drop the hunk.
	if o, n = parseHunkHeader("@@ nonsense"); o != 1 || n != 1 {
		t.Errorf("garbage header: old=%d new=%d", o, n)
	}
}
