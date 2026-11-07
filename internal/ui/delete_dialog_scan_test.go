package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestDirtyScanPrefix(t *testing.T) {
	tests := []struct {
		dirty, cached, scanned int
		want                   string
	}{
		{0, 0, 1, ""},
		{1, 1, 1, ""}, // the cached warning already says it
		{1, 0, 1, "Uncommitted changes will be discarded · "},
		{2, 1, 3, "Live check: 2 worktree(s) have uncommitted changes · "},
		{0, 1, 1, ""}, // cache was dirty, now clean: never retract a warning
	}
	for _, tt := range tests {
		if got := dirtyScanPrefix(tt.dirty, tt.cached, tt.scanned); got != tt.want {
			t.Errorf("dirtyScanPrefix(%d, %d, %d) = %q, want %q", tt.dirty, tt.cached, tt.scanned, got, tt.want)
		}
	}
}

func keyPress(s string) tea.KeyPressMsg {
	if s == "enter" {
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

// A worktree removal is `git worktree remove --force`, and the live
// uncommitted-changes check rides the dialog's async scan. Confirming before
// that scan answers could discard edits the dialog never warned about, so
// y/Enter must do nothing until it lands — and esc must still cancel.
func TestConfirmWaitsForPendingScan(t *testing.T) {
	confirmed := false
	d := NewConfirmDialog()
	d.ShowDanger("Remove Worktree?", "wt", nil, func() tea.Msg { confirmed = true; return nil })
	d.StartScan(7, "Checking…")

	for _, k := range []string{"y", "enter"} {
		if _, cmd := d.Update(keyPress(k)); cmd != nil || !d.IsVisible() {
			t.Fatalf("%q confirmed while the scan was pending", k)
		}
	}

	d.SetScan(6, "stale result for an earlier prompt")
	if _, cmd := d.Update(keyPress("y")); cmd != nil {
		t.Fatal("a stale scan result unlocked the confirm")
	}

	d.SetScan(7, "No background processes to stop")
	_, cmd := d.Update(keyPress("y"))
	if cmd == nil {
		t.Fatal("y did nothing after the scan landed")
	}
	cmd()
	if !confirmed {
		t.Error("onYes not run")
	}
}

func TestEscCancelsWhileScanPending(t *testing.T) {
	d := NewConfirmDialog()
	d.ShowDanger("Remove Worktree?", "wt", nil, func() tea.Msg { return nil })
	d.StartScan(1, "Checking…")
	d.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if d.IsVisible() {
		t.Error("esc did not cancel while the scan was pending")
	}
}

// A dialog with no scan confirms immediately, as before.
func TestConfirmWithoutScanIsImmediate(t *testing.T) {
	d := NewConfirmDialog()
	d.ShowDanger("Delete session?", "s", nil, func() tea.Msg { return nil })
	if _, cmd := d.Update(keyPress("y")); cmd == nil {
		t.Error("y did nothing on a dialog with no scan")
	}
}

// The live dirty warning must actually reach the dialog's scan line.
func TestScanResultCarriesDirtyWarning(t *testing.T) {
	h := &Home{confirmDialog: NewConfirmDialog()}
	h.confirmDialog.ShowDanger("Remove Worktree?", "wt", nil, func() tea.Msg { return nil })
	h.confirmDialog.StartScan(3, "Checking…")

	h.Update(worktreeHoldersScannedMsg{gen: 3, dirtyPrefix: dirtyScanPrefix(1, 0, 1)})

	view := h.confirmDialog.View()
	if !strings.Contains(view, "Uncommitted changes will be discarded") {
		t.Errorf("live dirty warning missing from the dialog:\n%s", view)
	}
	if !strings.Contains(view, "No background processes to stop") {
		t.Errorf("process line missing from the dialog:\n%s", view)
	}
	if h.confirmDialog.scanPending {
		t.Error("scan still pending after its result landed")
	}
}
