package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/releasenotes"
	"github.com/charmbracelet/x/ansi"
)

func TestRenderWhatsNewBadge(t *testing.T) {
	// The visible text (ANSI stripped) is stable regardless of frame — the
	// rainbow label plus the "· press Shift+W" key hint.
	const wantText = whatsNewText + " · press Shift+W"
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

	reelFor := func(r releasenotes.Release) string {
		d := NewReleaseNotesDialog()
		d.SetSize(90, 30)
		d.ShowWhatsNew("2.16.0")
		d.SetData([]releasenotes.Release{r}, nil)
		return ansi.Strip(d.View())
	}

	// A recent highlighted release: the reel shows the Highlights bullet, titles
	// as "What's New", and does NOT leak the non-highlight "Fixed" bullet. This
	// holds regardless of any seen version — the reel is re-viewable; the badge
	// (not the reel) tracks "seen".
	out := reelFor(fresh)
	if !strings.Contains(out, "What's New") {
		t.Error("reel should be titled What's New")
	}
	if !strings.Contains(out, "Shiny thing.") {
		t.Errorf("reel should show the highlight bullet, got:\n%s", out)
	}
	if strings.Contains(out, "Quiet fix.") {
		t.Errorf("reel should NOT show non-highlight bullets, got:\n%s", out)
	}

	// A release older than the 7-day window drops out, leaving the empty state.
	old := fresh
	old.Date = "2000-01-01"
	caughtUp := reelFor(old)
	if !strings.Contains(caughtUp, "in the last 7 days") {
		t.Errorf("out-of-window reel should show the empty state, got:\n%s", caughtUp)
	}
	if strings.Contains(caughtUp, "Shiny thing.") {
		t.Error("out-of-window reel should not show the highlight bullet")
	}
}

func TestReleaseNotesTabToggle(t *testing.T) {
	rel := releasenotes.Release{
		Version: "2.16.0",
		Date:    time.Now().Format("2006-01-02"),
		Sections: []releasenotes.Section{
			{Title: "Highlights", Bullets: []string{"**Reel bullet.** Shown in the reel."}},
			{Title: "Fixed", Bullets: []string{"**Full-only fix.** Only in full notes."}},
		},
	}
	d := NewReleaseNotesDialog()
	d.SetSize(90, 30)
	d.ShowWhatsNew("2.16.0")
	d.SetData([]releasenotes.Release{rel}, nil)
	tab := tea.KeyPressMsg{Code: tea.KeyTab}

	// Starts on the reel: the highlight shows, the non-highlight is hidden.
	out := ansi.Strip(d.View())
	if !strings.Contains(out, "What's New") || !strings.Contains(out, "Reel bullet.") {
		t.Errorf("expected the What's New reel with the highlight, got:\n%s", out)
	}
	if strings.Contains(out, "Full-only fix.") {
		t.Errorf("reel should hide non-highlight bullets, got:\n%s", out)
	}

	// Tab -> full Release Notes: title flips and the non-highlight bullet appears.
	d.Update(tab)
	out = ansi.Strip(d.View())
	if !strings.Contains(out, "Release Notes") || !strings.Contains(out, "Full-only fix.") {
		t.Errorf("after Tab expected full Release Notes with the fix, got:\n%s", out)
	}

	// Tab again -> back to the reel.
	d.Update(tab)
	out = ansi.Strip(d.View())
	if !strings.Contains(out, "What's New") || !strings.Contains(out, "Reel bullet.") {
		t.Errorf("second Tab should return to the reel, got:\n%s", out)
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
