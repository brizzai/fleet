package ui

import (
	"github.com/charmbracelet/x/ansi"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/linear"
	"github.com/brizzai/fleet/internal/workspace"
)

func ticketDialog(t *testing.T, tickets ...linear.Ticket) *WorktreeDialog {
	t.Helper()
	d := NewWorktreeDialog()
	d.SetSize(120, 40)
	d.Show(nil, nil, nil, "/repo", "master", []string{"BRZ"})
	d.tickets = tickets
	return d
}

// TestWorktreeCaretAndHighlightNeverCoexist is the invariant the whole design
// turns on: a blinking caret and a highlighted row are two things claiming the
// Enter key, and the user has to guess which one it obeys — an expensive guess,
// because a branch gets created either way.
func TestWorktreeCaretAndHighlightNeverCoexist(t *testing.T) {
	d := ticketDialog(t,
		linear.Ticket{Identifier: "BRZ-3182", Title: "Filter bar cramped"},
		linear.Ticket{Identifier: "BRZ-3040", Title: "Collapse resets"},
	)
	d.workspaces = []workspace.WorkspaceInfo{{Name: "wt-a", Path: "/a"}}

	states := []struct {
		name string
		f    worktreeFocus
		idx  int
	}{
		{"base branch", focusBaseBranch, 0},
		{"new branch, on input", focusNewBranch, ticketOnInput},
		{"new branch, ticket 0", focusNewBranch, 0},
		{"new branch, ticket 1", focusNewBranch, 1},
		{"worktree list", focusWorktreeList, 0},
	}

	for _, s := range states {
		d.setSelection(s.f, s.idx)

		onInput := d.focus == focusNewBranch && d.ticketCursor == ticketOnInput
		if got := d.newBranchInput.Focused(); got != onInput {
			t.Errorf("%s: new-branch caret = %v, want %v — the caret must live exactly where the highlight is",
				s.name, got, onInput)
		}
		if got := d.baseBranchInput.Focused(); got != (d.focus == focusBaseBranch) {
			t.Errorf("%s: base caret = %v", s.name, got)
		}

		// The render is the thing the user actually reads: exactly one marker.
		if n := strings.Count(d.View(), "▸"); n > 1 {
			t.Errorf("%s: %d ▸ markers on screen, want at most 1 — two highlighted rows "+
				"means two things claim the enter key", s.name, n)
		}
	}
}

// TestWorktreeSelectionMutatorIsTheOnlyWriter is what keeps the test above true
// six months from now: a stray `d.focus = x` anywhere else skips the clamping
// and the caret sync, and reintroduces the double highlight.
func TestWorktreeSelectionMutatorIsTheOnlyWriter(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "workspace_picker.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	writesInsideMutator := 0
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			as, ok := m.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range as.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok || ident.Name != "d" {
					continue
				}
				switch sel.Sel.Name {
				case "focus", "ticketCursor":
					if fn.Name.Name != "setSelection" {
						t.Errorf("%s writes d.%s directly at %s — only setSelection may move the "+
							"highlight, or the caret and the marker drift apart",
							fn.Name.Name, sel.Sel.Name, fset.Position(as.Pos()))
					} else {
						writesInsideMutator++
					}
				}
			}
			return true
		})
		return true
	})

	// Positive control: a scanner that finds nothing would pass vacuously.
	if writesInsideMutator < 2 {
		t.Fatalf("found only %d highlight writes inside setSelection — the scanner is broken", writesInsideMutator)
	}
}

// TestWorktreeStaleTicketReplyIgnored: type BRZ-3182, edit to BRZ-3184, and the
// slower first reply must not win — otherwise the field gets the wrong ticket's
// branch name, and that becomes a real git branch.
func TestWorktreeStaleTicketReplyIgnored(t *testing.T) {
	d := ticketDialog(t)
	d.newBranchInput.SetValue("BRZ-3182")
	d.onFieldChanged("BRZ-3182")
	stale := d.ticketGen

	d.newBranchInput.SetValue("BRZ-3184")
	d.onFieldChanged("BRZ-3184")
	current := d.ticketGen
	if stale == current {
		t.Fatal("generation did not advance on an edit")
	}

	d.applyTickets(worktreeTicketsMsg{
		gen: stale, byID: true,
		tickets: []linear.Ticket{{Identifier: "BRZ-3182", Title: "the wrong one"}},
	})
	if d.resolved != nil {
		t.Errorf("a stale reply resolved %s onto a field reading %q",
			d.resolved.Identifier, d.newBranchInput.Value())
	}

	d.applyTickets(worktreeTicketsMsg{
		gen: current, byID: true,
		tickets: []linear.Ticket{{Identifier: "BRZ-3184", Title: "the right one"}},
	})
	if d.resolved == nil || d.resolved.Identifier != "BRZ-3184" {
		t.Errorf("current reply did not install: %+v", d.resolved)
	}
}

// TestWorktreeTicketReplyNeverMovesHighlight — a picker that moves its own
// selection is the ambiguity coming back through the window.
func TestWorktreeTicketReplyNeverMovesHighlight(t *testing.T) {
	d := ticketDialog(t)
	d.newBranchInput.SetValue("drawer")
	d.onFieldChanged("drawer")
	gen := d.ticketGen

	d.applyTickets(worktreeTicketsMsg{gen: gen, tickets: []linear.Ticket{
		{Identifier: "BRZ-3182", Title: "a"}, {Identifier: "BRZ-3040", Title: "b"},
	}})
	if d.ticketCursor != ticketOnInput {
		t.Errorf("arriving suggestions moved the highlight to row %d; it must stay on the input", d.ticketCursor)
	}
	if !d.newBranchInput.Focused() {
		t.Error("caret left the field when suggestions arrived")
	}

	// A shorter list must clamp rather than strand the cursor off the end.
	d.setSelection(focusNewBranch, 1)
	d.onFieldChanged("drawer2")
	gen = d.ticketGen
	d.applyTickets(worktreeTicketsMsg{gen: gen, tickets: []linear.Ticket{{Identifier: "BRZ-1", Title: "only"}}})
	if d.ticketCursor != 0 {
		t.Errorf("cursor = %d after the list shrank to 1 row, want 0", d.ticketCursor)
	}
}

// TestWorktreeTypingReturnsHighlightAndKeepsKeystroke pins both halves: the
// jump back, and that the character that caused it is not swallowed.
func TestWorktreeTypingReturnsHighlightAndKeepsKeystroke(t *testing.T) {
	d := ticketDialog(t, linear.Ticket{Identifier: "BRZ-3182", Title: "x"})
	d.newBranchInput.SetValue("dra")
	d.setSelection(focusNewBranch, 0)
	if d.newBranchInput.Focused() {
		t.Fatal("precondition: caret should have left the field")
	}

	d, _ = d.Update(tea.KeyPressMsg{Code: 'w', Text: "w"})

	if d.ticketCursor != ticketOnInput {
		t.Error("typing did not return the highlight to the input")
	}
	if !strings.HasSuffix(d.newBranchInput.Value(), "w") {
		t.Errorf("the keystroke that caused the jump was swallowed: field = %q", d.newBranchInput.Value())
	}
}

// TestWorktreeEnterOnTicketFillsDerivedName — Enter on a row fills the field and
// does NOT create: the base branch may still be wrong and the name must stay
// editable. The second Enter is the one that acts.
func TestWorktreeEnterOnTicketFillsDerivedName(t *testing.T) {
	d := ticketDialog(t, linear.Ticket{Identifier: "BRZ-3182", Title: "Filter bar renders cramped"})
	d.setSelection(focusNewBranch, 0)

	d, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Error("Enter on a ticket row must not create a worktree")
	}
	want := "brz-3182-filter-bar-renders-cramped"
	if got := d.newBranchInput.Value(); got != want {
		t.Errorf("field = %q, want %q", got, want)
	}
	if strings.Contains(d.newBranchInput.Value(), "/") {
		t.Error("derived branch must not carry Linear's owner prefix")
	}
	if !d.newBranchInput.Focused() {
		t.Error("after picking, the caret must return to the field so the name can be edited")
	}

	_, cmd = d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("the second Enter must create")
	}
	msg, ok := cmd().(workspaceCreateMsg)
	if !ok {
		t.Fatalf("got %T, want workspaceCreateMsg", cmd())
	}
	if msg.ticket == nil || msg.ticket.Identifier != "BRZ-3182" {
		t.Errorf("creation message lost the ticket: %+v", msg.ticket)
	}
	if msg.branch != want {
		t.Errorf("branch = %q, want %q", msg.branch, want)
	}
}

// TestWorktreeEnterAlwaysCreates: no Linear state may ever block the dialog's
// primary action. The suggestion list can be empty, loading, latched off, or
// erroring — Enter still makes a worktree.
func TestWorktreeEnterAlwaysCreates(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*WorktreeDialog)
	}{
		{"no linear at all", func(d *WorktreeDialog) { d.linearTeams = nil }},
		{"latched off", func(d *WorktreeDialog) { d.ticketsOff = true }},
		{"lookup in flight", func(d *WorktreeDialog) { d.ticketPending = true }},
		{"error note showing", func(d *WorktreeDialog) { d.ticketNote = "linear: timed out" }},
		{"suggestions present", func(d *WorktreeDialog) {
			d.tickets = []linear.Ticket{{Identifier: "BRZ-1", Title: "x"}}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := ticketDialog(t)
			c.setup(d)
			d.newBranchInput.SetValue("my-experiment")

			_, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if cmd == nil {
				t.Fatal("Enter did not create")
			}
			msg, ok := cmd().(workspaceCreateMsg)
			if !ok {
				t.Fatalf("got %T", cmd())
			}
			if msg.branch != "my-experiment" {
				t.Errorf("branch = %q, want the literal text", msg.branch)
			}
			if msg.ticket != nil {
				t.Errorf("a typed name must carry no ticket, got %+v", msg.ticket)
			}
		})
	}
}

// TestWorktreeTicketDropsWhenFieldEditedAway: pick a ticket, then clear the
// field and type something else — the creation message must not still claim it.
func TestWorktreeTicketDropsWhenFieldEditedAway(t *testing.T) {
	d := ticketDialog(t, linear.Ticket{Identifier: "BRZ-3182", Title: "Filter bar"})
	d.setSelection(focusNewBranch, 0)
	d, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Editing the tail keeps the link — that is the point of the prefix rule.
	d.newBranchInput.SetValue("brz-3182-filter-bar-v2")
	d.onFieldChanged("brz-3182-filter-bar-v2")
	if d.ticketForCurrentInput() == nil {
		t.Error("appending to a derived name should keep the ticket link")
	}

	d.newBranchInput.SetValue("scratch")
	d.onFieldChanged("scratch")
	if got := d.ticketForCurrentInput(); got != nil {
		t.Errorf("field says %q but the message still claims %s",
			d.newBranchInput.Value(), got.Identifier)
	}
}

// TestWorktreeBlankRenderUnchangedWithoutLinear: a user with no Linear must not
// be able to tell this feature shipped.
func TestWorktreeBlankRenderUnchangedWithoutLinear(t *testing.T) {
	mk := func(teams []string) string {
		d := NewWorktreeDialog()
		d.SetSize(120, 40)
		d.Show(nil, nil, nil, "/repo", "master", teams)
		d.newBranchInput.SetValue("my-experiment")
		return d.View()
	}
	if mk(nil) == "" {
		t.Fatal("empty render")
	}
	plain := mk(nil)
	if strings.Contains(plain, "BRZ") || strings.Contains(plain, "ticket") {
		t.Errorf("a repo with no .linear.toml shows Linear chrome:\n%s", plain)
	}
	if !strings.Contains(plain, "tab: next  enter: create  esc: cancel") {
		t.Error("the long-standing footer should still be the default")
	}
}

// TestWorktreeFooterNamesEnter — the footer is the words half of the promise.
func TestWorktreeFooterNamesEnter(t *testing.T) {
	d := ticketDialog(t, linear.Ticket{Identifier: "BRZ-3182", Title: "Filter bar"})

	d.newBranchInput.SetValue("my-experiment")
	d.setSelection(focusNewBranch, ticketOnInput)
	if got := d.ticketFooter(); strings.Contains(got, "BRZ-3182") {
		t.Errorf("footer names a ticket while the highlight is on the input: %q", got)
	}

	d.setSelection(focusNewBranch, 0)
	if got := d.ticketFooter(); !strings.Contains(got, "BRZ-3182") {
		t.Errorf("footer = %q, want it to name the highlighted ticket", got)
	}

	d.workspaces = []workspace.WorkspaceInfo{{Name: "a", Path: "/a"}}
	d.setSelection(focusWorktreeList, 0)
	if got := d.ticketFooter(); !strings.Contains(got, "open worktree") {
		t.Errorf("footer = %q, want it to name opening a worktree", got)
	}
}

// TestTypedIdentifierBecomesTheBranchName is the bug this file's whole design
// was already written for and did not do.
//
// Typing BRZ-3217 fetched the ticket, materialized it into the worktree and
// named the session after it — and then created a git worktree literally called
// BRZ-3217, because the by-id reply set d.resolved and never touched the field.
// pickTicket promises in its own comment that "both ways of naming a ticket end
// up identical"; only the arrow-down path kept that promise.
func TestTypedIdentifierBecomesTheBranchName(t *testing.T) {
	d := ticketDialog(t)
	d.newBranchInput.SetValue("BRZ-3217")
	d.onFieldChanged("BRZ-3217")

	d.applyTickets(worktreeTicketsMsg{
		gen: d.ticketGen, byID: true,
		tickets: []linear.Ticket{{Identifier: "BRZ-3217", Title: "Fix the ingest guide"}},
	})

	want := linear.BranchNameFor("BRZ-3217", "Fix the ingest guide")
	if got := d.newBranchInput.Value(); got != want {
		t.Errorf("field should hold the branch name, not the bare identifier:\n got %q\nwant %q", got, want)
	}
	// The two paths must agree, which is the invariant that was broken.
	picked := ticketDialog(t)
	picked.pickTicket(linear.Ticket{Identifier: "BRZ-3217", Title: "Fix the ingest guide"})
	if picked.newBranchInput.Value() != d.newBranchInput.Value() {
		t.Errorf("typing and picking must produce the same branch: %q vs %q",
			d.newBranchInput.Value(), picked.newBranchInput.Value())
	}
	// The cursor has to follow, or the next keystroke lands mid-word.
	if d.newBranchInput.Position() != len([]rune(want)) {
		t.Errorf("cursor should sit at the end of the rewritten name, got %d want %d",
			d.newBranchInput.Position(), len([]rune(want)))
	}
}

// TestResolvedRewriteOnlyTouchesABareIdentifier guards the destructive half.
//
// The generation counter drops a reply a later keystroke invalidated, but it
// cannot see this: you pause on BRZ-321 on the way to BRZ-3217, the pause earns
// a round trip, BRZ-321 exists, and the reply is perfectly current by the time
// it lands. Rewriting then would put brz-321-<slug> under the cursor and the
// rest of what you typed would land on the end of it.
//
// The same check protects a name the user has already extended by hand.
func TestResolvedRewriteOnlyTouchesABareIdentifier(t *testing.T) {
	cases := []struct {
		name  string
		field string
	}{
		{"user typed past the resolved id", "BRZ-32170"},
		{"user already extended the name", "brz-3217-my-own-variant"},
		{"user typed a plain branch", "just-a-branch"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := ticketDialog(t)
			d.newBranchInput.SetValue(c.field)
			d.onFieldChanged(c.field)
			d.applyTickets(worktreeTicketsMsg{
				gen: d.ticketGen, byID: true,
				tickets: []linear.Ticket{{Identifier: "BRZ-3217", Title: "Fix the ingest guide"}},
			})
			if got := d.newBranchInput.Value(); got != c.field {
				t.Errorf("the field must not be rewritten under the user: got %q, want it left as %q", got, c.field)
			}
		})
	}
}

// TestResolvedLineReadsAsAppliedNotOffered pins the wording that keeps the
// confirmation line from impersonating a suggestion.
//
// It renders in the same place the selectable ticket rows do, so as a bare
// "BRZ-3217 · title" it read as a row you might still have to arrow onto — when
// in fact the naming had already happened and arrowing there does nothing. It
// is also the only thing on screen that explains why the text in the field
// changed by itself a moment earlier.
func TestResolvedLineReadsAsAppliedNotOffered(t *testing.T) {
	for _, c := range []struct{ name, field string }{
		{"auto-rewritten from a bare id", "BRZ-3217"},
		{"user typed the tail themselves", "brz-3217-my-own-variant"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := ticketDialog(t)
			d.SetSize(90, 44)
			d.newBranchInput.SetValue(c.field)
			d.onFieldChanged(c.field)
			d.applyTickets(worktreeTicketsMsg{
				gen: d.ticketGen, byID: true,
				tickets: []linear.Ticket{{Identifier: "BRZ-3217", Title: "Fix with external AI agent"}},
			})

			got := ansi.Strip(d.View())
			if !strings.Contains(got, "✓ named from BRZ-3217") {
				t.Errorf("the resolved line must say the naming already happened:\n%s", got)
			}
			// Nothing may offer an arrow-down that would achieve nothing.
			if d.visibleTicketCount() != 0 {
				t.Errorf("a resolved identifier must leave no selectable rows, got %d", d.visibleTicketCount())
			}
			if strings.Contains(got, "↓ tickets") {
				t.Errorf("the footer must not invite ↓ when there is nothing to pick:\n%s", got)
			}
		})
	}
}
