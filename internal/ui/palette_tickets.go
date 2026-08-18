package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/linear"
	"github.com/brizzai/fleet/internal/session"
)

// ticketListLimit caps the "my tickets" fetch. Fifty covers a real backlog
// while keeping one query well inside Linear's per-query complexity budget.
const ticketListLimit = 50

// ticketListTimeout bounds the fetch. It runs while the palette is already
// open and usable, so a slow answer costs the ticket rows and nothing else.
const ticketListTimeout = 12 * time.Second

// paletteTicketsMsg carries the loaded tickets back to Update.
type paletteTicketsMsg struct {
	tickets []linear.Ticket
	err     error
}

// loadPaletteTickets fetches your open assigned issues.
//
// Fired when the palette opens rather than on a timer: this is the same
// event-driven, one-shot posture as every other Linear call in fleet, which is
// what keeps the status workers clear of the network entirely.
func loadPaletteTickets() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), ticketListTimeout)
		defer cancel()
		tickets, err := linear.AssignedIssues(ctx, ticketListLimit)
		return paletteTicketsMsg{tickets: tickets, err: err}
	}
}

// sessionsByTicket maps a ticket identifier to the session already working it.
//
// This join is the whole reason the tab is worth having: Linear can list your
// issues, but only fleet knows which ones already have a worktree and what that
// session is doing right now.
func (h *Home) sessionsByTicket(tickets []linear.Ticket) map[string]*session.Session {
	if len(tickets) == 0 || len(h.sessions) == 0 {
		return nil
	}
	// Team keys come from the tickets themselves — the prefix of an identifier
	// IS its team — so this needs no repo config and works across every repo on
	// screen at once.
	seen := map[string]bool{}
	var teams []string
	for _, t := range tickets {
		if i := strings.IndexByte(t.Identifier, '-'); i > 0 {
			key := strings.ToUpper(t.Identifier[:i])
			if !seen[key] {
				seen[key] = true
				teams = append(teams, key)
			}
		}
	}

	gitInfo := h.gitInfo()
	out := map[string]*session.Session{}
	for _, s := range h.sessions {
		if s == nil {
			continue
		}
		branch := ""
		if info, ok := gitInfo[s.ProjectPath]; ok && info != nil {
			branch = info.Branch
		}
		id := linear.IdentifierFromBranch(branch, teams)
		if id == "" {
			// The worktree directory still carries the identifier when the git
			// cache is cold, same fallback branch inference uses.
			id = linear.IdentifierFromBranch(pathTailAfterRepo(s.ProjectPath), teams)
		}
		if id == "" {
			continue
		}
		// Prefer whichever session most wants attention, so a row never reports
		// "idle" while a sibling in the same worktree is waiting on you.
		if prev, ok := out[id]; !ok || ticketSessionRank(s) < ticketSessionRank(prev) {
			out[id] = s
		}
	}
	return out
}

// ticketSessionRank orders sessions by how much they want you, lowest first.
func ticketSessionRank(s *session.Session) int {
	switch s.Status {
	case session.StatusWaiting:
		return 0
	case session.StatusFinished:
		return 1
	case session.StatusRunning:
		return 2
	case session.StatusError:
		return 3
	}
	return 4
}

// ticketPaletteItems turns tickets into palette rows.
func (h *Home) ticketPaletteItems(tickets []linear.Ticket) []PaletteItem {
	byTicket := h.sessionsByTicket(tickets)

	// Identifiers vary in length (BRZ-453 vs BRZ-3142), so pad them to the
	// widest in the set: otherwise every title starts at a slightly different
	// column and the list reads as ragged rather than as a table.
	idWidth := 0
	for _, t := range tickets {
		if n := len(t.Identifier); n > idWidth {
			idWidth = n
		}
	}

	items := make([]PaletteItem, 0, len(tickets))
	for _, t := range tickets {
		// The list is sorted by priority, so priority has to be visible:
		// ordering on an invisible key reads as arbitrary. Only urgent and high
		// are marked — labelling all fifty rows is the density we just cut, and
		// blank-is-normal matches how the badge column already works. Shape,
		// never colour: the status dot owns colour in this list.
		name := fmt.Sprintf("%-2s  %-*s  %s", priorityMark(t.Priority), idWidth, t.Identifier, t.Title)
		it := PaletteItem{
			Kind: PaletteKindTicket,
			ID:   t.Identifier,
			Name: name,
			// The Linear state is the group header, so it is deliberately NOT
			// repeated on every row. The right column carries what fleet knows
			// instead — and stays empty when there is nothing to say.
			Group: t.StateName,
			// Haystack must be exactly Name + " " + <right column>, because the
			// renderer maps matched haystack indexes back onto those two
			// strings by offset. Composing it any other way lights up the
			// wrong characters.
			Haystack: name + " " + t.StateName,
		}
		if s := byTicket[t.Identifier]; s != nil {
			it.HasSession = true
			it.SessionStatus = s.Status
			// Deliberately no status WORD. The dot already carries it, in the
			// colour and shape the sidebar uses, and printing "suspended" at
			// the far right said the same thing a second time — while eating
			// the width the title needed. One fact, one place.
		}
		items = append(items, it)
	}
	return items
}

// priorityMark renders Linear's priority as shape. Empty for medium, low and
// unset, so the marks at the top of a group stand out instead of every row
// carrying one.
func priorityMark(p int) string {
	switch p {
	case 1:
		return "!!"
	case 2:
		return "!"
	}
	return ""
}

// openTicketFromPalette acts on a chosen ticket.
//
// Two outcomes, and which one you get is decided by the filesystem rather than
// by a mode: if the work already exists, go to it; if it doesn't, offer to
// create it. Creating routes through the ordinary worktree dialog rather than a
// new path of its own, so the base branch and the repo are still confirmed by
// the same screen that always confirms them.
func (h *Home) openTicketFromPalette(identifier string) (tea.Model, tea.Cmd) {
	var tickets []linear.Ticket
	for _, it := range h.commandPalette.items {
		if it.Kind == PaletteKindTicket && it.ID == identifier {
			tickets = append(tickets, linear.Ticket{Identifier: it.ID})
		}
	}
	if s := h.sessionsByTicket(tickets)[identifier]; s != nil {
		h.actionLog.Add("ticket jump", identifier, true)
		return h.jumpToSessionID(s.ID)
	}

	h.actionLog.Add("ticket new worktree", identifier, true)
	repoPath := h.resolveWorktreeBaseRepo()
	if repoPath == "" {
		h.setInfo("Select a repo first, then pick the ticket")
		return h, nil
	}
	// The identifier is handed to the dialog rather than turned into a branch
	// here: the dialog already knows how to resolve one, and going through it
	// means the base branch and the repo are confirmed on the same screen that
	// always confirms them.
	h.pendingTicketID = identifier
	h.worktreeDialog.ShowLoading()
	return h, tea.Batch(h.fetchWorkspaceListForRepo(repoPath), spinnerTickCmd)
}

// jumpToSessionID moves the cursor onto a session, expanding whatever hides it.
func (h *Home) jumpToSessionID(id string) (tea.Model, tea.Cmd) {
	for _, s := range h.sessions {
		if s != nil && s.ID == id {
			h.revealCheckout(s.ProjectPath)
			break
		}
	}
	h.rebuildFlatItems()
	for i, item := range h.flatItems {
		if item.Session != nil && item.Session.ID == id {
			h.cursor = i
			h.syncViewport()
			return h, h.fetchPreviewForSelected()
		}
	}
	h.setInfo("That session is hidden by the filter")
	return h, nil
}

// maybeLoadPaletteTickets kicks off the ticket fetch when the palette opens,
// and does nothing at all when Linear isn't connected — so a user who has never
// heard of Linear pays no round trip for pressing Ctrl+K.
func (h *Home) maybeLoadPaletteTickets() tea.Cmd {
	if !linear.Available() {
		return nil
	}
	return loadPaletteTickets()
}
