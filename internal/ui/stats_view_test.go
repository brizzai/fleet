package ui

import (
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/stats"
)

var statsTestSizes = [][2]int{
	{80, 24}, {81, 24}, {99, 30}, {100, 30}, {101, 31}, {119, 40}, {120, 40}, {121, 41},
	{160, 45}, {167, 45}, {200, 50}, {223, 51}, {250, 60}, {251, 61},
}

// statsFixtureReport is a synthetic, fully populated Report (no real data —
// brizzai/fleet is public).
func statsFixtureReport(rng stats.Range) stats.Report {
	loc := time.Local
	to := time.Date(2026, 9, 28, 16, 0, 0, 0, loc)
	from := to.AddDate(0, 0, -7)
	conc := make([]float64, 168)
	peaks := make([]int, 168)
	for i := range conc {
		h := i % 24
		if h >= 9 && h <= 19 {
			conc[i] = 1.5 + 2*math.Sin(float64(h-9)/10*math.Pi) + float64(i%5)*0.3
		} else if h == 22 {
			conc[i] = 0.4
		}
		peaks[i] = int(math.Ceil(conc[i]))
	}
	var days []stats.Day
	start := time.Date(2025, 9, 29, 0, 0, 0, 0, loc)
	for d := start; !d.After(time.Date(2026, 9, 28, 0, 0, 0, 0, loc)); d = d.AddDate(0, 0, 1) {
		n := 0
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday && d.YearDay()%7 != 3 {
			n = (d.YearDay()*37)%90 + 3
		}
		days = append(days, stats.Day{Date: d, Actions: n, AgentTime: time.Duration(n) * 3 * time.Minute})
	}
	return stats.Report{
		Range: rng, From: from, To: to, GeneratedAt: to,
		Since: time.Date(2025, 10, 8, 0, 0, 0, 0, loc),
		Hero: stats.Hero{
			Parallel: 2.7, ParallelPrev: 2.1, Peak: 7, PeakAt: time.Date(2026, 9, 22, 15, 42, 0, 0, loc),
			AgentTime: 41*time.Hour + 12*time.Minute, AgentTimePrev: 35 * time.Hour,
			ReplyMedian: 48 * time.Second, ReplyP90: 6*time.Minute + 12*time.Second, ReplyN: 212,
			CostUSD: 1412.37, Tokens: 38_600_000,
		},
		Concurrency: conc, ConcurrencyPeak: peaks,
		ShareSolo: 0.39, ShareTwo: 0.27, ShareThreePlus: 0.34,
		Days: days, Streak: 23, BestStreak: 41,
		Activity: []stats.ActionCount{
			{Action: "attach", Label: "Attaches", N: 1204},
			{Action: "space_jump", Label: "Space jumps", N: 812},
			{Action: "quick_approve", Label: "Quick approves", N: 301},
			{Action: "slot_jump", Label: "Slot jumps", N: 122},
			{Action: "fork", Label: "Forks", N: 14},
			{Action: "drawer_open", Label: "Drawer opens", N: 9},
			{Action: "snooze", Label: "Snoozes", N: 3},
		},
		Records: []stats.Record{
			{Label: "longest turn", Value: "47m 12s", Detail: "drawer-live-vt"},
			{Label: "busiest day", Value: "212 actions", Detail: "Sep 14"},
			{Label: "most at once", Value: "7 agents", Detail: "Sep 22 15:42"},
			{Label: "oldest session", Value: "187 days", Detail: "fleet master"},
		},
		FunFacts: []string{
			"Your agents did 22% of their work after you stopped typing for the night.",
			"You jumped with Space 812 times — that's a lap of the fleet every 9 minutes.",
			"38.6M tokens would have cost $1,412 at API list prices.",
		},
		Repos: []stats.RepoTime{
			{Name: "fleet", AgentTime: 28 * time.Hour, Sessions: 31},
			{Name: "fleet-analytics", AgentTime: 7 * time.Hour, Sessions: 6},
			{Name: "a-very-long-repository-name-indeed", AgentTime: 90 * time.Minute, Sessions: 2},
		},
		Agents: []stats.AgentTime{
			{Agent: "claude", AgentTime: 34 * time.Hour, Sessions: 33},
			{Agent: "codex", AgentTime: 6 * time.Hour, Sessions: 5},
			{Agent: "opencode", AgentTime: 72 * time.Minute, Sessions: 1},
		},
		Coverage: stats.Coverage{
			RecordingSince: time.Date(2026, 9, 3, 0, 0, 0, 0, loc),
			TurnsSince:     time.Date(2026, 8, 29, 0, 0, 0, 0, loc),
			ClaudeOnly:     true, Sessions: 39,
		},
	}
}

// statsSparseFixtureReport is a young install: one agent, a handful of
// repos, a long tail of single fleet actions, two months of backfilled turn
// days but fleet actions recorded only today (synthetic).
func statsSparseFixtureReport() stats.Report {
	loc := time.Local
	to := time.Date(2026, 9, 28, 18, 0, 0, 0, loc)
	peaks := make([]int, 168)
	for i := range peaks {
		if d, h := i/24, i%24; (d == 1 || d == 5 || d == 6) && h >= 9 && h <= 15 {
			peaks[i] = 1 + (h*7)%4
		}
	}
	conc := make([]float64, len(peaks))
	for i, p := range peaks {
		conc[i] = float64(p) * 0.7
	}
	var days []stats.Day
	today := time.Date(2026, 9, 28, 0, 0, 0, 0, loc)
	for d := time.Date(2025, 9, 29, 0, 0, 0, 0, loc); !d.After(today); d = d.AddDate(0, 0, 1) {
		day := stats.Day{Date: d}
		if !d.Before(time.Date(2026, 7, 30, 0, 0, 0, 0, loc)) && d.Weekday() != time.Saturday {
			day.AgentTime = time.Duration((d.YearDay()*37)%300+20) * time.Minute
		}
		if d.Equal(today) {
			day.Actions = 101
		}
		days = append(days, day)
	}
	return stats.Report{
		Range: stats.Range7d, From: today.AddDate(0, 0, -6), To: to, GeneratedAt: to,
		Since: time.Date(2026, 7, 30, 0, 0, 0, 0, loc),
		Hero: stats.Hero{
			Parallel: 1.3, ParallelPrev: 1.0, Peak: 4, PeakAt: time.Date(2026, 9, 23, 17, 2, 0, 0, loc),
			AgentTime: 21 * time.Hour, AgentTimePrev: 5*time.Hour + 25*time.Minute,
			ReplyMedian: 97 * time.Second, ReplyP90: 8*time.Minute + 44*time.Second, ReplyN: 30,
			CostUSD: 551, Tokens: 1_080_000_000,
		},
		Concurrency: conc, ConcurrencyPeak: peaks,
		ShareSolo: 0.78, ShareTwo: 0.19, ShareThreePlus: 0.03,
		Days: days, Streak: 2, BestStreak: 5,
		Activity: []stats.ActionCount{
			{Action: "space_jump", Label: "Space jumps", N: 57},
			{Action: "attach", Label: "Attaches", N: 34},
			{Action: "slot_jump", Label: "Slot jumps", N: 3},
			{Action: "snooze", Label: "Snoozes", N: 3},
			{Action: "mark_unread", Label: "Marked unread", N: 1},
			{Action: "open_pr", Label: "PRs opened", N: 1},
			{Action: "session_new", Label: "Sessions started", N: 1},
			{Action: "worktree_new", Label: "Worktrees created", N: 1},
		},
		Records: []stats.Record{
			{Label: "longest turn", Value: "1h 27m", Detail: "Sep 23 · alpha"},
			{Label: "most at once", Value: "4 sessions", Detail: "Sep 23 17:02"},
			{Label: "busiest day", Value: "101 actions", Detail: "Sep 28"},
			{Label: "longest attach", Value: "3m 51s", Detail: "Sep 28 · beta"},
			{Label: "most Space jumps", Value: "57 in a day", Detail: "Sep 28"},
			{Label: "most sessions started", Value: "10 in a day", Detail: "Sep 27"},
		},
		FunFacts: []string{
			"22% of the time an agent was working, another one was too.",
			"You pressed Space 57 times to jump to the next agent that needed you.",
			"98% of what your agents read came straight from the prompt cache.",
			"Your agents are busiest between 17:00 and 18:00.",
		},
		Repos: []stats.RepoTime{
			{Name: "alpha", AgentTime: 16*time.Hour + 32*time.Minute, Sessions: 18},
			{Name: "beta", AgentTime: 2*time.Hour + 50*time.Minute, Sessions: 5},
			{Name: "gamma", AgentTime: 68 * time.Minute, Sessions: 3},
			{Name: "fleet", AgentTime: 29*time.Minute + 46*time.Second, Sessions: 3},
		},
		Agents: []stats.AgentTime{{Agent: "claude", AgentTime: 21 * time.Hour, Sessions: 29}},
		Coverage: stats.Coverage{
			RecordingSince: today.Add(9 * time.Hour),
			TurnsSince:     time.Date(2026, 7, 30, 11, 0, 0, 0, loc),
			ClaudeOnly:     true, Sessions: 29,
		},
	}
}

func statsKey(s string) tea.KeyPressMsg {
	switch s {
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

// statsAssertFits fails unless the frame is exactly w × h: every line w wide
// (a short one leaves the previous frame showing through) and h lines.
func statsAssertFits(t *testing.T, name, out string, w, h int) {
	t.Helper()
	lines := strings.Split(out, "\n")
	if len(lines) != h {
		t.Errorf("%s: %d lines, want %d", name, len(lines), h)
	}
	for i, l := range lines {
		if lw := lipgloss.Width(l); lw != w {
			t.Errorf("%s: line %d is %d wide, want %d: %q", name, i, lw, w, ansi.Strip(l))
		}
	}
}

// statsAssertWithin fails when a box (the recap reel, composited over the
// screen) has a line wider than w or more than h lines.
func statsAssertWithin(t *testing.T, name, out string, w, h int) {
	t.Helper()
	lines := strings.Split(out, "\n")
	if len(lines) > h {
		t.Errorf("%s: %d lines, want ≤ %d", name, len(lines), h)
	}
	for i, l := range lines {
		if lw := lipgloss.Width(l); lw > w {
			t.Errorf("%s: line %d is %d wide, want ≤ %d: %q", name, i, lw, w, ansi.Strip(l))
		}
	}
}

// When the report fits without scrolling, the chart grows only to
// statsChartMaxRows, the page is anchored to the top — it starts right under
// the range tabs, with at most one blank row between bands and the footnote
// attached under the grid — and the rows a tall screen still has spare fall
// below it, rather than a chart of thin spikes, cards stretched around
// nothing, a page floated mid-screen, or a footnote pinned to the bottom.
func TestStatsViewBalancesSpareRows(t *testing.T) {
	fixtures := map[string]stats.Report{"rich": statsFixtureReport(stats.Range7d), "sparse": statsSparseFixtureReport()}
	checked := 0
	defer func() {
		if checked < 8 {
			t.Errorf("only %d sizes fit without scrolling; the test checks nothing", checked)
		}
	}()
	for name, r := range fixtures {
		for _, sz := range [][2]int{{140, 45}, {160, 45}, {200, 50}, {120, 60}, {250, 60}, {250, 62}, {251, 80}} {
			v := NewStatsView()
			v.SetSize(sz[0], sz[1])
			v.Show()
			v.SetReport(r)
			lines := v.contentLines()
			label := fmt.Sprintf("%s@%dx%d", name, sz[0], sz[1])
			if len(lines) > v.innerHeight() {
				continue // scrolls: nothing to spare
			}
			checked++
			if v.tabRows() != 2 {
				t.Errorf("%s: a page that fits keeps the blank row under the tabs (tabRows %d)", label, v.tabRows())
			}
			if len(lines) != v.bodyHeight()-1 {
				t.Errorf("%s: %d content lines, want %d", label, len(lines), v.bodyHeight()-1)
			}
			plain := make([]string, len(lines))
			for i, l := range lines {
				plain[i] = strings.TrimSpace(ansi.Strip(l))
			}
			if !strings.HasPrefix(plain[0], "╭") {
				t.Errorf("%s: the page does not start under the tabs: %q", label, plain[0])
			}
			last := len(plain) - 1
			for last >= 0 && plain[last] == "" {
				last--
			}
			for i := 1; i < last; i++ {
				if plain[i] == "" && plain[i+1] == "" {
					t.Errorf("%s: two blank rows at %d inside the page", label, i)
					break
				}
			}
			if !strings.Contains(plain[last], "fleet sessions") {
				t.Errorf("%s: the page does not end with the footnote: %q", label, plain[last])
			}
			// Attached: one blank row, then the grid's last border.
			if last < 2 || plain[last-1] != "" || !strings.HasPrefix(plain[last-2], "╰") {
				t.Errorf("%s: footnote not attached under the grid", label)
			}
			// The chart's plot rows: between the card's top border (+ pad) and
			// its axis.
			title, axis := -1, -1
			for i, l := range plain {
				if title < 0 && strings.Contains(l, "concurrency") {
					title = i
				}
				if title >= 0 && i > title && strings.Contains(l, "──────────") && !strings.HasPrefix(l, "╰") {
					axis = i
					break
				}
			}
			if title < 0 || axis < 0 {
				t.Fatalf("%s: no chart found", label)
			}
			if rows := axis - title - 2; rows > statsChartMaxRows || rows < statsChartMinRows {
				t.Errorf("%s: chart is %d rows, want %d–%d", label, rows, statsChartMinRows, statsChartMaxRows)
			}
		}
	}
}

// Samples never land on a mix of 1- and 2-column slots: with fewer than two
// samples per column every slot is at least two columns and all are equal.
func TestStatsChartSlotsAreUniform(t *testing.T) {
	for _, c := range [][2]int{{168, 70}, {168, 110}, {168, 235}, {300, 215}, {120, 196}, {7, 28}} {
		n, w := c[0], c[1]
		s, k, pad := statsChartSlots(n, w)
		if s*k+pad != w || pad >= k && k > 1 {
			t.Errorf("n=%d w=%d: %d slots × %d + %d pad", n, w, s, k, pad)
		}
		if n < 2*w && k < 2 {
			t.Errorf("n=%d w=%d: %d-column slots alias", n, w, k)
		}
	}
}

// Days before recording began are blank, not zero-activity squares.
func TestStatsHeatmapBlanksDaysBeforeRecording(t *testing.T) {
	loc := time.Local
	var days []stats.Day
	for d := time.Date(2026, 9, 1, 0, 0, 0, 0, loc); !d.After(time.Date(2026, 9, 28, 0, 0, 0, 0, loc)); d = d.AddDate(0, 0, 1) {
		days = append(days, stats.Day{Date: d})
	}
	days[len(days)-2].Actions = 3
	r := stats.Report{Days: days, Coverage: stats.Coverage{RecordingSince: time.Date(2026, 9, 24, 0, 0, 0, 0, loc)}}
	card, ok := statsHeatmapCard(r, 100)
	if !ok {
		t.Fatal("no heatmap")
	}
	body := ansi.Strip(strings.Join(card.body(96), "\n"))
	// Recording began on Thursday Sep 24: exactly the five recorded days
	// (Thu–Mon, zeros included) get a square; that week's Mon–Wed are blank
	// and the weeks before it aren't drawn at all.
	lines := strings.Split(body, "\n")
	grid := strings.Join(lines[1:len(lines)-1], "\n")
	if n := strings.Count(grid, "■"); n != 5 {
		t.Errorf("want 5 squares (recorded days only), got %d:\n%s", n, body)
	}
}

func TestStatsViewFitsEverySize(t *testing.T) {
	reports := map[string]*stats.Report{
		"rich":  statsPtr(statsFixtureReport(stats.Range7d)),
		"empty": {Range: stats.Range7d},
		"quiet": {Range: stats.Range7d, Coverage: stats.Coverage{Sessions: 2}},
		"none":  nil,
	}
	for name, r := range reports {
		for _, sz := range statsTestSizes {
			v := NewStatsView()
			v.SetSize(sz[0], sz[1])
			v.Show()
			if r != nil {
				v.SetReport(*r)
			}
			label := fmt.Sprintf("%s@%dx%d", name, sz[0], sz[1])
			statsAssertFits(t, label, v.View(), sz[0], sz[1])
			// Scrolled to the bottom as well.
			v, _ = v.Update(statsKey("end"))
			statsAssertFits(t, label+"/end", v.View(), sz[0], sz[1])
		}
	}
}

func statsPtr[T any](v T) *T { return &v }

func TestStatsViewRangeKeysEmitRangeMsg(t *testing.T) {
	v := NewStatsView()
	v.SetSize(100, 30)
	v.Show()
	cases := []struct {
		key  string
		want stats.Range
	}{
		{"]", stats.Range30d},
		{"right", stats.RangeAll},
		{"]", stats.Range7d}, // wraps
		{"[", stats.RangeAll},
		{"left", stats.Range30d},
		{"3", stats.RangeAll},
		{"1", stats.Range7d},
		{"2", stats.Range30d},
	}
	for _, c := range cases {
		var cmd tea.Cmd
		v, cmd = v.Update(statsKey(c.key))
		if cmd == nil {
			t.Fatalf("%q: no command", c.key)
		}
		msg, ok := cmd().(statsRangeMsg)
		if !ok || msg.Range != c.want || v.Range() != c.want {
			t.Fatalf("%q: got msg %#v range %v, want %v", c.key, msg, v.Range(), c.want)
		}
	}
}

// The tabs are the range picker: every range is spelled out on the first row
// with the report's window beside it, and the active one is a mode — never a
// fill. The old chip in the frame's border is gone.
func TestStatsViewRangeTabs(t *testing.T) {
	v := NewStatsView()
	v.SetSize(120, 40)
	v.Show()
	v.SetReport(statsFixtureReport(stats.Range7d))
	out := v.View()
	rows := strings.Split(out, "\n")
	tabs := ansi.Strip(rows[1])
	for _, want := range []string{"7 days", "30 days", "all time", "Sep 21 – Sep 28"} {
		if !strings.Contains(tabs, want) {
			t.Errorf("tab row lacks %q: %q", want, tabs)
		}
	}
	if !strings.Contains(rows[1], ModeOn().Render("7 days")) {
		t.Errorf("the active range is not rendered as a mode: %q", rows[1])
	}
	if strings.Contains(rows[1], "48;2;") || strings.Contains(rows[1], "48;5;") {
		t.Errorf("the tab row fills a background: %q", rows[1])
	}
	if strings.Contains(ansi.Strip(rows[0]), "7d") || strings.Contains(ansi.Strip(out), "‹") {
		t.Errorf("the old border chip is still drawn: %q", ansi.Strip(rows[0]))
	}
	// Re-picking the range already shown asks for nothing.
	if _, cmd := v.Update(statsKey("1")); cmd != nil {
		t.Error("1 on the 7-day range asked for a report again")
	}
}

func TestStatsViewEscHides(t *testing.T) {
	v := NewStatsView()
	v.SetSize(80, 24)
	v.Show()
	v, cmd := v.Update(statsKey("esc"))
	if v.IsVisible() || cmd != nil {
		t.Fatalf("esc: visible=%v cmd=%v", v.IsVisible(), cmd != nil)
	}
}

func TestStatsViewFirstRunStateOnEmptyCoverage(t *testing.T) {
	for _, sz := range statsTestSizes {
		v := NewStatsView()
		v.SetSize(sz[0], sz[1])
		v.Show()
		v.SetReport(stats.Report{Range: stats.Range7d})
		if out := ansi.Strip(v.View()); !strings.Contains(out, "Stats start recording now") {
			t.Fatalf("%dx%d: first-run state missing:\n%s", sz[0], sz[1], out)
		}
	}
}

// A new install's first `i` is the first-run screen even though the recorder
// has already started the clock (status changes) and the sidebar's sessions are
// noted: coverage is not content.
func TestStatsViewFirstRunIgnoresCoverageAlone(t *testing.T) {
	v := NewStatsView()
	v.SetSize(100, 30)
	v.Show()
	v.SetReport(stats.Report{Range: stats.Range7d, Coverage: stats.Coverage{
		RecordingSince: time.Now(), Sessions: 4, ClaudeOnly: true}})
	if out := ansi.Strip(v.View()); !strings.Contains(out, "Stats start recording now") {
		t.Fatalf("first-run state missing:\n%s", out)
	}
}

// Non-Claude agents have no turn data, so their share is a dash, not 0%.
func TestStatsAgentsWithoutTurnDataSaySo(t *testing.T) {
	r := stats.Report{Coverage: stats.Coverage{ClaudeOnly: true}, Agents: []stats.AgentTime{
		{Agent: "claude", AgentTime: time.Hour, Sessions: 2}, {Agent: "codex", Sessions: 4}}}
	out := ansi.Strip(strings.Join(statsAgentLines(r, 80), "\n"))
	if strings.Contains(out, " 0%") || !strings.Contains(out, "Codex") || !strings.Contains(out, "no turn data") {
		t.Fatalf("agents:\n%s", out)
	}
}

// A quiet range with history elsewhere is not "first run": it renders the
// normal screen, with dashes rather than zeros that read as measurements.
func TestStatsViewQuietRangeRendersDashes(t *testing.T) {
	v := NewStatsView()
	v.SetSize(100, 30)
	v.Show()
	old := time.Date(2026, 6, 1, 0, 0, 0, 0, time.Local)
	v.SetReport(stats.Report{Range: stats.Range7d, Coverage: stats.Coverage{Sessions: 3},
		Days: []stats.Day{{Date: old, Actions: 4}, {Date: old.AddDate(0, 0, 1)}}})
	out := ansi.Strip(v.View())
	if strings.Contains(out, "start recording") {
		t.Fatal("quiet range shows the first-run state")
	}
	if !strings.Contains(out, "—") || strings.Contains(out, "0.0×") {
		t.Fatalf("missing values should render as dashes:\n%s", out)
	}
}

func TestStatsViewScanScreen(t *testing.T) {
	v := NewStatsView()
	v.SetSize(80, 24)
	v.Show()
	v.SetProgress(stats.Progress{Done: 241, Total: 337, Tokens: 38_600_000, Turns: 1200})
	out := ansi.Strip(v.View())
	for _, want := range []string{"reading 241/337 conversations", "38.6M tokens", "never your prompts or code"} {
		if !strings.Contains(out, want) {
			t.Errorf("scan screen missing %q:\n%s", want, out)
		}
	}
	statsAssertFits(t, "scan", v.View(), 80, 24)

	// A report with data wins over the scan screen, which then shrinks to a note.
	v.SetReport(statsFixtureReport(stats.Range7d))
	if out := ansi.Strip(v.View()); strings.Contains(out, "never your prompts") {
		t.Error("scan screen still showing after a populated report arrived")
	}
}

func TestStatsViewRichContent(t *testing.T) {
	v := NewStatsView()
	v.SetSize(100, 200)
	v.Show()
	v.SetReport(statsFixtureReport(stats.Range7d))
	out := ansi.Strip(v.View())
	for _, want := range []string{
		"2.7×", "▲ from 2.1×", "41h 12m", "p90 6m 12s", "$1,412", "concurrency",
		"streak 23d · best 41d", "records", "fun facts", "fleet actions", "Space jumps", "✻ Claude",
		"turn data: Claude sessions only", statsFooterPromise,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if os.Getenv("STATS_DUMP") != "" {
		for _, sz := range statsTestSizes {
			v.SetSize(sz[0], sz[1])
			fmt.Println(v.View())
		}
	}
}

func TestStatsViewScrolls(t *testing.T) {
	v := NewStatsView()
	v.SetSize(80, 24)
	v.Show()
	v.SetReport(statsFixtureReport(stats.Range7d))
	if v.maxScroll() == 0 {
		t.Fatal("rich report should overflow 80x24")
	}
	v, _ = v.Update(statsKey("j"))
	if v.scroll != 1 {
		t.Fatalf("j: scroll=%d", v.scroll)
	}
	v, _ = v.Update(statsKey("end"))
	if v.scroll != v.maxScroll() {
		t.Fatalf("end: scroll=%d want %d", v.scroll, v.maxScroll())
	}
	v, _ = v.Update(statsKey("k"))
	if v.scroll != v.maxScroll()-1 {
		t.Fatalf("k: scroll=%d", v.scroll)
	}
}

func TestStatsBarIsExactWidth(t *testing.T) {
	for _, f := range []float64{0, 0.01, 0.33, 0.5, 0.999, 1, 2} {
		for _, w := range []int{1, 5, 17} {
			if got := lipgloss.Width(statsBar(f, w, DimStyle)); got != w {
				t.Errorf("statsBar(%v,%d) width %d", f, w, got)
			}
		}
	}
}

func TestStatsFormatting(t *testing.T) {
	cases := map[string]string{
		statsInt(34584):                          "34,584",
		statsInt(999):                            "999",
		statsDur(48 * time.Second):               "48s",
		statsDur(6*time.Minute + 12*time.Second): "6m 12s",
		statsDur(41*time.Hour + 12*time.Minute):  "41h 12m",
		statsMoney(4.123):                        "$4.12",
		statsMoney(1412.37):                      "$1,412",
		statsTokens(38_600_000):                  "38.6M",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}

// The charts spend the theme's data ramp (border → accent), never a status
// hue: green/red/yellow belong to the status dot.
func TestStatsViewSpendsNoStatusColour(t *testing.T) {
	v := NewStatsView()
	v.SetSize(160, 80)
	v.Show()
	v.SetReport(statsFixtureReport(stats.Range7d))
	out := v.View()
	for name, c := range map[string]string{"green": colorHex(ColorGreen), "red": colorHex(ColorRed), "yellow": colorHex(ColorYellow)} {
		r, g, b := hexToRGB(c).r, hexToRGB(c).g, hexToRGB(c).b
		if strings.Contains(out, fmt.Sprintf("38;2;%d;%d;%dm", r, g, b)) {
			t.Errorf("stats screen uses status colour %s", name)
		}
	}
}

// Empty sections are left out, not drawn as "nothing recorded" panels.
func TestStatsViewHidesEmptyCards(t *testing.T) {
	r := statsFixtureReport(stats.Range7d)
	r.Activity, r.FunFacts, r.Records = nil, nil, nil
	v := NewStatsView()
	v.SetSize(160, 80)
	v.Show()
	v.SetReport(r)
	out := ansi.Strip(v.View())
	for _, gone := range []string{"fleet actions", "fun facts", "records", "nothing recorded"} {
		if strings.Contains(out, gone) {
			t.Errorf("empty card %q still drawn", gone)
		}
	}
	for _, kept := range []string{"repos", "agents", "concurrency", "streak 23d"} {
		if !strings.Contains(out, kept) {
			t.Errorf("card %q missing", kept)
		}
	}
}

// Cards in one grid row end on the same line, and each is a closed box.
func TestStatsGridRowsShareHeight(t *testing.T) {
	cols := []statsColumn{
		{{title: "a", minW: 20, body: func(int) []string { return []string{"x"} }}},
		{{title: "b", minW: 20, body: func(int) []string { return []string{"1", "2", "3", "4"} }}},
	}
	lines := statsGrid(cols, 60, 0)
	if len(lines) != 6 {
		t.Fatalf("row height %d, want 6", len(lines))
	}
	last := ansi.Strip(lines[len(lines)-1])
	if strings.Count(last, "╰") != 2 || lipgloss.Width(lines[0]) != 60 {
		t.Fatalf("grid row not closed/full width:\n%s", strings.Join(lines, "\n"))
	}
}

// Nothing on the Stats screen fills a background: the ramp is foreground only.
func TestStatsViewNeverFills(t *testing.T) {
	v := NewStatsView()
	v.SetSize(200, 60)
	v.Show()
	v.SetReport(statsFixtureReport(stats.Range7d))
	if out := v.View(); strings.Contains(out, "[48;") || strings.Contains(out, ";48;") {
		t.Fatal("stats screen spends a background fill")
	}
}

// Backfilled agent time lights the heatmap even after fleet has started
// recording actions: scoring by actions alone drew every backfilled day as a
// zero dot the moment today had one action.
func TestStatsHeatmapCountsBackfilledAgentTime(t *testing.T) {
	r := statsSparseFixtureReport()
	card, ok := statsHeatmapCard(r, 60)
	if !ok {
		t.Fatal("no heatmap")
	}
	body := ansi.Strip(strings.Join(card.body(56), "\n"))
	if n := strings.Count(body, "■"); n < 20+5 { // legend holds 4
		t.Errorf("backfilled days drawn as zero dots (%d filled):\n%s", n, body)
	}
	// Days before the earliest data source stay blank, not zero dots.
	levels := statsDayLevels(r.Days)
	for i, d := range r.Days {
		if d.Date.Before(r.Coverage.TurnsSince.AddDate(0, 0, -1)) && levels[i] != 0 {
			t.Fatalf("day %v before any data has level %d", d.Date, levels[i])
		}
	}
	g := statsHeatGeometry(r, levels, 56)
	if want := statsMidnight(r.Coverage.TurnsSince); !g.first.Equal(want) {
		t.Errorf("heatmap starts %v, want the first backfilled turn %v", g.first, want)
	}
}

// Big figures draw their units in the same half-block font, a gap apart from
// the next group, so "21h 00m" can never read as "2100".
func TestStatsBigFigureDrawsUnitsFullSize(t *testing.T) {
	for _, s := range []string{"21h 00m", "1m 37s", "$551", "$1,412", "2.7×", "4", "48s"} {
		rows, ok := statsBigFigure(s)
		if !ok || len(rows) != 3 {
			t.Fatalf("%q: ok=%v rows=%d", s, ok, len(rows))
		}
		w := lipgloss.Width(rows[0])
		for i, r := range rows {
			if lipgloss.Width(r) != w {
				t.Errorf("%q: row %d is %d wide, want %d", s, i, lipgloss.Width(r), w)
			}
			plain := ansi.Strip(r)
			if i == 0 && strings.HasPrefix(s, "$") {
				// The one small glyph: '$' set as a superscript.
				if !strings.HasPrefix(plain, "$") {
					t.Errorf("%q: row 0 should open with a superscript $: %q", s, plain)
				}
				plain = strings.TrimPrefix(plain, "$")
			}
			if strings.Trim(plain, " ▀▄█") != "" {
				t.Errorf("%q: row %d holds a small glyph: %q", s, i, plain)
			}
		}
	}
	// The space between groups is a column gap wider than the one inside a
	// number, so the two groups separate.
	rows, _ := statsBigFigure("21h 00m")
	gaps := statsBlankRuns(rows)
	if len(gaps) == 0 || slices.Max(gaps) < 3 {
		t.Errorf("no group gap in 21h 00m: %v", gaps)
	}
	if _, ok := statsBigFigure("12k"); ok {
		t.Error("a glyph the font lacks must fall back to the small figure")
	}
	// A thousands comma is dropped rather than drawn: at this size any
	// one-pixel mark beside the digits reads as a decimal point (an earlier
	// comma sat half a row below the period and still read as one).
	for _, c := range [][3]string{{"$1,412", "$1412", "$1.412"}, {"1,234h", "1234h", "1.234h"}} {
		comma, _ := statsBigFigure(c[0])
		bare, _ := statsBigFigure(c[1])
		dot, _ := statsBigFigure(c[2])
		if !slices.Equal(statsStripRows(comma), statsStripRows(bare)) {
			t.Errorf("%q should draw as %q", c[0], c[1])
		}
		if slices.Equal(statsStripRows(comma), statsStripRows(dot)) {
			t.Errorf("%q draws the same as %q", c[0], c[2])
		}
	}
}

func statsStripRows(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = ansi.Strip(r)
	}
	return out
}

// statsBlankRuns is the widths of the all-blank column runs between inked
// columns of a big figure.
func statsBlankRuns(rows []string) []int {
	plain := make([][]rune, len(rows))
	w := 0
	for i, r := range rows {
		plain[i] = []rune(ansi.Strip(r))
		w = max(w, len(plain[i]))
	}
	blank := func(x int) bool {
		for _, p := range plain {
			if x < len(p) && p[x] != ' ' {
				return false
			}
		}
		return true
	}
	var runs []int
	run := 0
	for x := range w {
		if blank(x) {
			run++
			continue
		}
		if run > 0 {
			runs = append(runs, run)
		}
		run = 0
	}
	return runs
}

// A wide hero tile centres its figure instead of hugging the left edge.
func TestStatsHeroTilesCentreTheirFigures(t *testing.T) {
	lines := statsHeroRows(statsSparseFixtureReport(), 246, statsStyle{pad: 1, big: true})
	fig := []rune(ansi.Strip(lines[2])) // top border, pad, first figure row
	inner := fig[1 : slices.Index(fig[1:], '│')+1]
	tile := len(inner)
	lead := tile - len([]rune(strings.TrimLeft(string(inner), " ")))
	if tile < 20 || lead < tile/4 {
		t.Errorf("figure hugs the tile's left edge (lead %d in a %d-wide tile): %q", lead, tile, string(fig))
	}
}

// One agent is not a split worth a card: it rides a line under the repos.
func TestStatsSingleAgentFoldsIntoRepos(t *testing.T) {
	v := NewStatsView()
	v.SetSize(200, 50)
	v.Show()
	v.SetReport(statsSparseFixtureReport())
	out := ansi.Strip(v.View())
	if strings.Contains(out, "agents ─") || strings.Contains(out, "100%") {
		t.Errorf("single-agent card still drawn:\n%s", out)
	}
	if !strings.Contains(out, "Claude · 29 sessions") {
		t.Errorf("agent line missing under the repos:\n%s", out)
	}
}

// The actions list stops at its top entries and says how many it left out.
func TestStatsActionsTrimTheLongTail(t *testing.T) {
	r := statsSparseFixtureReport()
	lines := statsActivityLines(r, 40, statsActionRows)
	if len(lines) != statsActionRows || !strings.Contains(ansi.Strip(lines[len(lines)-1]), "+3 more") {
		t.Errorf("actions:\n%s", ansi.Strip(strings.Join(lines, "\n")))
	}
	if all := statsActivityLines(r, 40, 20); len(all) != len(r.Activity) {
		t.Errorf("room for all %d, got %d lines", len(r.Activity), len(all))
	}
}

// No card is left mostly empty on a wide screen: at most 30% of a card's
// body (rounded up to a whole row — a card can't be blank by part of a row)
// may be rows left under its content. statsBestGrid penalises a hollow card
// on its own, not only the page's total blank cells, which let a one-row
// layout strand half of fleet actions under a tall records + facts stack.
func TestStatsNoCardIsMostlyBlank(t *testing.T) {
	fixtures := map[string]stats.Report{"rich": statsFixtureReport(stats.Range7d), "sparse": statsSparseFixtureReport()}
	for name, r := range fixtures {
		for _, sz := range [][2]int{{200, 50}, {250, 60}, {250, 62}} {
			v := NewStatsView()
			v.SetSize(sz[0], sz[1])
			v.Show()
			v.SetReport(r)
			cards := statsCardBlankness(v.View(), sz[0])
			if len(cards) < 6 {
				t.Fatalf("%s@%dx%d: found only %d cards; the measure is blind", name, sz[0], sz[1], len(cards))
			}
			for _, c := range cards {
				if limit := (c.inner*30 + 99) / 100; c.blank > limit {
					t.Errorf("%s@%dx%d: %q is %d of %d rows blank (%.0f%%)",
						name, sz[0], sz[1], c.title, c.blank, c.inner, c.frac*100)
				}
			}
		}
	}
}

type statsCardBlank struct {
	title        string
	blank, inner int
	frac         float64
}

// statsCardBlankness finds every bordered card in a rendered frame (narrower
// than the frame itself) and measures the share of its body that is blank
// rows left under the content — the stretch a row gave a short card — not
// counting the pad rows every card carries in the roomy styles, nor a blank
// separator line inside the content.
func statsCardBlankness(frame string, w int) []statsCardBlank {
	var grid [][]rune
	for _, l := range strings.Split(ansi.Strip(frame), "\n") {
		grid = append(grid, []rune(l))
	}
	at := func(x, y int) rune {
		if y < 0 || y >= len(grid) || x < 0 || x >= len(grid[y]) {
			return 0
		}
		return grid[y][x]
	}
	type box struct {
		title          string
		x0, x1, y0, y1 int
		rows           []bool // per inner row: blank?
	}
	var boxes []box
	for y := range grid {
		for x := range grid[y] {
			if grid[y][x] != '╭' {
				continue
			}
			x1 := x + 1
			for x1 < len(grid[y]) && grid[y][x1] != '╮' {
				x1++
			}
			if x1 >= len(grid[y]) || x1-x >= w-4 {
				continue // the frame itself
			}
			y1 := y + 1
			for at(x, y1) == '│' {
				y1++
			}
			if at(x, y1) != '╰' {
				continue
			}
			b := box{title: strings.Trim(string(grid[y][x+1:x1]), "─ "), x0: x, x1: x1, y0: y, y1: y1}
			if i := strings.Index(b.title, "─"); i >= 0 {
				b.title = strings.TrimSpace(b.title[:i])
			}
			for yy := y + 1; yy < y1; yy++ {
				b.rows = append(b.rows, strings.TrimSpace(string(grid[yy][x+1:x1])) == "")
			}
			boxes = append(boxes, b)
		}
	}
	// The pad the style put inside every card: blank rows at the top of all of them.
	pad := 1
	for _, b := range boxes {
		if len(b.rows) == 0 || !b.rows[0] {
			pad = 0
		}
	}
	var out []statsCardBlank
	for _, b := range boxes {
		rows := b.rows
		if len(rows) > 2*pad {
			rows = rows[pad : len(rows)-pad]
		}
		n := 0
		for i := len(rows) - 1; i >= 0 && rows[i]; i-- {
			n++
		}
		if len(rows) > 0 {
			out = append(out, statsCardBlank{b.title, n, len(rows), float64(n) / float64(len(rows))})
		}
	}
	return out
}

// The plot leaves a column before the card's right edge, so the bar for the
// hour in progress doesn't run into the border.
func TestStatsChartLeavesARightMargin(t *testing.T) {
	r := statsFixtureReport(stats.Range7d)
	r.ConcurrencyPeak[len(r.ConcurrencyPeak)-1] = 7 // a bar in the last slot
	card, ok := statsConcurrencyCard(r, 6)
	if !ok {
		t.Fatal("no chart")
	}
	for _, w := range []int{60, 117, 196, 245} {
		for i, l := range card.body(w)[:6] {
			if got := lipgloss.Width(l); got != w-1 {
				t.Errorf("w=%d: plot row %d is %d wide, want %d", w, i, got, w-1)
			}
		}
	}
}
