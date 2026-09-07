package ui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/tmux"
	"github.com/brizzai/fleet/internal/vterm"
)

// readerStreamReadyMsg carries an async attach back to Update, exactly as the
// drawer's does — NewOutputReader forks `tmux -C attach` plus a PTY, which is
// far too heavy for the Update goroutine.
type readerStreamReadyMsg struct {
	target string
	w, h   int
	term   *vterm.Terminal
	reader *tmux.OutputReader
	err    error
}

// readerOutputMsg is a coalesced "the pane wrote something, draw a frame" wake.
type readerOutputMsg struct{}

// sessionForKey is the agent working on a pull request, if one is.
func (h *Home) sessionForKey(k reviewKey) *session.Session {
	for _, s := range h.sessions {
		if kk, ok := h.reviewKeyFor(s); ok && kk == k {
			return s
		}
	}
	return nil
}

// syncReaderStream keeps the reader's session tab attached to the right pane.
//
// The same shape as the drawer's syncShellStream, and deliberately a separate
// copy of the state rather than a shared one: the drawer and the reader can be
// looking at different panes at the same time, so one set of fields would have
// them fighting over the emulator. The sequence itself — seed before attach,
// resize both sides together, coalesce the wakes — is the drawer's, because it
// is the one that has been proven against real panes.
func (h *Home) syncReaderStream() {
	if !h.reader.Visible() || !h.reader.SessionTabActive() {
		h.teardownReaderStream()
		return
	}
	s := h.sessionForKey(h.reader.key())
	if s == nil || !s.IsAlive() {
		h.teardownReaderStream()
		h.reader.SetSession(h.readerSessionSnapshot(s))
		return
	}

	w, ht := h.reader.SessionPaneSize()
	if w < 1 || ht < 1 {
		return // the panel has not rendered yet, so its size is unknown
	}
	target := s.TmuxSessionName
	healthy := h.readerTerm != nil && h.readerStreamTarget == target &&
		h.readerReader != nil && !h.readerReader.Failed()
	if !healthy {
		h.startReaderStreamAsync(target, w, ht)
		h.reader.SetSession(h.readerSessionSnapshot(s))
		return
	}
	if cw, ch := h.readerTerm.Size(); cw != w || ch != ht {
		h.readerTerm.Resize(w, ht)
		_ = h.readerReader.Resize(w, ht)
	}
	h.reader.SetSession(h.readerSessionSnapshot(s))
}

// readerSessionSnapshot is what the reader is told about the agent.
func (h *Home) readerSessionSnapshot(s *session.Session) readerSession {
	if s == nil {
		return readerSession{}
	}
	out := readerSession{Present: true, Title: s.Title, Status: string(s.GetStatus())}
	if h.readerTerm != nil && h.readerStreamTarget == s.TmuxSessionName {
		out.Frame = h.readerTerm.Render()
	}
	return out
}

// startReaderStreamAsync builds the emulator and the control reader off the
// Update goroutine.
func (h *Home) startReaderStreamAsync(target string, w, ht int) {
	if h.readerStreamPending == target {
		return
	}
	h.readerStreamPending = target
	go func() {
		term := vterm.New(w, ht)
		// Seeded BEFORE the reader attaches: control mode replays nothing, so
		// without this the tab is blank until the agent's next write — which on
		// an idle session is never.
		if seed := drawerSeedBytes(tmux.CapturePaneANSI(target)); len(seed) > 0 {
			term.Write(seed)
		}
		reader, err := tmux.NewOutputReader(target, w, ht, func(b []byte) {
			term.Write(b)
			if h.readerWake.CompareAndSwap(false, true) {
				h.send(readerOutputMsg{})
			}
		})
		h.send(readerStreamReadyMsg{target: target, w: w, h: ht, term: term, reader: reader, err: err})
	}()
}

// applyReaderStream installs a completed attach, or drops it if the tab has
// moved on while it was in flight.
func (h *Home) applyReaderStream(msg readerStreamReadyMsg) {
	if h.readerStreamPending == msg.target {
		h.readerStreamPending = ""
	}
	if msg.err != nil {
		debuglog.Logger.Debug("reader stream: attach failed", "target", msg.target, "err", msg.err)
		return
	}
	if !h.reader.Visible() || !h.reader.SessionTabActive() {
		go msg.reader.Close()
		return
	}
	// The requested size must still be current, or the emulator would render at
	// a width the panel no longer has.
	if w, ht := h.reader.SessionPaneSize(); w != msg.w || ht != msg.h {
		go msg.reader.Close()
		return
	}
	h.dropReaderStream(true)
	h.readerTerm, h.readerReader, h.readerStreamTarget = msg.term, msg.reader, msg.target
}

// teardownReaderStream detaches, off the Update goroutine.
func (h *Home) teardownReaderStream() { h.dropReaderStream(true) }

func (h *Home) dropReaderStream(async bool) {
	if h.readerReader != nil {
		r := h.readerReader
		if async {
			go r.Close()
		} else {
			r.Close()
		}
	}
	h.readerReader, h.readerTerm = nil, nil
	h.readerStreamTarget, h.readerStreamPending = "", ""
}

// handleReviewSession attaches to the review's agent, or starts one.
func (h *Home) handleReviewSession(msg reviewSessionMsg) (tea.Model, tea.Cmd) {
	if !msg.start {
		s := h.sessionForKey(msg.key)
		if s == nil {
			return h, nil
		}
		// The stream goes first: attaching hands the terminal to tmux, and two
		// clients on one pane is how the emulator ends up rendering a screen
		// sized for the other one.
		h.dropReaderStream(false)
		h.actionLog.Add("attach from reader", itoa(msg.key.pr), true)
		return h, h.attachSession(s)
	}

	r, ok := h.findReviewByKey(msg.key)
	if !ok || r.URL == "" {
		h.setError(fmt.Errorf("#%d is not in the review queue — press c to refresh it", msg.key.pr))
		return h, nil
	}
	// Straight through startReview, which is what the `c` queue's Enter runs:
	// preparing the worktree, copying settings and seeding the review prompt
	// are all the same job whichever surface asked for it.
	return h.startReview(r.URL)
}
