package ui

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/discovery"
)

func TestTakeoverPicker(t *testing.T) {
	h := adoptTestHome(t)

	h.Update(takeoverScanMsg{})
	if h.takeover != nil {
		t.Fatal("an empty scan opened the picker")
	}

	items := []discovery.Recent{
		{Path: t.TempDir(), ClaudeSessionID: "a", Title: "one", Where: "idle · pts/1 · pid 1"},
		{Path: t.TempDir(), ClaudeSessionID: "b", Title: "two", Where: "busy · pts/2 · pid 2"},
	}
	h.Update(takeoverScanMsg{items: items})
	if h.takeover == nil {
		t.Fatal("a scan with sessions did not open the picker")
	}
	if v := h.View(); v.Content == "" {
		t.Error("picker drew nothing")
	}

	h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.takeover != nil {
		t.Fatal("esc did not close the picker")
	}

	// A claude on PATH, so the launch gets past the CLI check.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	h.Update(takeoverScanMsg{items: items})
	h.Update(tea.KeyPressMsg{Code: 'a', ShiftedCode: 'A', Mod: tea.ModShift, Text: "A"})
	if got := len(h.takeover.LaunchSet()); got != 2 {
		t.Fatalf("A selected %d, want 2", got)
	}
	_, cmd := h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.takeover != nil || cmd == nil {
		t.Error("enter should close the picker and start the resumes")
	}
}

func TestRightArrowOpensASessionRow(t *testing.T) {
	h, s := scrollTestHome(t)
	h.cfg.EnterMode = "split" // Enter's split mode focuses instead of attaching: observable without tmux
	h.flatItems = []SidebarItem{{IsCheckoutHeader: true, RepoPath: "/tmp/scroll-test"}, {Session: s, RepoPath: "/tmp/scroll-test"}}

	h.cursor = 0
	h.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if h.focusMode {
		t.Fatal("→ on a header opened a session; it should only expand")
	}

	h.flatItems = []SidebarItem{{IsCheckoutHeader: true, RepoPath: "/tmp/scroll-test"}, {Session: s, RepoPath: "/tmp/scroll-test"}}
	h.cursor = 1
	h.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if !h.focusMode {
		t.Error("→ on a session row did not open it the way Enter does")
	}
}

func TestSplitViewLeavesOnCtrlQNotEsc(t *testing.T) {
	// The decision only: driving handleFocusKey would start a tmux control
	// client on the real server.
	if isFocusExitKey(tea.KeyPressMsg{Code: tea.KeyEscape}) {
		t.Error("Esc leaves the split; it must go to the agent")
	}
	if !isFocusExitKey(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}) {
		t.Error("Ctrl+Q does not leave the split")
	}
}
