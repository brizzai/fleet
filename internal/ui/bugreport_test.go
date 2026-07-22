package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// bugFormDialog returns a dialog already past the type picker on the plain-bug
// form, which is where these tests' assertions live. `!` now opens on the
// picker, so tests that exercise submission have to step through it.
func bugFormDialog() *BugReportDialog {
	d := NewBugReportDialog()
	d.Show("v0.0.0-test", 0, NewErrorHistory(50), NewActionLog(100), 100, 40, nil, 0, nil)
	d.stage = stageForm
	d.kind = kindBug
	return d
}

func TestBugReportDialog_EnterWithGhMissing_ReturnsCmd(t *testing.T) {
	// Ensure gh is not found regardless of the test environment.
	t.Setenv("PATH", t.TempDir())

	d := bugFormDialog()
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
	d := bugFormDialog()
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
	d := bugFormDialog()
	d.submitting = true
	d.descInput.SetValue("something broke")

	_, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if cmd != nil {
		t.Fatal("expected nil cmd while already submitting")
	}
}

func TestBugReportDialog_Esc_Hides(t *testing.T) {
	d := bugFormDialog()
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
