package ui

import (
	"strings"

	"github.com/brizzai/fleet/internal/git"
	"github.com/charmbracelet/x/ansi"
)

// baseOnInput is the base-cursor value meaning "the field itself": the caret is
// visible and no row carries the highlight. Mirrors ticketOnInput, kept as its
// own name because it indexes a different set of rows.
const baseOnInput = -1

// branchMaxRows caps the suggestion list, matching ticketMaxRows. Small on
// purpose — this completes a field you are already typing in, it is not the
// branch browser the `b` key opens.
const branchMaxRows = 5

// remotePrefix is the only remote ListBranches reads, so it is the only one
// suggestions can offer.
const remotePrefix = "origin/"

// visibleBranchCount is how many rows are rendered while the Base branch field
// carries the highlight, which is what the cursor must be clamped against.
//
// Deliberately NOT gated on focus, unlike renderBranchBlock: the ↑ handler asks
// for the last row while focus is still on the New branch field, and a focus
// gate would answer 0 there and silently land on the input instead.
func (d *WorktreeDialog) visibleBranchCount() int {
	return len(d.branchMatches)
}

// rebuildBranchMatches refilters the branch list against the field's text.
//
// Each match is stored as the exact ref that will be written into the field,
// which is why a remote-only branch comes out origin/-prefixed: the worktree is
// created with `git worktree add <path> -b <new> <base>`, and with -b present
// git resolves <base> as a plain revision with no remote-tracking DWIM. A bare
// remote-only name does not resolve there, so offering one would be a row that
// looks fine and fails on Enter.
//
// A field already reading origin/… keeps its prefix, and only branches that
// have a remote can match it — GetDefaultBranch pre-fills origin/<default>
// precisely so a new worktree starts from the remote tip, and quietly swapping
// that for the local branch would change what gets built without saying so.
func (d *WorktreeDialog) rebuildBranchMatches() {
	d.branchMatches = nil
	if len(d.branches) == 0 {
		return
	}

	text := strings.TrimSpace(d.baseBranchInput.Value())
	wantRemote := strings.HasPrefix(text, remotePrefix)
	q := strings.ToLower(strings.TrimPrefix(text, remotePrefix))

	d.branchMatches = d.matchBranches(q, wantRemote)

	// A field sitting on a complete ref is at rest, and the one row echoing it
	// back answers a question nobody asked — Enter on it is a no-op. The useful
	// question there is "what else is there", so fall back to the unfiltered
	// list. This matters because the field is PRE-FILLED with origin/<default>:
	// without it, focusing the field for the first time shows a single row
	// repeating what is already on the line above it, and browsing would mean
	// clearing a field you probably wanted to keep.
	if len(d.branchMatches) == 1 && d.branchMatches[0] == text {
		// wantRemote is deliberately kept: you asked for a remote ref, so the
		// wider list is the other remote refs.
		d.branchMatches = d.matchBranches("", wantRemote)
	}
}

// matchBranches returns the refs whose branch name contains q, in the form each
// one takes as a base ref. An empty q matches everything, so an at-rest field
// lists the most recently committed branches — ListBranches is already in that
// order.
func (d *WorktreeDialog) matchBranches(q string, wantRemote bool) []string {
	var out []string
	for _, b := range d.branches {
		if wantRemote && !b.HasRemote {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(b.Name), q) {
			continue
		}
		out = append(out, baseRefFor(b, wantRemote))
		if len(out) >= branchMaxRows {
			break
		}
	}
	return out
}

// baseRefFor is the form of a branch that can serve as a base ref.
func baseRefFor(b git.BranchInfo, wantRemote bool) string {
	if b.IsRemote || (wantRemote && b.HasRemote) {
		return remotePrefix + b.Name
	}
	return b.Name
}

// pickBaseBranch fills the field from a highlighted row and returns the
// highlight — and with it the caret — to the field, so the ref stays editable.
// The same shape as pickTicket, for the same reason.
func (d *WorktreeDialog) pickBaseBranch(ref string) {
	d.baseBranchInput.SetValue(ref)
	d.baseBranchInput.SetCursor(len([]rune(ref)))
	// Write the value, then the change detector, or routeToInput refilters
	// against text it already knows about.
	d.lastBaseInput = ref
	d.rebuildBranchMatches()
	d.err = ""
	d.setSelection(focusBaseBranch, baseOnInput)
}

// renderBranchBlock renders the suggestion rows under the Base branch field.
//
// Empty unless that field carries the highlight: the field always holds text, so
// rendering unconditionally would park five rows in the middle of the dialog for
// every user, including everyone who never touches the base branch.
func (d *WorktreeDialog) renderBranchBlock(innerW int) string {
	if d.focus != focusBaseBranch {
		return ""
	}
	var b strings.Builder
	for i, ref := range d.branchMatches {
		row := ansi.Truncate(ref, maxInt(innerW-4, 12), "…")
		if d.baseCursor == i {
			b.WriteString(SelectionMarker(true).Render("▸ ") + selTitle().Render(row))
		} else {
			b.WriteString("  " + DimStyle.Render(row))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// baseFooter names what Enter does while the Base branch field has the
// highlight. Empty means "let the ticket footer, then the default, speak".
func (d *WorktreeDialog) baseFooter() string {
	if d.focus != focusBaseBranch {
		return ""
	}
	if d.baseCursor >= 0 && d.baseCursor < len(d.branchMatches) {
		return "⏎ use " + d.branchMatches[d.baseCursor] + "  ↑ back to typing  esc: cancel"
	}
	if len(d.branchMatches) > 0 {
		return "↓ branches  tab: next field  ⏎ create  esc: cancel"
	}
	return ""
}
