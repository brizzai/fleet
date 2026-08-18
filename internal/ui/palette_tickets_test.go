package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

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
	if !byID["BRZ-2644"].HasSession || byID["BRZ-2644"].SessionStatus != session.StatusRunning {
		t.Errorf("BRZ-2644 should carry its live session, got %+v", byID["BRZ-2644"])
	}
	if !byID["BRZ-2996"].HasSession || byID["BRZ-2996"].SessionStatus != session.StatusWaiting {
		t.Errorf("BRZ-2996 should carry its live session, got %+v", byID["BRZ-2996"])
	}
	// A ticket with no worktree must be plainly absent, not merely quiet.
	if byID["BRZ-3013"].HasSession {
		t.Error("BRZ-3013 has no worktree and must not claim a session")
	}
	// The status is carried by the badge, never repeated as a word — the dot
	// already says it, and the word ate the width the title needed.
	for _, it := range items {
		if it.Detail != "" {
			t.Errorf("%s carries a redundant detail %q; the badge is the status", it.ID, it.Detail)
		}
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

// renderedPalette returns the palette's view with ANSI stripped, for asserting
// on layout rather than styling.
func renderedPalette(t *testing.T, h *Home) string {
	t.Helper()
	return ansi.Strip(h.commandPalette.View())
}

// TestTicketTabLayout pins what the tab actually looks like: grouped by Linear
// state with counts, the state NOT repeated on every row, and fleet presence
// carried in the badge column.
func TestTicketTabLayout(t *testing.T) {
	h := ticketHome(t)
	h.sessions = []*session.Session{
		{ID: "a", ProjectPath: "/c/brizzai-brz-2644-x", Status: session.StatusRunning},
	}
	h.writeGitInfo(func(m map[string]*git.RepoInfo) bool {
		m["/c/brizzai-brz-2644-x"] = &git.RepoInfo{Branch: "brz-2644-x"}
		return true
	})
	tickets := []linear.Ticket{
		{Identifier: "BRZ-2644", Title: "Storage optimization", StateName: "In Progress", StateType: "started"},
		{Identifier: "BRZ-3142", Title: "BYOCH backfill", StateName: "Todo", StateType: "unstarted"},
		{Identifier: "BRZ-2732", Title: "audit feature flags", StateName: "Todo", StateType: "unstarted"},
	}
	h.commandPalette.SetSize(120, 40)
	h.commandPalette.ShowOnTab(h.buildPaletteItems(), nil, PaletteTabTickets)
	h.commandPalette.SetTickets(h.ticketPaletteItems(tickets))

	got := renderedPalette(t, h)

	// Grouped, with counts.
	for _, want := range []string{"In Progress  1", "Todo  2"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing group header %q:\n%s", want, got)
		}
	}
	// The header carries the state, so the row must not repeat it.
	if strings.Count(got, "In Progress") != 1 {
		t.Errorf("the Linear state should appear once, as a header, not on every row:\n%s", got)
	}
	// Fleet presence is the badge: a session shows a dot, and a ticket with no
	// worktree shows nothing at all in that column.
	var withSession, without string
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "BRZ-2644") {
			withSession = line
		}
		if strings.Contains(line, "BRZ-3142") {
			without = line
		}
	}
	if !strings.ContainsAny(withSession, "●◐○✕·") {
		t.Errorf("a ticket with a live session must carry a status dot: %q", withSession)
	}
	if strings.ContainsAny(without, "●◐○✕·") {
		t.Errorf("a ticket with no worktree must leave the badge column blank: %q", without)
	}
	// A ticket row must never be badged "cmd" — the badge column is where
	// "is this in fleet" lives.
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "BRZ-") && strings.Contains(line, "cmd") {
			t.Errorf("ticket row carries a cmd badge: %q", line)
		}
	}
	// Identifiers pad to a common width so titles start in one column.
	if !strings.Contains(got, "BRZ-2732  audit") {
		t.Errorf("identifiers should pad to a common width so titles align:\n%s", got)
	}
	// Titles must not be truncated to nothing on a wide terminal.
	if !strings.Contains(got, "Storage optimization") {
		t.Errorf("title was truncated despite the width being available:\n%s", got)
	}
	// One prompt marker, not two.
	if strings.Contains(got, "> >") {
		t.Errorf("the input is drawing a second prompt:\n%s", got)
	}
}

// TestFilteredTicketsKeepTheirState: with no headers, the state has to come
// back onto the row or a searched ticket loses it entirely.
func TestFilteredTicketsKeepTheirState(t *testing.T) {
	h := ticketHome(t)
	h.commandPalette.SetSize(120, 40)
	h.commandPalette.ShowOnTab(h.buildPaletteItems(), nil, PaletteTabTickets)
	h.commandPalette.SetTickets(h.ticketPaletteItems([]linear.Ticket{
		{Identifier: "BRZ-2644", Title: "Storage optimization", StateName: "In Progress", StateType: "started"},
	}))

	h.commandPalette.filterInput.SetValue("storage")
	h.commandPalette.rebuildFiltered()

	got := renderedPalette(t, h)
	if strings.Contains(got, "In Progress  1") {
		t.Errorf("filtering must drop the group headers:\n%s", got)
	}
	if !strings.Contains(got, "In Progress") {
		t.Errorf("a filtered row must carry its state, since no header does:\n%s", got)
	}
}

// TestPriorityIsVisibleBecauseItIsSorted: the list orders by priority, so
// priority has to show. Ordering on an invisible key reads as arbitrary.
//
// Only urgent and high are marked — labelling all fifty rows is the density
// this view was trimmed to avoid, and blank-is-normal matches how the badge
// column already works.
func TestPriorityIsVisibleBecauseItIsSorted(t *testing.T) {
	h := ticketHome(t)
	h.commandPalette.SetSize(120, 40)
	h.commandPalette.ShowOnTab(h.buildPaletteItems(), nil, PaletteTabTickets)
	h.commandPalette.SetTickets(h.ticketPaletteItems([]linear.Ticket{
		{Identifier: "BRZ-1", Title: "urgent one", StateName: "Todo", StateType: "unstarted", Priority: 1},
		{Identifier: "BRZ-2", Title: "high one", StateName: "Todo", StateType: "unstarted", Priority: 2},
		{Identifier: "BRZ-3", Title: "medium one", StateName: "Todo", StateType: "unstarted", Priority: 3},
		{Identifier: "BRZ-4", Title: "unset one", StateName: "Todo", StateType: "unstarted", Priority: 0},
	}))

	got := renderedPalette(t, h)
	rows := map[string]string{}
	for _, line := range strings.Split(got, "\n") {
		for _, id := range []string{"BRZ-1", "BRZ-2", "BRZ-3", "BRZ-4"} {
			if strings.Contains(line, id+" ") {
				rows[id] = line
			}
		}
	}
	if !strings.Contains(rows["BRZ-1"], "!!") {
		t.Errorf("urgent must be marked: %q", rows["BRZ-1"])
	}
	if !strings.Contains(rows["BRZ-2"], "!") || strings.Contains(rows["BRZ-2"], "!!") {
		t.Errorf("high must be marked once: %q", rows["BRZ-2"])
	}
	for _, id := range []string{"BRZ-3", "BRZ-4"} {
		if strings.Contains(rows[id], "!") {
			t.Errorf("%s is medium/unset and must carry no mark: %q", id, rows[id])
		}
	}

	// The mark is shape, not colour: colour in this list means session status
	// and adding a second colour language would make neither readable.
	raw := h.commandPalette.View()
	for _, line := range strings.Split(raw, "\n") {
		if strings.Contains(ansi.Strip(line), "urgent one") && strings.Contains(line, "38;2") {
			// The row may be styled as a whole; what must not happen is the
			// mark carrying its own colour distinct from the title's.
			plain := ansi.Strip(line)
			if strings.Index(plain, "!!") > strings.Index(plain, "BRZ-1") {
				t.Error("the mark should precede the identifier")
			}
		}
	}
}

// TestPriorityColumnOnlyInTheTicketsTab pins the alignment.
//
// The priority mark started life baked into Name, which meant the mixed tab
// gained a lead column no other kind had — every ticket title sat four columns
// right of every command and worktree, and the list read as broken. It is a
// column of its own now, rendered only where every row is a ticket.
func TestPriorityColumnOnlyInTheTicketsTab(t *testing.T) {
	h := ticketHome(t)
	h.commandPalette.SetSize(120, 40)
	h.commandPalette.ShowOnTab(h.buildPaletteItems(), nil, PaletteTabTickets)
	h.commandPalette.SetTickets(h.ticketPaletteItems([]linear.Ticket{
		{Identifier: "BRZ-2124", Title: "The magic fix button", StateName: "In Review", StateType: "started", Priority: 2},
	}))

	// In the tickets tab the mark shows.
	if got := renderedPalette(t, h); !strings.Contains(got, "!") {
		t.Errorf("the tickets tab must show the priority mark:\n%s", got)
	}

	// In the mixed tab it must not, and ticket names must start in the same
	// column as command names.
	h.commandPalette.activeTab = PaletteTabAll
	h.commandPalette.filterInput.SetValue("magic")
	h.commandPalette.rebuildFiltered()
	got := renderedPalette(t, h)

	var ticketLine string
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "BRZ-2124") {
			ticketLine = line
		}
	}
	if ticketLine == "" {
		t.Fatalf("expected the ticket to match 'magic':\n%s", got)
	}
	if strings.Contains(ticketLine, "!") {
		t.Errorf("the mixed tab must not carry the priority column: %q", ticketLine)
	}
	if !strings.Contains(ticketLine, "tkt  BRZ-2124") {
		t.Errorf("ticket names must start right after the badge, like every other kind: %q", ticketLine)
	}
}

// TestMixedTabCarriesStateAndPriorityOnTheRight covers what a ticket loses
// outside its own tab.
//
// The tickets tab gives the state a header and the priority a column of its
// own. A mixed list has neither, so without putting both in the right column a
// ticket row there shows nothing but its title.
func TestMixedTabCarriesStateAndPriorityOnTheRight(t *testing.T) {
	h := ticketHome(t)
	h.commandPalette.SetSize(120, 40)
	h.commandPalette.ShowOnTab(h.buildPaletteItems(), nil, PaletteTabAll)
	h.commandPalette.SetTickets(h.ticketPaletteItems([]linear.Ticket{
		{Identifier: "BRZ-3013", Title: "TS sdk consider pushing spanprocessor", StateName: "Todo", StateType: "unstarted", Priority: 1},
		{Identifier: "BRZ-2365", Title: "Tighten the backend scope", StateName: "Backlog", StateType: "backlog", Priority: 0},
	}))
	h.commandPalette.filterInput.SetValue("spanprocessor")
	h.commandPalette.rebuildFiltered()

	got := renderedPalette(t, h)
	if !strings.Contains(got, "Todo  !!") {
		t.Errorf("a ticket in the mixed tab must carry its state AND priority:\n%s", got)
	}
}

// TestMixedTabHasNoStateHeaders: a "Todo" header sitting above a run of
// commands would describe rows it has nothing to do with.
func TestMixedTabHasNoStateHeaders(t *testing.T) {
	h := ticketHome(t)
	h.commandPalette.SetSize(120, 40)
	h.commandPalette.ShowOnTab(nil, nil, PaletteTabAll)
	h.commandPalette.SetTickets(h.ticketPaletteItems([]linear.Ticket{
		{Identifier: "BRZ-3013", Title: "TS sdk", StateName: "Todo", StateType: "unstarted", Priority: 1},
		{Identifier: "BRZ-2365", Title: "Tighten scope", StateName: "Backlog", StateType: "backlog"},
	}))

	got := renderedPalette(t, h)
	for _, line := range strings.Split(got, "\n") {
		trimmed := strings.TrimSpace(strings.Trim(line, "│"))
		if trimmed == "Todo  2" || trimmed == "Todo  1" || trimmed == "Backlog  1" {
			t.Errorf("the mixed tab must not group by Linear state: %q\n%s", line, got)
		}
	}
	// Sanity: the tickets ARE present, so the assertion above is not vacuous.
	if !strings.Contains(got, "BRZ-3013") {
		t.Fatalf("precondition: tickets should be listed:\n%s", got)
	}

	// And in the tickets tab the headers must be back.
	h.commandPalette.activeTab = PaletteTabTickets
	h.commandPalette.rebuildFiltered()
	if !strings.Contains(renderedPalette(t, h), "Todo  1") {
		t.Error("the tickets tab must group by state")
	}
}
