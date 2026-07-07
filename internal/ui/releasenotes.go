package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/releasenotes"
	"github.com/charmbracelet/x/ansi"
)

// ReleaseNotesDialog is a scrollable changelog: every release, newest-first,
// with the running version marked INSTALLED and anything newer grouped up top as
// "update to get these". Data is loaded async (see loadReleaseNotes in app.go);
// the dialog shows a loading state until SetData arrives.
type ReleaseNotesDialog struct {
	visible   bool
	width     int
	height    int
	scroll    int
	installed string // normalized running version, e.g. "2.15.0" or "dev"
	releases  []releasenotes.Release
	loading   bool
	err       error
}

// NewReleaseNotesDialog creates an empty release-notes dialog.
func NewReleaseNotesDialog() *ReleaseNotesDialog {
	return &ReleaseNotesDialog{}
}

// Show opens the dialog in its loading state; installedVersion is the running
// build's version (raw — it's normalized here for matching).
func (d *ReleaseNotesDialog) Show(installedVersion string) {
	d.visible = true
	d.loading = true
	d.err = nil
	d.releases = nil
	d.scroll = 0
	d.installed = releasenotes.NormalizeVersion(installedVersion)
}

// SetData installs the loaded releases (or the load error) and stops loading.
func (d *ReleaseNotesDialog) SetData(rs []releasenotes.Release, err error) {
	d.loading = false
	d.releases = rs
	d.err = err
	d.scroll = clampInt(d.scroll, 0, d.maxScroll())
}

func (d *ReleaseNotesDialog) Hide()           { d.visible = false }
func (d *ReleaseNotesDialog) IsVisible() bool { return d.visible }

// SetSize records the terminal size and re-clamps scroll so View stays read-only.
func (d *ReleaseNotesDialog) SetSize(w, h int) {
	d.width, d.height = w, h
	d.scroll = clampInt(d.scroll, 0, d.maxScroll())
}

// Update scrolls when the notes overflow; esc/q close. Keys are ignored while
// loading. Unlike the help overlay, stray keys don't close — this dialog scrolls
// a lot, so closing is deliberate (esc/q).
func (d *ReleaseNotesDialog) Update(msg tea.Msg) (*ReleaseNotesDialog, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}
	if d.loading {
		if s := key.String(); s == "esc" || s == "q" {
			d.Hide()
		}
		return d, nil
	}
	maxScroll := d.maxScroll()
	switch key.String() {
	case "esc", "q":
		d.Hide()
		return d, nil
	case "up", "k":
		d.scroll--
	case "down", "j":
		d.scroll++
	case "pgup":
		d.scroll -= d.pageStep()
	case "pgdown":
		d.scroll += d.pageStep()
	case "home", "g":
		d.scroll = 0
	case "end", "G":
		d.scroll = maxScroll
	}
	d.scroll = clampInt(d.scroll, 0, maxScroll)
	return d, nil
}

// --- geometry ---------------------------------------------------------------

// dialogWidth targets 72 cols, clamped to the screen with a sane minimum.
func (d *ReleaseNotesDialog) dialogWidth() int {
	return clampInt(72, 48, max(48, d.width-4))
}

// innerWidth is the content width inside the border+padding frame.
func (d *ReleaseNotesDialog) innerWidth() int {
	return max(20, d.dialogWidth()-DialogStyle.GetHorizontalFrameSize())
}

// visibleRows is how many content rows fit in the scroll window (leaving room
// for the title, hint, and the two ⋮ indicator lines).
func (d *ReleaseNotesDialog) visibleRows() int {
	const chrome = 2 /*title+blank*/ + 2 /*blank+hint*/ + 2 /*⋮ above/below*/
	return max(1, d.height-DialogStyle.GetVerticalFrameSize()-chrome)
}

func (d *ReleaseNotesDialog) maxScroll() int {
	return max(0, len(d.contentLines())-d.visibleRows())
}

func (d *ReleaseNotesDialog) pageStep() int {
	return max(1, d.visibleRows()-1)
}

// --- rendering --------------------------------------------------------------

// View renders the dialog centered over the screen.
func (d *ReleaseNotesDialog) View() string {
	if d.loading {
		return d.box(TitleStyle.Render("Release Notes") + "\n\n" + DimStyle.Render("Loading release notes…"))
	}
	if d.err != nil {
		msg := TitleStyle.Render("Release Notes") + "\n\n" +
			ErrorStyle.Render("Couldn't load release notes.") + "\n" +
			DimStyle.Render(ansi.Truncate(d.err.Error(), d.innerWidth(), "…"))
		return d.box(msg)
	}

	content := d.contentLines()
	var lines []string
	lines = append(lines, TitleStyle.Render("Release Notes"), "")

	visible := d.visibleRows()
	if len(content) <= visible {
		lines = append(lines, content...)
	} else {
		scroll := clampInt(d.scroll, 0, len(content)-visible)
		above, below := scroll, len(content)-visible-scroll
		if above > 0 {
			lines = append(lines, DimStyle.Render(fmt.Sprintf("⋮ +%d above", above)))
		} else {
			lines = append(lines, "")
		}
		lines = append(lines, content[scroll:scroll+visible]...)
		if below > 0 {
			lines = append(lines, DimStyle.Render(fmt.Sprintf("⋮ +%d below", below)))
		} else {
			lines = append(lines, "")
		}
	}

	hint := "↑↓ scroll · esc close"
	lines = append(lines, "", DimStyle.Render(hint))
	return d.box(strings.Join(lines, "\n"))
}

// box wraps content in the standard dialog frame at a stable width and centers it.
func (d *ReleaseNotesDialog) box(content string) string {
	b := DialogStyle.Width(d.dialogWidth()).Render(content)
	b = lipgloss.NewStyle().MaxWidth(d.width).MaxHeight(d.height).Render(b)
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, b)
}

// contentLines builds the full (unscrolled) list of rendered content rows. Every
// line is guaranteed to fit innerWidth so it never wraps and throws off the
// scroll math.
func (d *ReleaseNotesDialog) contentLines() []string {
	inner := d.innerWidth()
	if len(d.releases) == 0 {
		return []string{DimStyle.Render("No releases found.")}
	}

	var out []string
	add := func(s string) { out = append(out, ansi.Truncate(s, inner, "…")) }

	devBuild := d.installed == "dev" || d.installed == ""
	if devBuild {
		add(DimStyle.Render("Running a dev build — no installed release to match."))
		add("")
	}

	newerEmitted := false
	installedSeen := false
	for _, r := range d.releases {
		cmp := 0
		if !devBuild {
			cmp = releasenotes.CompareVersions(r.Version, d.installed)
		}
		switch {
		case !devBuild && cmp > 0 && !newerEmitted:
			add(lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("◆ NEWER — update to get these"))
			add("")
			newerEmitted = true
		case !devBuild && cmp <= 0 && newerEmitted && !installedSeen:
			// Transition out of the newer block into installed/past.
			installedSeen = true
		}
		d.appendRelease(&out, r, !devBuild && cmp == 0, inner)
		add("")
	}
	// Trim a trailing blank for a tidy bottom.
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// appendRelease renders one release (version line + sections + bullets) into out.
func (d *ReleaseNotesDialog) appendRelease(out *[]string, r releasenotes.Release, installed bool, inner int) {
	add := func(s string) { *out = append(*out, ansi.Truncate(s, inner, "…")) }

	verStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorText)
	if installed {
		verStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	}
	header := verStyle.Render("v" + r.Version)
	if r.Date != "" {
		header += "  " + DimStyle.Render(r.Date)
	}
	if r.Prerelease {
		header += "  " + DimStyle.Render("(prerelease)")
	}
	if installed {
		badge := lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent).Bold(true).Padding(0, 1).Render("INSTALLED")
		header += "  " + badge
	}
	add(header)

	if len(r.Sections) == 0 {
		add(DimStyle.Render("  (no notes)"))
		return
	}
	textWidth := max(8, inner-4) // "  • " / "    " indent
	for _, sec := range r.Sections {
		if sec.Title != "" {
			add("  " + sectionTitleStyle(sec.Title).Render(sec.Title))
		}
		for _, bullet := range sec.Bullets {
			wrapped := strings.Split(ansi.Wordwrap(bullet, textWidth, ""), "\n")
			for i, ln := range wrapped {
				prefix := "    "
				if i == 0 {
					prefix = "  " + lipgloss.NewStyle().Foreground(ColorAccent).Render("•") + " "
				}
				add(prefix + HelpDescStyle.Render(ln))
			}
		}
	}
}

// sectionTitleStyle colors a changelog section label by type, muted so the
// bullets stay the focus.
func sectionTitleStyle(title string) lipgloss.Style {
	c := ColorTextDim
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "added":
		c = ColorGreen
	case "improved":
		c = ColorBlue
	case "changed":
		c = ColorYellow
	case "fixed":
		c = ColorOrange
	case "removed", "deprecated":
		c = ColorGray
	case "security":
		c = ColorRed
	}
	return lipgloss.NewStyle().Bold(true).Foreground(c)
}
