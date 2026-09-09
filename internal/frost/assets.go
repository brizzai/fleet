// Package frost freezes a rendered frame into a grid of styled cells and runs
// cell-level effects over it: a small side-view sim with one driveable actor,
// ballistic probes, impact damage and debris that settles under gravity. It is
// a pure simulation — no Bubble Tea, no tmux — so the UI only freezes a frame,
// forwards keys and ticks, and asks for the next frame to draw.
package frost

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"hash/fnv"
	"image/color"
	"io"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
)

// Sprite geometry. The glyph strings are unpacked from pack at init; the
// tables here only carry positions.
const (
	SpriteW = 13
	SpriteH = 4

	pivotRow = 2 // the arm pivots off this sprite row
)

var (
	spriteRows [SpriteH]string // the actor, facing right
	altBase    string          // bottom row shifted one cell; alternated while moving
	arms       [4]arm          // one sprite per elevation bucket
	bursts     [4][]burstCell  // impact animation frames
	card       []string        // the card stamped into the frozen frame
	quips      []string        // what the actor says, by event (see quip*)
)

// cardRows is how many pack strings the card takes; the rest are quips.
const cardRows = 8

// Quip indexes into quips.
const (
	quipStart = iota
	quipFirstHit
	quipSessionHit
	quipRazed100
	quipRazed300
	quipBuried
	quipSelfHit
	quipBye
	quipCrit

	quipCount
)

// armCell is one glyph of an arm sprite relative to its pivot: dx grows away
// from the base in the facing direction, dy < 0 is up.
type armCell struct {
	dx, dy int
	g      rune
}

type arm struct {
	cells []armCell
	tip   [2]int // where a probe leaves
}

// burstCell is one glyph of an impact frame relative to the impact cell; the
// frame's colour comes from the burst palette it is drawn with.
type burstCell struct {
	dx, dy int
	g      string
}

var armGeom = [4]struct {
	at  [][2]int
	tip [2]int
}{
	{[][2]int{{0, 0}, {1, 0}, {2, 0}, {3, 0}}, [2]int{4, 0}},
	{[][2]int{{0, 0}, {1, 0}, {2, -1}, {3, -1}}, [2]int{4, -2}},
	{[][2]int{{0, 0}, {1, -1}, {2, -2}, {3, -3}}, [2]int{3, -4}},
	{[][2]int{{0, 0}, {0, -1}, {0, -2}, {0, -3}}, [2]int{0, -4}},
}

var burstGeom = [4][][2]int{
	{{0, 0}},
	{{-1, 0}, {0, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}},
	{{-1, -1}, {0, -1}, {1, -1}, {-2, 0}, {-1, 0}, {0, 0}, {1, 0}, {2, 0}, {-1, 1}, {0, 1}, {1, 1}},
	{{-2, -1}, {2, -1}, {0, 1}},
}

// armBucket picks the arm sprite for an elevation in degrees (0 = level).
func armBucket(angle int) int {
	switch {
	case angle < 15:
		return 0
	case angle < 45:
		return 1
	case angle < 75:
		return 2
	default:
		return 3
	}
}

// mirror maps each quadrant/box glyph to its horizontal reflection. Anything
// absent is symmetric.
var mirror = map[rune]rune{
	'▐': '▌', '▌': '▐',
	'▛': '▜', '▜': '▛',
	'▝': '▘', '▘': '▝',
	'▗': '▖', '▖': '▗',
	'▟': '▙', '▙': '▟',
	'▚': '▞', '▞': '▚',
	'╱': '╲', '╲': '╱',
	'▸': '◂', '◂': '▸',
}

func mirrorRune(r rune) rune {
	if m, ok := mirror[r]; ok {
		return m
	}
	return r
}

// mirrorRow reflects a sprite row: reverse the cells, then reflect each glyph.
func mirrorRow(s string) string {
	rs := []rune(s)
	out := make([]rune, len(rs))
	for i, r := range rs {
		out[len(rs)-1-i] = mirrorRune(r)
	}
	return string(out)
}

func rgb(r, g, b uint8) uv.Style { return uv.Style{Fg: color.RGBA{R: r, G: g, B: b, A: 255}} }

var (
	actorStyle  = rgb(217, 119, 87)
	baseStyle   = rgb(138, 143, 152)
	trackStyle  = rgb(92, 99, 112)
	armStyle    = rgb(184, 190, 200)
	probeStyle  = rgb(249, 226, 175)
	critStyle   = rgb(140, 195, 255) // a crit: the ball's rim, burst body
	critGlow    = rgb(232, 244, 255) // a crit: the ball's core, trail, burst flash
	flashStyle  = rgb(249, 226, 175)
	fireStyle   = rgb(250, 179, 135)
	emberStyle  = rgb(120, 124, 140)
	traceStyle  = rgb(168, 152, 112)
	statusStyle = rgb(120, 124, 140)

	// The card is filled so it reads over whatever the frozen frame has
	// behind it; the fill also makes its blank cells solid. Neutral gray on
	// purpose: a terminal without truecolor snaps a dark blue-gray to the
	// nearest of 256 colors, which is navy; a gray stays on the gray ramp.
	cardBg         = color.RGBA{R: 48, G: 48, B: 48, A: 255}
	cardStyle      = uv.Style{Fg: color.RGBA{R: 200, G: 204, B: 216, A: 255}, Bg: cardBg}
	cardTitleStyle = uv.Style{Fg: color.RGBA{R: 217, G: 119, B: 87, A: 255}, Bg: cardBg}
	cardEdgeStyle  = uv.Style{Fg: color.RGBA{R: 110, G: 114, B: 130, A: 255}, Bg: cardBg}
	bubbleStyle    = uv.Style{Fg: color.RGBA{R: 230, G: 232, B: 240, A: 255}, Bg: cardBg}
)

// Burst palettes, one style per animation frame.
var (
	burstStyles     = [4]uv.Style{flashStyle, flashStyle, fireStyle, emberStyle}
	critBurstStyles = [4]uv.Style{critGlow, critGlow, critStyle, emberStyle}
)

// pack is the glyph data: a gzip-compressed JSON array of strings, in the
// order init reads them.
const pack = "H4sIAAAAAAACE7VRvW4TQRB+lQ83aaw8AI0VAQUCUUEVpVjfje9W5+weu3ux3FkRslJQxIl9/AWno0Aioedt9knYv7NsJQoSgr3R3Hwz387fHvYA2Pbctl9se9aJt52/10fPR7/a9morupFZotj2+r7wtnxKxM+2XT+sE3H9R64nuh46+RjxuwC8jnjdSRvx+04S/h5/H0KXbZj1bJNsYduLoKNxuQsXG9KiA6sfdjn797K6CdmXp4jHXv3Ck5cHb54+w+uDVy8CjoHl6S7z3rPLsvNzO78AcsVPKDoWdn4JMH58l61rljnWiCunSWfAkGXVX1WmE1JTU3JRgGswGKYKMlus1c//s81bn31MZk9D0YhlRiq4PvS+90tZR8OUzEAKwoRpTKSqXKMh8By6lM04h+82TG9kIKRrbhgnIy4oOOzsW8g7CWg4jd5MccMzNkbJTXSMWZOTW4KoBo/SNxj4CDFtSIGKApoy5YqWPM9JQDeqVlzHS0gJKinYMQ8VVR98hKlsUAk5cSMSaqa1azV/7AlC1pR6JkV7/g1SgYqmznzbkHCPLUW4qnlOQ6b2QbwoDWpFWpPuQ0i4XboO3dqOfgOrFdDxUAQAAA=="

func init() {
	raw, err := base64.StdEncoding.DecodeString(pack)
	if err != nil {
		panic(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		panic(err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		panic(err)
	}
	var s []string
	if err := json.Unmarshal(data, &s); err != nil {
		panic(err)
	}
	copy(spriteRows[:], s[0:SpriteH])
	altBase = s[SpriteH]
	at := SpriteH + 1
	for i := range arms {
		rs := []rune(s[at+i])
		for j, p := range armGeom[i].at {
			arms[i].cells = append(arms[i].cells, armCell{p[0], p[1], rs[j]})
		}
		arms[i].tip = armGeom[i].tip
	}
	at += len(arms)
	for i := range bursts {
		rs := []rune(s[at+i])
		for j, p := range burstGeom[i] {
			bursts[i] = append(bursts[i], burstCell{p[0], p[1], string(rs[j])})
		}
	}
	at += len(bursts)
	card = s[at : at+cardRows]
	at += cardRows
	quips = s[at : at+quipCount]
	gate = GateText{Label: s[at+quipCount], Keywords: s[at+quipCount+1], Prompt: s[at+quipCount+2], Wrong: s[at+quipCount+3], Hint: s[at+quipCount+4]}
}

// GateText is the copy for the prompt that fronts frost mode elsewhere in the
// app: how the entry is found, the hint it always shows, what it asks for,
// and what it says to a wrong answer. The right answer starts a run.
type GateText struct {
	Label    string // the entry's name
	Keywords string // extra search terms that surface it
	Prompt   string // what it asks for
	Wrong    string // said to a wrong answer
	Hint     string // the other way in, always shown
}

var gate GateText

// Gate returns the prompt's copy.
func Gate() GateText { return gate }

// gateKey is the hash of the answer the prompt accepts.
var gateKey uint64 = 0x59f74680b7643863

// Unlock reports whether answer is the one the prompt accepts. Case and
// surrounding space do not count.
func Unlock(answer string) bool {
	f := fnv.New64a()
	_, _ = f.Write([]byte(strings.ToLower(strings.TrimSpace(answer))))
	return f.Sum64() == gateKey
}
