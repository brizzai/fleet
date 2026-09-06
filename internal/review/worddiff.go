package review

import "unicode"

// Span is a half-open range of rune offsets into a line's text.
type Span struct{ Start, End int }

// wordDiffMaxTokens caps the LCS table. The algorithm is O(n·m) and a line of
// minified JavaScript can be thousands of tokens, which is a visible stall on a
// key press for a line nobody can read anyway.
const wordDiffMaxTokens = 240

// wordDiffMinSimilarity is how much two lines must have in common before the
// changed runes are worth marking.
//
// Below it, a paired −/+ is not an edit of the same line, it is two different
// lines that happen to be adjacent — and marking those lights up nearly every
// rune, which is louder than the row tint it sits on and says nothing.
const wordDiffMinSimilarity = 0.34

// WordDiff finds the runs that differ between a deleted line and the added line
// that replaced it, so the reader can brighten only what actually changed.
//
// Word-level rather than rune-level on purpose: a rune diff of `retryDelay` →
// `retryTimeout` marks a scatter of letters inside one identifier, which reads
// as corruption. Splitting on identifier boundaries marks the identifier.
//
// Returns nil for both when the lines are too dissimilar to be one edit.
func WordDiff(oldText, newText string) (delSpans, addSpans []Span) {
	a, b := splitWords(oldText), splitWords(newText)
	if len(a) == 0 || len(b) == 0 || len(a) > wordDiffMaxTokens || len(b) > wordDiffMaxTokens {
		return nil, nil
	}

	keepA, keepB, common := lcsMask(a, b)
	if similarity(a, b, common) < wordDiffMinSimilarity {
		return nil, nil
	}
	return marked(a, keepA), marked(b, keepB)
}

// word is one token with its rune offsets in the source line.
type word struct {
	text       string
	start, end int
}

// splitWords cuts a line into identifier runs, whitespace runs, and single
// punctuation runes.
//
// Punctuation is one token each so that `f(a, b)` → `f(a, c)` marks `b`/`c` and
// not the whole argument list; whitespace is grouped so that a re-indent does
// not mark every column it moved through.
func splitWords(s string) []word {
	var out []word
	rs := []rune(s)
	i := 0
	for i < len(rs) {
		start := i
		switch {
		case isWordRune(rs[i]):
			for i < len(rs) && isWordRune(rs[i]) {
				i++
			}
		case unicode.IsSpace(rs[i]):
			for i < len(rs) && unicode.IsSpace(rs[i]) {
				i++
			}
		default:
			i++
		}
		out = append(out, word{text: string(rs[start:i]), start: start, end: i})
	}
	return out
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// lcsMask marks which tokens on each side are part of the longest common
// subsequence, and returns how many that is.
func lcsMask(a, b []word) (keepA, keepB []bool, common int) {
	n, m := len(a), len(b)
	// table[i][j] = LCS length of a[i:] and b[j:]
	table := make([][]int, n+1)
	for i := range table {
		table[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i].text == b[j].text {
				table[i][j] = table[i+1][j+1] + 1
				continue
			}
			if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}

	keepA, keepB = make([]bool, n), make([]bool, m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i].text == b[j].text:
			keepA[i], keepB[j] = true, true
			common++
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			i++
		default:
			j++
		}
	}
	return keepA, keepB, common
}

// similarity weighs the common tokens against the longer side, ignoring
// whitespace — two lines differing only in indentation are the same line, and
// counting the spaces would call them different.
func similarity(a, b []word, common int) float64 {
	count := func(w []word) int {
		n := 0
		for _, t := range w {
			if !isBlank(t.text) {
				n++
			}
		}
		return n
	}
	na, nb := count(a), count(b)
	longer := na
	if nb > longer {
		longer = nb
	}
	if longer == 0 {
		return 1
	}
	return float64(common) / float64(longer)
}

func isBlank(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// marked turns the unkept tokens into merged spans.
//
// Trailing whitespace is trimmed off a span before it is emitted: a highlighted
// run of trailing spaces is a colored bar hanging off the end of the change
// with no glyph in it, which reads as a rendering fault.
func marked(w []word, keep []bool) []Span {
	var out []Span
	i := 0
	for i < len(w) {
		if keep[i] {
			i++
			continue
		}
		start := i
		for i < len(w) && !keep[i] {
			i++
		}
		end := i
		// Pull blank-only tokens off both ends of the run.
		for start < end && isBlank(w[start].text) {
			start++
		}
		for end > start && isBlank(w[end-1].text) {
			end--
		}
		if start < end {
			out = append(out, Span{Start: w[start].start, End: w[end-1].end})
		}
	}
	return out
}
