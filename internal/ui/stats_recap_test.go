package ui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/stats"
)

func statsFixtureRecap() stats.Recap {
	return stats.Recap{
		WeekStart: time.Date(2026, 9, 21, 0, 0, 0, 0, time.Local),
		Cards: []stats.RecapCard{
			{Big: "6", Caption: "agents at the same moment", Lines: []string{"Tuesday 14:32", "in fix-drawer · ticket-3217 · analytics"}, Spark: []float64{1, 2, 3, 6, 4, 2, 1}, RepoLines: []int{1}},
			{Big: "41h 12m", Caption: "of agent work", Lines: []string{"▲ 18% on the week before"}},
			{Big: "812", Caption: "Space jumps", Lines: []string{"a lap of the fleet every 9 minutes"}},
			{Big: "$1,412", Caption: "at API list prices", Lines: []string{"money you didn't pay"}},
			{Big: "2.7×", Caption: "agents per busy hour", Spark: []float64{2, 2.4, 3, 2.7, 2.9}},
		},
	}
}

func TestRecapPagingCopyAndClose(t *testing.T) {
	v := NewRecapView()
	v.SetSize(100, 30)
	v.Show(statsFixtureRecap())
	if !v.IsVisible() || v.Page() != 0 {
		t.Fatal("Show should open on the first card")
	}
	v, _ = v.Update(statsKey("left"))
	if v.Page() != 0 {
		t.Fatal("← on the first card must not wrap")
	}
	v, _ = v.Update(statsKey("right"))
	v, _ = v.Update(statsKey("l"))
	if v.Page() != 2 {
		t.Fatalf("page = %d, want 2", v.Page())
	}
	v, cmd := v.Update(statsKey("c"))
	if cmd == nil {
		t.Fatal("c produced no command")
	}
	msg, ok := cmd().(statsCopyMsg)
	if !ok || !strings.HasPrefix(msg.Text, "812 Space jumps") {
		t.Fatalf("copy msg = %#v", msg)
	}
	for range 10 {
		v, _ = v.Update(statsKey("right"))
	}
	if v.Page() != 4 {
		t.Fatalf("→ past the end: page = %d, want 4", v.Page())
	}
	v, _ = v.Update(statsKey("esc"))
	if v.IsVisible() {
		t.Fatal("esc should close the reel")
	}
}

func TestRecapBoxIsFixedAndFits(t *testing.T) {
	for _, sz := range statsTestSizes {
		v := NewRecapView()
		v.SetSize(sz[0], sz[1])
		v.Show(statsFixtureRecap())
		var h0, w0 int
		for p := range 5 {
			out := v.View()
			statsAssertWithin(t, fmt.Sprintf("recap@%dx%d/p%d", sz[0], sz[1], p), out, sz[0], sz[1])
			h, w := lipgloss.Height(out), lipgloss.Width(out)
			if p == 0 {
				h0, w0 = h, w
			} else if h != h0 || w != w0 {
				t.Errorf("%dx%d: box resized on page %d (%dx%d vs %dx%d)", sz[0], sz[1], p, w, h, w0, h0)
			}
			if os.Getenv("STATS_DUMP") != "" && sz[0] == 100 {
				fmt.Println(out)
			}
			v, _ = v.Update(statsKey("right"))
		}
	}
	// An empty recap renders, too.
	v := NewRecapView()
	v.SetSize(80, 24)
	v.Show(stats.Recap{})
	if out := ansi.Strip(v.View()); !strings.Contains(out, "Not enough happened") {
		t.Fatalf("empty recap:\n%s", out)
	}
}

func TestRecapCardText(t *testing.T) {
	card := stats.RecapCard{Big: "6", Caption: "agents at the same moment", Lines: []string{"Tuesday 14:32", " ", "in client-acme · BRZ-1232"}, RepoLines: []int{2}}
	got := recapCardText(card, false)
	want := "6 agents at the same moment\nTuesday 14:32\n— my week in fleet"
	if got != want {
		t.Fatalf("repo names must be left out by default: got %q want %q", got, want)
	}
	if with := recapCardText(card, true); !strings.Contains(with, "client-acme") {
		t.Fatalf("r must put repo names back: %q", with)
	}
	if strings.Contains(got, "\x1b") {
		t.Fatal("copied text carries ANSI")
	}
}

// A headline figure is one line: the number bold, its units dim — no custom
// digit font, whose thin strokes read as spindly next to real text.
func TestStatsFigureSplitsNumberFromUnits(t *testing.T) {
	for s, want := range map[string]string{"6": "6", "41h 12m": "41h 12m", "$1,412": "$1,412", "2.7×": "2.7×", "—": "—"} {
		got := statsFigure(s)
		if ansi.Strip(got) != want || strings.Contains(got, "\n") {
			t.Errorf("statsFigure(%q) = %q", s, ansi.Strip(got))
		}
	}
}

func TestStatsBadgeIsStaticAndUnfilled(t *testing.T) {
	b := renderStatsBadge()
	if ansi.Strip(b) != statsBadgeText {
		t.Fatalf("badge = %q", ansi.Strip(b))
	}
	if strings.Contains(b, "[48;") {
		t.Fatal("badge spends a background fill")
	}
}

// `c` leaves repo names out by default and `r` puts them back: a copied card
// is meant to be shared, and repo names often carry client or ticket names.
func TestRecapCopyRedactsReposUntilAsked(t *testing.T) {
	v := NewRecapView()
	v.SetSize(100, 30)
	v.Show(statsFixtureRecap())
	_, cmd := v.Update(statsKey("c"))
	msg := cmd().(statsCopyMsg)
	if strings.Contains(msg.Text, "fix-drawer") || !msg.Redacted {
		t.Fatalf("default copy leaked repo names: %#v", msg)
	}
	if !strings.Contains(ansi.Strip(v.View()), "r repos") {
		t.Fatal("a card with repo lines should advertise r")
	}
	_, cmd = v.Update(statsKey("r"))
	if on := cmd().(statsRecapReposMsg); !on.On {
		t.Fatal("r should turn repo names on")
	}
	_, cmd = v.Update(statsKey("c"))
	if msg := cmd().(statsCopyMsg); !strings.Contains(msg.Text, "fix-drawer") || msg.Redacted {
		t.Fatalf("copy after r should carry repo names: %#v", msg)
	}
}
