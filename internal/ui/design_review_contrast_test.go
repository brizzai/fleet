package ui

import (
	"image/color"
	"math"
	"testing"

	"github.com/brizzai/fleet/internal/review"
)

// wcagLuminance is the relative luminance of a colour, per WCAG 2.1.
func wcagLuminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	f := func(v uint32) float64 {
		x := float64(v>>8) / 255
		if x <= 0.03928 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*f(r) + 0.7152*f(g) + 0.0722*f(b)
}

// contrastRatio is WCAG's, where 1.0 means the two colours are identical.
func contrastRatio(a, b color.Color) float64 {
	la, lb := wcagLuminance(a), wcagLuminance(b)
	hi, lo := math.Max(la, lb), math.Min(la, lb)
	return (hi + 0.05) / (lo + 0.05)
}

// minWordContrast is what already reads well elsewhere in the diff: a comment
// on the quiet added-line wash scores 3.51 in fleet-pink, and nobody has ever
// complained about that one.
const minWordContrast = wordMarkMinContrast

// A word-diff mark is the one place the two-channel rule needs a concession.
// The background carries a THIRD fact — "these words changed" — on top of
// added/deleted, and the syntax colours were chosen for contrast against the
// row's quiet wash rather than against a mark. Reusing them put a comment on a
// changed word at a contrast ratio of 1.13, where 1.0 is invisible.
//
// Every theme, because the colours are derived and a seventh palette gets the
// diff for free — including this bug, if nothing checks.
func TestWordDiffTextStaysReadable(t *testing.T) {
	classes := map[string]review.TokenClass{
		"plain":   review.TokPlain,
		"keyword": review.TokKeyword,
		"type":    review.TokType,
		"string":  review.TokString,
		"number":  review.TokNumber,
		"comment": review.TokComment,
		"func":    review.TokFunc,
	}

	for _, p := range BuiltinPalettes {
		ApplyPalette(p)
		for _, bg := range []struct {
			name string
			c    color.Color
		}{
			{"added", ColorDiffAddWord},
			{"deleted", ColorDiffDelWord},
		} {
			for name, class := range classes {
				fg := wordRunStyle(class).GetForeground()
				if got := contrastRatio(fg, bg.c); got < minWordContrast {
					t.Errorf("%s: %s on a %s word mark scores %.2f, want >= %.2f — "+
						"at 1.0 the text is the background",
						p.Name, name, bg.name, got, minWordContrast)
				}
			}
		}
	}
	ApplyPalette(PaletteFleetPink)
}

// The mark still has to look like a mark. If the background drifts toward the
// row's own wash to win the contrast test, the word diff stops saying anything.
func TestWordDiffMarkStaysVisible(t *testing.T) {
	const minMarkSeparation = wordMarkMinSeparation
	for _, p := range BuiltinPalettes {
		ApplyPalette(p)
		for _, pair := range []struct {
			name        string
			quiet, mark color.Color
		}{
			{"added", ColorDiffAddBg, ColorDiffAddWord},
			{"deleted", ColorDiffDelBg, ColorDiffDelWord},
		} {
			if got := contrastRatio(pair.quiet, pair.mark); got < minMarkSeparation {
				t.Errorf("%s: the %s word mark is only %.2f against its own row — "+
					"a mark you cannot see is not marking anything",
					p.Name, pair.name, got)
			}
		}
	}
	ApplyPalette(PaletteFleetPink)
}
