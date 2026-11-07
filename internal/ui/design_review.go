package ui

import (
	"fmt"
	"image/color"
	"math"

	"charm.land/lipgloss/v2"
)

// The review reader's colors: diff state and syntax.
//
// Two color languages meet in the diff panel, and keeping them in separate
// channels is the whole reason the reader is legible:
//
//   - The ROW TINT owns added/deleted. A wash behind the line, and a saturated
//     block behind its number.
//   - The FOREGROUND owns syntax. Keyword, type, string, number, comment.
//
// Painting the foreground green to mean "added" — which is what the reader did
// before — spends the only channel that can say what the code *is*, and makes
// word-level diff impossible: you cannot brighten the runes that changed inside
// a line that is already one flat color.
//
// This does not breach "one thing owns a color" (docs/design-system.md §5).
// Green means added in the background and string in the foreground; they are
// different channels of the same cell and never compete for the same pixel.
//
// Every value is DERIVED from the active palette rather than hand-picked per
// theme. Six themes times thirteen colors is seventy-eight values nobody would
// keep in agreement, and a derived set cannot drift: a new theme gets a working
// diff and working syntax colors the moment its palette exists.
//
// Declared bare and constructed only in applyReviewPalette, which ApplyPalette
// calls — the same single-writer rule styles.go documents, so a style this file
// forgets renders as nothing in every theme rather than default-pink in five.
var (
	// Diff state.
	ColorDiffAddBg     color.Color
	ColorDiffAddGutter color.Color
	ColorDiffAddWord   color.Color
	ColorDiffDelBg     color.Color
	ColorDiffDelGutter color.Color
	ColorDiffDelWord   color.Color
	ColorDiffHunkBg    color.Color

	// The row under the diff's cursor. A LIGHTENED version of whatever tint the
	// row already carries, rather than a flat colour of its own: the row still
	// has to say added or deleted while it says "you are here", and a cursor
	// fill that replaced the tint would trade one fact for the other.
	ColorDiffCursorBg    color.Color
	ColorDiffCursorAddBg color.Color
	ColorDiffCursorDelBg color.Color

	// Syntax. Mapped onto hues the palette already defines, so a theme author
	// picks six colors and gets a code highlighter as a side effect.
	ColorSynKeyword color.Color
	ColorSynType    color.Color
	ColorSynString  color.Color
	ColorSynNumber  color.Color
	ColorSynComment color.Color
	ColorSynFunc    color.Color
)

var (
	// Row tints. Applied to the whole row width — a fill that stops mid-row
	// reads as a rendering fault (design-system §2).
	DiffAddRowStyle lipgloss.Style
	DiffDelRowStyle lipgloss.Style
	DiffHunkStyle   lipgloss.Style

	// Cursor row. The full width of the panel, because a highlight that stops
	// after the gutter marks a column rather than a line.
	DiffCursorRowStyle    lipgloss.Style
	DiffCursorAddRowStyle lipgloss.Style
	DiffCursorDelRowStyle lipgloss.Style

	// Gutter cells: the line number sits on a saturated block, so the change
	// rail is readable even where the row tint is too quiet to notice.
	DiffAddGutterStyle lipgloss.Style
	DiffDelGutterStyle lipgloss.Style
	DiffNumStyle       lipgloss.Style

	// Word-level diff: the runes that actually changed between a paired −/+.
	DiffAddWordStyle lipgloss.Style
	DiffDelWordStyle lipgloss.Style

	// Sign column. Keeps its color when the row is tinted, because a tint is
	// easy to miss on a dim monitor and the glyph is not.
	DiffAddSignStyle lipgloss.Style
	DiffDelSignStyle lipgloss.Style

	// Syntax.
	SynKeywordStyle lipgloss.Style
	SynTypeStyle    lipgloss.Style
	SynStringStyle  lipgloss.Style
	SynNumberStyle  lipgloss.Style
	SynCommentStyle lipgloss.Style
	SynFuncStyle    lipgloss.Style
	SynPlainStyle   lipgloss.Style

	// Word-span variants. See applyReviewPalette for why a mark needs its own
	// set rather than reusing the plain ones.
	SynKeywordWordStyle lipgloss.Style
	SynTypeWordStyle    lipgloss.Style
	SynStringWordStyle  lipgloss.Style
	SynNumberWordStyle  lipgloss.Style
	SynCommentWordStyle lipgloss.Style
	SynFuncWordStyle    lipgloss.Style
	SynPlainWordStyle   lipgloss.Style

	// Comment tags, by type. Semantic: an issue is not a nit.
	CommentIssueStyle      lipgloss.Style
	CommentNitStyle        lipgloss.Style
	CommentQuestionStyle   lipgloss.Style
	CommentSuggestionStyle lipgloss.Style

	// FileBandStyle separates two files inside one continuous stream. A band
	// rather than bold text: the reader scrolls, and a header that reads as a
	// line of prose is one you fall straight past.
	FileBandStyle lipgloss.Style

	// Search hit. Deliberately NOT the accent fill — the accent means "the
	// keyboard is here", and a screen with nine highlighted matches would have
	// nine of those. Yellow, the same hue that means "wants attention".
	SearchHitStyle     lipgloss.Style
	SearchCurrentStyle lipgloss.Style
)

const (
	// wordMarkMinContrast is the WCAG ratio text must clear against a word-diff
	// mark. 3.4 is what already reads well elsewhere in the diff: a comment on
	// the quiet added-line wash scores 3.51 in fleet-pink.
	wordMarkMinContrast = 3.4

	// wordMarkMinSeparation is how far the mark must stand off the row it sits
	// on. A mark you cannot see is not marking anything.
	wordMarkMinSeparation = 1.3
)

// relLuminance is WCAG 2.1 relative luminance.
func relLuminance(c color.Color) float64 {
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

// contrast is the WCAG ratio, where 1.0 means the two colours are identical.
func contrast(a, b color.Color) float64 {
	la, lb := relLuminance(a), relLuminance(b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

// markBackground picks how far a word mark is mixed toward its hue.
//
// SEARCHED rather than fixed, because a fixed factor is only ever tuned against
// one palette. The first version used 0.55 and landed on a mid-tone that light
// and dark text both lost against; 0.30 fixed fleet-pink and left five other
// themes with a mark too faint to see, since a palette whose red sits close to
// its background needs a wider spread to separate at all.
func markBackground(bg, hue, quiet, gutter color.Color) color.Color {
	// It must also stay off the GUTTER's tint. The three tints are a ladder —
	// row wash, gutter, word mark — and on nord the search walked straight onto
	// the gutter's value, which puts the same colour in the number cell and in
	// the code beside it and reads as the gutter bleeding into the row.
	const gutterClearance = 1.06
	best := mixColor(bg, hue, 0.30)
	for t := 0.24; t <= 0.72; t += 0.02 {
		c := mixColor(bg, hue, t)
		if contrast(c, gutter) < gutterClearance {
			continue
		}
		best = c
		if contrast(c, quiet) >= wordMarkMinSeparation {
			return c
		}
	}
	return best
}

// liftForMark moves a syntax colour toward the text colour until it is readable
// on every word mark it can land on.
//
// Toward text rather than replacing the colour, so a string on a changed word is
// still recognisably a string — and only as far as it has to go, so the hue
// survives wherever the palette allows.
func liftForMark(fg, text color.Color, bgs ...color.Color) color.Color {
	ok := func(c color.Color) bool {
		for _, bg := range bgs {
			if contrast(c, bg) < wordMarkMinContrast {
				return false
			}
		}
		return true
	}
	for t := 0.0; t < 1.0; t += 0.05 {
		if c := mixColor(fg, text, t); ok(c) {
			return c
		}
	}
	return text
}

// applyReviewPalette derives every review color from p. Called by ApplyPalette;
// never call it anywhere else, or a theme change would leave one set stale.
func applyReviewPalette(p Palette) {
	ColorDiffAddBg = mixColor(p.Bg, p.Green, 0.13)
	ColorDiffAddGutter = mixColor(p.Bg, p.Green, 0.38)
	// Searched, not fixed. At the 0.55 this used to be, the mark landed on a
	// MID-TONE — #567e62 in fleet-pink — the worst place to sit on a dark
	// theme, because light and dark text both lose against it: a comment on it
	// scored a contrast ratio of 1.13, where 1.0 is invisible.
	ColorDiffAddWord = markBackground(p.Bg, p.Green, ColorDiffAddBg, ColorDiffAddGutter)
	ColorDiffDelBg = mixColor(p.Bg, p.Red, 0.13)
	ColorDiffDelGutter = mixColor(p.Bg, p.Red, 0.38)
	ColorDiffDelWord = markBackground(p.Bg, p.Red, ColorDiffDelBg, ColorDiffDelGutter)
	ColorDiffHunkBg = mixColor(p.Bg, p.Border, 0.30)

	// Toward Border rather than toward white: it raises the row's luminance
	// while leaving its hue alone, so an added line under the cursor is still
	// visibly green and a deleted one still red.
	ColorDiffCursorBg = mixColor(p.Bg, p.Border, 0.42)
	ColorDiffCursorAddBg = mixColor(ColorDiffAddBg, p.Border, 0.42)
	ColorDiffCursorDelBg = mixColor(ColorDiffDelBg, p.Border, 0.42)

	ColorSynKeyword = p.Purple
	ColorSynType = p.Blue
	ColorSynString = p.Green
	ColorSynNumber = p.Orange
	ColorSynComment = p.TextDim
	ColorSynFunc = p.Yellow

	DiffAddRowStyle = lipgloss.NewStyle().Background(ColorDiffAddBg)
	DiffDelRowStyle = lipgloss.NewStyle().Background(ColorDiffDelBg)
	DiffHunkStyle = lipgloss.NewStyle().Background(ColorDiffHunkBg).Foreground(p.Blue)

	DiffCursorRowStyle = lipgloss.NewStyle().Background(ColorDiffCursorBg)
	DiffCursorAddRowStyle = lipgloss.NewStyle().Background(ColorDiffCursorAddBg)
	DiffCursorDelRowStyle = lipgloss.NewStyle().Background(ColorDiffCursorDelBg)

	DiffAddGutterStyle = lipgloss.NewStyle().Background(ColorDiffAddGutter).Foreground(p.Text)
	DiffDelGutterStyle = lipgloss.NewStyle().Background(ColorDiffDelGutter).Foreground(p.Text)
	DiffNumStyle = lipgloss.NewStyle().Foreground(mixColor(p.Bg, p.TextDim, 0.75))

	DiffAddWordStyle = lipgloss.NewStyle().Background(ColorDiffAddWord).Foreground(p.Text)
	DiffDelWordStyle = lipgloss.NewStyle().Background(ColorDiffDelWord).Foreground(p.Text)

	DiffAddSignStyle = lipgloss.NewStyle().Background(ColorDiffAddBg).Foreground(p.Green).Bold(true)
	DiffDelSignStyle = lipgloss.NewStyle().Background(ColorDiffDelBg).Foreground(p.Red).Bold(true)

	SynKeywordStyle = lipgloss.NewStyle().Foreground(ColorSynKeyword)
	SynTypeStyle = lipgloss.NewStyle().Foreground(ColorSynType)
	SynStringStyle = lipgloss.NewStyle().Foreground(ColorSynString)
	SynNumberStyle = lipgloss.NewStyle().Foreground(ColorSynNumber)
	SynCommentStyle = lipgloss.NewStyle().Foreground(ColorSynComment).Italic(true)
	SynFuncStyle = lipgloss.NewStyle().Foreground(ColorSynFunc)
	SynPlainStyle = lipgloss.NewStyle().Foreground(p.Text)

	// The same six colours, lifted for a word-diff span.
	//
	// A word mark is the one place the two-channel rule needs a concession: the
	// background is carrying a THIRD fact — "these are the words that changed" —
	// on top of added/deleted, and the syntax colour underneath it was chosen
	// for contrast against the row's quiet wash, not against a mark. Lifting
	// TOWARD TEXT rather than replacing the colour keeps the hue, so a string on
	// a changed word is still recognisably a string.
	//
	// Each is lifted only as far as it has to go to clear both marks, so the
	// hue survives wherever the palette allows it to.
	lift := func(c color.Color) color.Color {
		return liftForMark(c, p.Text, ColorDiffAddWord, ColorDiffDelWord)
	}
	SynKeywordWordStyle = lipgloss.NewStyle().Foreground(lift(ColorSynKeyword))
	SynTypeWordStyle = lipgloss.NewStyle().Foreground(lift(ColorSynType))
	SynStringWordStyle = lipgloss.NewStyle().Foreground(lift(ColorSynString))
	SynNumberWordStyle = lipgloss.NewStyle().Foreground(lift(ColorSynNumber))
	SynCommentWordStyle = lipgloss.NewStyle().Foreground(lift(ColorSynComment)).Italic(true)
	SynFuncWordStyle = lipgloss.NewStyle().Foreground(lift(ColorSynFunc))
	SynPlainWordStyle = lipgloss.NewStyle().Foreground(lift(p.Text))

	CommentIssueStyle = lipgloss.NewStyle().Foreground(p.Red).Bold(true)
	CommentNitStyle = lipgloss.NewStyle().Foreground(p.Blue).Bold(true)
	CommentQuestionStyle = lipgloss.NewStyle().Foreground(p.Yellow).Bold(true)
	CommentSuggestionStyle = lipgloss.NewStyle().Foreground(p.Green).Bold(true)

	FileBandStyle = lipgloss.NewStyle().Background(mixColor(p.Bg, p.Border, 0.55)).Foreground(p.Text).Bold(true)

	SearchHitStyle = lipgloss.NewStyle().Background(mixColor(p.Bg, p.Yellow, 0.40)).Foreground(p.Text)
	SearchCurrentStyle = lipgloss.NewStyle().Background(p.Yellow).Foreground(p.Bg).Bold(true)
}

// mixColor blends b into a at ratio t (0 = all a, 1 = all b).
//
// Its own arithmetic rather than lipgloss.Blend1D: Blend1D returns a gradient
// of n stops and picking "13% along" out of it means choosing an n and rounding
// an index, which is two decisions to get one color. RGBA() reports
// alpha-premultiplied 16-bit components; every palette color is an opaque hex
// literal, so >>8 is the 8-bit channel.
func mixColor(a, b color.Color, t float64) color.Color {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	ch := func(x, y uint32) int {
		v := float64(x>>8)*(1-t) + float64(y>>8)*t
		switch {
		case v < 0:
			return 0
		case v > 255:
			return 255
		}
		return int(v + 0.5)
	}
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x",
		ch(ar, br), ch(ag, bg), ch(ab, bb)))
}
