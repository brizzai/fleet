package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/workspace"
)

// branchFixture covers the three shapes that behave differently: local-only,
// local with a remote counterpart, and remote-only.
func branchFixture() []git.BranchInfo {
	return []git.BranchInfo{
		{Name: "master", HasRemote: true},
		{Name: "base-branch", HasRemote: true},
		{Name: "fix/local-only"},
		{Name: "feat/remote-only", IsRemote: true, HasRemote: true},
	}
}

func branchDialog(t *testing.T, branches ...git.BranchInfo) *WorktreeDialog {
	t.Helper()
	d := NewWorktreeDialog()
	d.SetSize(120, 40)
	d.Show(nil, nil, nil, "/repo", "origin/master", nil, branches)
	return d
}

var (
	keyDown     = tea.KeyPressMsg{Code: tea.KeyDown}
	keyUp       = tea.KeyPressMsg{Code: tea.KeyUp}
	keyTab      = tea.KeyPressMsg{Code: tea.KeyTab}
	keyShiftTab = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	keyEnter    = tea.KeyPressMsg{Code: tea.KeyEnter}
)

func (d *WorktreeDialog) send(msg tea.Msg) *WorktreeDialog {
	out, _ := d.Update(msg)
	return out
}

// TestBaseBranchSuggestionsMatchOnName: the ordinary case — type a fragment,
// get the branches containing it, in the form that goes into the field.
func TestBaseBranchSuggestionsMatchOnName(t *testing.T) {
	d := branchDialog(t, branchFixture()...)
	d.setSelection(focusBaseBranch, baseOnInput)
	d.baseBranchInput.SetValue("")
	d.lastBaseInput = ""
	d.rebuildBranchMatches()

	// Typed rather than injected: this is the path routeToInput's change
	// detector actually runs on, and a test that skips it would pass with the
	// detector unwired.
	for _, r := range "branch" {
		d = d.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if got := d.baseBranchInput.Value(); got != "branch" {
		t.Fatalf("field = %q after typing, want %q", got, "branch")
	}

	want := []string{"base-branch"}
	if got := d.branchMatches; !equalStrings(got, want) {
		t.Errorf("matches = %v, want %v", got, want)
	}
}

// TestBaseBranchRemoteOnlyIsAlwaysPrefixed is the dead-click guard. `git
// worktree add <path> -b <new> <base>` resolves <base> as a plain revision with
// no remote-tracking DWIM, so a bare remote-only name would fail on Enter after
// looking perfectly valid on screen.
func TestBaseBranchRemoteOnlyIsAlwaysPrefixed(t *testing.T) {
	d := branchDialog(t, branchFixture()...)
	d.baseBranchInput.SetValue("remote-only")
	d.rebuildBranchMatches()

	want := []string{"origin/feat/remote-only"}
	if got := d.branchMatches; !equalStrings(got, want) {
		t.Errorf("matches = %v, want %v — a remote-only branch must be offered as a ref that resolves", got, want)
	}
}

// TestBaseBranchOriginPrefixIsPreserved: GetDefaultBranch pre-fills
// origin/<default> so a worktree starts from the remote tip. A picker that
// silently swapped that for the local branch would change what gets built.
func TestBaseBranchOriginPrefixIsPreserved(t *testing.T) {
	d := branchDialog(t, branchFixture()...)

	d.baseBranchInput.SetValue("origin/")
	d.rebuildBranchMatches()
	for _, ref := range d.branchMatches {
		if !strings.HasPrefix(ref, "origin/") {
			t.Errorf("origin/ query offered %q — the prefix the user typed must survive", ref)
		}
	}
	// The local-only branch has no origin/ ref, so it cannot answer this query.
	for _, ref := range d.branchMatches {
		if strings.Contains(ref, "local-only") {
			t.Errorf("origin/ query offered %q, which has no remote", ref)
		}
	}

	// Without the prefix, the same branch comes back in its local form. Typed
	// partially so the at-rest widening (see TestBaseBranchAtRestFieldListsTheRest)
	// stays out of the way — this is about the prefix, not the list length.
	d.baseBranchInput.SetValue("mast")
	d.rebuildBranchMatches()
	if got := d.branchMatches; !equalStrings(got, []string{"master"}) {
		t.Errorf("bare query = %v, want [master] — no prefix asked for, none added", got)
	}
}

// TestBaseBranchAtRestFieldListsTheRest is why the pre-filled origin/<default>
// does not make this feature useless on the one screen it matters on. Filtering
// that text yields exactly one row echoing the field, which answers nothing and
// does nothing on Enter — so an at-rest field lists the alternatives instead.
func TestBaseBranchAtRestFieldListsTheRest(t *testing.T) {
	d := branchDialog(t, branchFixture()...)

	// Show() pre-fills origin/master, which matches origin/master and nothing else.
	if len(d.branchMatches) < 2 {
		t.Fatalf("field at rest on %q offered %v — want the other branches, not one row repeating the field",
			d.baseBranchInput.Value(), d.branchMatches)
	}
	for _, ref := range d.branchMatches {
		if !strings.HasPrefix(ref, "origin/") {
			t.Errorf("widened list offered %q — the origin/ the field asked for must survive", ref)
		}
	}

	// A partial match that is NOT the whole field still filters normally: the
	// widening is for the at-rest case only, not a general fallback.
	d.baseBranchInput.SetValue("mast")
	d.rebuildBranchMatches()
	if got := d.branchMatches; !equalStrings(got, []string{"master"}) {
		t.Errorf("partial query = %v, want [master] — a unique match must not widen", got)
	}
}

// TestBaseBranchEmptyFieldListsRecent: an emptied field is a browse, not a
// dead end.
func TestBaseBranchEmptyFieldListsRecent(t *testing.T) {
	d := branchDialog(t, branchFixture()...)
	d.baseBranchInput.SetValue("")
	d.rebuildBranchMatches()
	if len(d.branchMatches) != len(branchFixture()) {
		t.Errorf("empty field matched %d branches, want all %d", len(d.branchMatches), len(branchFixture()))
	}
}

// TestBaseBranchTabSkipsSuggestionRows is the whole point of splitting tab from
// ↓: five suggestion rows must never sit between you and the next field.
func TestBaseBranchTabSkipsSuggestionRows(t *testing.T) {
	d := branchDialog(t, branchFixture()...)
	d.setSelection(focusBaseBranch, baseOnInput)
	if d.visibleBranchCount() == 0 {
		t.Fatal("fixture produced no rows, the test proves nothing")
	}

	d = d.send(keyTab)
	if d.focus != focusNewBranch || d.ticketCursor != ticketOnInput {
		t.Errorf("tab landed on focus=%v cursor=%d, want the New branch field", d.focus, d.ticketCursor)
	}

	d = d.send(keyShiftTab)
	if d.focus != focusBaseBranch || d.baseCursor != baseOnInput {
		t.Errorf("shift+tab landed on focus=%v cursor=%d, want the Base branch field", d.focus, d.baseCursor)
	}
}

// TestBaseBranchArrowsWalkRowsBothWays: ↓ and ↑ must retrace the same path, or
// backing out of a suggestion list lands somewhere you were never offered.
func TestBaseBranchArrowsWalkRowsBothWays(t *testing.T) {
	d := branchDialog(t, branchFixture()...)
	d.setSelection(focusBaseBranch, baseOnInput)
	n := d.visibleBranchCount()

	for i := 0; i < n; i++ {
		d = d.send(keyDown)
		if d.focus != focusBaseBranch || d.baseCursor != i {
			t.Fatalf("after %d ↓: focus=%v cursor=%d, want base row %d", i+1, d.focus, d.baseCursor, i)
		}
	}
	d = d.send(keyDown)
	if d.focus != focusNewBranch || d.ticketCursor != ticketOnInput {
		t.Fatalf("↓ past the last row: focus=%v, want the New branch field", d.focus)
	}

	d = d.send(keyUp)
	if d.focus != focusBaseBranch || d.baseCursor != n-1 {
		t.Fatalf("↑ from the New branch field: focus=%v cursor=%d, want base row %d", d.focus, d.baseCursor, n-1)
	}
	for i := n - 2; i >= 0; i-- {
		d = d.send(keyUp)
		if d.baseCursor != i {
			t.Fatalf("↑ walk: cursor=%d, want %d", d.baseCursor, i)
		}
	}
	d = d.send(keyUp)
	if d.baseCursor != baseOnInput || !d.baseBranchInput.Focused() {
		t.Fatalf("↑ from row 0: cursor=%d focused=%v, want the field", d.baseCursor, d.baseBranchInput.Focused())
	}
}

// TestBaseBranchEnterFillsAndDoesNotCreate: Enter on a row is an accept, not a
// submit — the New branch field is still empty at that point.
func TestBaseBranchEnterFillsAndDoesNotCreate(t *testing.T) {
	d := branchDialog(t, branchFixture()...)
	d.setSelection(focusBaseBranch, 0)
	want := d.branchMatches[0]

	d, cmd := d.Update(keyEnter)
	if cmd != nil {
		t.Error("enter on a suggestion returned a command — it must not create a worktree")
	}
	if !d.visible {
		t.Error("enter on a suggestion closed the dialog")
	}
	if got := d.baseBranchInput.Value(); got != want {
		t.Errorf("field = %q, want %q", got, want)
	}
	if d.baseCursor != baseOnInput || !d.baseBranchInput.Focused() {
		t.Errorf("after accept: cursor=%d focused=%v, want the highlight and caret back on the field",
			d.baseCursor, d.baseBranchInput.Focused())
	}
}

// TestBaseBranchTypingFromARowKeepsTheKeystroke: the rule the snooze dialog set
// and the ticket rows follow — returning to the field must not eat the letter
// that returned you to it.
func TestBaseBranchTypingFromARowKeepsTheKeystroke(t *testing.T) {
	d := branchDialog(t, branchFixture()...)
	d.baseBranchInput.SetValue("")
	d.rebuildBranchMatches()
	d.setSelection(focusBaseBranch, 0)

	d = d.send(key("m"))
	if d.baseCursor != baseOnInput {
		t.Errorf("typing left the highlight on row %d", d.baseCursor)
	}
	if got := d.baseBranchInput.Value(); got != "m" {
		t.Errorf("field = %q, want %q — the keystroke that moved the highlight must still land", got, "m")
	}
}

// TestBaseBranchRowsHiddenUnlessFocused: the field always holds text, so
// rendering unconditionally would park five rows in the dialog for every user
// including everyone who never touches the base branch.
func TestBaseBranchRowsHiddenUnlessFocused(t *testing.T) {
	d := branchDialog(t, branchFixture()...)
	d.workspaces = []workspace.WorkspaceInfo{{Name: "wt-a", Path: "/a"}}

	// Show() leaves focus on the New branch field.
	if got := d.renderBranchBlock(d.innerWidth()); got != "" {
		t.Errorf("rows rendered with focus on the New branch field:\n%s", got)
	}
	d.setSelection(focusBaseBranch, baseOnInput)
	if got := d.renderBranchBlock(d.innerWidth()); got == "" {
		t.Error("no rows rendered with the Base branch field focused")
	}
	if n := strings.Count(d.View(), "▸"); n > 1 {
		t.Errorf("%d ▸ markers on screen, want at most 1", n)
	}
}

// TestBaseBranchWithoutBranchesIsInert: a failed `git for-each-ref` must cost
// the suggestions and nothing else.
func TestBaseBranchWithoutBranchesIsInert(t *testing.T) {
	with := branchDialog(t)
	without := NewWorktreeDialog()
	without.SetSize(120, 40)
	without.Show(nil, nil, nil, "/repo", "origin/master", nil, nil)

	if with.View() != without.View() {
		t.Error("a dialog with no branches renders differently from one that never had any")
	}

	d := branchDialog(t)
	d.setSelection(focusBaseBranch, baseOnInput)
	d = d.send(keyDown)
	if d.focus != focusNewBranch {
		t.Errorf("↓ with no rows landed on focus=%v, want the New branch field", d.focus)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
