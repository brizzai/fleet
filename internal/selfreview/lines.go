package selfreview

import (
	"regexp"
	"strconv"
	"strings"
)

// changedLine is one +/- row of a unified diff. A removed line has no new-side
// number, so it is anchored at the new line it sits before (and an added line
// at the old line it sits before) — that is what lets a range on either side
// claim both halves of a replaced block.
type changedLine struct {
	added  bool
	old    int // removed: its old number; added: the old line it precedes
	newNum int // added: its new number; removed: the new line it precedes
}

type lineRange struct{ start, end int }

type parsedPatch struct {
	lines    []changedLine
	newHunks []lineRange
	oldHunks []lineRange
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func parsePatch(patch string) parsedPatch {
	var p parsedPatch
	if patch == "" {
		return p
	}
	oldNo, newNo := 0, 0
	inHunk := false
	for line := range strings.SplitSeq(patch, "\n") {
		if m := hunkHeader.FindStringSubmatch(line); m != nil {
			a, _ := strconv.Atoi(m[1])
			b := atoiDefault(m[2], 1)
			c, _ := strconv.Atoi(m[3])
			d := atoiDefault(m[4], 1)
			p.oldHunks = append(p.oldHunks, lineRange{a, a + max(b, 1) - 1})
			p.newHunks = append(p.newHunks, lineRange{c, c + max(d, 1) - 1})
			oldNo, newNo = a, c
			if b == 0 {
				oldNo = a + 1 // "-a,0" names the line *before* the insertion
			}
			if d == 0 {
				newNo = c + 1
			}
			inHunk = true
			continue
		}
		if !inHunk || line == "" {
			continue
		}
		switch line[0] {
		case '+':
			p.lines = append(p.lines, changedLine{added: true, old: oldNo, newNum: newNo})
			newNo++
		case '-':
			p.lines = append(p.lines, changedLine{old: oldNo, newNum: newNo})
			oldNo++
		case ' ':
			oldNo++
			newNo++
		case '\\': // "\ No newline at end of file"
		default:
			inHunk = false // "diff --git" of a following file, or junk
		}
	}
	return p
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// covers reports whether a stop hunk claims a changed line.
func (h Hunk) covers(l changedLine) bool {
	n := l.newNum
	if h.Side == "old" {
		n = l.old
	}
	return n >= h.Start && n <= h.End
}

// intersects reports whether a hunk's range overlaps any diff hunk on its side.
func (p parsedPatch) intersects(h Hunk) bool {
	ranges := p.newHunks
	if h.Side == "old" {
		ranges = p.oldHunks
	}
	for _, r := range ranges {
		if h.Start <= r.end && h.End >= r.start {
			return true
		}
	}
	return false
}

// fileLevel is the "whole file" anchor (fleet's fallback, or the model's own
// choice for a file it cannot point into).
func (h Hunk) fileLevel() bool { return h.Start == 0 && h.End == 0 }
