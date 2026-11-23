package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/stats"
)

var ctrlQ = tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}

// ctrl+q closes whatever is open in one press — including an overlay whose
// first esc only clears its filter, and a stack of views opened on top of each
// other — so "back to the sidebar" is the same chord that detaches a session.
func TestCtrlQClosesEveryOverlay(t *testing.T) {
	cases := map[string]func(h *Home){
		"help with a filter typed": func(h *Home) {
			h.helpOverlay.Show()
			h.routeToModal(tea.KeyPressMsg{Code: 'x', Text: "x"}) //nolint:errcheck // typing into the filter
		},
		"settings": func(h *Home) { h.settingsDialog.Show() },
		"stats":    func(h *Home) { h.statsView.Show() },
		"recap over stats": func(h *Home) {
			h.statsView.Show()
			h.recapView.Show(stats.Recap{})
		},
	}
	for name, open := range cases {
		t.Run(name, func(t *testing.T) {
			h := newPersistTestHome(t)
			open(h)
			if !h.modalOpen() {
				t.Fatalf("%s: nothing opened", name)
			}
			h.handleKey(ctrlQ) //nolint:errcheck // closing is the assertion
			if h.modalOpen() {
				t.Fatalf("ctrl+q left something open after %s", name)
			}
		})
	}
}

// With nothing open, ctrl+q is not fleet's to take: it falls through to the
// normal key path rather than being swallowed by the closer.
func TestCtrlQWithNothingOpenFallsThrough(t *testing.T) {
	h := newPersistTestHome(t)
	if _, ok := h.closeModalsOnCtrlQ(); ok {
		t.Fatal("ctrl+q claimed the key with nothing open")
	}
}
