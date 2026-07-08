package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/releasenotes"
	"github.com/charmbracelet/x/ansi"
)

func TestRenderWhatsNewBadge(t *testing.T) {
	// The visible text (ANSI stripped) is stable regardless of frame — the
	// rainbow label plus the "· press W" key hint.
	const wantText = whatsNewText + " · press W"
	for _, frame := range []int{0, 1, 7, 13, 100} {
		got := ansi.Strip(renderWhatsNewBadge(frame))
		if got != wantText {
			t.Errorf("frame %d text = %q, want %q", frame, got, wantText)
		}
	}

	// Width is constant across frames so the top-right overlay never jitters.
	w0 := lipgloss.Width(renderWhatsNewBadge(0))
	for _, frame := range []int{1, 3, 9, 42} {
		if w := lipgloss.Width(renderWhatsNewBadge(frame)); w != w0 {
			t.Errorf("frame %d width = %d, want %d (stable)", frame, w, w0)
		}
	}

	// Animation: different frames produce different escape sequences (the
	// rainbow drifts and the shine sweeps), and the output is actually colored.
	if renderWhatsNewBadge(0) == renderWhatsNewBadge(4) {
		t.Error("expected frames 0 and 4 to differ (badge should animate)")
	}
	if !strings.Contains(renderWhatsNewBadge(0), "\x1b[") {
		t.Error("expected ANSI color escapes in the badge output")
	}
}

func TestReleaseNotesWhatsNewReel(t *testing.T) {
	today := time.Now().Format("2006-01-02")
	fresh := releasenotes.Release{
		Version: "2.16.0",
		Date:    today,
		Sections: []releasenotes.Section{
			{Title: "Highlights", Bullets: []string{"**Shiny thing.** You'll notice it."}},
			{Title: "Added", Bullets: []string{"**Shiny thing.** You'll notice it."}},
			{Title: "Fixed", Bullets: []string{"**Quiet fix.** Nobody saw the bug."}},
		},
	}

	render := func(seen string) string {
		d := NewReleaseNotesDialog()
		d.SetSize(90, 30)
		d.ShowWhatsNew("2.16.0", seen)
		d.SetData([]releasenotes.Release{fresh}, nil)
		return ansi.Strip(d.View())
	}

	// Unseen: the reel shows the Highlights bullet, titles as "What's New", and
	// does NOT leak the non-highlight "Fixed" bullet.
	out := render("2.15.0")
	if !strings.Contains(out, "What's New") {
		t.Error("reel should be titled What's New")
	}
	if !strings.Contains(out, "Shiny thing.") {
		t.Errorf("reel should show the highlight bullet, got:\n%s", out)
	}
	if strings.Contains(out, "Quiet fix.") {
		t.Errorf("reel should NOT show non-highlight bullets, got:\n%s", out)
	}

	// Already seen: empty state, no highlight bullet.
	caughtUp := render("2.16.0")
	if !strings.Contains(caughtUp, "caught up") {
		t.Errorf("seen reel should show the caught-up empty state, got:\n%s", caughtUp)
	}
	if strings.Contains(caughtUp, "Shiny thing.") {
		t.Error("seen reel should not show the highlight bullet")
	}
}

func TestReleaseNotesFullViewHidesHighlights(t *testing.T) {
	// The full changelog view renders the type sections but suppresses the
	// curated Highlights duplicate.
	rel := releasenotes.Release{
		Version: "2.16.0",
		Date:    time.Now().Format("2006-01-02"),
		Sections: []releasenotes.Section{
			{Title: "Highlights", Bullets: []string{"**Dup marker line.**"}},
			{Title: "Added", Bullets: []string{"**Real added item.**"}},
		},
	}
	d := NewReleaseNotesDialog()
	d.SetSize(90, 30)
	d.Show("2.16.0")
	d.SetData([]releasenotes.Release{rel}, nil)
	out := ansi.Strip(d.View())
	if !strings.Contains(out, "Real added item.") {
		t.Errorf("full view should show the Added bullet, got:\n%s", out)
	}
	if strings.Contains(out, "Dup marker line.") || strings.Contains(out, "Highlights") {
		t.Errorf("full view should hide the Highlights duplicate, got:\n%s", out)
	}
}

func TestIsUnseenHighlight(t *testing.T) {
	today := func() string { return time.Now().Format("2006-01-02") }
	hl := releasenotes.Section{Title: "Highlights", Bullets: []string{"**Thing.** Nice."}}
	added := releasenotes.Section{Title: "Added", Bullets: []string{"**Thing.** Nice."}}

	cases := []struct {
		name string
		r    releasenotes.Release
		seen string
		want bool
	}{
		{"fresh highlighted release", releasenotes.Release{Version: "2.16.0", Date: today(), Sections: []releasenotes.Section{hl, added}}, "2.15.0", true},
		{"already seen", releasenotes.Release{Version: "2.15.0", Date: today(), Sections: []releasenotes.Section{hl}}, "2.15.0", false},
		{"no highlights section", releasenotes.Release{Version: "2.16.0", Date: today(), Sections: []releasenotes.Section{added}}, "2.15.0", false},
		{"too old", releasenotes.Release{Version: "2.16.0", Date: "2000-01-01", Sections: []releasenotes.Section{hl}}, "2.15.0", false},
		{"empty highlights", releasenotes.Release{Version: "2.16.0", Date: today(), Sections: []releasenotes.Section{{Title: "Highlights"}}}, "2.15.0", false},
	}
	for _, c := range cases {
		if got := isUnseenHighlight(c.r, c.seen); got != c.want {
			t.Errorf("%s: isUnseenHighlight = %v, want %v", c.name, got, c.want)
		}
	}
}
