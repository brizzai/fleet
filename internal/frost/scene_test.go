package frost

import (
	"math"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// screenWith builds a blank w×h frame with text written at (x, y).
func screenWith(w, h, x, y int, text string) *Grid {
	g := NewGrid(w, h)
	for _, r := range text {
		cw := ansi.StringWidth(string(r))
		g.Set(x, y, Cell{Content: string(r), Width: cw})
		x += cw
	}
	return g
}

// settle runs the entrance so the actor is parked and taking input.
func settle(s *Scene) {
	for i := 0; i < 100 && s.entering; i++ {
		s.Step()
	}
}

func TestMirrorIsAnInvolution(t *testing.T) {
	for _, row := range spriteRows {
		if got := mirrorRow(mirrorRow(row)); got != row {
			t.Errorf("mirror twice: %q → %q", row, got)
		}
	}
	for r, m := range mirror {
		if back := mirror[m]; back != r {
			t.Errorf("mirror[%q]=%q but mirror[%q]=%q", r, m, m, back)
		}
	}
}

func TestUnpackedAssetsHaveTheExpectedShape(t *testing.T) {
	for i, row := range spriteRows {
		if n := len([]rune(row)); n != SpriteW {
			t.Errorf("row %d is %d wide, want %d", i, n, SpriteW)
		}
	}
	if n := len([]rune(altBase)); n != SpriteW {
		t.Errorf("altBase is %d wide, want %d", n, SpriteW)
	}
	for i, a := range arms {
		if len(a.cells) != len(armGeom[i].at) {
			t.Errorf("arm %d has %d cells, want %d", i, len(a.cells), len(armGeom[i].at))
		}
	}
	for i, b := range bursts {
		if len(b) != len(burstGeom[i]) {
			t.Errorf("burst %d has %d cells, want %d", i, len(b), len(burstGeom[i]))
		}
	}
	if len(card) != cardRows {
		t.Fatalf("card has %d rows, want %d", len(card), cardRows)
	}
	if len(quips) != quipCount {
		t.Fatalf("%d quips unpacked, want %d", len(quips), quipCount)
	}
	w := len([]rune(card[0]))
	for i, row := range card {
		if n := len([]rune(row)); n != w {
			t.Errorf("card row %d is %d wide, want %d", i, n, w)
		}
	}
}

func TestFreezeKeepsTextAndStyle(t *testing.T) {
	frame := "ab\n" + "\x1b[38;2;217;119;87mcd\x1b[m"
	g := Freeze(frame, 4, 2)
	if got := g.At(0, 0).Content + g.At(1, 0).Content; got != "ab" {
		t.Fatalf("row 0: got %q", got)
	}
	if c := g.At(0, 1); c.Content != "c" || c.Style.Fg == nil {
		t.Fatalf("row 1 col 0: %+v, want styled c", c)
	}
	if c := g.At(2, 0); !c.Style.IsZero() || c.Content != " " {
		t.Fatalf("unwritten cell should be a plain blank: %+v", c)
	}
	if got := ansi.Strip(g.Render(0)); got != "ab  \ncd  " {
		t.Fatalf("render: %q", got)
	}
}

func TestImpactCracksThenClearsAndShedsDebris(t *testing.T) {
	s := New(screenWith(40, 10, 10, 3, "hello world"), 1)
	settle(s)
	// Hit the 'w' (x=16). The core — that cell and two either side — is
	// cleared outright; the rest of the footprint only cracks.
	s.impact(16, 3, false)
	if got := ansi.Strip(s.world.Render(0)); !strings.Contains(got, "hel▒     ▒d") {
		t.Fatalf("after one hit: %q", got)
	}
	if len(s.falling) == 0 {
		t.Fatal("cleared glyphs should become debris")
	}
	if c := s.falling[0].cell.Content; c == " " || c == "▒" {
		t.Fatalf("debris should carry the original glyph, got %q", c)
	}
	s.impact(16, 3, false)
	if c := s.world.At(13, 3); c.Content != " " {
		t.Fatalf("second hit should clear the cracked cell, got %q", c.Content)
	}
}

func TestDebrisLandsAndActorClimbsIt(t *testing.T) {
	s := New(NewGrid(40, 10), 1)
	settle(s)
	before := s.top()
	under, _ := s.baseSpan()
	for i := 0; i < 3; i++ {
		s.falling = append(s.falling, Debris{x: float64(under + 3), y: 2, cell: Cell{Content: "x", Width: 1}})
		for step := 0; step < 200 && len(s.falling) > 0; step++ {
			s.stepDebris()
		}
	}
	if len(s.falling) != 0 {
		t.Fatal("debris never landed")
	}
	total := 0
	for x := range s.heap {
		total += len(s.heap[x])
	}
	if total != 3 {
		t.Fatalf("heap holds %d cells, want 3", total)
	}
	if got := s.top(); got >= before {
		t.Fatalf("actor should ride up on debris: top %d, was %d", got, before)
	}
}

func TestHeapRelaxesIntoAMound(t *testing.T) {
	s := New(NewGrid(20, 10), 1)
	for i := 0; i < 6; i++ {
		s.heap[10] = append(s.heap[10], Cell{Content: "x", Width: 1})
	}
	for i := 0; i < 50; i++ {
		s.tick++
		s.relaxHeap()
	}
	for x := 1; x < s.W; x++ {
		if d := len(s.heap[x]) - len(s.heap[x-1]); d > 1 || d < -1 {
			t.Fatalf("column %d differs from its neighbour by %d", x, d)
		}
	}
}

func TestProbeArcsAndHits(t *testing.T) {
	s := New(screenWith(60, 12, 20, 6, "target"), 1)
	settle(s)
	s.Actor.Angle = 30
	s.emit()
	if len(s.probes) != 1 {
		t.Fatal("emit should launch a probe")
	}
	hit := false
	for i := 0; i < 400 && !hit; i++ {
		s.Step()
		hit = len(s.bursts) > 0
	}
	if !hit {
		t.Fatal("probe never hit anything")
	}
	if len(s.probes) != 0 {
		t.Fatal("a probe that hit should be gone")
	}
}

func TestTraceStopsAtTheFirstHit(t *testing.T) {
	s := New(screenWith(60, 12, 20, 9, "wall"), 1)
	settle(s)
	s.Actor.Facing, s.Actor.Angle = -1, 0
	for _, d := range s.trace() {
		if d[0] <= 23 && d[1] == 9 {
			t.Fatalf("trace dot %v is inside the wall", d)
		}
	}
}

func TestDrivesInFromTheRightAndParks(t *testing.T) {
	s := New(NewGrid(80, 24), 1)
	if s.Actor.Facing != -1 || !s.entering {
		t.Fatal("actor should start off-screen right, facing in")
	}
	if x0, _ := s.baseSpan(); x0 < 80 {
		t.Fatalf("actor should start off-screen, base at %d", x0)
	}
	s.Key("space")
	if len(s.probes) != 0 {
		t.Fatal("input during the entrance should be ignored")
	}
	settle(s)
	if s.entering {
		t.Fatal("entrance never finished")
	}
	if x0, x1 := s.baseSpan(); x0 < 60 || x1 >= 80 {
		t.Fatalf("base %d..%d should park at the right edge", x0, x1)
	}
	if s.bubble != quips[quipStart] || s.bubbleLeft == 0 {
		t.Fatalf("should speak on arrival, got %q", s.bubble)
	}
}

func TestEmitDipsTheArmWithoutMoving(t *testing.T) {
	s := New(NewGrid(80, 24), 1)
	settle(s)
	x := s.Actor.X
	s.emit()
	if s.Actor.X != x {
		t.Fatalf("emit must not move the actor: %v → %v", x, s.Actor.X)
	}
	if s.Actor.recoil == 0 {
		t.Fatal("recoil should be set")
	}
}

func TestSpeaksOnFirstHitAndSessionGlyph(t *testing.T) {
	s := New(screenWith(60, 20, 10, 3, "● run"), 1)
	settle(s)
	s.impact(13, 3, false) // the 'r'; ● sits in the ring and survives as a crack
	if s.bubble != quips[quipFirstHit] {
		t.Fatalf("first hit should say %q, got %q", quips[quipFirstHit], s.bubble)
	}
	s.impact(10, 3, false) // the ● itself
	if s.bubble != quips[quipSessionHit] {
		t.Fatalf("hitting a session glyph should say %q, got %q", quips[quipSessionHit], s.bubble)
	}
	// Each line is said once.
	s.bubbleLeft = 0
	s.impact(10, 3, false)
	if s.bubbleLeft != 0 {
		t.Fatal("a line should not repeat")
	}
}

func TestExitCollapsesTheFrameThenFinishes(t *testing.T) {
	s := New(screenWith(60, 16, 5, 2, "the whole frame comes down"), 1)
	settle(s)
	if s.Key("esc") {
		t.Fatal("the first esc should start the collapse, not exit")
	}
	if !s.collapsing || s.Done() {
		t.Fatal("collapse should be running")
	}
	// Everything falls, then the debris stays up for a beat before the run ends.
	for i := 0; i < collapseCap && (s.collapseRow < s.H || len(s.falling) > 0); i++ {
		s.Step()
	}
	if s.Done() {
		t.Fatal("the run ended the moment the debris settled; it should linger first")
	}
	for i := 0; i < collapseLinger && !s.Done(); i++ {
		s.Step()
	}
	if s.Done() {
		t.Fatalf("the run ended %d ticks into a %d-tick linger", s.settled, collapseLinger)
	}
	s.Step()
	if !s.Done() {
		t.Fatal("collapse never finished")
	}
	for y := 0; y < s.H; y++ {
		for x := 0; x < s.W; x++ {
			if s.damage[y*s.W+x] < 2 && s.world.Solid(x, y) {
				t.Fatalf("cell (%d,%d) %q survived the collapse", x, y, s.world.At(x, y).Content)
			}
		}
	}
	total := 0
	for x := range s.heap {
		total += len(s.heap[x])
	}
	if total == 0 {
		t.Fatal("the frame should have landed as debris")
	}
	// A second exit key during a collapse ends the run immediately.
	s2 := New(NewGrid(40, 10), 1)
	settle(s2)
	s2.Key("q")
	if !s2.Key("q") {
		t.Fatal("second exit key should end the run at once")
	}
}

func TestCardIsStampedAndTakesDamage(t *testing.T) {
	s := New(NewGrid(80, 24), 1)
	out := ansi.Strip(s.world.Render(0))
	title := strings.TrimSpace(strings.Trim(card[1], "│"))
	if !strings.Contains(out, title) {
		t.Fatalf("card missing:\n%s", out)
	}
	for y := 0; y < s.H; y++ {
		for x := 0; x < s.W; x++ {
			if s.world.At(x, y).Content == "╭" {
				s.impact(x+5, y, false)
				if c := s.world.At(x+5, y); c.Content != " " {
					t.Fatalf("card edge should be cleared at the impact, got %q", c.Content)
				}
				if len(s.falling) == 0 {
					t.Fatal("card glyphs should fall as debris")
				}
				return
			}
		}
	}
	t.Fatal("card corner not found")
}

func TestCardSkippedOnATinyFrame(t *testing.T) {
	s := New(NewGrid(20, 6), 1)
	if strings.Contains(ansi.Strip(s.world.Render(0)), "╭") {
		t.Fatal("card should not be stamped where it cannot fit")
	}
}

func TestRenderIsAlwaysFullSize(t *testing.T) {
	s := New(screenWith(30, 8, 3, 2, "abc 漢字 def"), 1)
	settle(s)
	s.Actor.Facing = -1
	s.Actor.X = 0
	s.emit()
	s.bubble, s.bubbleLeft = "wide 漢 bubble", bubbleTicks
	for i := 0; i < 30; i++ {
		s.Step()
		for shift := -1; shift <= 1; shift++ {
			s.shake = 1
			s.tick = shift + 1
			out := ansi.Strip(s.Render())
			for y, line := range strings.Split(out, "\n") {
				if w := ansi.StringWidth(line); w != s.W {
					t.Fatalf("tick %d row %d is %d wide, want %d: %q", i, y, w, s.W, line)
				}
			}
		}
	}
}

func TestKeysDriveAimEmitExit(t *testing.T) {
	s := New(NewGrid(40, 10), 1)
	settle(s)
	s.Key("up")
	if s.Actor.Angle != 50 {
		t.Fatalf("angle %d, want 50", s.Actor.Angle)
	}
	s.Key("left")
	if s.Actor.Facing != -1 || s.Actor.driveLeft == 0 {
		t.Fatal("left should face left and start driving")
	}
	x := s.Actor.X
	s.Step()
	if s.Actor.X >= x {
		t.Fatal("actor should have moved left")
	}
	s.Key("space")
	if len(s.probes) != 1 {
		t.Fatal("space should emit")
	}
}

func TestFreezeClipsAnOversizedFrame(t *testing.T) {
	// Bubble Tea clips a line wider than the terminal; a naive replay would
	// wrap it and scroll the top row off.
	g := Freeze("row0\nAAAAAAAAAA\nrow2", 8, 2)
	var rows []string
	for y := 0; y < 2; y++ {
		var b strings.Builder
		for x := 0; x < 8; x++ {
			b.WriteString(g.At(x, y).Content)
		}
		rows = append(rows, b.String())
	}
	if rows[0] != "row0    " || rows[1] != "AAAAAAAA" {
		t.Fatalf("rows = %q, want the first line kept and the second clipped", rows)
	}
}

func TestSetBlanksTheSpacerOfTheGlyphItCovers(t *testing.T) {
	g := NewGrid(6, 1)
	g.Set(0, 0, Cell{Content: "🔥", Width: 2})
	g.Set(2, 0, Cell{Content: "🔥", Width: 2})
	g.Set(1, 0, Cell{Content: "🔥", Width: 2}) // covers the left half of the second
	if c := g.At(3, 0); c.Width != 1 || c.Content != " " || g.Solid(3, 0) {
		t.Fatalf("cell 3 = %+v (solid=%v), want a blank, not an orphaned spacer", c, g.Solid(3, 0))
	}
	if c := g.At(1, 0); c.Content != "🔥" || c.Width != 2 || g.At(2, 0).Width != 0 {
		t.Fatalf("the glyph written last should own cells 1–2, got %+v / %+v", c, g.At(2, 0))
	}
}

func TestWideGlyphShedsAOneColumnChip(t *testing.T) {
	s := New(NewGrid(12, 4), 1)
	s.fling(5, 3, Cell{Content: "🔥", Width: 2}, 0)
	if len(s.falling) != 1 || s.falling[0].cell.Content != "▪" || s.falling[0].cell.Width != 1 {
		t.Fatalf("falling = %+v, want one ▪ chip", s.falling)
	}
	// A heap holds one column per cell, so a chip beside another must render.
	s.falling = nil
	s.heap[5] = append(s.heap[5], Cell{Content: "▪", Width: 1})
	s.heap[6] = append(s.heap[6], Cell{Content: "x", Width: 1})
	s.Render()
	if a, b := s.frame.At(5, 3).Content, s.frame.At(6, 3).Content; a != "▪" || b != "x" {
		t.Fatalf("bottom row renders %q %q, want ▪ x", a, b)
	}
}

func TestChipHittingAMoundSideStaysInItsColumn(t *testing.T) {
	s := New(NewGrid(20, 10), 1)
	for i := 0; i < 6; i++ {
		s.heap[10] = append(s.heap[10], Cell{Content: "#", Width: 1})
	}
	// Row 8, drifting right into a mound whose top is at row 3.
	s.falling = []Debris{{x: 9.4, y: 8, vx: 0.3, cell: Cell{Content: "c", Width: 1}}}
	s.stepDebris()
	if len(s.heap[10]) != 6 {
		t.Fatalf("the chip was stacked on top of the mound it ran into (heap[10] = %d tall)", len(s.heap[10]))
	}
	landed := len(s.heap[9]) == 1
	stillFalling := len(s.falling) == 1 && s.falling[0].x == 9 && s.falling[0].vx == 0
	if !landed && !stillFalling {
		t.Fatalf("chip should stay in column 9: falling=%+v heap[9]=%d", s.falling, len(s.heap[9]))
	}
}

func TestCritClearsAWiderFootprintOutright(t *testing.T) {
	text := "abcdefghijklmnopqrstuvwxyz0123456789"
	build := func() *Scene {
		g := screenWith(40, 8, 2, 4, text)
		for x := 2; x < 2+len(text); x++ {
			g.Set(x, 2, Cell{Content: "#", Width: 1})
			g.Set(x, 6, Cell{Content: "#", Width: 1})
		}
		return New(g, 1)
	}
	cell := func(s *Scene, x, y int) string { return s.world.At(x, y).Content }

	plain := build()
	plain.impact(20, 4, false)
	if cell(plain, 17, 4) != "▒" || cell(plain, 16, 4) == " " || cell(plain, 20, 2) != "#" {
		t.Fatalf("a plain impact should crack ±3 on its row and leave rows ±2 alone: %q %q %q",
			cell(plain, 17, 4), cell(plain, 16, 4), cell(plain, 20, 2))
	}

	crit := build()
	crit.impact(20, 4, true)
	for dx := -6; dx <= 6; dx++ {
		if got := cell(crit, 20+dx, 4); got != " " {
			t.Fatalf("crit left %q at dx=%d on its row; want everything within ±6 cleared", got, dx)
		}
	}
	if cell(crit, 13, 4) == " " || cell(crit, 27, 4) == " " {
		t.Fatal("crit reached past ±6 on its row")
	}
	if cell(crit, 20, 2) != " " || cell(crit, 24, 2) != " " || cell(crit, 25, 2) != "#" {
		t.Fatalf("crit should clear rows ±2 out to ±4: %q %q %q", cell(crit, 20, 2), cell(crit, 24, 2), cell(crit, 25, 2))
	}
	for y := 0; y < crit.H; y++ {
		for x := 0; x < crit.W; x++ {
			if cell(crit, x, y) == "▒" {
				t.Fatalf("crit cracked (%d,%d) instead of clearing it", x, y)
			}
		}
	}
	if crit.shake != critShake || !crit.bursts[0].big {
		t.Fatalf("crit should shake longer with a big burst: shake=%d big=%v", crit.shake, crit.bursts[0].big)
	}
	if crit.bubble != quips[quipCrit] {
		t.Fatalf("crit should say %q, got %q", quips[quipCrit], crit.bubble)
	}
}

func TestCritProbeDrawsATrailAndOutlivesAPlainBurst(t *testing.T) {
	// Tall frame, straight up: the shot is in the air far longer than any
	// cooldown, so what fires below is not confused with what lands.
	s := New(NewGrid(200, 60), 1)
	settle(s)
	s.Actor.Angle = 90
	s.fire(true)
	s.Step()
	out := s.Render()
	if !strings.Contains(out, "◉") || !strings.Contains(out, "✦") {
		t.Fatal("a crit in flight should draw as ◉ with a ✦ trail")
	}
	px, py := int(math.Round(s.probes[0].x)), int(math.Round(s.probes[0].y))
	if l, r := s.frame.At(px-1, py).Content, s.frame.At(px+1, py).Content; l != "◖" || r != "◗" {
		t.Fatalf("the ball should be three cells wide, got %q ◉ %q", l, r)
	}
	// A crit holds the next shot longer than a plain one; the sim keeps running.
	if s.Actor.cooldown != critCooldown-1 {
		t.Fatalf("cooldown = %d one tick after a crit, want %d", s.Actor.cooldown, critCooldown-1)
	}
	for s.Actor.cooldown > 0 {
		s.emit()
		s.Step()
	}
	if len(s.probes) != 1 {
		t.Fatalf("%d probes in the air; nothing should fire during the crit cooldown", len(s.probes))
	}
	s.emit()
	if len(s.probes) != 2 {
		t.Fatal("firing should work again once the crit cooldown has run out")
	}
	for i := 0; i < 400 && len(s.bursts) == 0; i++ {
		s.Step()
	}
	if len(s.bursts) == 0 || !s.bursts[0].big {
		t.Fatalf("crit impact should leave a big burst, got %+v", s.bursts)
	}
	s.Render()
	b := s.bursts[0]
	if got := s.frame.At(b.x, b.y).Style; !got.Equal(&critGlow) {
		t.Fatalf("a big burst should draw in the crit palette, got %+v", got)
	}
	ticks := 0
	for ; len(s.bursts) > 0 && ticks < 50; ticks++ {
		s.Step()
	}
	if want := len(bursts)*burstFrame + bigBurstLag*burstFrame - 1; ticks != want {
		t.Fatalf("big burst lived %d ticks, want %d", ticks, want)
	}
}

func TestCritRollsAboutOneInEight(t *testing.T) {
	s := New(NewGrid(40, 10), 7)
	settle(s)
	const shots = 800
	crits := 0
	for i := 0; i < shots; i++ {
		s.Actor.cooldown = 0
		s.emit()
	}
	if len(s.probes) != shots {
		t.Fatalf("%d probes launched, want %d", len(s.probes), shots)
	}
	for _, p := range s.probes {
		if p.crit {
			crits++
		}
	}
	if crits < shots/critOdds/2 || crits > shots/critOdds*2 {
		t.Fatalf("%d crits in %d shots; want about 1 in %d", crits, shots, critOdds)
	}
	// Never back to back: at least critGap plain shots separate two crits.
	since := critGap
	for i, p := range s.probes {
		if !p.crit {
			since++
			continue
		}
		if since < critGap {
			t.Fatalf("shot %d crit only %d shots after the previous crit", i, since)
		}
		since = 0
	}
}

func TestCritGapIsEnforcedEvenWhenTheRollWouldHit(t *testing.T) {
	s := New(NewGrid(40, 10), 1)
	settle(s)
	s.fire(true)
	for i := 0; i < 2000; i++ {
		s.Actor.cooldown = 0
		s.emit()
	}
	plain := 0
	for _, p := range s.probes[1:] {
		if p.crit {
			break
		}
		plain++
	}
	if plain < critGap {
		t.Fatalf("a crit came %d shots after a forced one, want at least %d", plain, critGap)
	}
}
