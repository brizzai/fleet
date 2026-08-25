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

// typeBase replaces the Base branch field's text the way a user does — every
// rune through Update, so routeToInput's change detector runs and the field
// stops being at rest.
//
// Poking SetValue + rebuildBranchMatches instead leaves baseAtRest true from
// Show, so the assertions land on the at-rest branch while claiming to test
// filtering. Three tests here did exactly that and passed for the wrong reason.
func typeBase(t *testing.T, d *WorktreeDialog, text string) *WorktreeDialog {
	t.Helper()
	d.setSelection(focusBaseBranch, baseOnInput)
	d.baseBranchInput.SetValue("")
	for _, r := range text {
		d = d.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if d.baseAtRest {
		t.Fatalf("typing %q left the field at rest — the change detector did not run", text)
	}
	return d
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
	d = typeBase(t, d, "remote-only")

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

	d = typeBase(t, d, "origin/")
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

	// Without the prefix, the same branch comes back in its local form.
	d = typeBase(t, d, "mast")
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

	// Typing filters, whatever the text happens to be.
	d = typeBase(t, d, "mast")
	if got := d.branchMatches; !equalStrings(got, []string{"master"}) {
		t.Errorf("typed query = %v, want [master] — a field being typed into must filter", got)
	}
}

// TestBaseBranchEmptyFieldListsRecent: an emptied field is a browse, not a
// dead end.
func TestBaseBranchEmptyFieldListsRecent(t *testing.T) {
	d := branchDialog(t, branchFixture()...)
	d = typeBase(t, d, "x")
	d = d.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
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
	d = typeBase(t, d, "x")
	d = d.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
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

// TestBaseBranchWidenedListExcludesTheField — the widened list exists because a
// row echoing the field is a no-op on Enter, so putting that same row back at
// position 0 reintroduces exactly what it was there to remove.
func TestBaseBranchWidenedListExcludesTheField(t *testing.T) {
	d := branchDialog(t, branchFixture()...)
	d.setSelection(focusBaseBranch, baseOnInput)

	if len(d.branchMatches) == 0 {
		t.Fatal("no rows at rest, the test proves nothing")
	}
	for _, ref := range d.branchMatches {
		if ref == d.baseBranchInput.Value() {
			t.Errorf("widened list offered %q, the value already in the field — ⏎ on it does nothing", ref)
		}
	}
}

// TestBaseBranchExclusionRunsBeforeTheCap: filtering the field's own value out
// of matchBranches' RESULT would quietly return four rows where five fit.
func TestBaseBranchExclusionRunsBeforeTheCap(t *testing.T) {
	// The excluded branch sorts first, so a post-filter would lose a row.
	branches := []git.BranchInfo{
		{Name: "master", HasRemote: true},
		{Name: "b1", HasRemote: true}, {Name: "b2", HasRemote: true},
		{Name: "b3", HasRemote: true}, {Name: "b4", HasRemote: true},
		{Name: "b5", HasRemote: true},
	}
	d := branchDialog(t, branches...)
	d.setSelection(focusBaseBranch, baseOnInput)

	if got := len(d.branchMatches); got != branchMaxRows {
		t.Errorf("widened list has %d rows, want %d — the exclusion must be applied before the cap, not to its result",
			got, branchMaxRows)
	}
}

// TestBaseBranchTypingNeverWidens is the gate the doc sentence describes.
//
// Equality with the field was the wrong test for "at rest": typing toward
// master-fix passes THROUGH master, a complete branch name, so the list went
// 1 row → 5 → 0 across two keystrokes. The dialog is vertically centred, so
// that is the whole box jumping four rows mid-word.
func TestBaseBranchTypingNeverWidens(t *testing.T) {
	branches := []git.BranchInfo{
		{Name: "master"}, {Name: "topic-a"}, {Name: "topic-b"},
		{Name: "topic-c"}, {Name: "topic-d"},
	}
	d := branchDialog(t, branches...)

	for _, step := range []struct {
		typed string
		want  int
	}{
		{"mast", 1}, {"maste", 1}, {"master", 1}, {"master-", 0}, {"master-f", 0},
	} {
		d = typeBase(t, d, step.typed)
		if got := len(d.branchMatches); got != step.want {
			t.Errorf("typed %q -> %d rows %v, want %d", step.typed, got, d.branchMatches, step.want)
		}
	}
}

// TestBaseBranchSettlesOnOpenAndOnPick: the flip side — the two ways a field
// arrives at a value without being typed into must both widen.
func TestBaseBranchSettlesOnOpenAndOnPick(t *testing.T) {
	d := branchDialog(t, branchFixture()...)
	if !d.baseAtRest {
		t.Error("a freshly opened dialog is not at rest")
	}
	if len(d.branchMatches) < 2 {
		t.Errorf("opened with %v, want the alternatives listed", d.branchMatches)
	}

	d = typeBase(t, d, "base")
	if d.baseAtRest {
		t.Fatal("typing left the field at rest")
	}
	d.setSelection(focusBaseBranch, 0)
	d, _ = d.Update(keyEnter)

	if !d.baseAtRest {
		t.Error("accepting a suggestion left the field un-settled — it is no longer being typed into")
	}
	for _, ref := range d.branchMatches {
		if ref == d.baseBranchInput.Value() {
			t.Errorf("after accepting, the list offers %q back", ref)
		}
	}
}

// TestWorktreeTabCyclesBothWays: tab is documented as "next field", so a tab
// that does nothing on the last field is a dead key. Wrapping only forward
// would just move the dead end onto shift+tab.
func TestWorktreeTabCyclesBothWays(t *testing.T) {
	d := branchDialog(t, branchFixture()...)
	d.workspaces = []workspace.WorkspaceInfo{{Name: "wt-a", Path: "/a"}}

	order := []worktreeFocus{focusBaseBranch, focusNewBranch, focusWorktreeList}
	d.setSelection(focusBaseBranch, baseOnInput)
	for i := 1; i <= len(order); i++ { // one extra step, to land back on the start
		d = d.send(keyTab)
		if want := order[i%len(order)]; d.focus != want {
			t.Fatalf("tab #%d landed on focus=%v, want %v", i, d.focus, want)
		}
	}
	for i := len(order) - 1; i >= 0; i-- {
		d = d.send(keyShiftTab)
		if want := order[i]; d.focus != want {
			t.Fatalf("shift+tab back to %d landed on focus=%v, want %v", i, d.focus, want)
		}
	}

	// With no worktrees the cycle is two fields, and must still close.
	d2 := branchDialog(t, branchFixture()...)
	d2.setSelection(focusNewBranch, ticketOnInput)
	d2 = d2.send(keyTab)
	if d2.focus != focusBaseBranch {
		t.Errorf("tab off the last field with no worktrees landed on %v, want the base field", d2.focus)
	}
}

// TestWorktreeFocusedDialogIsNoTallerThanAtRest.
//
// The dialog has no height budget — wrapDialog only Places, and the worktree
// list loop is unbounded — so five suggestion rows took an 80x24 terminal from
// 21 box lines to 26, pushing the footer (which names what Enter does) off the
// bottom mid-interaction. Hiding the worktree list while the base field has the
// highlight reclaims more than the rows add.
//
// TestWorktreeDialogRowsNeverOverflow cannot catch this: it is width-only and
// runs at height 40.
func TestWorktreeFocusedDialogIsNoTallerThanAtRest(t *testing.T) {
	var wss []workspace.WorkspaceInfo
	for i := 0; i < 6; i++ {
		n := "wt-" + string(rune('a'+i))
		wss = append(wss, workspace.WorkspaceInfo{Name: n, Branch: "b", Path: "/" + n})
	}
	branches := []git.BranchInfo{
		{Name: "master", HasRemote: true}, {Name: "b1", HasRemote: true},
		{Name: "b2", HasRemote: true}, {Name: "b3", HasRemote: true},
		{Name: "b4", HasRemote: true}, {Name: "b5", HasRemote: true},
	}

	const termH = 24
	boxLines := func(focus worktreeFocus) int {
		d := NewWorktreeDialog()
		d.SetSize(80, termH)
		d.Show(wss, nil, nil, "/r", "origin/master", nil, branches)
		d.setSelection(focus, baseOnInput)
		n := 0
		for _, l := range strings.Split(d.View(), "\n") {
			if strings.ContainsAny(l, "│╭╰") {
				n++
			}
		}
		return n
	}

	atRest, focused := boxLines(focusNewBranch), boxLines(focusBaseBranch)
	if focused > atRest {
		t.Errorf("focusing the base field grew the dialog %d -> %d lines; the suggestions must not cost "+
			"more height than the worktree list they replace", atRest, focused)
	}
	if focused > termH {
		t.Errorf("focused dialog is %d lines on a %d-row terminal — the footer scrolls off", focused, termH)
	}
}

// TestWorktreeListHiddenWhileBaseFocusedStaysReachable: hidden is not gone. The
// list is still in the model, and shift+tab still cycles onto it.
func TestWorktreeListHiddenWhileBaseFocusedStaysReachable(t *testing.T) {
	d := branchDialog(t, branchFixture()...)
	d.workspaces = []workspace.WorkspaceInfo{{Name: "wt-alpha", Path: "/a", Branch: "b"}}

	d.setSelection(focusBaseBranch, baseOnInput)
	if strings.Contains(d.View(), "wt-alpha") {
		t.Error("worktree list rendered while the base field has the highlight")
	}

	d = d.send(keyShiftTab)
	if d.focus != focusWorktreeList {
		t.Fatalf("shift+tab from base landed on %v, want the worktree list", d.focus)
	}
	if !strings.Contains(d.View(), "wt-alpha") {
		t.Error("worktree list still hidden after cycling onto it — hidden must not mean unreachable")
	}
}
