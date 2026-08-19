package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/linear"
	"github.com/brizzai/fleet/internal/workspace"
	"github.com/charmbracelet/x/ansi"
)

// ticketDebounce is how long the field must be still before a lookup fires.
//
// Not per-keystroke: each lookup is a network round trip, so typing an
// 8-character identifier at normal speed would fork eight of them and their
// replies would land out of order. 250ms is below the threshold where a pause
// feels deliberate, and the generation counter cleans up the rest.
const ticketDebounce = 250 * time.Millisecond

// ticketLookupTimeout bounds one lookup. Generous against a measured ~0.5s, and
// past it the answer is worthless anyway — Enter never waits on this.
const ticketLookupTimeout = 6 * time.Second

// ticketMinQueryLen is the shortest prose that earns a search. Below this a
// query returns noise and still costs half a second.
const ticketMinQueryLen = 3

type (
	// worktreeTicketTickMsg is the debounce firing. gen is the value at the
	// keystroke that scheduled it; a newer keystroke makes it stale.
	worktreeTicketTickMsg struct{ gen int }

	// worktreeTicketsMsg is a completed lookup.
	worktreeTicketsMsg struct {
		gen     int
		query   string
		byID    bool
		tickets []linear.Ticket
		err     error
	}
)

// ticketsEnabled reports whether any ticket surface should exist at all.
func (d *WorktreeDialog) ticketsEnabled() bool {
	return len(d.linearTeams) > 0 && !d.ticketsOff
}

// visibleTicketCount is how many rows are actually rendered, which is what the
// cursor must be clamped against — a terminal resize can shrink the block below
// the number of tickets fetched, and a cursor past the last rendered row is an
// invisible highlight.
func (d *WorktreeDialog) visibleTicketCount() int {
	if !d.ticketsEnabled() {
		return 0
	}
	return min(len(d.tickets), ticketMaxRows)
}

// onFieldChanged reacts synchronously to new text in the New branch field and
// returns the debounce tick, if a lookup is warranted.
//
// The synchronous half matters as much as the async one: dropping a stale
// resolution here is what stops an old ticket's title sitting under a changed
// identifier for the length of the debounce.
func (d *WorktreeDialog) onFieldChanged(text string) tea.Cmd {
	d.lastInput = text

	// Keep the resolution while the field still leads with its identifier, so
	// tweaking the tail (…-v2) doesn't silently drop the ticket link.
	if d.resolved != nil && !strings.HasPrefix(strings.ToLower(text), strings.ToLower(d.resolved.Identifier)) {
		d.resolved = nil
	}
	if !d.ticketsEnabled() {
		return nil
	}

	d.ticketGen++
	gen := d.ticketGen
	return tea.Tick(ticketDebounce, func(time.Time) tea.Msg {
		return worktreeTicketTickMsg{gen: gen}
	})
}

// onDebounceElapsed decides whether the pause earns a round trip.
func (d *WorktreeDialog) onDebounceElapsed(m worktreeTicketTickMsg) tea.Cmd {
	if !d.visible || m.gen != d.ticketGen || !d.ticketsEnabled() {
		return nil
	}
	text := strings.TrimSpace(d.newBranchInput.Value())
	if text == "" {
		d.tickets, d.ticketNote, d.ticketPending = nil, "", false
		return nil
	}

	teams := d.linearTeams
	gen := m.gen

	if id, ok := linear.LooksLikeIdentifier(text, teams); ok {
		if d.resolved != nil && strings.EqualFold(d.resolved.Identifier, id) {
			return nil // already resolved; don't refire on a redraw
		}
		d.ticketPending = true
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), ticketLookupTimeout)
			defer cancel()
			t, err := linear.Fetch(ctx, id)
			return worktreeTicketsMsg{gen: gen, query: text, byID: true, tickets: []linear.Ticket{t}, err: err}
		}
	}

	if len([]rune(text)) < ticketMinQueryLen {
		return nil
	}
	d.ticketPending = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), ticketLookupTimeout)
		defer cancel()
		items, err := linear.Search(ctx, text, ticketMaxRows)
		return worktreeTicketsMsg{gen: gen, query: text, tickets: items, err: err}
	}
}

// applyTickets installs a reply, or drops it.
//
// The generation check is what stops this failure: type BRZ-3182, edit to
// BRZ-3184, and the slower first reply overwrites the field with the wrong
// ticket's branch name — which then becomes a real git branch.
//
// It deliberately never moves the highlight. If the cursor is on the input (the
// overwhelmingly common case) it stays there, caret intact, whatever lands.
func (d *WorktreeDialog) applyTickets(m worktreeTicketsMsg) {
	if !d.visible || m.gen != d.ticketGen {
		return
	}
	d.ticketPending = false
	d.ticketNote = ""

	if m.err != nil {
		switch {
		case errors.Is(m.err, linear.ErrNotFound):
			if m.byID {
				// "no such issue" is the wrong diagnosis when the credential
				// simply cannot see this repo's workspace — the issue exists,
				// we are looking in the wrong place.
				if note, wrong := d.workspaceMismatchNote(); wrong {
					d.ticketNote = note
				} else {
					d.ticketNote = m.query + " — no such issue"
				}
			}
		case errors.Is(m.err, linear.ErrNotAuthenticated):
			d.ticketNote = "linear: credential rejected — Ctrl+K → Connect Linear"
			d.ticketsOff = true // it will keep failing; stop spending round trips
		case errors.Is(m.err, linear.ErrNotConnected):
			// Never connected is a resting state, not a complaint. Go quiet.
			d.ticketsOff = true
		case errors.Is(m.err, context.DeadlineExceeded):
			d.ticketNote = "linear: timed out"
		default:
			d.ticketNote = "linear: unavailable"
		}
		d.tickets = nil
		if d.focus == focusNewBranch {
			d.setSelection(focusNewBranch, d.ticketCursor)
		}
		return
	}

	if m.byID {
		// An identifier resolves IN PLACE. The highlight does not jump to a
		// row — a picker that moves its own selection is the ambiguity coming
		// back through the window.
		d.tickets = nil
		if len(m.tickets) > 0 && m.tickets[0].Ok() {
			t := m.tickets[0]
			d.resolved = &t
		}
		if d.focus == focusNewBranch {
			d.setSelection(focusNewBranch, ticketOnInput)
		}
		return
	}

	d.tickets = m.tickets
	if len(d.tickets) > ticketMaxRows {
		d.tickets = d.tickets[:ticketMaxRows]
	}
	// A search that matched nothing is normally not worth a word. But when the
	// credential belongs to a workspace that has none of this repo's teams,
	// EVERY search returns nothing, and rendering that as silence leaves the
	// user typing into a feature that looks broken with no way to find out why.
	if len(d.tickets) == 0 {
		if note, wrong := d.workspaceMismatchNote(); wrong {
			d.ticketNote = note
		}
	}
	if d.focus == focusNewBranch {
		d.setSelection(focusNewBranch, d.ticketCursor)
	}
}

// workspaceMismatchNote reports when the connected Linear workspace contains
// none of this repo's teams.
//
// This is a real and easily-hit state: authorizing browser sign-in against the
// wrong workspace produces a credential that works perfectly and can see none
// of your issues. Nothing else in the flow catches it — the team keys come from
// a file, so the dialog lights up; the API answers happily; the results are
// simply always empty.
func (d *WorktreeDialog) workspaceMismatchNote() (string, bool) {
	ws, known := linear.WorkspaceInfo()
	if !known || len(d.linearTeams) == 0 || len(ws.TeamKeys) == 0 {
		return "", false
	}
	for _, repoTeam := range d.linearTeams {
		for _, wsTeam := range ws.TeamKeys {
			if strings.EqualFold(repoTeam, wsTeam) {
				return "", false
			}
		}
	}
	name := ws.Name
	if name == "" {
		name = "that workspace"
	}
	// Kept short deliberately: this renders on one line under a narrow input,
	// and the first version wrapped and truncated mid-word into "Ctrl+K →
	// Conn…", which is worse than useless.
	return fmt.Sprintf("%s has no %s team — reconnect: Ctrl+K", name, strings.Join(d.linearTeams, "/")), true
}

// PrefillTicket seeds the New branch field with an identifier and starts its
// lookup, for a ticket chosen somewhere else (the palette's tickets tab).
//
// It goes through the same debounce and generation guard as typing, so the
// resolved title arrives by exactly the path a typed identifier would — there
// is no second way for a ticket to become a branch name.
func (d *WorktreeDialog) PrefillTicket(identifier string) tea.Cmd {
	if identifier == "" || !d.visible {
		return nil
	}
	d.newBranchInput.SetValue(identifier)
	d.newBranchInput.SetCursor(len([]rune(identifier)))
	d.setSelection(focusNewBranch, ticketOnInput)
	return d.onFieldChanged(identifier)
}

// pickTicket fills the field from a highlighted row and collapses back to the
// resolved state, so both ways of naming a ticket end up identical.
func (d *WorktreeDialog) pickTicket(t linear.Ticket) {
	branch := linear.BranchNameFor(t.Identifier, t.Title)
	d.newBranchInput.SetValue(branch)
	d.newBranchInput.SetCursor(len([]rune(branch)))
	d.lastInput = branch // don't re-query the name we just wrote
	ticket := t
	d.resolved = &ticket
	d.tickets = nil
	d.ticketNote = ""
	d.ticketPending = false
	d.ticketGen++ // invalidate anything in flight
	d.setSelection(focusNewBranch, ticketOnInput)
	d.err = ""
}

// ticketForCurrentInput returns the ticket the field currently denotes, for the
// creation message. Nil when the user typed a plain branch name.
func (d *WorktreeDialog) ticketForCurrentInput() *linear.Ticket {
	if d.resolved == nil {
		return nil
	}
	text := strings.ToLower(strings.TrimSpace(d.newBranchInput.Value()))
	if !strings.HasPrefix(text, strings.ToLower(d.resolved.Identifier)) {
		return nil
	}
	t := *d.resolved
	return &t
}

// renderTicketBlock renders the resolution line and the suggestion rows.
//
// Exactly one of them can carry the highlight, and the caret lives with it —
// see setSelection. innerW is the dialog's content width.
func (d *WorktreeDialog) renderTicketBlock(innerW int) string {
	if !d.ticketsEnabled() {
		return ""
	}
	var b strings.Builder

	switch {
	case d.ticketPending:
		b.WriteString(DimStyle.Render("  ⋯ searching Linear…"))
		b.WriteString("\n")
	case d.resolved != nil:
		line := "  " + d.resolved.Identifier
		b.WriteString(PROpenStyle.Render(line))
		b.WriteString(DimStyle.Render(" · " + ansi.Truncate(d.resolved.Title, maxInt(innerW-len(line)-3, 8), "…")))
		b.WriteString("\n")
	case d.ticketNote != "":
		b.WriteString(DimStyle.Render("  " + ansi.Truncate(d.ticketNote, maxInt(innerW-2, 8), "…")))
		b.WriteString("\n")
	}

	for i := 0; i < d.visibleTicketCount(); i++ {
		t := d.tickets[i]
		selected := d.focus == focusNewBranch && d.ticketCursor == i
		// Pad the RAW identifier before styling — padding a styled string
		// counts the ANSI bytes and the columns come out ragged.
		row := fmt.Sprintf("%-9s %s", t.Identifier, t.Title)
		row = ansi.Truncate(row, maxInt(innerW-4, 12), "…")
		if selected {
			b.WriteString(SelectionMarker(true).Render("▸ ") + selTitle().Render(row))
		} else {
			b.WriteString("  " + DimStyle.Render(row))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ticketFooter names what Enter will do right now, so the highlight's promise
// is also stated in words. Empty means "use the dialog's default footer".
func (d *WorktreeDialog) ticketFooter() string {
	if d.focus == focusWorktreeList {
		return "⏎ open worktree  esc: cancel"
	}
	if d.focus != focusNewBranch {
		return ""
	}
	if d.ticketCursor >= 0 && d.ticketCursor < len(d.tickets) {
		return "⏎ use " + d.tickets[d.ticketCursor].Identifier + "  ↑ back to typing  esc: cancel"
	}
	text := strings.TrimSpace(d.newBranchInput.Value())
	if text == "" {
		if d.ticketsEnabled() {
			return "type a branch name or a ticket  esc: cancel"
		}
		return ""
	}
	if d.resolved != nil {
		return "⏎ create " + workspace.SanitizeBranchName(text)
	}
	if d.visibleTicketCount() > 0 {
		return "↓ tickets  ⏎ create worktree  esc: cancel"
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
