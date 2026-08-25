package ui

import (
	"github.com/charmbracelet/x/ansi"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/jira"
	"github.com/brizzai/fleet/internal/linear"
	"github.com/brizzai/fleet/internal/ticket"
	"github.com/brizzai/fleet/internal/ticketing"
	"github.com/brizzai/fleet/internal/workspace"
)

// boundTo builds the provider set the worktree dialog is handed, from tracker
// keys alone.
//
// The real provider values are used rather than a stub: both are zero-size and
// only Kind() and Name() are reached from a test, since every lookup reply is
// injected as a message rather than fetched. Passing the real ones also means a
// provider that changed its Kind breaks these tests, which is the point.
func boundTo(keys ...string) []ticketing.Bound {
	if len(keys) == 0 {
		return nil
	}
	return []ticketing.Bound{{Provider: linear.New(), Keys: keys}}
}

// boundToBoth hands the dialog two trackers, which is the state every
// per-provider rule here exists for.
func boundToBoth(linearKeys, jiraKeys []string) []ticketing.Bound {
	var out []ticketing.Bound
	if len(linearKeys) > 0 {
		out = append(out, ticketing.Bound{Provider: linear.New(), Keys: linearKeys})
	}
	if len(jiraKeys) > 0 {
		out = append(out, ticketing.Bound{Provider: jira.New(), Keys: jiraKeys})
	}
	return out
}

func ticketDialog(t *testing.T, tickets ...ticket.Ticket) *WorktreeDialog {
	t.Helper()
	d := NewWorktreeDialog()
	d.SetSize(120, 40)
	d.Show(nil, nil, nil, "/repo", "master", boundTo("BRZ"))
	d.tickets = tickets
	return d
}

// TestWorktreeCaretAndHighlightNeverCoexist is the invariant the whole design
// turns on: a blinking caret and a highlighted row are two things claiming the
// Enter key, and the user has to guess which one it obeys — an expensive guess,
// because a branch gets created either way.
func TestWorktreeCaretAndHighlightNeverCoexist(t *testing.T) {
	d := ticketDialog(t,
		ticket.Ticket{Identifier: "BRZ-3182", Title: "Filter bar cramped"},
		ticket.Ticket{Identifier: "BRZ-3040", Title: "Collapse resets"},
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
		tickets: []ticket.Ticket{{Identifier: "BRZ-3182", Title: "the wrong one"}},
	})
	if d.resolved != nil {
		t.Errorf("a stale reply resolved %s onto a field reading %q",
			d.resolved.Identifier, d.newBranchInput.Value())
	}

	d.applyTickets(worktreeTicketsMsg{
		gen: current, byID: true,
		tickets: []ticket.Ticket{{Identifier: "BRZ-3184", Title: "the right one"}},
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

	d.applyTickets(worktreeTicketsMsg{gen: gen, tickets: []ticket.Ticket{
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
	d.applyTickets(worktreeTicketsMsg{gen: gen, tickets: []ticket.Ticket{{Identifier: "BRZ-1", Title: "only"}}})
	if d.ticketCursor != 0 {
		t.Errorf("cursor = %d after the list shrank to 1 row, want 0", d.ticketCursor)
	}
}

// TestWorktreeTypingReturnsHighlightAndKeepsKeystroke pins both halves: the
// jump back, and that the character that caused it is not swallowed.
func TestWorktreeTypingReturnsHighlightAndKeepsKeystroke(t *testing.T) {
	d := ticketDialog(t, ticket.Ticket{Identifier: "BRZ-3182", Title: "x"})
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
	d := ticketDialog(t, ticket.Ticket{Identifier: "BRZ-3182", Title: "Filter bar renders cramped"})
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

// TestWorktreeEnterAlwaysCreates: no tracker state may ever block the dialog's
// primary action. The suggestion list can be empty, loading, latched off, or
// erroring — Enter still makes a worktree.
func TestWorktreeEnterAlwaysCreates(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*WorktreeDialog)
	}{
		{"no tracker at all", func(d *WorktreeDialog) { d.bound, d.ticketKeys = nil, nil }},
		{"latched off", func(d *WorktreeDialog) { d.markTrackerOff("linear") }},
		{"lookup in flight", func(d *WorktreeDialog) { d.ticketPending = true }},
		{"error note showing", func(d *WorktreeDialog) { d.ticketNote = "Linear: timed out" }},
		{"suggestions present", func(d *WorktreeDialog) {
			d.tickets = []ticket.Ticket{{Identifier: "BRZ-1", Title: "x"}}
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
	d := ticketDialog(t, ticket.Ticket{Identifier: "BRZ-3182", Title: "Filter bar"})
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

// TestWorktreeBlankRenderUnchangedWithoutTracker: a user with no tracker must
// not be able to tell this feature shipped.
//
// The opt-out-by-absence rule, executable. It has to hold for BOTH providers
// now — the failure it guards against is a connected Jira user seeing ticket
// chrome under the branch field of every unrelated repo on their machine, which
// is exactly what a "search every project on the site" fallback would produce.
func TestWorktreeBlankRenderUnchangedWithoutTracker(t *testing.T) {
	mk := func(teams []string) string {
		d := NewWorktreeDialog()
		d.SetSize(120, 40)
		d.Show(nil, nil, nil, "/repo", "master", boundTo(teams...))
		d.newBranchInput.SetValue("my-experiment")
		return d.View()
	}
	if mk(nil) == "" {
		t.Fatal("empty render")
	}
	plain := mk(nil)
	if strings.Contains(plain, "BRZ") || strings.Contains(plain, "ticket") {
		t.Errorf("a repo that names no team and no project shows ticket chrome:\n%s", plain)
	}
	if !strings.Contains(plain, "tab: next  enter: create  esc: cancel") {
		t.Error("the long-standing footer should still be the default")
	}

	// And the same for a repo that names a Jira project: the chrome must appear
	// only because THIS repo asked for it.
	jiraRepo := func() string {
		d := NewWorktreeDialog()
		d.SetSize(120, 40)
		d.Show(nil, nil, nil, "/repo", "master", boundToBoth(nil, []string{"OPS"}))
		d.newBranchInput.SetValue("my-experiment")
		return d.View()
	}()
	if !strings.Contains(jiraRepo, "OPS") {
		t.Errorf("a Jira-tracked repo should disclose its project key:\n%s", jiraRepo)
	}
}

// TestWorktreeFooterNamesEnter — the footer is the words half of the promise.
func TestWorktreeFooterNamesEnter(t *testing.T) {
	d := ticketDialog(t, ticket.Ticket{Identifier: "BRZ-3182", Title: "Filter bar"})

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
		tickets: []ticket.Ticket{{Identifier: "BRZ-3217", Title: "Fix the ingest guide"}},
	})

	want := ticket.BranchNameFor("BRZ-3217", "Fix the ingest guide")
	if got := d.newBranchInput.Value(); got != want {
		t.Errorf("field should hold the branch name, not the bare identifier:\n got %q\nwant %q", got, want)
	}
	// The two paths must agree, which is the invariant that was broken.
	picked := ticketDialog(t)
	picked.pickTicket(ticket.Ticket{Identifier: "BRZ-3217", Title: "Fix the ingest guide"})
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
				tickets: []ticket.Ticket{{Identifier: "BRZ-3217", Title: "Fix the ingest guide"}},
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
				tickets: []ticket.Ticket{{Identifier: "BRZ-3217", Title: "Fix with external AI agent"}},
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

// TestOneBrokenTrackerDoesNotSilenceTheOther pins why ticketsOff is a map.
//
// With Linear and Jira both configured, a single flag meant a rejected Jira
// token took the Linear suggestions down with it — two credentials that fail
// completely independently, latched together. The note must name the tracker
// that failed, and the surviving one must keep answering.
func TestOneBrokenTrackerDoesNotSilenceTheOther(t *testing.T) {
	d := NewWorktreeDialog()
	d.SetSize(120, 40)
	d.Show(nil, nil, nil, "/repo", "master", boundToBoth([]string{"BRZ"}, []string{"OPS"}))

	d.applyTickets(worktreeTicketsMsg{
		gen: d.ticketGen, query: "OPS-1", byID: true,
		err: ticket.ErrNotAuthenticated, tracker: "Jira", provider: "jira",
	})

	if !d.ticketsOff["jira"] {
		t.Error("a rejected Jira credential should latch Jira off")
	}
	if d.ticketsOff["linear"] {
		t.Error("Linear was latched off by Jira's failure")
	}
	if !d.ticketsEnabled() {
		t.Error("the dialog went inert though Linear is still usable")
	}
	if !strings.Contains(d.ticketNote, "Jira") {
		t.Errorf("note should name the tracker that failed, got %q", d.ticketNote)
	}

	// And the live set narrows to the working tracker, so no further round trip
	// is spent on the broken one.
	bound, keys, names := d.live()
	if len(bound) != 1 || bound[0].Provider.Kind() != "linear" {
		t.Errorf("live bound = %+v, want Linear only", bound)
	}
	if len(keys) != 1 || keys[0] != "BRZ" {
		t.Errorf("live keys = %v, want [BRZ] — an OPS identifier must stop resolving", keys)
	}
	if len(names) != 1 || names[0] != "Linear" {
		t.Errorf("live names = %v", names)
	}

	// Latching the last one off finally makes the dialog inert.
	d.applyTickets(worktreeTicketsMsg{
		gen: d.ticketGen, query: "BRZ-1", byID: true,
		err: ticket.ErrNotAuthenticated, tracker: "Linear", provider: "linear",
	})
	if d.ticketsEnabled() {
		t.Error("with both trackers latched off the dialog must go inert")
	}
}

// TestFanOutSearchFailureLatchesNothing: a search that failed belongs to no
// single provider, so latching on it would silence a tracker that may be fine.
func TestFanOutSearchFailureLatchesNothing(t *testing.T) {
	d := NewWorktreeDialog()
	d.SetSize(120, 40)
	d.Show(nil, nil, nil, "/repo", "master", boundToBoth([]string{"BRZ"}, []string{"OPS"}))

	d.applyTickets(worktreeTicketsMsg{gen: d.ticketGen, query: "some prose", err: ticket.ErrNotAuthenticated})

	if len(d.ticketsOff) != 0 {
		t.Errorf("a fan-out failure latched %v — it names no single provider", d.ticketsOff)
	}
	if !strings.Contains(d.ticketNote, "tickets") {
		t.Errorf("a fan-out failure should say \"tickets\", not guess a tracker: %q", d.ticketNote)
	}
}

// TestSearchingLineNamesTheTrackers: with two connected, a user who just added
// Jira needs to see that the search reaches it.
func TestSearchingLineNamesTheTrackers(t *testing.T) {
	d := NewWorktreeDialog()
	d.SetSize(120, 40)
	d.Show(nil, nil, nil, "/repo", "master", boundToBoth([]string{"BRZ"}, []string{"OPS"}))
	d.ticketPending = true

	got := ansi.Strip(d.renderTicketBlock(80))
	if !strings.Contains(got, "searching Linear and Jira…") {
		t.Errorf("searching line = %q, want both trackers named", strings.TrimSpace(got))
	}

	// A repo tracking two teams of ONE tracker binds it twice; "Linear and
	// Linear" is not a sentence.
	d2 := NewWorktreeDialog()
	d2.SetSize(120, 40)
	d2.Show(nil, nil, nil, "/repo", "master", []ticketing.Bound{
		{Provider: linear.New(), Keys: []string{"BRZ"}},
		{Provider: linear.New(), Keys: []string{"PRD"}},
	})
	d2.ticketPending = true
	got = ansi.Strip(d2.renderTicketBlock(80))
	if strings.Contains(got, "Linear and Linear") {
		t.Errorf("repeated provider names: %q", strings.TrimSpace(got))
	}
}

// TestJiraIdentifierResolvesThroughItsOwnProvider: the union gates the shape
// test, and the owning provider is picked by the key prefix — so a Jira key in
// a repo that tracks both must not be sent to Linear.
func TestJiraIdentifierResolvesThroughItsOwnProvider(t *testing.T) {
	bound := boundToBoth([]string{"BRZ"}, []string{"OPS"})

	if _, ok := ticket.LooksLikeIdentifier("OPS-42", []string{"BRZ", "OPS"}); !ok {
		t.Fatal("OPS-42 should pass the shape test for a repo tracking OPS")
	}
	p, ok := ticketing.OwnerIn(bound, "OPS-42")
	if !ok || p.Kind() != "jira" {
		t.Errorf("OPS-42 resolved to %v, want the Jira provider", p)
	}
	p, ok = ticketing.OwnerIn(bound, "brz-1")
	if !ok || p.Kind() != "linear" {
		t.Errorf("brz-1 resolved to %v, want the Linear provider", p)
	}
}
