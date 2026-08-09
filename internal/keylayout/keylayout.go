// Package keylayout maps characters produced by non-Latin keyboard layouts back
// to the US-QWERTY character at the same physical key position.
//
// A terminal reports the character a layout produced, not the key that was
// pressed. fleet dispatches on that character (a switch over msg.String()), so
// with a Hebrew layout the physical `j` key arrives as 'ח', no case matches, and
// every letter command silently does nothing.
//
// The safety rule, enforced once in buildToUS rather than by discipline: a
// mapping only exists when the produced character is non-ASCII. Every fleet
// keybinding is ASCII, so a lookup can never shadow a key the user actually
// pressed — which is why callers need no "is this already bound?" check, and why
// a Latin layout (AZERTY, QWERTZ, Dvorak) is never touched. The cost is that
// positions producing ASCII on their own layout are not remapped: Hebrew's `q`
// emits '/' and Greek's `q` emits ';', and stealing those would cost the user a
// character they meant to type.
package keylayout

import "unicode/utf8"

// layout is one keyboard layout, written as US-QWERTY key -> character produced
// at that physical position. That direction is deliberate: it's how a keyboard
// chart reads, which is the only practical way to review these tables against
// one. ToUS consumes the inverse.
type layout struct {
	name string
	keys map[rune]rune
}

// hebrew is the standard Israeli layout. Caseless: shift produces the same
// letter, so uppercase commands can't be expressed on it at all.
var hebrew = layout{
	name: "hebrew",
	keys: map[rune]rune{
		'q': '/', 'w': '\'', 'e': 'ק', 'r': 'ר', 't': 'א', 'y': 'ט', 'u': 'ו', 'i': 'ן', 'o': 'ם', 'p': 'פ',
		'a': 'ש', 's': 'ד', 'd': 'ג', 'f': 'כ', 'g': 'ע', 'h': 'י', 'j': 'ח', 'k': 'ל', 'l': 'ך', ';': 'ף', '\'': ',',
		'z': 'ז', 'x': 'ס', 'c': 'ב', 'v': 'ה', 'b': 'נ', 'n': 'מ', 'm': 'צ', ',': 'ת', '.': 'ץ', '/': '.',
	},
}

// russian is the standard ЙЦУКЕН layout. Cased, so the capitals are listed too
// and uppercase commands (A, R, S, W, …) work.
var russian = layout{
	name: "russian",
	keys: map[rune]rune{
		'`': 'ё',
		'q': 'й', 'w': 'ц', 'e': 'у', 'r': 'к', 't': 'е', 'y': 'н', 'u': 'г', 'i': 'ш', 'o': 'щ', 'p': 'з', '[': 'х', ']': 'ъ',
		'a': 'ф', 's': 'ы', 'd': 'в', 'f': 'а', 'g': 'п', 'h': 'р', 'j': 'о', 'k': 'л', 'l': 'д', ';': 'ж', '\'': 'э',
		'z': 'я', 'x': 'ч', 'c': 'с', 'v': 'м', 'b': 'и', 'n': 'т', 'm': 'ь', ',': 'б', '.': 'ю',

		'Q': 'Й', 'W': 'Ц', 'E': 'У', 'R': 'К', 'T': 'Е', 'Y': 'Н', 'U': 'Г', 'I': 'Ш', 'O': 'Щ', 'P': 'З',
		'A': 'Ф', 'S': 'Ы', 'D': 'В', 'F': 'А', 'G': 'П', 'H': 'Р', 'J': 'О', 'K': 'Л', 'L': 'Д',
		'Z': 'Я', 'X': 'Ч', 'C': 'С', 'V': 'М', 'B': 'И', 'N': 'Т', 'M': 'Ь',
	},
}

// arabic is the standard Arabic 101 layout. Caseless, like Hebrew.
var arabic = layout{
	name: "arabic",
	keys: map[rune]rune{
		'q': 'ض', 'w': 'ص', 'e': 'ث', 'r': 'ق', 't': 'ف', 'y': 'غ', 'u': 'ع', 'i': 'ه', 'o': 'خ', 'p': 'ح', '[': 'ج', ']': 'د',
		'a': 'ش', 's': 'س', 'd': 'ي', 'f': 'ب', 'g': 'ل', 'h': 'ا', 'j': 'ت', 'k': 'ن', 'l': 'م', ';': 'ك', '\'': 'ط',
		'z': 'ئ', 'x': 'ء', 'c': 'ؤ', 'v': 'ر', 'n': 'ى', 'm': 'ة', ',': 'و', '.': 'ز', '/': 'ظ',
		// 'b' has no entry: that key produces the two-rune ligature "لا", which
		// arrives as two characters and so can never be one keypress to map.
	},
}

// greek is the standard Greek layout. Cased, but shift+w and shift+s both
// produce Σ, so only S carries it — an inverse map can hold one of them, and S
// is the live binding (settings).
var greek = layout{
	name: "greek",
	keys: map[rune]rune{
		'q': ';', 'w': 'ς', 'e': 'ε', 'r': 'ρ', 't': 'τ', 'y': 'υ', 'u': 'θ', 'i': 'ι', 'o': 'ο', 'p': 'π',
		'a': 'α', 's': 'σ', 'd': 'δ', 'f': 'φ', 'g': 'γ', 'h': 'η', 'j': 'ξ', 'k': 'κ', 'l': 'λ',
		'z': 'ζ', 'x': 'χ', 'c': 'ψ', 'v': 'ω', 'b': 'β', 'n': 'ν', 'm': 'μ',

		'A': 'Α', 'B': 'Β', 'C': 'Ψ', 'D': 'Δ', 'E': 'Ε', 'F': 'Φ', 'G': 'Γ', 'H': 'Η', 'I': 'Ι',
		'J': 'Ξ', 'K': 'Κ', 'L': 'Λ', 'M': 'Μ', 'N': 'Ν', 'O': 'Ο', 'P': 'Π', 'R': 'Ρ', 'S': 'Σ',
		'T': 'Τ', 'U': 'Θ', 'V': 'Ω', 'X': 'Χ', 'Y': 'Υ', 'Z': 'Ζ',
	},
}

var layouts = []layout{hebrew, russian, arabic, greek}

var toUS = buildToUS(layouts)

// buildToUS inverts the layout tables, dropping every entry whose produced
// character is ASCII. That drop is the package's safety rule: what survives can
// only ever be a character no fleet keybinding uses.
func buildToUS(ls []layout) map[rune]rune {
	m := make(map[rune]rune)
	for _, l := range ls {
		for us, produced := range l.keys {
			if produced < utf8.RuneSelf {
				continue
			}
			m[produced] = us
		}
	}
	return m
}

// ToUS maps a character produced by a non-Latin keyboard layout to the
// US-QWERTY character at the same physical key. It reports ok=false for
// anything it doesn't know — which includes every ASCII rune, by construction.
func ToUS(r rune) (rune, bool) {
	us, ok := toUS[r]
	return us, ok
}
