package ui

import (
	"fmt"
	"image/color"

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

// applyReviewPalette derives every review color from p. Called by ApplyPalette;
// never call it anywhere else, or a theme change would leave one set stale.
func applyReviewPalette(p Palette) {
	ColorDiffAddBg = mixColor(p.Bg, p.Green, 0.13)
	ColorDiffAddGutter = mixColor(p.Bg, p.Green, 0.38)
	ColorDiffAddWord = mixColor(p.Bg, p.Green, 0.55)
	ColorDiffDelBg = mixColor(p.Bg, p.Red, 0.13)
	ColorDiffDelGutter = mixColor(p.Bg, p.Red, 0.38)
	ColorDiffDelWord = mixColor(p.Bg, p.Red, 0.55)
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
