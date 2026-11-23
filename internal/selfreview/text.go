package selfreview

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// cleanLine makes model or diff-derived text safe to put on one terminal line:
// escape sequences and other control characters are removed (the diff is
// attacker-influenced, and a field that carried an ESC into the TUI could
// repaint the screen), and all whitespace runs collapse to one space.
func cleanLine(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	s = stripEscapes(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		case unicode.IsControl(r), r == '\u200b', r == '\ufeff', isBidi(r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// cleanBlock is cleanLine for multi-line input (prompts, claims, rules): it
// keeps newlines and tabs, drops other control characters.
func cleanBlock(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	s = stripEscapes(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r':
			b.WriteRune('\n')
		case unicode.IsControl(r), isBidi(r):
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// isBidi reports the bidi override/isolate controls (Trojan Source).
func isBidi(r rune) bool {
	return (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069')
}

// stripEscapes removes ANSI CSI/OSC sequences, which are made of printable
// characters after the ESC and would otherwise survive as visible junk.
func stripEscapes(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != 0x1b {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(s) {
			break
		}
		switch s[i+1] {
		case '[': // CSI: params then a final byte in 0x40–0x7e
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j
		case ']': // OSC: until BEL or ST
			j := i + 2
			for j < len(s) {
				if s[j] == 0x07 {
					break
				}
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					j++
					break
				}
				j++
			}
			i = j
		default:
			i++ // two-byte escape
		}
	}
	return b.String()
}

// capWords keeps at most n words, marking a cut with "…". It reports whether
// it cut.
func capWords(s string, n int) (string, bool) {
	w := strings.Fields(s)
	if len(w) <= n {
		return strings.Join(w, " "), false
	}
	return strings.Join(w[:n], " ") + "…", true
}

// VerdictMinWords is the shortest clause capVerdict will cut back to; a
// shorter one says too little to be worth the clean cut.
const VerdictMinWords = 8

// capVerdict holds a verdict to n words without cutting mid-clause: it ends at
// the last clause boundary at or before the budget — after ";", ",", ":" or
// "—", or before "and"/"but" — when that keeps at least VerdictMinWords words,
// and at the budget itself otherwise. "…" marks a cut, and only a cut.
func capVerdict(s string, n int) (string, bool) {
	w := strings.Fields(s)
	if len(w) <= n {
		return strings.Join(w, " "), false
	}
	keep := n
	for k := n; k >= VerdictMinWords; k-- {
		if clauseEndsAfter(w, k) {
			keep = k
			break
		}
	}
	out := strings.Join(w[:keep], " ")
	out = strings.TrimRight(out, ";,:—–- ")
	return out + "…", true
}

// clauseEndsAfter reports whether a clause boundary falls between w[k-1] and
// w[k].
func clauseEndsAfter(w []string, k int) bool {
	if k <= 0 || k >= len(w) {
		return false
	}
	if last := w[k-1]; strings.HasSuffix(last, ";") || strings.HasSuffix(last, ",") ||
		strings.HasSuffix(last, ":") || strings.HasSuffix(last, "—") {
		return true
	}
	switch strings.ToLower(w[k]) {
	case "and", "but", "—", "–":
		return true
	}
	return false
}

// capRunes keeps at most n runes (including the "…" marking a cut).
func capRunes(s string, n int) (string, bool) {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s, false
	}
	r := []rune(s)
	return strings.TrimRightFunc(string(r[:n-1]), unicode.IsSpace) + "…", true
}

// shortSHA is the 7-character form people read.
func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
