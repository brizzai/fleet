package ui

import (
	"hash/fnv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/frost"
)

// Frost mode: a particular run of sidebar keys freezes the frame into a cell
// grid and hands it to internal/frost. This file is the trigger, the freeze
// and the tick loop; everything else lives there.

type frostTickMsg struct{}

// frostTickInterval is the sim's frame time. 20 fps is plenty for cell-sized
// motion, and every tick repaints the whole screen.
const frostTickInterval = 50 * time.Millisecond

// frostTickCmd paces the sim. Deliberately not gated on FreezeAnim: that flag
// stills decorative loops so screen captures settle, and a frost run only
// exists because someone asked for it — frozen, a capture would show nothing
// of what it does.
func frostTickCmd() tea.Cmd {
	return tea.Tick(frostTickInterval, func(time.Time) tea.Msg { return frostTickMsg{} })
}

// A run of trailLen sidebar keys, matched by hash. Every key of it still does
// its ordinary job; only the one that completes the run is taken.
const trailLen = 8

var trailKey uint64 = 0xd556428827d09ca5

// noteTrail feeds one sidebar keypress into the detector and reports whether
// it completed the run. The state is the last trailLen keys, so a stutter
// (a key repeated once too often) still completes on the next try.
func (h *Home) noteTrail(key string) bool {
	h.keyTrail = append(h.keyTrail, key)
	if len(h.keyTrail) > trailLen {
		h.keyTrail = h.keyTrail[1:]
	}
	if len(h.keyTrail) < trailLen {
		return false
	}
	f := fnv.New64a()
	for _, k := range h.keyTrail {
		_, _ = f.Write([]byte(k))
		_, _ = f.Write([]byte{0})
	}
	if f.Sum64() != trailKey {
		return false
	}
	h.keyTrail = nil
	return true
}

// startFrost freezes what is on screen and starts the sim over it.
func (h *Home) startFrost() tea.Cmd {
	if h.width == 0 || !h.booted || h.frost != nil {
		return nil
	}
	debuglog.Logger.Info("frost: start", "size", h.width*h.height)
	h.actionLog.Add("frost", "start", true)
	analytics.Track(analytics.EventFrostMode, nil)
	screen := frost.Freeze(h.composeScreen(), h.width, h.height)
	h.frost = frost.New(screen, uint64(time.Now().UnixNano()))
	return frostTickCmd()
}

// endFrost drops the sim; the next View paints the live UI again.
func (h *Home) endFrost() {
	h.frost = nil
	h.sidebarDirty = true
}
