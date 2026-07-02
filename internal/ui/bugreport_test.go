package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestBugReportDialog_EnterWithGhMissing_ReturnsCmd(t *testing.T) {
	// Ensure gh is not found regardless of the test environment.
	t.Setenv("PATH", t.TempDir())

	d := NewBugReportDialog()
	d.visible = true
	d.descInput.SetValue("something broke")

	_, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if cmd == nil {
		t.Fatal("expected non-nil cmd when gh is missing, got nil (dialog would freeze)")
	}
	if !d.submitting {
		t.Fatal("expected submitting to be true after enter")
	}

	msg := cmd()
	if _, ok := msg.(bugReportOpenErrMsg); !ok {
		t.Fatalf("expected bugReportOpenErrMsg, got %T", msg)
	}
}

func TestBugReportDialog_EnterWithEmptyDesc_Noop(t *testing.T) {
	d := NewBugReportDialog()
	d.visible = true
	d.descInput.SetValue("")

	_, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if cmd != nil {
		t.Fatal("expected nil cmd for empty description")
	}
	if d.submitting {
		t.Fatal("submitting should stay false for empty description")
	}
}

func TestBugReportDialog_EnterWhileSubmitting_Noop(t *testing.T) {
	d := NewBugReportDialog()
	d.visible = true
	d.submitting = true
	d.descInput.SetValue("something broke")

	_, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if cmd != nil {
		t.Fatal("expected nil cmd while already submitting")
	}
}

func TestBugReportDialog_Esc_Hides(t *testing.T) {
	d := NewBugReportDialog()
	d.visible = true
	d.submitting = true

	_, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEsc})

	if d.visible {
		t.Fatal("expected dialog to be hidden after esc")
	}
	if cmd == nil {
		t.Fatal("expected non-nil cmd from esc")
	}
	msg := cmd()
	if _, ok := msg.(bugReportClosedMsg); !ok {
		t.Fatalf("expected bugReportClosedMsg, got %T", msg)
	}
}
