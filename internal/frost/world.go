package frost

import (
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// Cell is one screen cell: a grapheme, its column width and its style. Width
// 2 is a wide glyph whose right half is a Width-0 spacer in the next cell.
type Cell struct {
	Content string
	Width   int
	Style   uv.Style
}

var blank = Cell{Content: " ", Width: 1}

// Grid is a fixed-size buffer of cells, row-major.
type Grid struct {
	W, H  int
	cells []Cell
}

// NewGrid returns a w×h grid of blanks.
func NewGrid(w, h int) *Grid {
	g := &Grid{W: w, H: h, cells: make([]Cell, w*h)}
	for i := range g.cells {
		g.cells[i] = blank
	}
	return g
}

// Freeze captures a rendered frame (an ANSI string, one line per row, as
// Bubble Tea would paint it) into a Grid, styles intact. It replays the frame
// through a virtual terminal rather than parsing escapes by hand, so anything
// the real terminal would have shown — colors, bold, faint, wide glyphs —
// lands in the right cell. Lines are joined with CRLF because the emulator
// treats a bare LF as line-feed only.
func Freeze(frame string, w, h int) *Grid {
	g := NewGrid(w, h)
	if w <= 0 || h <= 0 {
		return g
	}
	emu := vt.NewEmulator(w, h)
	_, _ = emu.Write([]byte(strings.ReplaceAll(frame, "\n", "\r\n")))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := emu.CellAt(x, y)
			if c == nil {
				continue
			}
			cell := Cell{Content: c.Content, Width: c.Width, Style: c.Style}
			if cell.Content == "" && cell.Width != 0 {
				cell = Cell{Content: " ", Width: 1, Style: c.Style}
			}
			g.cells[y*w+x] = cell
		}
	}
	return g
}

// In reports whether (x, y) is inside the grid.
func (g *Grid) In(x, y int) bool { return x >= 0 && x < g.W && y >= 0 && y < g.H }

// At returns the cell at (x, y), or a blank outside the grid.
func (g *Grid) At(x, y int) Cell {
	if !g.In(x, y) {
		return blank
	}
	return g.cells[y*g.W+x]
}

// Set writes a cell, keeping wide glyphs consistent: overwriting either half of
// a wide glyph blanks the other half, so a row never comes out a column short.
func (g *Grid) Set(x, y int, c Cell) {
	if !g.In(x, y) {
		return
	}
	i := y*g.W + x
	switch g.cells[i].Width {
	case 2:
		if x+1 < g.W {
			g.cells[i+1] = blank
		}
	case 0:
		if x > 0 {
			g.cells[i-1] = blank
		}
	}
	if c.Width == 0 {
		c.Width = 1
	}
	if c.Content == "" {
		c.Content = " "
	}
	if c.Width == 1 && ansi.StringWidth(c.Content) == 2 {
		c.Width = 2 // a caller that guessed wrong would leave the row a column wide
	}
	g.cells[i] = c
	if c.Width == 2 {
		if x+1 < g.W {
			g.cells[i+1] = Cell{Width: 0}
		} else {
			g.cells[i] = blank
		}
	}
}

// CopyFrom overwrites this grid with src's cells (same dimensions assumed).
func (g *Grid) CopyFrom(src *Grid) { copy(g.cells, src.cells) }

// Solid reports whether the cell is something a probe stops on: any visible
// glyph, or a space carrying a background fill (a selection pill is a wall).
func (g *Grid) Solid(x, y int) bool {
	if !g.In(x, y) {
		return false
	}
	c := g.cells[y*g.W+x]
	if c.Width == 0 {
		return true // right half of a wide glyph
	}
	return (c.Content != "" && c.Content != " ") || c.Style.Bg != nil
}

// Render serialises the grid as one ANSI line per row, emitting a style
// sequence only where the style changes. shift moves the whole picture that
// many columns (screen shake); cells shifted off an edge are dropped and the
// vacated edge is blank.
func (g *Grid) Render(shift int) string {
	var b strings.Builder
	b.Grow(g.W * g.H * 4)
	for y := 0; y < g.H; y++ {
		if y > 0 {
			b.WriteByte('\n')
		}
		var cur uv.Style
		for col := 0; col < g.W; {
			c := blank
			if sx := col - shift; sx >= 0 && sx < g.W {
				c = g.cells[y*g.W+sx]
			}
			if c.Width == 2 && col+1 >= g.W {
				c = blank
			}
			if c.Width == 0 || c.Content == "" {
				c = Cell{Content: " ", Width: 1, Style: c.Style}
			}
			if !c.Style.Equal(&cur) {
				b.WriteString(ansi.ResetStyle)
				if !c.Style.IsZero() {
					b.WriteString(c.Style.String())
				}
				cur = c.Style
			}
			b.WriteString(c.Content)
			col += c.Width
		}
		b.WriteString(ansi.ResetStyle)
	}
	return b.String()
}
