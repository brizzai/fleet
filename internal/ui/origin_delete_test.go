package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
)

// confirmKey sends one key to a ConfirmDialog and returns the resulting cmd.
// Space is delivered as a rune so its String() is " " — the same value a real
// space press yields (the app's space-jump binding relies on that).
func confirmKey(d *ConfirmDialog, s string) tea.Cmd {
	var km tea.KeyMsg
	switch s {
	case " ":
		km = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}
	case "enter":
		km = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		km = tea.KeyMsg{Type: tea.KeyEsc}
	default:
		km = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	_, cmd := d.Update(km)
	return cmd
}

// The checkbox gate must swallow y/Enter until the box is ticked, then confirm.
func TestConfirmDialogCheckboxGate(t *testing.T) {
	fired := false
	d := NewConfirmDialog()
	d.ShowDanger("Forget entire origin?", "fleet", []string{"x"}, func() tea.Msg {
		fired = true
		return nil
	})
	d.RequireCheckbox("Yes, forget the whole origin group")

	// y/Enter are inert while unticked, and the dialog stays open.
	if cmd := confirmKey(d, "y"); cmd != nil {
		t.Fatal("y confirmed before the checkbox was ticked")
	}
	if cmd := confirmKey(d, "enter"); cmd != nil {
		t.Fatal("enter confirmed before the checkbox was ticked")
	}
	if !d.IsVisible() {
		t.Fatal("dialog closed before confirmation")
	}

	// Tick the box, then y fires onYes and closes the dialog.
	confirmKey(d, " ")
	cmd := confirmKey(d, "y")
	if cmd == nil {
		t.Fatal("y did not confirm after the checkbox was ticked")
	}
	cmd() // run the returned command to trigger onYes
	if !fired {
		t.Fatal("onYes was not invoked")
	}
	if d.IsVisible() {
		t.Fatal("dialog stayed open after confirmation")
	}
}

// Esc cancels even when the checkbox gate is active.
func TestConfirmDialogCheckboxEscCancels(t *testing.T) {
	d := NewConfirmDialog()
	d.ShowDanger("Forget entire origin?", "fleet", nil, func() tea.Msg { return nil })
	d.RequireCheckbox("confirm")
	confirmKey(d, "esc")
	if d.IsVisible() {
		t.Fatal("esc should cancel even when a checkbox is required")
	}
}

// Without RequireCheckbox, y confirms immediately (ordinary deletes are unchanged).
func TestConfirmDialogNoCheckboxConfirmsImmediately(t *testing.T) {
	d := NewConfirmDialog()
	d.ShowDanger("Delete Session?", "s", nil, func() tea.Msg { return nil })
	if cmd := confirmKey(d, "y"); cmd == nil {
		t.Fatal("y should confirm immediately when no checkbox is required")
	}
}

// A reused dialog must not carry a prior prompt's checkbox requirement.
func TestConfirmDialogCheckboxResetOnReshow(t *testing.T) {
	d := NewConfirmDialog()
	d.ShowDanger("Forget entire origin?", "fleet", nil, func() tea.Msg { return nil })
	d.RequireCheckbox("confirm")
	// Re-show as a plain danger prompt (no RequireCheckbox).
	d.ShowDanger("Delete Session?", "s", nil, func() tea.Msg { return nil })
	if cmd := confirmKey(d, "y"); cmd == nil {
		t.Fatal("checkbox requirement leaked into the next prompt")
	}
}

// checkoutsForOrigin must gather every checkout under an origin from all three
// sidebar sources (sessions, pinned repos, pending workspaces), deduped, and
// must not leak checkouts belonging to a different origin.
func TestCheckoutsForOrigin(t *testing.T) {
	h := newPersistTestHome(t)

	const origin = "github.com/acme/repo"
	const mainRepo = "/tmp/acme-main"
	const wtRepo = "/tmp/acme-wt"
	const otherRepo = "/tmp/other"

	gi := map[string]*git.RepoInfo{
		mainRepo:  {OriginKey: origin},
		wtRepo:    {OriginKey: origin, IsWorktreeRepo: true},
		otherRepo: {OriginKey: "github.com/acme/other"},
	}
	h.gitInfoCache.Store(&gi)

	// mainRepo via a session, wtRepo via a pinned repo + a duplicate pending
	// workspace (which must dedupe), otherRepo via a session under a different origin.
	h.sessions = []*session.Session{
		session.NewSession("s1", mainRepo),
		session.NewSession("s2", otherRepo),
	}
	h.pinnedRepos[wtRepo] = true
	h.pendingWorkspaces = []*PendingWorkspace{{RepoPath: wtRepo}}

	got := h.checkoutsForOrigin(origin)
	set := map[string]bool{}
	for _, r := range got {
		set[r] = true
	}
	if len(got) != 2 || !set[mainRepo] || !set[wtRepo] {
		t.Fatalf("checkoutsForOrigin = %v, want exactly [%s %s]", got, mainRepo, wtRepo)
	}
	if set[otherRepo] {
		t.Error("checkoutsForOrigin leaked a checkout from a different origin")
	}
}

// checkoutPathLines lists every checkout up to the cap, then collapses the rest
// into a single "+N more" line so a group-forget dialog can't grow unbounded.
func TestCheckoutPathLines(t *testing.T) {
	// Under the cap: every path is listed, none collapsed.
	got := checkoutPathLines([]string{"/a", "/b", "/c"})
	if len(got) != 3 {
		t.Fatalf("under cap: got %d lines (%v), want 3", len(got), got)
	}

	// Over the cap (4): first 4 listed, remainder folded into "…and N more".
	got = checkoutPathLines([]string{"/a", "/b", "/c", "/d", "/e", "/f"})
	if len(got) != 5 {
		t.Fatalf("over cap: got %d lines (%v), want 5", len(got), got)
	}
	if last := got[len(got)-1]; last != "…and 2 more" {
		t.Errorf("over cap: last line = %q, want %q", last, "…and 2 more")
	}
}
