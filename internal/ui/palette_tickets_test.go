package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/linear"
	"github.com/brizzai/fleet/internal/session"
)

func ticketHome(t *testing.T) *Home {
	t.Helper()
	storage, err := session.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	h := NewHome(storage, &config.Config{TickIntervalSec: 2}, "test", analytics.Identity{})
	h.width, h.height = 120, 40
	return h
}

// TestPaletteTicketsReachThePalette is the routing guard, applied to the tab
// before the same bug can happen a third time.
//
// routeToModal carries key and paste messages only, so a tea.Cmd result reaches
// a dialog only if Home.Update forwards it. The ticket fetch is a tea.Cmd
// result; without the case, the tab would sit empty forever with every unit
// test still green.
func TestPaletteTicketsReachThePalette(t *testing.T) {
	h := ticketHome(t)
	h.commandPalette.ShowOnTab(h.buildPaletteItems(), nil, PaletteTabTickets)

	if h.commandPalette.TicketsLoaded() {
		t.Fatal("precondition: nothing loaded yet")
	}
	model, _ := h.Update(paletteTicketsMsg{tickets: []linear.Ticket{
		{Identifier: "BRZ-2644", Title: "Storage optimization", StateName: "In Progress", StateType: "started"},
		{Identifier: "BRZ-3013", Title: "TS sdk spanprocessor", StateName: "Todo", StateType: "unstarted"},
	}})
	h = model.(*Home)

	if !h.commandPalette.TicketsLoaded() {
		t.Fatal("Home.Update dropped the ticket reply — the tab can never fill")
	}
	var got []string
	for _, it := range h.commandPalette.items {
		if it.Kind == PaletteKindTicket {
			got = append(got, it.ID)
		}
	}
	if len(got) != 2 {
		t.Fatalf("palette holds %v, want both tickets", got)
	}

	// A second load must replace, not append — otherwise reopening doubles it.
	model, _ = h.Update(paletteTicketsMsg{tickets: []linear.Ticket{
		{Identifier: "BRZ-2644", Title: "Storage optimization", StateName: "In Progress", StateType: "started"},
	}})
	h = model.(*Home)
	count := 0
	for _, it := range h.commandPalette.items {
		if it.Kind == PaletteKindTicket {
			count++
		}
	}
	if count != 1 {
		t.Errorf("second load left %d ticket rows, want 1 — rows are accumulating", count)
	}
}

// TestTicketRowsCarryTheirSession pins the join, which is the only thing this
// tab shows that Linear cannot.
func TestTicketRowsCarryTheirSession(t *testing.T) {
	h := ticketHome(t)
	h.sessions = []*session.Session{
		{ID: "s1", Title: "storage", ProjectPath: "/code/brizzai-brz-2644-storage", Status: session.StatusRunning},
		{ID: "s2", Title: "waiting one", ProjectPath: "/code/brizzai-brz-2996-subagents", Status: session.StatusWaiting},
	}
	h.writeGitInfo(func(m map[string]*git.RepoInfo) bool {
		m["/code/brizzai-brz-2644-storage"] = &git.RepoInfo{Branch: "brz-2644-storage-optimization"}
		m["/code/brizzai-brz-2996-subagents"] = &git.RepoInfo{Branch: "brz-2996-subagents-drilldown"}
		return true
	})

	tickets := []linear.Ticket{
		{Identifier: "BRZ-2644", Title: "Storage optimization", StateName: "In Progress", StateType: "started"},
		{Identifier: "BRZ-2996", Title: "subagents drilldown", StateName: "In Progress", StateType: "started"},
		{Identifier: "BRZ-3013", Title: "TS sdk", StateName: "Todo", StateType: "unstarted"},
	}
	items := h.ticketPaletteItems(tickets)
	if len(items) != 3 {
		t.Fatalf("built %d rows, want 3", len(items))
	}

	byID := map[string]PaletteItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	if !strings.Contains(byID["BRZ-2644"].Detail, "running") {
		t.Errorf("BRZ-2644 should report its live session, got %q", byID["BRZ-2644"].Detail)
	}
	if !strings.Contains(byID["BRZ-2996"].Detail, "waiting") {
		t.Errorf("BRZ-2996 should report its live session, got %q", byID["BRZ-2996"].Detail)
	}
	// A ticket with no worktree must say only its Linear state — inventing a
	// session status for it would be a lie about the machine.
	if strings.Contains(byID["BRZ-3013"].Detail, "·") {
		t.Errorf("BRZ-3013 has no session; detail should be the state alone, got %q", byID["BRZ-3013"].Detail)
	}

	// Typing either the number or a word from the title must find the row.
	if !strings.Contains(byID["BRZ-2644"].Haystack, "2644") ||
		!strings.Contains(strings.ToLower(byID["BRZ-2644"].Haystack), "storage") {
		t.Errorf("haystack should match identifier and title: %q", byID["BRZ-2644"].Haystack)
	}
}

// TestWaitingSessionWinsTheTicketRow: several sessions can share a worktree, and
// the row must report the one that wants you, not whichever came first.
func TestWaitingSessionWinsTheTicketRow(t *testing.T) {
	h := ticketHome(t)
	h.sessions = []*session.Session{
		{ID: "a", ProjectPath: "/code/brizzai-brz-2644-x", Status: session.StatusIdle},
		{ID: "b", ProjectPath: "/code/brizzai-brz-2644-x", Status: session.StatusWaiting},
		{ID: "c", ProjectPath: "/code/brizzai-brz-2644-x", Status: session.StatusRunning},
	}
	h.writeGitInfo(func(m map[string]*git.RepoInfo) bool {
		m["/code/brizzai-brz-2644-x"] = &git.RepoInfo{Branch: "brz-2644-x"}
		return true
	})
	got := h.sessionsByTicket([]linear.Ticket{{Identifier: "BRZ-2644"}})["BRZ-2644"]
	if got == nil || got.ID != "b" {
		t.Fatalf("row picked %v, want the waiting session", got)
	}
}

// TestTicketsTabIsInertWithoutLinear: pressing the key with nothing connected
// must cost no round trip and no confusion.
func TestTicketsTabIsInertWithoutLinear(t *testing.T) {
	// Explicit rather than inherited: a developer with a key exported in their
	// shell would otherwise skip this and never learn it broke.
	t.Setenv(linear.APIKeyEnvVar, "")

	h := ticketHome(t)
	if linear.Available() {
		t.Fatal("precondition: no credential should resolve with the env cleared and nothing warmed")
	}
	if cmd := h.maybeLoadPaletteTickets(); cmd != nil {
		t.Error("an unconnected fleet must not spend a request on Ctrl+K")
	}
}
