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
	// Every set priority now carries a gauge, and the gauges must be DISTINCT —
	// a ladder where two rungs render alike is not a ladder. Only "no priority"
	// is blank, because absence should read as absence down the column.
	want := map[string]string{"BRZ-1": "▰▰▰", "BRZ-2": "▰▰▱", "BRZ-3": "▰▱▱"}
	for id, gauge := range want {
		if !strings.Contains(rows[id], gauge) {
			t.Errorf("%s must carry %q: %q", id, gauge, rows[id])
		}
	}
	if seen := map[string]bool{}; true {
		for _, g := range want {
			if seen[g] {
				t.Errorf("two priorities render the same gauge %q", g)
			}
			seen[g] = true
		}
	}
	for _, id := range []string{"BRZ-4"} {
		if strings.ContainsAny(rows[id], "▰▱") {
			t.Errorf("%s has no priority set and must carry no gauge: %q", id, rows[id])
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
// right of every command and worktree, and the list read as broken. The LEAD
// column is now rendered only where every row is a ticket; the mark itself
// still reaches the mixed tab, on the right, where it costs no alignment.
func TestPriorityColumnOnlyInTheTicketsTab(t *testing.T) {
	h := ticketHome(t)
	h.commandPalette.SetSize(120, 40)
	h.commandPalette.ShowOnTab(h.buildPaletteItems(), nil, PaletteTabTickets)
	h.commandPalette.SetTickets(h.ticketPaletteItems([]linear.Ticket{
		{Identifier: "BRZ-2124", Title: "The magic fix button", StateName: "In Review", StateType: "started", Priority: 2},
	}))

	// In the tickets tab the mark shows.
	if got := renderedPalette(t, h); !strings.Contains(got, "▰▰▱") {
		t.Errorf("the tickets tab must show the priority gauge:\n%s", got)
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
	// The mark may appear on the RIGHT — TestMixedTabCarriesStateAndPriorityOnTheRight
	// covers why: outside the tickets tab there is no header for the state and no
	// lead column for the priority, so a ticket row would otherwise show its title
	// and nothing else. What must not come back is the LEAD column, which is what
	// broke the alignment. So the assertion is positional, not "is a ! present".
	if plain := ansi.Strip(ticketLine); strings.Index(plain, "▰") < strings.Index(plain, "BRZ-2124") {
		t.Errorf("the mixed tab must not carry a priority LEAD column: %q", ticketLine)
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
	if !strings.Contains(got, "Todo  ▰▰▰") {
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

// TestMixedTabKeepsRepoAndWorktreeBranches pins the blast radius of the
// mixed-tab detail rewrite.
//
// ticketRightColumn puts a ticket's state and priority on the right, because
// outside the tickets tab there is no header for one and no lead column for the
// other. It ran for EVERY non-recent row, and it returns "" for a command, a
// repo or a worktree — so it blanked Detail, which is exactly where repo and
// worktree rows carry their branch name. Recent rows took the other branch of
// the loop and kept theirs, so one list showed some branches and not others.
//
// The empty query matters: this only bites when nothing is typed, which is why
// the screenshot that prompted the feature never showed it.
func TestMixedTabKeepsRepoAndWorktreeBranches(t *testing.T) {
	d := NewCommandPaletteDialog()
	d.SetSize(110, 40)
	d.Show([]PaletteItem{
		{ID: "c1", Name: "Settings", Shortcut: "S", Kind: PaletteKindCommand},
		{ID: "r1", Name: "stonks", Detail: "master", Kind: PaletteKindRepo, Haystack: "stonks master"},
		{ID: "w1", Name: "stonks-esports", Detail: "esports", Kind: PaletteKindWorktree, Haystack: "stonks-esports esports"},
	}, nil)

	want := map[string]string{"stonks": "master", "stonks-esports": "esports"}
	for _, it := range d.filtered {
		if w, ok := want[it.Name]; ok && it.Detail != w {
			t.Errorf("%s lost its branch in the all tab with no query: Detail=%q, want %q",
				it.Name, it.Detail, w)
		}
	}
}

// TestTicketsTabSpellsOutTheFleetJoin pins the one thing this view shows that
// Linear cannot: that a worktree already exists for a ticket, and what the
// session in it is doing.
//
// It used to be a bare coloured dot in the far-left badge column — two facts in
// one glyph, with no legend and nothing beside it to give it meaning. The badge
// column is also 4 wide because the MIXED tab puts "cmd "/"repo"/"wkt " in it,
// so in this tab it contributed four dead columns of indent and a one-character
// dot.
func TestTicketsTabSpellsOutTheFleetJoin(t *testing.T) {
	h := ticketHome(t)
	h.commandPalette.SetSize(100, 40)
	h.commandPalette.ShowOnTab(h.buildPaletteItems(), nil, PaletteTabTickets)
	items := h.ticketPaletteItems([]linear.Ticket{
		{Identifier: "BRZ-1", Title: "has a worktree", StateName: "Todo", StateType: "unstarted", Priority: 1},
		{Identifier: "BRZ-2", Title: "not in fleet", StateName: "Todo", StateType: "unstarted", Priority: 1},
	})
	items[0].HasSession, items[0].SessionStatus = true, session.StatusWaiting
	h.commandPalette.SetTickets(items)
	h.commandPalette.rebuildFiltered()

	got := renderedPalette(t, h)
	var withSession, without string
	for _, line := range strings.Split(got, "\n") {
		switch {
		case strings.Contains(line, "BRZ-1 "):
			withSession = line
		case strings.Contains(line, "BRZ-2 "):
			without = line
		}
	}
	if !strings.Contains(withSession, "waiting") {
		t.Errorf("a ticket with a worktree must say what its session is doing: %q", withSession)
	}
	if strings.Contains(without, "waiting") || strings.Contains(without, "idle") {
		t.Errorf("a ticket with no worktree must say nothing: %q", without)
	}

	// The gauge must sit right after the cursor marker — no badge column left.
	// Measured in COLUMNS, not bytes: ▰ and ▸ are three bytes each, so a byte
	// index here would be nonsense.
	plain := strings.TrimLeft(ansi.Strip(withSession), "│ ")
	i := strings.Index(plain, "▰")
	if i < 0 {
		t.Fatalf("expected a gauge on the row: %q", plain)
	}
	if cols := runeLen(plain[:i]); cols > 2 {
		t.Errorf("the gauge starts %d columns in — the badge column is back: %q", cols, plain)
	}
	j := strings.Index(plain, "BRZ-1")
	if j < 0 {
		t.Fatalf("expected an identifier on the row: %q", plain)
	}
	if cols := runeLen(plain[i:j]); cols > 4 {
		t.Errorf("%d columns between the gauge and the identifier: %q", cols, plain)
	}
}

// TestFilteredTicketsTabDoesNotDoubleThePriority guards a latent bug the right
// column's rewrite exposed.
//
// While filtering, headers are dropped and the state folds back onto the row.
// It did that through ticketRightColumn, which also appends the priority mark —
// correct in the MIXED tab, where no lead column exists, and wrong in the
// tickets tab, where the gauge is already rendering it two columns to the left.
// The same gauge would appear twice on one row.
func TestFilteredTicketsTabDoesNotDoubleThePriority(t *testing.T) {
	h := ticketHome(t)
	h.commandPalette.SetSize(120, 40)
	h.commandPalette.ShowOnTab(h.buildPaletteItems(), nil, PaletteTabTickets)
	h.commandPalette.SetTickets(h.ticketPaletteItems([]linear.Ticket{
		{Identifier: "BRZ-2644", Title: "Storage optimization", StateName: "In Progress", StateType: "started", Priority: 1},
	}))
	h.commandPalette.filterInput.SetValue("storage")
	h.commandPalette.rebuildFiltered()

	var row string
	for _, line := range strings.Split(renderedPalette(t, h), "\n") {
		if strings.Contains(line, "BRZ-2644") {
			row = ansi.Strip(line)
		}
	}
	if row == "" {
		t.Fatal("expected the filtered ticket to render")
	}
	if n := strings.Count(row, "▰▰▰"); n != 1 {
		t.Errorf("the priority gauge should appear exactly once, found %d: %q", n, row)
	}
	if !strings.Contains(row, "In Progress") {
		t.Errorf("a filtered row must still carry its state: %q", row)
	}
}
