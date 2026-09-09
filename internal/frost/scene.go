package frost

import (
	"fmt"
	"math"
	"math/rand/v2"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Tuning. The sim runs at a fixed tick (the UI schedules the interval); every
// rate below is per tick. Rows are twice as tall as columns are wide, so a
// vertical velocity is halved to look like the same speed on screen.
const (
	probeGravity   = 0.045 // rows per tick²
	debrisGravity  = 0.08
	driveSpeed     = 0.9 // columns per tick while a drive key is held
	driveTicks     = 3   // ticks of motion one key event adds (key repeat keeps it topped up)
	driveMax       = 12  // cap on banked motion, so a burst of presses is not a long slide
	angleStep      = 5   // degrees per ↑/↓
	emitCooldown   = 4
	critOdds       = 8  // one emit in critOdds is a crit: twice the footprint, cleared outright
	critCooldown   = 16 // a crit holds the next shot longer, so there is time to watch it
	critGap        = 3  // plain shots that must go between one crit and the next
	flashTicks     = 2
	shakeTicks     = 2
	critShake      = 4
	burstFrame     = 2 // ticks per impact frame
	maxDebris      = 600
	probeSubsteps  = 6
	traceDots      = 40
	recoilTicks    = 2
	bubbleTicks    = 40 // how long a line of speech stays up
	collapseRows   = 3  // rows released per tick when the frame comes down
	collapseCap    = 80 // ticks before a collapse counts as settled whether or not debris has
	collapseLinger = 30 // ticks the settled debris stays on screen before the run ends
)

// Actor is the driveable unit.
type Actor struct {
	X          float64 // sprite origin column (may sit a little off-screen while facing left)
	Facing     int     // +1 right, -1 left
	Angle      int     // arm elevation in degrees, 0 = level, 90 = straight up
	driveLeft  int     // ticks of drive momentum left
	vx         float64
	trackPhase int
	cooldown   int
	recoil     int // ticks the arm dips after emitting
}

// Probe is a projectile in flight. Positions are in cells; y is fractional rows.
// A crit carries its last few positions so it can draw a trail.
type Probe struct {
	x, y, vx, vy float64
	crit         bool
	past         [3][2]float64
}

// Burst is an impact animation at a cell. A big one is the same animation
// stamped in a cluster that blooms outward: each outer copy runs a frame or
// two behind the centre.
type Burst struct {
	x, y int
	age  int
	big  bool
}

// bigBurstStamps are a big burst's outer copies around the centre stamp, with
// the frames each runs behind. Rows are tall, so the cluster is wide.
var bigBurstStamps = [...]struct{ dx, dy, lag int }{
	{-3, 0, 1}, {3, 0, 1}, {-2, -1, 1}, {2, -1, 1},
	{-6, 0, 2}, {6, 0, 2},
}

const bigBurstLag = 2 // frames the outermost copies run behind

// Debris is a glyph knocked loose and falling.
type Debris struct {
	x, y, vx, vy float64
	cell         Cell
}

// Scene is one run over a frozen frame.
type Scene struct {
	W, H int

	world    *Grid   // the frame, taking damage
	pristine *Grid   // the frame as frozen, for the glyph a cleared cell sheds
	frame    *Grid   // scratch buffer each Render composes into
	damage   []uint8 // per cell: 0 intact, 1 cracked, 2 gone

	Actor     Actor
	sinceCrit int // shots since the last crit; a crit needs critGap of them
	probes    []Probe
	bursts    []Burst
	falling   []Debris
	heap      [][]Cell // per column, bottom-up: debris that has landed
	flash     int
	shake     int
	tick      int
	power     float64
	rng       *rand.Rand
	showKeys  bool

	entering bool // driving in from off-screen; input is ignored until parked
	razed    int  // frame cells cleared by impacts

	// Speech: one line at a time, each event line said once.
	bubble     string
	bubbleLeft int
	said       [quipCount]bool

	// Leaving: the frame is released row by row into debris; once it has
	// settled (or collapseCap ticks have passed) the debris lingers for
	// collapseLinger ticks, and then the run ends.
	collapsing  bool
	collapseRow int
	collapseAge int
	settled     int // ticks spent lingering on the settled debris
	done        bool
}

// New starts a scene over a frozen frame. The actor spawns on the floor at
// the right, facing in; launch velocity scales with the frame so a 45° probe
// crosses about two thirds of the width and a vertical one just clears the
// top row — whichever asks for more. The card is stamped into the frame
// itself, so it is as much a target as everything else.
func New(screen *Grid, seed uint64) *Scene {
	s := &Scene{
		W:        screen.W,
		H:        screen.H,
		world:    screen,
		pristine: NewGrid(screen.W, screen.H),
		frame:    NewGrid(screen.W, screen.H),
		damage:   make([]uint8, screen.W*screen.H),
		heap:     make([][]Cell, screen.W),
		rng:      rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
	}
	s.sinceCrit = critGap // the first shot may crit
	s.stampCard()
	s.pristine.CopyFrom(screen)
	byWidth := math.Sqrt(1.4 * float64(s.W) * probeGravity)
	byHeight := 2 * math.Sqrt(2*probeGravity*float64(s.H+2))
	s.power = min(max(byWidth, byHeight, 1.8), 5)
	// Parked position is the right edge; the actor starts past it and drives in.
	s.Actor = Actor{X: float64(s.W + 2), Facing: -1, Angle: 45}
	s.entering = true
	return s
}

// parkX is where the entrance ends.
func (s *Scene) parkX() float64 { return float64(s.W - SpriteW - 1) }

// Done reports that the run has ended and the frame can be dropped.
func (s *Scene) Done() bool { return s.done }

// say puts a line of speech above the actor. Each event's line is said once.
func (s *Scene) say(q int) {
	if q >= len(quips) || s.said[q] {
		return
	}
	s.said[q] = true
	s.bubble = quips[q]
	s.bubbleLeft = bubbleTicks
}

// stampCard writes the card, centred, into the world grid. It lives there
// rather than in the status line so it takes damage like anything else.
func (s *Scene) stampCard() {
	if len(card) == 0 {
		return
	}
	w := len([]rune(card[0]))
	x0, y0 := (s.W-w)/2, (s.H-len(card))/2-1
	if x0 < 0 || y0 < 1 {
		return // too small a frame to hold it; the status legend still exists
	}
	for r, row := range card {
		for i, ch := range []rune(row) {
			style := cardStyle
			switch {
			case r == 1 && ch != '│':
				style = cardTitleStyle
			case r == 0 || r == len(card)-1 || ch == '│':
				style = cardEdgeStyle
			}
			s.world.Set(x0+i, y0+r, Cell{Content: string(ch), Width: 1, Style: style})
		}
	}
}

// Key applies one keypress (Bubble Tea's Key.String() spelling). It reports
// true when the run should end now. The first exit key starts the collapse
// instead; a second one during it ends the run at once.
func (s *Scene) Key(k string) (exit bool) {
	switch k {
	case "esc", "q":
		if s.collapsing {
			return true
		}
		s.beginCollapse()
		return false
	}
	if s.entering || s.collapsing {
		return false
	}
	switch k {
	case "left", "h", "a":
		s.drive(-1)
	case "right", "l", "d":
		s.drive(1)
	case "up", "k", "w":
		s.Actor.Angle = min(90, s.Actor.Angle+angleStep)
	case "down", "j", "s":
		s.Actor.Angle = max(0, s.Actor.Angle-angleStep)
	case "space", "enter", "f":
		s.emit()
	case "?":
		s.showKeys = !s.showKeys
	}
	return false
}

// beginCollapse starts releasing the frame into debris, top row first.
func (s *Scene) beginCollapse() {
	s.collapsing = true
	s.collapseRow = 0
	s.collapseAge = 0
	s.say(quipBye)
}

// drive banks a few ticks of motion per key event. Terminals report no key
// release, so "held" arrives as repeats ~30ms apart; banking (rather than
// setting) keeps the actor rolling between them, and a tap still moves a step.
func (s *Scene) drive(dir int) {
	if dir != s.Actor.Facing {
		s.Actor.driveLeft = 0 // turning around cancels the old momentum
	}
	s.Actor.Facing = dir
	s.Actor.vx = float64(dir) * driveSpeed
	s.Actor.driveLeft = min(s.Actor.driveLeft+driveTicks, driveMax)
}

// Step advances the simulation one tick.
func (s *Scene) Step() {
	s.tick++
	a := &s.Actor
	if s.bubbleLeft > 0 {
		s.bubbleLeft--
	}
	if a.recoil > 0 {
		a.recoil--
	}
	if s.entering {
		a.X -= driveSpeed
		a.trackPhase++
		if a.X <= s.parkX() {
			a.X = s.parkX()
			s.entering = false
			s.say(quipStart)
		}
	}
	if s.collapsing {
		s.stepCollapse()
	}
	if a.driveLeft > 0 {
		a.driveLeft--
		a.trackPhase++
		a.X += a.vx
		lo, hi := s.xRange()
		a.X = min(max(a.X, lo), hi)
	}
	if a.cooldown > 0 {
		a.cooldown--
	}
	if s.flash > 0 {
		s.flash--
	}
	if s.shake > 0 {
		s.shake--
	}
	s.stepProbes()
	s.stepBursts()
	s.stepDebris()
	s.relaxHeap()
	if !s.collapsing && s.H-SpriteH-s.top() >= 3 {
		s.say(quipBuried)
	}
}

// stepCollapse releases the next rows of the frame as debris and ends the
// run once everything released has landed.
func (s *Scene) stepCollapse() {
	s.collapseAge++
	for n := 0; n < collapseRows && s.collapseRow < s.H; n++ {
		y := s.collapseRow
		s.collapseRow++
		for x := 0; x < s.W; x++ {
			i := y*s.W + x
			if s.damage[i] == 2 || !s.world.Solid(x, y) {
				continue
			}
			s.damage[i] = 2
			s.world.Set(x, y, blank)
			s.fling(x, y, s.pristine.At(x, y), s.rng.IntN(3)-1)
		}
	}
	if (s.collapseRow >= s.H && len(s.falling) == 0) || s.collapseAge >= collapseCap {
		s.settled++
		if s.settled > collapseLinger {
			s.done = true
		}
	}
}

// xRange is the span the sprite origin may occupy so the base stays on screen.
func (s *Scene) xRange() (lo, hi float64) {
	if s.Actor.Facing > 0 {
		return -1, float64(s.W - SpriteW)
	}
	return 0, float64(s.W - SpriteW + 1)
}

// baseSpan is the columns the bottom row covers.
func (s *Scene) baseSpan() (x0, x1 int) {
	x := int(math.Round(s.Actor.X))
	if s.Actor.Facing > 0 {
		return x + 1, x + SpriteW - 1
	}
	return x, x + SpriteW - 2
}

// top is the row of the sprite's first row: the actor rides on the tallest
// debris under its base, which is what lets it climb what it knocks down.
func (s *Scene) top() int {
	x0, x1 := s.baseSpan()
	lift := 0
	for x := max(x0, 0); x <= x1 && x < s.W; x++ {
		lift = max(lift, len(s.heap[x]))
	}
	return s.H - SpriteH - lift
}

// pivot is the cell the arm sprite hangs off: just past the base's end.
func (s *Scene) pivot() (x, y int) {
	x = int(math.Round(s.Actor.X))
	if s.Actor.Facing > 0 {
		return x + SpriteW, s.top() + pivotRow
	}
	return x - 1, s.top() + pivotRow
}

// nozzle is the cell a probe leaves from.
func (s *Scene) nozzle() (x, y int) {
	px, py := s.pivot()
	tip := arms[armBucket(s.Actor.Angle)].tip
	return px + s.Actor.Facing*tip[0], py + tip[1]
}

// emit launches a probe from the nozzle; the arm dips for a moment. One
// emit in critOdds rolls a crit, once critGap plain shots have gone since
// the last one.
func (s *Scene) emit() {
	if s.Actor.cooldown > 0 {
		return
	}
	s.fire(s.sinceCrit >= critGap && s.rng.IntN(critOdds) == 0)
}

// fire launches a probe, crit or not, without rolling.
func (s *Scene) fire(crit bool) {
	s.Actor.cooldown = emitCooldown
	s.sinceCrit++
	if crit {
		s.Actor.cooldown = critCooldown
		s.sinceCrit = 0
	}
	s.flash = flashTicks
	nx, ny := s.nozzle()
	p := s.launch(float64(nx), float64(ny))
	p.crit = crit
	s.probes = append(s.probes, p)
	s.Actor.recoil = recoilTicks
}

// launch builds a probe leaving (x, y) at the actor's current elevation.
func (s *Scene) launch(x, y float64) Probe {
	rad := float64(s.Actor.Angle) * math.Pi / 180
	return Probe{
		x:    x,
		y:    y,
		vx:   float64(s.Actor.Facing) * s.power * math.Cos(rad),
		vy:   -s.power * math.Sin(rad) / 2,
		past: [3][2]float64{{x, y}, {x, y}, {x, y}},
	}
}

// advance moves a probe one sub-step and reports the cell it hit, if any.
// Above the frame it keeps flying (it will come back down); off either side
// or below the floor it is gone.
func (s *Scene) advance(p *Probe) (hit, gone bool, hx, hy int) {
	p.vy += probeGravity / probeSubsteps
	p.x += p.vx / probeSubsteps
	p.y += p.vy / probeSubsteps
	cx, cy := int(math.Round(p.x)), int(math.Round(p.y))
	if cx < 0 || cx >= s.W || cy < -4*s.H {
		return false, true, 0, 0
	}
	if cy >= s.H {
		return true, false, cx, s.H - 1
	}
	if cy >= 0 && s.solid(cx, cy) {
		return true, false, cx, cy
	}
	return false, false, 0, 0
}

// solid is anything a probe stops on: surviving text, or landed debris.
func (s *Scene) solid(x, y int) bool {
	if s.heapAt(x, y) {
		return true
	}
	return s.damage[y*s.W+x] < 2 && s.world.Solid(x, y)
}

func (s *Scene) heapAt(x, y int) bool {
	return x >= 0 && x < s.W && y >= s.H-len(s.heap[x]) && y < s.H
}

func (s *Scene) stepProbes() {
	kept := s.probes[:0]
	for i := range s.probes {
		p := s.probes[i]
		p.past[2], p.past[1], p.past[0] = p.past[1], p.past[0], [2]float64{p.x, p.y}
		var hit, gone bool
		var hx, hy int
		for sub := 0; sub < probeSubsteps && !hit && !gone; sub++ {
			hit, gone, hx, hy = s.advance(&p)
		}
		switch {
		case hit:
			s.impact(hx, hy, p.crit)
		case gone:
		default:
			kept = append(kept, p)
		}
	}
	s.probes = kept
}

// impact damages a footprint around the hit: a wide, shallow ellipse (rows
// are tall). The core — the impact cell and two neighbours either side on its
// row — is cleared outright; the ring cracks on the first hit and clears on
// the next, so text visibly breaks before it disappears. Landed debris in the
// footprint is thrown back into the air. A crit doubles the footprint and
// clears all of it outright, with a bigger burst and a longer shake.
func (s *Scene) impact(cx, cy int, crit bool) {
	s.bursts = append(s.bursts, Burst{x: cx, y: cy, big: crit})
	s.shake = shakeTicks
	rx, ry, reach := 3, 1, 10
	if crit {
		s.shake = critShake
		rx, ry, reach = 6, 2, 40
	}
	if x0, x1 := s.baseSpan(); cx >= x0 && cx <= x1 && cy >= s.top() {
		s.say(quipSelfHit)
	}
	for dy := -ry; dy <= ry; dy++ {
		for dx := -rx; dx <= rx; dx++ {
			if dx*dx+4*dy*dy > reach {
				continue
			}
			x, y := cx+dx, cy+dy
			if !s.world.In(x, y) {
				continue
			}
			if s.heapAt(x, y) {
				s.fling(x, y, s.popHeap(x, y), dx)
				continue
			}
			i := y*s.W + x
			if s.damage[i] == 2 || !s.world.Solid(x, y) {
				continue
			}
			core := crit || (dy == 0 && dx >= -2 && dx <= 2)
			if s.damage[i] == 0 && !core {
				c := s.world.At(x, y)
				s.world.Set(x, y, Cell{Content: "▒", Width: 1, Style: c.Style})
				s.damage[i] = 1
				continue
			}
			s.damage[i] = 2
			s.world.Set(x, y, blank)
			s.fling(x, y, s.pristine.At(x, y), dx)
			s.razed++
			switch s.pristine.At(x, y).Content {
			case "●", "◐", "✻", "◇", "△", "✕":
				s.say(quipSessionHit)
			}
		}
	}
	s.say(quipFirstHit)
	if s.razed >= 300 {
		s.say(quipRazed300)
	} else if s.razed >= 100 {
		s.say(quipRazed100)
	}
	if crit {
		s.say(quipCrit)
	}
}

// fling turns a cell into falling debris, popped up and away from the impact
// so the heap it lands in spreads instead of stacking.
func (s *Scene) fling(x, y int, c Cell, dx int) {
	cap := maxDebris
	if s.collapsing {
		cap = s.W * s.H // the whole frame is coming down; the cap is for play
	}
	if len(s.falling) >= cap || c.Width == 0 {
		return
	}
	if c.Content == "" || c.Content == " " || ansi.StringWidth(c.Content) != 1 {
		// A filled space sheds a chip, not an invisible one; so does a glyph
		// wider than the one column a heap cell has.
		c.Content = "▪"
	}
	c.Width = 1
	away := float64(dx) * 0.12
	s.falling = append(s.falling, Debris{
		x:    float64(x),
		y:    float64(y),
		vx:   away + (s.rng.Float64()-0.5)*0.6,
		vy:   -(0.35 + s.rng.Float64()*0.55),
		cell: c,
	})
}

// popHeap removes the debris cell at (x, y) from its column; what sat above
// it settles down one row.
func (s *Scene) popHeap(x, y int) Cell {
	col := s.heap[x]
	i := s.H - 1 - y
	c := col[i]
	s.heap[x] = append(col[:i], col[i+1:]...)
	return c
}

func (s *Scene) stepBursts() {
	kept := s.bursts[:0]
	for _, b := range s.bursts {
		b.age++
		life := len(bursts) * burstFrame
		if b.big {
			life += bigBurstLag * burstFrame // until the outermost copies finish
		}
		if b.age < life {
			kept = append(kept, b)
		}
	}
	s.bursts = kept
}

// stepDebris drops falling glyphs through the (background) text until they
// meet the floor or a heap, then stacks them.
func (s *Scene) stepDebris() {
	surface := func(col int) float64 { return float64(s.H - 1 - len(s.heap[col])) }
	kept := s.falling[:0]
	for _, d := range s.falling {
		from := int(math.Round(d.x))
		d.vy += debrisGravity
		d.vx *= 0.9
		d.x += d.vx
		d.y += d.vy
		col := int(math.Round(d.x))
		if col < 0 || col >= s.W {
			continue
		}
		// Drifting into a column whose mound already stands above the chip is
		// hitting the mound's side, not landing on it: the chip stays in the
		// column it came from instead of being stacked rows higher in one tick.
		if col != from && d.y > surface(col)+1 {
			col = from
			d.x = float64(from)
			d.vx = 0
		}
		if d.y >= surface(col) {
			if len(s.heap[col]) < s.H {
				s.heap[col] = append(s.heap[col], d.cell)
			}
			continue
		}
		kept = append(kept, d)
	}
	s.falling = kept
}

// relaxHeap lets debris slide off steep columns, one cell per column per
// tick, so heaps settle into mounds the actor can drive up. Alternating the
// sweep direction keeps the mounds from leaning.
func (s *Scene) relaxHeap() {
	height := func(x int) int {
		if x < 0 || x >= s.W {
			return math.MaxInt32 // the frame edge is a wall
		}
		return len(s.heap[x])
	}
	sweep := func(x int) {
		h := height(x)
		if h < 2 {
			return
		}
		l, r := height(x-1), height(x+1)
		to := -1
		switch {
		case h-l >= 2 && h-r >= 2:
			to = x - 1
			if r < l || (r == l && s.rng.IntN(2) == 0) {
				to = x + 1
			}
		case h-l >= 2:
			to = x - 1
		case h-r >= 2:
			to = x + 1
		}
		if to < 0 {
			return
		}
		top := s.heap[x][h-1]
		s.heap[x] = s.heap[x][:h-1]
		s.heap[to] = append(s.heap[to], top)
	}
	if s.tick%2 == 0 {
		for x := 0; x < s.W; x++ {
			sweep(x)
		}
	} else {
		for x := s.W - 1; x >= 0; x-- {
			sweep(x)
		}
	}
}

// trace follows where a probe launched now would go, as a sparse dotted arc
// that stops at the first thing it would hit. It is the only precise sight:
// the arm sprite has four elevations, the angle has nineteen.
func (s *Scene) trace() [][2]int {
	nx, ny := s.nozzle()
	p := s.launch(float64(nx), float64(ny))
	var dots [][2]int
	for step := 0; step < 200 && len(dots) < traceDots; step++ {
		var hit, gone bool
		for sub := 0; sub < probeSubsteps && !hit && !gone; sub++ {
			hit, gone, _, _ = s.advance(&p)
		}
		if hit || gone {
			break
		}
		if step%2 == 1 {
			dots = append(dots, [2]int{int(math.Round(p.x)), int(math.Round(p.y))})
		}
	}
	return dots
}

// Render composes the next frame: the damaged screen, then landed and
// falling debris, the trace, the actor, probes, bursts and the status line.
func (s *Scene) Render() string {
	f := s.frame
	f.CopyFrom(s.world)
	for x := 0; x < s.W; x++ {
		for i, c := range s.heap[x] {
			f.Set(x, s.H-1-i, c)
		}
	}
	for _, d := range s.falling {
		f.Set(int(math.Round(d.x)), int(math.Round(d.y)), d.cell)
	}
	if !s.entering && !s.collapsing {
		for _, d := range s.trace() {
			if f.In(d[0], d[1]) && !f.Solid(d[0], d[1]) {
				f.Set(d[0], d[1], Cell{Content: "·", Width: 1, Style: traceStyle})
			}
		}
	}
	s.drawActor(f)
	if s.bubbleLeft > 0 {
		s.drawBubble(f)
	}
	for _, p := range s.probes {
		if !p.crit {
			f.Set(int(math.Round(p.x)), int(math.Round(p.y)), Cell{Content: "●", Width: 1, Style: probeStyle})
			continue
		}
		for i, q := range p.past {
			g := "·"
			if i == 0 {
				g = "✦"
			}
			f.Set(int(math.Round(q[0])), int(math.Round(q[1])), Cell{Content: g, Width: 1, Style: critGlow})
		}
		// A three-cell ball: half-circle rims around a bright core.
		x, y := int(math.Round(p.x)), int(math.Round(p.y))
		f.Set(x-1, y, Cell{Content: "◖", Width: 1, Style: critStyle})
		f.Set(x, y, Cell{Content: "◉", Width: 1, Style: critGlow})
		f.Set(x+1, y, Cell{Content: "◗", Width: 1, Style: critStyle})
	}
	for _, b := range s.bursts {
		if !b.big {
			s.drawBurst(f, b.x, b.y, b.age, burstStyles)
			continue
		}
		s.drawBurst(f, b.x, b.y, b.age, critBurstStyles)
		for _, o := range bigBurstStamps {
			s.drawBurst(f, b.x+o.dx, b.y+o.dy, b.age-o.lag*burstFrame, critBurstStyles)
		}
	}
	if s.flash > 0 {
		nx, ny := s.nozzle()
		f.Set(nx, ny, Cell{Content: "✦", Width: 1, Style: flashStyle})
	}
	s.drawStatus(f)
	shift := 0
	if s.shake > 0 {
		shift = 1 - 2*(s.tick%2)
	}
	return f.Render(shift)
}

func (s *Scene) drawActor(f *Grid) {
	x0, y0 := int(math.Round(s.Actor.X)), s.top()
	rows := spriteRows
	if s.Actor.driveLeft > 0 && s.Actor.trackPhase%2 == 1 {
		rows[SpriteH-1] = altBase
	}
	if s.Actor.Facing < 0 {
		for i := range rows {
			rows[i] = mirrorRow(rows[i])
		}
	}
	styles := [SpriteH]uv.Style{actorStyle, actorStyle, baseStyle, trackStyle}
	for r, row := range rows {
		for i, ch := range []rune(row) {
			if ch != ' ' {
				f.Set(x0+i, y0+r, Cell{Content: string(ch), Width: 1, Style: styles[r]})
			}
		}
	}
	px, py := s.pivot()
	bucket := armBucket(s.Actor.Angle)
	if s.Actor.recoil > 0 && bucket > 0 {
		bucket-- // the arm dips with the kick
	}
	for _, c := range arms[bucket].cells {
		ch := c.g
		if s.Actor.Facing < 0 {
			ch = mirrorRune(ch)
		}
		f.Set(px+s.Actor.Facing*c.dx, py+c.dy, Cell{Content: string(ch), Width: 1, Style: armStyle})
	}
}

// drawBubble draws the current line of speech in a box above the actor's
// head, its tail over the head's centre, kept on screen. Skipped when there
// is no room above.
func (s *Scene) drawBubble(f *Grid) {
	text := []rune(s.bubble)
	w := len(text) + 4
	x0 := int(math.Round(s.Actor.X))
	tail := x0 + 6 // the head's centre column, either facing
	bx := min(max(tail-3, 0), s.W-w)
	by := s.top() - 4
	if by < 1 || w > s.W {
		return
	}
	put := func(x, y int, r rune) {
		f.Set(x, y, Cell{Content: string(r), Width: 1, Style: bubbleStyle})
	}
	for i := 0; i < w; i++ {
		top, bottom := '─', '─'
		switch i {
		case 0:
			top, bottom = '╭', '╰'
		case w - 1:
			top, bottom = '╮', '╯'
		}
		if bx+i == tail && i > 0 && i < w-1 {
			bottom = '┬'
		}
		put(bx+i, by, top)
		put(bx+i, by+2, bottom)
	}
	put(bx, by+1, '│')
	put(bx+1, by+1, ' ')
	for i, r := range text {
		put(bx+2+i, by+1, r)
	}
	put(bx+w-2, by+1, ' ')
	put(bx+w-1, by+1, '│')
}

// drawStatus writes the elevation (and, on `?`, the keys — for after the
// card is gone) into the top right corner. Right-aligned because the frozen
// header's breadcrumb is on the left and is a target like anything else.
// drawBurst stamps one frame of the impact animation at (x, y) in the given
// palette; an age out of range draws nothing.
func (s *Scene) drawBurst(f *Grid, x, y, age int, styles [4]uv.Style) {
	if age < 0 || age >= len(bursts)*burstFrame {
		return
	}
	frame := age / burstFrame
	for _, c := range bursts[frame] {
		f.Set(x+c.dx, y+c.dy, Cell{Content: c.g, Width: 1, Style: styles[frame]})
	}
}

func (s *Scene) drawStatus(f *Grid) {
	text := fmt.Sprintf(" ∠ %2d° ", s.Actor.Angle)
	if s.showKeys {
		text = " ←→ drive  ↑↓ aim  space fire  esc back  " + text
	}
	rs := []rune(text)
	x0 := s.W - len(rs)
	for i, ch := range rs {
		f.Set(x0+i, 0, Cell{Content: string(ch), Width: 1, Style: statusStyle})
	}
}
