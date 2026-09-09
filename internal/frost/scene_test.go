package frost

import (
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
	if len(quips) < 8 {
		t.Fatalf("only %d quips unpacked", len(quips))
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
	s.impact(16, 3)
	if got := ansi.Strip(s.world.Render(0)); !strings.Contains(got, "hel▒     ▒d") {
		t.Fatalf("after one hit: %q", got)
	}
	if len(s.falling) == 0 {
		t.Fatal("cleared glyphs should become debris")
	}
	if c := s.falling[0].cell.Content; c == " " || c == "▒" {
		t.Fatalf("debris should carry the original glyph, got %q", c)
	}
	s.impact(16, 3)
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

func TestEmitKicksBackAndDipsTheArm(t *testing.T) {
	s := New(NewGrid(80, 24), 1)
	settle(s)
	x := s.Actor.X
	s.emit()
	if s.Actor.X != x+1 {
		t.Fatalf("facing left, emit should kick right: %v → %v", x, s.Actor.X)
	}
	if s.Actor.recoil == 0 {
		t.Fatal("recoil should be set")
	}
}

func TestSpeaksOnFirstHitAndSessionGlyph(t *testing.T) {
	s := New(screenWith(60, 20, 10, 3, "● run"), 1)
	settle(s)
	s.impact(13, 3) // the 'r'; ● sits in the ring and survives as a crack
	if s.bubble != quips[quipFirstHit] {
		t.Fatalf("first hit should say %q, got %q", quips[quipFirstHit], s.bubble)
	}
	s.impact(10, 3) // the ● itself
	if s.bubble != quips[quipSessionHit] {
		t.Fatalf("hitting a session glyph should say %q, got %q", quips[quipSessionHit], s.bubble)
	}
	// Each line is said once.
	s.bubbleLeft = 0
	s.impact(10, 3)
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
	for i := 0; i < collapseCap+5 && !s.Done(); i++ {
		s.Step()
	}
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
				s.impact(x+5, y)
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
