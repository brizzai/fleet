package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/releasenotes"
	"github.com/charmbracelet/x/ansi"
)

func sampleReleases() []releasenotes.Release {
	return []releasenotes.Release{
		{Version: "2.15.0", Date: "2026-07-06", Sections: []releasenotes.Section{
			{Title: "Added", Bullets: []string{"Auto-suspend idle sessions under memory pressure so a big fleet can't OOM-crash every session at once."}},
			{Title: "Fixed", Bullets: []string{"Worktree names no longer snowball."}},
		}},
		{Version: "2.14.0", Date: "2026-07-05", Sections: []releasenotes.Section{
			{Title: "Improved", Bullets: []string{"Quitting shows a shutting-down indicator and exits faster."}},
		}},
		{Version: "2.13.0", Date: "2026-07-02", Sections: []releasenotes.Section{
			{Title: "Added", Bullets: []string{"New terminal drawer for live repo-scoped shells."}},
		}},
	}
}

// strip returns s with ANSI styling removed, for substring/width assertions.
func strip(s string) string { return ansi.Strip(s) }

func TestReleaseNotesInstalledAndNewerGrouping(t *testing.T) {
	d := NewReleaseNotesDialog()
	d.SetSize(100, 40)
	d.Show("v2.14.0") // installed is the middle release
	d.SetData(sampleReleases(), nil)

	joined := strip(strings.Join(d.contentLines(), "\n"))
	if !strings.Contains(joined, "NEWER") {
		t.Errorf("expected a NEWER group header (2.15.0 is newer than installed 2.14.0)\n%s", joined)
	}
	if !strings.Contains(joined, "INSTALLED") {
		t.Errorf("expected an INSTALLED badge on 2.14.0\n%s", joined)
	}
	// The installed badge must sit on the 2.14.0 line, and 2.15.0 must appear
	// above it (newest-first).
	iNewer := strings.Index(joined, "v2.15.0")
	iInstalled := strings.Index(joined, "INSTALLED")
	if iNewer < 0 || iInstalled < 0 || iNewer > iInstalled {
		t.Errorf("expected v2.15.0 to render above the INSTALLED badge; got newer@%d installed@%d", iNewer, iInstalled)
	}
}

func TestReleaseNotesNoLineOverflows(t *testing.T) {
	d := NewReleaseNotesDialog()
	d.SetSize(90, 40)
	d.Show("2.14.0")
	d.SetData(sampleReleases(), nil)

	inner := d.innerWidth()
	for _, line := range d.contentLines() {
		if w := lipgloss.Width(line); w > inner {
			t.Errorf("content line width %d exceeds inner width %d: %q", w, inner, strip(line))
		}
	}
}

func TestReleaseNotesDevBuildHasNoInstalledMarker(t *testing.T) {
	d := NewReleaseNotesDialog()
	d.SetSize(100, 40)
	d.Show("dev")
	d.SetData(sampleReleases(), nil)

	joined := strip(strings.Join(d.contentLines(), "\n"))
	if strings.Contains(joined, "INSTALLED") {
		t.Errorf("dev build should not mark any release INSTALLED\n%s", joined)
	}
	if !strings.Contains(joined, "dev build") {
		t.Errorf("dev build should show an explanatory note\n%s", joined)
	}
}

func TestReleaseNotesLoadingAndError(t *testing.T) {
	d := NewReleaseNotesDialog()
	d.SetSize(100, 40)
	d.Show("2.15.0")
	if !strings.Contains(strip(d.View()), "Loading") {
		t.Error("expected a loading state before data arrives")
	}

	d.SetData(nil, errTest)
	if !strings.Contains(strip(d.View()), "Couldn't load") {
		t.Error("expected an error state when load fails with no data")
	}
}

var errTest = errTestType("network unreachable")

type errTestType string

func (e errTestType) Error() string { return string(e) }
