package review

import "testing"

func mark(s string, spans []Span) string {
	rs := []rune(s)
	out, prev := "", 0
	for _, sp := range spans {
		out += string(rs[prev:sp.Start]) + "«" + string(rs[sp.Start:sp.End]) + "»"
		prev = sp.End
	}
	return out + string(rs[prev:])
}

func TestWordDiffMarksOnlyTheChangedToken(t *testing.T) {
	del, add := WordDiff(`issuer := "brizz-demo"`, `issuer := "fleet-demo"`)
	if got := mark(`issuer := "brizz-demo"`, del); got != `issuer := "«brizz»-demo"` {
		t.Errorf("del = %s", got)
	}
	if got := mark(`issuer := "fleet-demo"`, add); got != `issuer := "«fleet»-demo"` {
		t.Errorf("add = %s", got)
	}
}

func TestWordDiffMarksAnAddedArgument(t *testing.T) {
	const o, n = `CommandType::List => ListCommand.execute(&self)`, `CommandType::List { backend } => ListCommand.execute(&self)`
	_, add := WordDiff(o, n)
	if got := mark(n, add); got != `CommandType::List «{ backend }» => ListCommand.execute(&self)` {
		t.Errorf("add = %s", got)
	}
}

// Two lines that merely sit next to each other are not one edit; marking them
// lights up nearly every rune and says less than the row tint already does.
func TestWordDiffRefusesDissimilarLines(t *testing.T) {
	del, add := WordDiff(`return fmt.Errorf("gh timed out")`, `paneFinishedFirstAt time.Time`)
	if del != nil || add != nil {
		t.Errorf("want no marks, got del=%v add=%v", del, add)
	}
}

// A re-indent is not a change of content, and counting the whitespace would
// call it one.
func TestWordDiffIgnoresIndentationForSimilarity(t *testing.T) {
	del, add := WordDiff("\t\tif err != nil {", "    if err != nil {")
	if len(del) != 0 || len(add) != 0 {
		t.Errorf("indent-only change should mark nothing: del=%v add=%v", del, add)
	}
}

func TestWordDiffHandlesEmptyAndHugeLines(t *testing.T) {
	if d, a := WordDiff("", "x"); d != nil || a != nil {
		t.Errorf("empty side should mark nothing")
	}
	huge := ""
	for i := 0; i < wordDiffMaxTokens+10; i++ {
		huge += "a "
	}
	if d, a := WordDiff(huge, huge+"b"); d != nil || a != nil {
		t.Errorf("over the token cap should mark nothing")
	}
}
