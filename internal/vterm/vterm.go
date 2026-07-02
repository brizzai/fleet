// Package vterm wraps a virtual terminal emulator behind a small, thread-safe
// interface. It exists to insulate the rest of fleet from charmbracelet/x/vt,
// which is pre-1.0 (pseudo-versioned, no compatibility promise) and has an
// open data race in its own SafeEmulator. We therefore drive a plain
// *vt.Emulator behind our own mutex, serialize every access, and never call
// Emulator.Close() (the upstream Close doesn't synchronize with Read/Write — the
// feeding goroutine is stopped by closing its byte source instead). Swapping the
// backend later is a one-file change.
package vterm

import (
	"sync"

	"github.com/charmbracelet/x/vt"
)

// screen-title strip states (see stripScreenTitles).
const (
	tfNormal     = iota
	tfEsc        // saw ESC in normal flow
	tfInTitle    // inside an ESC k … title string (dropping)
	tfInTitleEsc // inside a title, saw ESC (maybe the ST terminator)
)

// Terminal is a concurrency-safe virtual terminal. A producer goroutine feeds
// bytes via Write while the render goroutine calls Render/Cursor; all access is
// serialized by mu. Dirty tracks whether new bytes have arrived since the last
// Render, so callers can skip redundant repaints.
type Terminal struct {
	mu    sync.Mutex
	emu   *vt.Emulator
	w, h  int
	dirty bool
	tf    int // stripScreenTitles state, carried across Write calls
}

// New creates a Terminal sized w×h. Sizes are clamped to at least 1×1.
func New(w, h int) *Terminal {
	w, h = clampDim(w), clampDim(h)
	return &Terminal{emu: vt.NewEmulator(w, h), w: w, h: h}
}

// Write feeds raw terminal bytes (already octal-decoded from %output) into the
// emulator and marks the screen dirty. Safe to call from a reader goroutine.
func (t *Terminal) Write(b []byte) {
	if len(b) == 0 {
		return
	}
	t.mu.Lock()
	if filtered := t.stripScreenTitles(b); len(filtered) > 0 {
		_, _ = t.emu.Write(filtered)
		t.dirty = true
	}
	t.mu.Unlock()
}

// stripScreenTitles drops screen/tmux "set window title" sequences
// (ESC k <title> ST, terminated by ESC \ or BEL) from the byte stream. Shells
// running under a screen/tmux $TERM set the window title with this escape on
// every command; charmbracelet/x/vt does not recognize ESC k and would leak the
// title text onto the screen (e.g. "echo" bleeding into a command's output).
// tmux's own emulator consumes it, so this keeps us faithful to capture-pane.
// State is carried across calls (t.tf) since a sequence can span Write chunks.
func (t *Terminal) stripScreenTitles(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		switch t.tf {
		case tfNormal:
			if c == 0x1b {
				t.tf = tfEsc // hold; the next byte decides
			} else {
				out = append(out, c)
			}
		case tfEsc:
			if c == 'k' {
				t.tf = tfInTitle // ESC k → start of a screen title; drop until ST
			} else {
				out = append(out, 0x1b, c) // ordinary escape — emit the held ESC + byte
				t.tf = tfNormal
			}
		case tfInTitle:
			switch c {
			case 0x07: // BEL terminator
				t.tf = tfNormal
			case 0x1b: // maybe the ESC of an ST terminator
				t.tf = tfInTitleEsc
			}
			// otherwise drop the title text
		case tfInTitleEsc:
			if c != 0x1b { // ST (ESC \) or any non-ESC ends the title
				t.tf = tfNormal
			}
		}
	}
	return out
}

// Resize changes the emulated screen size. Keep this in lockstep with the tmux
// pane size or wrap points diverge from what the program inside the pane assumes.
func (t *Terminal) Resize(w, h int) {
	w, h = clampDim(w), clampDim(h)
	t.mu.Lock()
	if w != t.w || h != t.h {
		t.emu.Resize(w, h)
		t.w, t.h = w, h
		t.dirty = true
	}
	t.mu.Unlock()
}

// Render returns the current screen as an ANSI string (styles + links intact)
// and clears the dirty flag.
func (t *Terminal) Render() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dirty = false
	return t.emu.Render()
}

// Cursor returns the cursor's (x, y) position in cells.
func (t *Terminal) Cursor() (int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.emu.CursorPosition()
	return c.X, c.Y
}

// Size returns the emulator's current width and height.
func (t *Terminal) Size() (int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.w, t.h
}

// Dirty reports whether bytes have arrived since the last Render.
func (t *Terminal) Dirty() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dirty
}

func clampDim(v int) int {
	if v < 1 {
		return 1
	}
	return v
}
