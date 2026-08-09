package keylayout

import (
	"testing"
	"unicode/utf8"
)

// These tables were transcribed from keyboard charts, so the tests below check
// the properties a transcription slip would break — a wrong or duplicated
// character is invisible to anyone who doesn't read the script.

// TestNoASCIISource pins the package's safety rule. An ASCII key in the lookup
// would let a remap shadow a real fleet binding (every one of which is ASCII),
// turning a character the user typed on purpose into a different command.
func TestNoASCIISource(t *testing.T) {
	for r, us := range toUS {
		if r < utf8.RuneSelf {
			t.Errorf("ASCII rune %q is a lookup source (maps to %q) — it can shadow a real keybinding", r, us)
		}
	}
}

// TestLayoutsProduceDistinctRunes catches a duplicated character within one
// layout: two US keys claiming the same output means one of them is a typo, and
// the inverse map would silently keep whichever came last.
func TestLayoutsProduceDistinctRunes(t *testing.T) {
	for _, l := range layouts {
		seen := make(map[rune]rune, len(l.keys))
		for us, produced := range l.keys {
			if prev, dup := seen[produced]; dup {
				t.Errorf("%s: %q is produced by both %q and %q", l.name, produced, prev, us)
				continue
			}
			seen[produced] = us
		}
	}
}

// TestLayoutsDoNotCollide guards the merge. If two layouts produced the same
// character, the merged lookup would depend on layout order and one script
// would quietly win — a bug that only shows up for speakers of the loser.
func TestLayoutsDoNotCollide(t *testing.T) {
	owner := make(map[rune]string)
	for _, l := range layouts {
		for _, produced := range l.keys {
			if produced < utf8.RuneSelf {
				continue // dropped by buildToUS, so it can't collide
			}
			if prev, dup := owner[produced]; dup {
				t.Errorf("%q is produced by both %s and %s", produced, prev, l.name)
				continue
			}
			owner[produced] = l.name
		}
	}
}

// TestLayoutsCoverLowercaseLetters asserts every layout reaches every lowercase
// command key, and that each gap is one we decided on rather than one we missed.
func TestLayoutsCoverLowercaseLetters(t *testing.T) {
	// Documented gaps. Both kinds are unfixable, not unfinished: an ASCII
	// producer is dropped by the safety rule, and a ligature arrives as two
	// characters so it is never a single keypress.
	gaps := map[string]map[rune]string{
		"hebrew": {'q': "produces '/'", 'w': "produces '\\''"},
		"greek":  {'q': "produces ';'"},
		"arabic": {'b': "produces the ligature \"لا\""},
	}

	for _, l := range layouts {
		reachable := make(map[rune]bool)
		for us, produced := range l.keys {
			if produced >= utf8.RuneSelf {
				reachable[us] = true
			}
		}
		for us := 'a'; us <= 'z'; us++ {
			if reachable[us] {
				if why, expected := gaps[l.name][us]; expected {
					t.Errorf("%s: %q is now reachable but still listed as a gap (%s) — drop the exception", l.name, us, why)
				}
				continue
			}
			if _, expected := gaps[l.name][us]; !expected {
				t.Errorf("%s: no key maps to %q and it is not a documented gap", l.name, us)
			}
		}
	}
}

// TestCasedLayoutsCoverUppercaseCommands covers fleet's uppercase bindings on
// the layouts that have case at all. Hebrew and Arabic are excluded on purpose:
// their scripts are caseless, so shift+letter is the same byte as letter and no
// table can recover it — see the issue #239 discussion.
func TestCasedLayoutsCoverUppercaseCommands(t *testing.T) {
	commands := []rune{'A', 'D', 'F', 'R', 'S', 'W', 'X', 'Y'}
	// Greek shift+w and shift+s both produce Σ; S owns it (settings), so W is
	// unreachable there by design.
	gaps := map[string]map[rune]bool{"greek": {'W': true}}

	for _, l := range []layout{russian, greek} {
		for _, cmd := range commands {
			if _, ok := l.keys[cmd]; ok {
				continue
			}
			if !gaps[l.name][cmd] {
				t.Errorf("%s: uppercase command %q is unreachable and not a documented gap", l.name, cmd)
			}
		}
	}
}

func TestToUS(t *testing.T) {
	tests := []struct {
		name string
		in   rune
		want rune
		ok   bool
	}{
		{"hebrew j", 'ח', 'j', true},
		{"hebrew k", 'ל', 'k', true},
		{"hebrew a", 'ש', 'a', true},
		{"hebrew d", 'ג', 'd', true},
		{"hebrew context menu", 'ץ', '.', true},
		{"russian j", 'о', 'j', true},
		{"russian uppercase", 'Й', 'Q', true},
		{"russian drawer", 'ё', '`', true},
		{"arabic q", 'ض', 'q', true},
		{"greek a", 'α', 'a', true},
		{"greek uppercase", 'Σ', 'S', true},
		{"ascii letter is never remapped", 'j', 0, false},
		{"ascii punctuation is never remapped", '.', 0, false},
		{"unknown rune", '猫', 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ToUS(tt.in)
			if ok != tt.ok {
				t.Fatalf("ToUS(%q) ok = %v, want %v", tt.in, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("ToUS(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
