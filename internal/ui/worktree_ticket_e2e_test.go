package ui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/linear"
	"github.com/brizzai/fleet/internal/session"
)

// TestTicketLookupSurvivesTheRealMessageLoop drives a real Home from a keypress
// all the way to the lookup being dispatched, following every tea.Cmd the way
// the runtime does.
//
// This exists because two separate bugs shipped past a full green suite: the
// dialog's messages were never routed to it, and every unit test called
// d.Update directly, so the dialog was exercised without the wiring that feeds
// it. Testing a component in isolation cannot see a gap between components.
func TestTicketLookupSurvivesTheRealMessageLoop(t *testing.T) {
	storage, err := session.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer storage.Close()

	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	h.width, h.height = 120, 40
	h.worktreeDialog.SetSize(120, 40)
	h.worktreeDialog.Show(nil, nil, nil, "/repo", "master", []string{"BRZ"})

	// Type "sdk" the way a user does: one key at a time, through Home.
	var pending []tea.Cmd
	for _, r := range "sdk" {
		model, cmd := h.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		h = model.(*Home)
		if cmd != nil {
			pending = append(pending, cmd)
		}
	}
	if h.worktreeDialog.newBranchInput.Value() != "sdk" {
		t.Fatalf("field holds %q — the keys never reached the input",
			h.worktreeDialog.newBranchInput.Value())
	}
	if len(pending) == 0 {
		t.Fatal("typing scheduled nothing; the debounce was never armed")
	}

	// The debounce fires. Deliver its message the way the runtime would: into
	// Home.Update, NOT straight into the dialog — that difference is the bug
	// this test exists for.
	gen := h.worktreeDialog.ticketGen
	model, cmd := h.Update(worktreeTicketTickMsg{gen: gen})
	h = model.(*Home)

	if cmd == nil {
		t.Fatal("the debounce tick produced no lookup — Home.Update dropped it, " +
			"so no suggestion can ever appear")
	}
	if !h.worktreeDialog.ticketPending {
		t.Error("the dialog does not consider a lookup in flight, so its spinner and " +
			"its generation guard are both out of step with reality")
	}

	// And the reply must land back on the dialog through the same route.
	model, _ = h.Update(worktreeTicketsMsg{
		gen:     gen,
		query:   "sdk",
		tickets: []linear.Ticket{{Identifier: "BRZ-3013", Title: "TS sdk spanprocessor"}},
	})
	h = model.(*Home)
	if h.worktreeDialog.ticketPending {
		t.Error("the reply never reached the dialog: it still thinks a lookup is in flight")
	}
	if got := h.worktreeDialog.visibleTicketCount(); got != 1 {
		t.Errorf("dialog shows %d suggestions, want 1 — the reply was dropped", got)
	}
}

// TestWrongWorkspaceIsNamedNotSilent covers the state that made the feature
// look broken with nothing on screen to explain it.
//
// Authorizing browser sign-in against the wrong workspace yields a credential
// that works perfectly and can see none of your issues. The team keys come from
// a file, so the dialog lights up; the API answers happily; every search is
// empty. Silence is the one response that leaves no way to find out why.
func TestWrongWorkspaceIsNamedNotSilent(t *testing.T) {
	d := NewWorktreeDialog()
	d.SetSize(120, 40)
	d.Show(nil, nil, nil, "/repo", "master", []string{"BRZ"})

	// Connected to a workspace that has no BRZ team.
	linear.SetWorkspaceForTest(linear.Workspace{Name: "fleet", TeamKeys: []string{"FLE"}})
	t.Cleanup(func() { linear.SetWorkspaceForTest(linear.Workspace{}) })

	d.ticketGen = 7
	d.applyTickets(worktreeTicketsMsg{gen: 7, query: "sdk", tickets: nil})
	if d.ticketNote == "" {
		t.Fatal("an always-empty search must say why, not render silence")
	}
	if !strings.Contains(d.ticketNote, "fleet") || !strings.Contains(d.ticketNote, "BRZ") {
		t.Errorf("the note must name the workspace and the missing team, got %q", d.ticketNote)
	}

	// And a by-id miss must not be reported as "no such issue" — the issue
	// exists, we are looking in the wrong workspace.
	d.ticketGen = 8
	d.applyTickets(worktreeTicketsMsg{gen: 8, query: "BRZ-3013", byID: true, err: linear.ErrNotFound})
	if strings.Contains(d.ticketNote, "no such issue") {
		t.Errorf("wrong diagnosis for a wrong-workspace credential: %q", d.ticketNote)
	}

	// The converse: a workspace that DOES hold the team stays quiet on an
	// ordinary empty search.
	linear.SetWorkspaceForTest(linear.Workspace{Name: "Brizz", TeamKeys: []string{"BRZ", "PRD"}})
	d.ticketGen = 9
	d.applyTickets(worktreeTicketsMsg{gen: 9, query: "zzzz", tickets: nil})
	if d.ticketNote != "" {
		t.Errorf("an ordinary no-match must stay silent, got %q", d.ticketNote)
	}
}
