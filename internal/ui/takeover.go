package ui

import (
	"fmt"
	"os"
	"os/exec"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/discovery"
)

// Taking over Claude sessions that run outside fleet: list them from Claude
// Code's own pid files, let the user pick, and resume each conversation in a
// fleet session. The originals keep running until the user closes them — a
// process can't be moved between terminals, and fleet doesn't kill anything.

type takeoverScanMsg struct{ items []discovery.Recent }

// scanTakeover lists the running outside sessions off the Update goroutine.
func (h *Home) scanTakeover() tea.Cmd {
	known := make(map[string]bool, len(h.sessions))
	for _, s := range h.sessions {
		if s.ClaudeSessionID != "" {
			known[s.ClaudeSessionID] = true
		}
	}
	return func() tea.Msg {
		home, err := os.UserHomeDir()
		if err != nil {
			return takeoverScanMsg{}
		}
		return takeoverScanMsg{items: discovery.RunningClaude(home, known)}
	}
}

func (h *Home) openTakeover(msg takeoverScanMsg) (tea.Model, tea.Cmd) {
	if len(msg.items) == 0 {
		h.setInfo("No Claude sessions running outside fleet")
		return h, nil
	}
	h.takeover = NewTakeover(msg.items)
	return h, nil
}

// handleTakeoverKey drives the picker; it owns the keyboard while open.
func (h *Home) handleTakeoverKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch normalizeKey(msg).String() {
	case "j", "down":
		h.takeover.Move(1)
	case "k", "up":
		h.takeover.Move(-1)
	case "space":
		h.takeover.Toggle()
	case "A":
		h.takeover.ToggleAll()
	case "esc", "ctrl+c":
		h.takeover = nil
	case "enter":
		items := h.takeover.LaunchSet()
		h.takeover = nil
		if _, err := exec.LookPath("claude"); err != nil {
			h.setError("agent_cli_missing", fmt.Errorf("claude CLI not found: install Claude Code to create sessions"))
			return h, nil
		}
		h.setInfo(fmt.Sprintf("Took over %d — close the original windows", len(items)))
		return h, h.resumeSet(items, "takeover")
	}
	return h, nil
}
