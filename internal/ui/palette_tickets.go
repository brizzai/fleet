package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/ticket"
	"github.com/brizzai/fleet/internal/ticketing"
)

// ticketListLimit caps the "my tickets" fetch, per tracker.
//
// A hundred, which is also what both providers fall back to when handed no
// limit at all. It is one page either way — Linear takes `first` up to 250 and
// this projection (eight scalar fields and one nested state) stays far inside
// the per-query complexity budget, and Jira's /search/jql accepts it in a
// single POST. The tail matters because the fetch is ordered by updatedAt and
// the cut therefore falls on RECENCY, not on state: a fifty-row cap silently
// truncated exactly the untouched backlog the tickets tab's todo-only scope
// exists to show.
const ticketListLimit = 100

// ticketListTimeout bounds the fetch. It runs while the palette is already
// open and usable, so a slow answer costs the ticket rows and nothing else.
const ticketListTimeout = 12 * time.Second

// paletteTicketsMsg carries the loaded tickets back to Update.
type paletteTicketsMsg struct {
	tickets []ticket.Ticket
	err     error
}

// loadPaletteTickets fetches your open assigned issues, from every connected
// tracker at once.
//
// Fired when the palette opens rather than on a timer: this is the same
// event-driven, one-shot posture as every other tracker call in fleet, which is
// what keeps the status workers clear of the network entirely.
//
// One list rather than one tab per tracker. The question the tab answers is
// "what am I meant to be working on", and that question does not have a Linear
// half and a Jira half — the identifiers already say which is which, and
// ticketing.Assigned re-sorts the merged set by the same rule either one uses
// alone.
func loadPaletteTickets() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), ticketListTimeout)
		defer cancel()
		tickets, err := ticketing.Assigned(ctx, ticketListLimit)
		return paletteTicketsMsg{tickets: tickets, err: err}
	}
}

// sessionsByTicket maps a ticket identifier to the session already working it.
//
// This join is the whole reason the tab is worth having: a tracker can list
// your issues, but only fleet knows which ones already have a worktree and what
// that session is doing right now.
func (h *Home) sessionsByTicket(tickets []ticket.Ticket) map[string]*session.Session {
	if len(tickets) == 0 || len(h.sessions) == 0 {
		return nil
	}
	// Tracker keys come from the tickets themselves — the prefix of an
	// identifier IS its team or project — so this needs no repo config, works
	// across every repo on screen at once, and needs no idea of which tracker
	// a given row came from.
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
		id := ticket.IdentifierFromBranch(branch, teams)
		if id == "" {
			// The worktree directory still carries the identifier when the git
			// cache is cold, same fallback branch inference uses.
			root, ok := session.LookupRepoRoot(s.ProjectPath)
			if !ok {
				// Cache-only, because this runs when the palette opens — on the
				// Update goroutine. A worktree is its own root anyway.
				root = s.ProjectPath
			}
			id = ticket.IdentifierFromBranch(pathTailAfterRepo(s.ProjectPath, root), teams)
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
	// GetStatus, not the field: Status is written under s.mu by the worker,
	// and this runs on the Update goroutine.
	switch s.GetStatus() {
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
func (h *Home) ticketPaletteItems(tickets []ticket.Ticket) []PaletteItem {
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
		// Priority rides in its own column (see PaletteItem.Priority), NOT in
		// the name: baked into the name it could not be coloured, and it pushed
		// every ticket title out of line with every command and worktree row in
		// the mixed tab.
		name := fmt.Sprintf("%-*s  %s", idWidth, t.Identifier, t.Title)
		it := PaletteItem{
			Kind: PaletteKindTicket,
			ID:   t.Identifier,
			Name: name,
			// int(t.Priority), not a translation: ticket.Priority is defined
			// on Linear's numbering (0 unset, 1 urgent … 4 low) precisely so
			// the gauge and its colour ladder need no mapping layer, and so a
			// Jira row and a Linear row of the same urgency render identically.
			Priority: int(t.Priority),
			// The state is the group header, so it is deliberately NOT
			// repeated on every row. The right column carries what fleet knows
			// instead — and stays empty when there is nothing to say.
			Group: t.StateName,
			// The category, beside the name: the name is what the team called
			// the state and cannot be compared, and this is what the tickets
			// tab's todo-only scope filters on.
			TicketStarted: t.State == ticket.StateStarted,
			// Haystack must be exactly Name + " " + <right column>, because the
			// renderer maps matched haystack indexes back onto those two
			// strings by offset. Composing it any other way lights up the
			// wrong characters.
			Haystack: name + " " + t.StateName,
		}
		if s := byTicket[t.Identifier]; s != nil {
			it.HasSession = true
			it.SessionStatus = s.GetStatus()
			// Deliberately no status WORD. The dot already carries it, in the
			// colour and shape the sidebar uses, and printing "suspended" at
			// the far right said the same thing a second time — while eating
			// the width the title needed. One fact, one place.
		}
		items = append(items, it)
	}
	return items
}

// openTicketFromPalette acts on a chosen ticket.
//
// Two outcomes, and which one you get is decided by the filesystem rather than
// by a mode: if the work already exists, go to it; if it doesn't, offer to
// create it. Creating routes through the ordinary worktree dialog rather than a
// new path of its own, so the base branch and the repo are still confirmed by
// the same screen that always confirms them.
func (h *Home) openTicketFromPalette(identifier string) (tea.Model, tea.Cmd) {
	var tickets []ticket.Ticket
	for _, it := range h.commandPalette.items {
		if it.Kind == PaletteKindTicket && it.ID == identifier {
			tickets = append(tickets, ticket.Ticket{Identifier: it.ID})
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
	if !ticketing.Available() {
		return nil
	}
	return loadPaletteTickets()
}
