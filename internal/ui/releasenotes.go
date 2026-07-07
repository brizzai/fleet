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

// Fixed chrome around the scrollable content region. The two indicator slots are
// always rendered (blank when there's nothing hidden), so one of them doubles as
// the gap under the header rule — no stacked blank lines.
const (
	rnHeaderLines    = 2 // title row + rule
	rnFooterLines    = 2 // blank + hint
	rnIndicatorLines = 2 // ⋮ above / ⋮ below
)

// dialogWidth uses most of the terminal width (~80%) so the notes get a wide
// reading pane, clamped so it always fits and never gets uselessly narrow.
func (d *ReleaseNotesDialog) dialogWidth() int {
	maxW := max(20, d.width-4)
	return clampInt(d.width*4/5, min(72, maxW), maxW)
}

// innerWidth is the content width inside the border+padding frame.
func (d *ReleaseNotesDialog) innerWidth() int {
	return max(20, d.dialogWidth()-DialogStyle.GetHorizontalFrameSize())
}

// scrollGeometry returns the content lines plus the scroll window sizing,
// computed once so Update (paging/clamping) and View (rendering) never disagree.
func (d *ReleaseNotesDialog) scrollGeometry() (content []string, visible, maxScroll int) {
	content = d.contentLines()
	availH := max(1, d.height-DialogStyle.GetVerticalFrameSize()-rnHeaderLines-rnFooterLines-rnIndicatorLines)
	if len(content) <= availH {
		return content, len(content), 0
	}
	return content, availH, len(content) - availH
}

func (d *ReleaseNotesDialog) maxScroll() int {
	_, _, m := d.scrollGeometry()
	return m
}

func (d *ReleaseNotesDialog) pageStep() int {
	_, visible, _ := d.scrollGeometry()
	return max(1, visible-1)
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

	inner := d.innerWidth()
	content, visible, maxScroll := d.scrollGeometry()
	scroll := clampInt(d.scroll, 0, maxScroll)
	above, below := scroll, maxScroll-scroll

	// Header, then the always-present "above" slot (blank at the top, so it also
	// serves as the gap under the rule), the content window, the "below" slot,
	// and the footer hint.
	lines := []string{d.titleRow(inner), d.rule(inner), indicator(above, "above")}
	lines = append(lines, content[scroll:scroll+visible]...)
	lines = append(lines, indicator(below, "below"))
	lines = append(lines, "", DimStyle.Render("↑↓ scroll · esc close"))
	return d.box(strings.Join(lines, "\n"))
}

// titleRow is the header: "Release Notes" on the left, release count on the right.
func (d *ReleaseNotesDialog) titleRow(inner int) string {
	right := ""
	if n := len(d.releases); n > 0 {
		right = DimStyle.Render(fmt.Sprintf("%d releases", n))
	}
	return padLR(TitleStyle.Render("Release Notes"), right, inner)
}

// rule renders a full-width dim horizontal divider.
func (d *ReleaseNotesDialog) rule(w int) string {
	return lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("─", w))
}

// indicator renders a dim "⋮ +N above/below" line (blank when count is zero).
func indicator(n int, where string) string {
	if n <= 0 {
		return ""
	}
	return DimStyle.Render(fmt.Sprintf("⋮ +%d %s", n, where))
}

// box wraps content in the standard dialog frame at a stable width and centers it.
func (d *ReleaseNotesDialog) box(content string) string {
	b := DialogStyle.Width(d.dialogWidth()).Render(content)
	b = lipgloss.NewStyle().MaxWidth(d.width).MaxHeight(d.height).Render(b)
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, b)
}

// contentLines builds the full (unscrolled) list of rendered content rows.
// Version-header and divider rows are pre-fit to innerWidth; every other line is
// truncated to it, so no line ever wraps and throws off the scroll math.
func (d *ReleaseNotesDialog) contentLines() []string {
	inner := d.innerWidth()
	if len(d.releases) == 0 {
		return []string{DimStyle.Render("No releases found.")}
	}

	var out []string
	fit := func(s string) { out = append(out, ansi.Truncate(s, inner, "…")) }
	raw := func(s string) { out = append(out, s) } // pre-fit rows (rules, padded headers)

	devBuild := d.installed == "dev" || d.installed == ""
	if devBuild {
		fit(DimStyle.Render("· Running a dev build — no installed release to match."))
		raw("")
	}

	newerEmitted := false
	sep := false // emit a divider before the next release?
	for _, r := range d.releases {
		cmp := 0
		if !devBuild {
			cmp = releasenotes.CompareVersions(r.Version, d.installed)
		}
		// Gate the INSTALLED marker on !Prerelease: NormalizeVersion strips the
		// pre-release suffix, so a prerelease and its GA collapse to the same
		// version — without this both would claim the badge.
		installed := !devBuild && cmp == 0 && !r.Prerelease

		if !devBuild && cmp > 0 && !newerEmitted {
			fit(lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("◆ NEWER — update to get these"))
			raw("")
			newerEmitted = true
			sep = false // the header already separates the first newer release
		} else if sep {
			raw("")
			raw(d.rule(inner))
			raw("")
		}
		d.appendRelease(&out, r, installed, inner)
		sep = true
	}
	return out
}

// appendRelease renders one release (header row + sections + bullets) into out.
func (d *ReleaseNotesDialog) appendRelease(out *[]string, r releasenotes.Release, installed bool, inner int) {
	fit := func(s string) { *out = append(*out, ansi.Truncate(s, inner, "…")) }

	verStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorText)
	if installed {
		verStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	}
	left := verStyle.Render("v" + r.Version)
	if installed {
		left += "  " + lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent).Bold(true).Padding(0, 1).Render("INSTALLED")
	}
	if r.Prerelease {
		left += "  " + lipgloss.NewStyle().Foreground(ColorYellow).Render("pre-release")
	}
	// Version left, date (+ relative age) right — a clean column down the right
	// edge. Truncate to inner so a very narrow terminal (right cluster wider than
	// inner) can't wrap it to two rows and desync the scroll window.
	right := DimStyle.Render(r.Date)
	if a := releasenotes.Ago(r.Date); a != "" {
		right += DimStyle.Render(fmt.Sprintf("  (%s)", a))
	}
	fit(padLR(left, right, inner))

	if len(r.Sections) == 0 {
		fit(DimStyle.Render("  (no notes)"))
		return
	}
	textWidth := max(8, inner-4) // "  • " / "    " indent
	dot := lipgloss.NewStyle().Foreground(ColorAccent).Render("•")
	for _, sec := range r.Sections {
		if sec.Title != "" {
			fit("  " + sectionTitleStyle(sec.Title).Render(sec.Title))
		}
		for _, bullet := range sec.Bullets {
			// Render inline markdown (`code`, **bold**) first, then wrap: ansi's
			// wrapper is ANSI-aware so it keeps the styling intact across breaks.
			wrapped := strings.Split(ansi.Wordwrap(renderInlineMarkdown(bullet), textWidth, ""), "\n")
			for i, ln := range wrapped {
				prefix := "    "
				if i == 0 {
					prefix = "  " + dot + " "
				}
				fit(prefix + ln)
			}
		}
	}
}

// renderInlineMarkdown styles a subset of inline markdown found in changelog
// bullets: `code` spans and **bold** runs. Normal text keeps the body color, so
// the returned string is fully styled and shouldn't be re-wrapped in a base
// style. Backtick/asterisk markers are ASCII, so byte scanning is multibyte-safe
// for the surrounding text.
func renderInlineMarkdown(s string) string {
	base := HelpDescStyle
	codeStyle := lipgloss.NewStyle().Foreground(ColorOrange)
	boldStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorText)

	var out, buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			out.WriteString(base.Render(buf.String()))
			buf.Reset()
		}
	}
	for i := 0; i < len(s); {
		switch {
		case s[i] == '`':
			// A code span opens with a run of N backticks and closes on the next
			// run of exactly N — so `` ` `` (double-backtick delimiters) can hold a
			// literal backtick. Fall back to literal text if there's no closer.
			n := backtickRun(s, i)
			end := findBacktickRun(s, i+n, n)
			if end < 0 {
				buf.WriteString(s[i : i+n])
				i += n
				continue
			}
			flush()
			out.WriteString(codeStyle.Render(trimCodeSpan(s[i+n : end])))
			i = end + n
		case strings.HasPrefix(s[i:], "**"):
			rest := s[i+2:]
			j := strings.Index(rest, "**")
			if j < 0 {
				buf.WriteString("**")
				i += 2
				continue
			}
			flush()
			out.WriteString(boldStyle.Render(rest[:j]))
			i = i + 2 + j + 2
		default:
			buf.WriteByte(s[i])
			i++
		}
	}
	flush()
	return out.String()
}

// backtickRun returns the number of consecutive backticks starting at i.
func backtickRun(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	return n
}

// findBacktickRun returns the start index of the next run of exactly n backticks
// at or after from, or -1 if there is none.
func findBacktickRun(s string, from, n int) int {
	for i := from; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		run := backtickRun(s, i)
		if run == n {
			return i
		}
		i += run
	}
	return -1
}

// trimCodeSpan applies CommonMark's rule: if a code span both begins and ends
// with a space and isn't all spaces, one space is trimmed from each end (so
// “ ` “ yields a literal backtick).
func trimCodeSpan(c string) string {
	if len(c) >= 2 && c[0] == ' ' && c[len(c)-1] == ' ' && strings.TrimSpace(c) != "" {
		return c[1 : len(c)-1]
	}
	return c
}

// padLR places left and right on one row padded to width, truncating left if the
// two would collide.
func padLR(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		left = ansi.Truncate(left, max(1, width-lipgloss.Width(right)-1), "…")
		gap = max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	}
	return left + strings.Repeat(" ", gap) + right
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
