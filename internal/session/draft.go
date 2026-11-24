package session

import (
	"strings"

	"github.com/brizzai/fleet/internal/agent"
)

// HasInputDraft reports whether the agent's input box holds text the user typed
// and has not sent — the sidebar's ✎ marker. Claude only: it is the one agent
// whose box fleet can read (dim ghost text vs typed text, see findClaudeInput).
func (s *Session) HasInputDraft() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.inputDraft
}

// SetInputDraft sets the draft flag directly. The status pass owns it in
// production (refreshInputDraft); this is for callers that render a session
// without a pane behind it (sidebar fixtures, previews).
func (s *Session) SetInputDraft(v bool) {
	s.mu.Lock()
	s.inputDraft = v
	s.mu.Unlock()
}

// refreshInputDraft re-reads the draft flag from the pane. It rides the capture
// the status pass already makes (the tmux session caches a capture for a moment,
// so this costs no extra tmux call), and never touches status.
//
// A capture that fails leaves the flag as it was: a frame we could not read is not
// evidence the draft is gone, and flickering ✎ off on a busy tmux would read as
// the text having vanished.
func (s *Session) refreshInputDraft() {
	if agent.Parse(string(s.Agent)) != agent.Claude {
		return
	}
	raw, err := s.getCapturer().CapturePane()
	if err != nil {
		return
	}
	draft := paneHasDraft(raw)
	s.mu.Lock()
	s.inputDraft = draft
	s.mu.Unlock()
}

// paneHasDraft is true when a Claude pane's input box holds typed text. Claude's
// own prompt suggestion is the same characters rendered dim and is NOT a draft:
// nobody typed it.
func paneHasDraft(rawPane string) bool {
	rawLines := strings.Split(strings.TrimRight(rawPane, "\n"), "\n")
	clean := make([]string, len(rawLines))
	for i, l := range rawLines {
		clean[i] = StripANSI(l)
	}
	in, _, ok := findClaudeInput(rawLines, clean)
	return ok && in.Kind == InputDraft
}
