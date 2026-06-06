package ui

import (
	"fmt"
	"strings"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// settingsClosedMsg is sent when the settings dialog closes.
type settingsClosedMsg struct{}

var (
	editorPresets   = []string{"code", "cursor", "vim", "nvim", "nano", "emacs", "zed"}
	tickPresets     = []int{1, 2, 3, 5, 10}
	statusStyleSet  = []string{"icon", "bar"}
	chevronStyleSet = []string{"triangle", "plusminus"}
	densitySet      = []string{"normal", "compact"}
	enterModeSet    = []string{"attach", "split"}
	defaultAgentSet = []string{"claude", "codex"}
)

// settingsFocus tracks which pane (category rail or detail list) has the cursor.
type settingsFocus int

const (
	focusCategories settingsFocus = iota
	focusDetail
)

// settingRow is one line in a category's detail pane. A subheader row is a
// non-interactive group label that cursor navigation skips.
type settingRow struct {
	label     string
	subheader bool
	value     func(c *config.Config) string    // display value (interactive rows only)
	cycle     func(d *SettingsDialog, dir int) // mutate cfg + live-apply (interactive rows only)
}

// settingsCategory is a left-rail entry with its detail rows. showPreview marks
// the category that renders the live sidebar preview alongside its settings.
type settingsCategory struct {
	name        string
	showPreview bool
	rows        []settingRow
}

// SettingsDialog provides a master-detail UI for configuring fleet settings:
// categories on the left, the selected category's settings on the right, and a
// live mock-sidebar preview for the Appearance category.
type SettingsDialog struct {
	visible        bool
	width          int
	height         int
	cfg            *config.Config
	origTheme      string
	categories     []settingsCategory
	categoryCursor int
	rowCursor      int
	focus          settingsFocus
}

// NewSettingsDialog creates a settings dialog.
func NewSettingsDialog(cfg *config.Config) *SettingsDialog {
	return &SettingsDialog{cfg: cfg, categories: buildSettingsCategories()}
}

func (d *SettingsDialog) Show() {
	d.visible = true
	d.origTheme = d.cfg.Theme
	d.categoryCursor = 0
	d.focus = focusCategories
	d.rowCursor = d.firstSelectableRow(0)
}

func (d *SettingsDialog) Hide()           { d.visible = false }
func (d *SettingsDialog) IsVisible() bool { return d.visible }
func (d *SettingsDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

func (d *SettingsDialog) curRows() []settingRow { return d.categories[d.categoryCursor].rows }

// maxBlockHeight is the tallest the detail/rail/preview block needs to be across
// every category. Using a single constant keeps the modal a fixed height as the
// user switches categories.
func (d *SettingsDialog) maxBlockHeight() int {
	h := len(d.categories) // rail height
	for _, c := range d.categories {
		if len(c.rows) > h {
			h = len(c.rows)
		}
	}
	return h
}

// firstSelectableRow returns the first non-subheader row index of a category.
func (d *SettingsDialog) firstSelectableRow(cat int) int {
	for i, r := range d.categories[cat].rows {
		if !r.subheader {
			return i
		}
	}
	return 0
}

// nextSelectableRow steps the row cursor past subheaders in the given direction.
func (d *SettingsDialog) nextSelectableRow(cur, dir int) int {
	rows := d.curRows()
	next := cur + dir
	for next >= 0 && next < len(rows) {
		if !rows[next].subheader {
			return next
		}
		next += dir
	}
	return cur
}

// Update handles key events for the settings dialog.
func (d *SettingsDialog) Update(msg tea.Msg) (*SettingsDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	switch keyMsg.String() {
	case "esc", "q":
		_ = d.cfg.Save()
		d.Hide()
		return d, func() tea.Msg { return settingsClosedMsg{} }

	case "tab", "shift+tab":
		if d.focus == focusCategories {
			d.focus = focusDetail
			d.rowCursor = d.firstSelectableRow(d.categoryCursor)
		} else {
			d.focus = focusCategories
		}

	case "j", "down":
		if d.focus == focusCategories {
			d.categoryCursor = (d.categoryCursor + 1) % len(d.categories)
			d.rowCursor = d.firstSelectableRow(d.categoryCursor)
		} else {
			d.rowCursor = d.nextSelectableRow(d.rowCursor, 1)
		}

	case "k", "up":
		if d.focus == focusCategories {
			d.categoryCursor = (d.categoryCursor + len(d.categories) - 1) % len(d.categories)
			d.rowCursor = d.firstSelectableRow(d.categoryCursor)
		} else {
			d.rowCursor = d.nextSelectableRow(d.rowCursor, -1)
		}

	case "l", "right", "enter":
		if d.focus == focusCategories {
			// Dive from the rail into the detail list.
			d.focus = focusDetail
			d.rowCursor = d.firstSelectableRow(d.categoryCursor)
		} else {
			d.cycleCurrent(1)
		}

	case "h", "left":
		if d.focus == focusDetail {
			d.cycleCurrent(-1)
		}
	}

	return d, nil
}

// cycleCurrent applies the focused detail row's cycle function.
func (d *SettingsDialog) cycleCurrent(dir int) {
	rows := d.curRows()
	if d.rowCursor < 0 || d.rowCursor >= len(rows) {
		return
	}
	r := rows[d.rowCursor]
	if r.subheader || r.cycle == nil {
		return
	}
	r.cycle(d, dir)
}

// View renders the settings dialog.
func (d *SettingsDialog) View() string {
	const (
		railW  = 14
		ruleW  = 3 // " │ " vertical separator between columns
		chrome = 6 // rounded border (2) + horizontal padding (2×2)
	)

	// The detail column flexes with the terminal so the modal fits narrow panes
	// (the old dialog clamped its width the same way); the floor keeps values
	// readable. detailW + railW + ruleW never exceeds the content area, so the
	// bordered box stays within d.width.
	detailW := 36
	if fit := d.width - chrome - railW - ruleW; fit < detailW {
		detailW = fit
	}
	if detailW < 20 {
		detailW = 20
	}

	rail := d.renderRail()
	detail := d.renderDetail()
	// Constant block height across all categories so the modal doesn't grow or
	// shrink vertically when switching between Appearance and Behavior.
	blockH := d.maxBlockHeight()

	cat := d.categories[d.categoryCursor]

	// The preview column appears only when there's room left beyond the rail,
	// both rules, and the detail column — so the modal keeps a constant size and
	// narrow terminals simply omit it (rather than overflowing).
	previewW := 0
	if avail := d.width - chrome - railW - ruleW - detailW - ruleW; avail >= 24 {
		previewW = min(avail, 44)
	}

	railCol := padBlock(rail, railW, blockH)
	detailCol := padBlock(detail, detailW, blockH)
	rule := verticalRule(blockH)

	// Fixed content width: always reserve room for the preview column when it
	// fits the terminal, even on Behavior (whose right region is just blank).
	contentW := railW + ruleW + detailW
	if previewW > 0 {
		contentW += ruleW + previewW
	}

	var body string
	if previewW > 0 && cat.showPreview {
		previewCol := padBlock(d.renderPreviewColumn(previewW, blockH), previewW, blockH)
		body = lipgloss.JoinHorizontal(lipgloss.Top, railCol, rule, detailCol, rule, previewCol)
	} else {
		body = lipgloss.JoinHorizontal(lipgloss.Top, railCol, rule, detailCol)
	}
	// Pad to the fixed content size so the bordered box never changes width.
	body = padBlock(body, contentW, blockH)

	// Controls footer.
	hint := "tab panes   ↑↓ move   ←→ change   esc save"
	controls := lipgloss.NewStyle().Foreground(ColorTextDim).Render(hint)

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	title := titleStyle.Render("Settings")

	content := title + "\n\n" + body + "\n\n" + controls

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2)
	box := boxStyle.Render(content)
	// Safety net for extreme sizes: never exceed the terminal, so a too-small
	// window truncates the modal rather than wrap-corrupting the surrounding UI.
	box = lipgloss.NewStyle().MaxWidth(d.width).MaxHeight(d.height).Render(box)

	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}

// renderRail renders the category list (left pane).
func (d *SettingsDialog) renderRail() string {
	var b strings.Builder
	for i, c := range d.categories {
		selected := i == d.categoryCursor
		label := c.name
		var style lipgloss.Style
		switch {
		case selected && d.focus == focusCategories:
			style = lipgloss.NewStyle().Bold(true).Foreground(ColorBg).Background(ColorAccent)
			label = " " + label
		case selected:
			style = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
			label = " " + label
		default:
			style = lipgloss.NewStyle().Foreground(ColorTextDim)
			label = " " + label
		}
		b.WriteString(style.Render(label))
		if i < len(d.categories)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderDetail renders the selected category's settings rows (right pane).
func (d *SettingsDialog) renderDetail() string {
	var b strings.Builder
	rows := d.curRows()
	for i, r := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		if r.subheader {
			b.WriteString(lipgloss.NewStyle().Foreground(ColorTextDim).Bold(true).Render(r.label))
			continue
		}

		selected := d.focus == focusDetail && i == d.rowCursor
		labelStyle := lipgloss.NewStyle().Width(13).Align(lipgloss.Left)
		var arrowStyle, valueStyle lipgloss.Style
		if selected {
			labelStyle = labelStyle.Foreground(ColorAccent).Bold(true)
			arrowStyle = lipgloss.NewStyle().Foreground(ColorAccent)
			valueStyle = lipgloss.NewStyle().Foreground(ColorText).Bold(true)
		} else {
			labelStyle = labelStyle.Foreground(ColorText)
			arrowStyle = lipgloss.NewStyle().Foreground(ColorTextDim)
			valueStyle = lipgloss.NewStyle().Foreground(ColorTextDim)
		}
		val := ""
		if r.value != nil {
			val = r.value(d.cfg)
		}
		line := labelStyle.Render(r.label) + "  " +
			arrowStyle.Render("◂") + " " +
			valueStyle.Render(val) + " " +
			arrowStyle.Render("▸")
		b.WriteString(line)
	}
	return b.String()
}

// renderPreviewColumn renders the live mock sidebar plus a caption + legend.
func (d *SettingsDialog) renderPreviewColumn(w, h int) string {
	caption := lipgloss.NewStyle().Foreground(ColorTextDim).Bold(true).Render("Preview")
	legend := lipgloss.NewStyle().Foreground(ColorTextDim).Render("● run ◐ wait ✕ err · ✻◇ agent")
	// caption (1) + blank (1) + sidebar + legend (1).
	sidebarH := h - 3
	if sidebarH < 4 {
		sidebarH = 4
	}
	sidebar := RenderSidebarPreview(w, sidebarH)
	return caption + "\n\n" + sidebar + "\n" + legend
}

// --- column layout helpers ---

// padBlock pads/truncates a multiline block to exactly w columns × h rows,
// reusing the shared panel-sizing helpers (ensureExactHeight/ensureExactWidth).
func padBlock(s string, w, h int) string {
	return ensureExactWidth(ensureExactHeight(s, h), w)
}

// verticalRule returns a dim vertical separator h rows tall.
func verticalRule(h int) string {
	bar := lipgloss.NewStyle().Foreground(ColorBorder).Render("│")
	lines := make([]string, h)
	for i := range lines {
		lines[i] = " " + bar + " "
	}
	return strings.Join(lines, "\n")
}

// --- category / row definitions ---

func buildSettingsCategories() []settingsCategory {
	onOff := func(b bool) string {
		if b {
			return "On"
		}
		return "Off"
	}
	toggle := func(get func(*config.Config) bool, set func(*config.Config, bool), live bool) settingRow {
		return settingRow{
			value: func(c *config.Config) string { return onOff(get(c)) },
			cycle: func(d *SettingsDialog, _ int) {
				set(d.cfg, !get(d.cfg))
				if live {
					ApplyDisplayConfig(d.cfg)
				}
			},
		}
	}

	appearance := settingsCategory{
		name:        "Appearance",
		showPreview: true,
		rows: []settingRow{
			{label: "THEME", subheader: true},
			{
				label: "Theme",
				value: func(c *config.Config) string {
					name := c.Theme
					if name == "" {
						name = DefaultPaletteName
					}
					return PaletteDisplayName(name)
				},
				cycle: cycleTheme,
			},
			{
				label: "Status style",
				value: func(c *config.Config) string { return c.GetStatusIndicator() },
				cycle: func(d *SettingsDialog, dir int) {
					d.cfg.StatusIndicator = cycleString(d.cfg.GetStatusIndicator(), statusStyleSet, dir)
					ApplyDisplayConfig(d.cfg)
				},
			},
			{label: "ICONS", subheader: true},
			withLabel("Agent icons", toggle(
				(*config.Config).IsShowAgentGlyphs,
				func(c *config.Config, v bool) { c.ShowAgentGlyphs = &v }, true)),
			withLabel("Slot badges", toggle(
				(*config.Config).IsShowSlotBadges,
				func(c *config.Config, v bool) { c.ShowSlotBadges = &v }, true)),
			{label: "GIT", subheader: true},
			withLabel("PR badges", toggle(
				(*config.Config).IsShowPRBadges,
				func(c *config.Config, v bool) { c.ShowPRBadges = &v }, true)),
			withLabel("Dirty marker", toggle(
				(*config.Config).IsShowDirtyIndicator,
				func(c *config.Config, v bool) { c.ShowDirtyIndicator = &v }, true)),
			withLabel("Status pills", toggle(
				(*config.Config).IsShowStatusPills,
				func(c *config.Config, v bool) { c.ShowStatusPills = &v }, true)),
			withLabel("Header counts", toggle(
				(*config.Config).IsShowHeaderCounts,
				func(c *config.Config, v bool) { c.ShowHeaderCounts = &v }, true)),
			{label: "LAYOUT", subheader: true},
			{
				label: "Chevron",
				value: func(c *config.Config) string { return c.GetChevronStyle() },
				cycle: func(d *SettingsDialog, dir int) {
					d.cfg.ChevronStyle = cycleString(d.cfg.GetChevronStyle(), chevronStyleSet, dir)
					ApplyDisplayConfig(d.cfg)
				},
			},
			{
				label: "Density",
				value: func(c *config.Config) string { return c.GetSidebarDensity() },
				cycle: func(d *SettingsDialog, dir int) {
					d.cfg.SidebarDensity = cycleString(d.cfg.GetSidebarDensity(), densitySet, dir)
					ApplyDisplayConfig(d.cfg)
				},
			},
		},
	}

	behavior := settingsCategory{
		name: "Behavior",
		rows: []settingRow{
			{
				label: "Editor",
				value: func(c *config.Config) string { return c.GetEditor() },
				cycle: func(d *SettingsDialog, dir int) {
					d.cfg.Editor = cycleString(d.cfg.GetEditor(), editorPresets, dir)
				},
			},
			{
				label: "Tick (sec)",
				value: func(c *config.Config) string { return fmt.Sprintf("%d", c.TickIntervalSec) },
				cycle: func(d *SettingsDialog, dir int) {
					cur := d.cfg.TickIntervalSec
					if cur <= 0 {
						cur = 2
					}
					idx := indexOfInt(tickPresets, cur)
					if idx < 0 {
						idx = 1
					}
					d.cfg.TickIntervalSec = tickPresets[(idx+dir+len(tickPresets))%len(tickPresets)]
				},
			},
			withLabel("Auto-name", toggle(
				(*config.Config).IsAutoNameEnabled,
				func(c *config.Config, v bool) { c.AutoNameSessions = &v }, false)),
			withLabel("Auto-update", toggle(
				(*config.Config).IsAutoUpdateEnabled,
				func(c *config.Config, v bool) { c.AutoUpdate = &v }, false)),
			withLabel("Copy .claude", toggle(
				(*config.Config).IsCopyClaudeSettingsEnabled,
				func(c *config.Config, v bool) { c.CopyClaudeSettings = &v }, false)),
			{
				label: "Enter mode",
				value: func(c *config.Config) string { return c.GetEnterMode() },
				cycle: func(d *SettingsDialog, dir int) {
					d.cfg.EnterMode = cycleString(d.cfg.GetEnterMode(), enterModeSet, dir)
				},
			},
			withLabel("Telemetry", toggle(
				(*config.Config).IsTelemetryEnabled,
				func(c *config.Config, v bool) { c.Telemetry = &v }, false)),
			{
				label: "Default agent",
				value: func(c *config.Config) string {
					if c.GetDefaultAgent() == "codex" {
						return "Codex"
					}
					return "Claude"
				},
				cycle: func(d *SettingsDialog, dir int) {
					d.cfg.DefaultAgent = cycleString(d.cfg.GetDefaultAgent(), defaultAgentSet, dir)
				},
			},
		},
	}

	return []settingsCategory{appearance, behavior}
}

// withLabel attaches a label to a row built by the generic toggle helper.
func withLabel(label string, r settingRow) settingRow {
	r.label = label
	return r
}

func cycleTheme(d *SettingsDialog, dir int) {
	names := make([]string, len(BuiltinPalettes))
	for i, p := range BuiltinPalettes {
		names[i] = p.Name
	}
	current := d.cfg.Theme
	if current == "" {
		current = DefaultPaletteName
	}
	idx := indexOf(names, current)
	if idx < 0 {
		idx = 0
	}
	d.cfg.Theme = names[(idx+dir+len(names))%len(names)]
	ApplyPalette(PaletteByName(d.cfg.Theme))
	analytics.Track(analytics.EventThemeChanged, map[string]interface{}{"theme": d.cfg.Theme})
}

// cycleString returns the preset reached by stepping dir from cur (wrapping).
func cycleString(cur string, presets []string, dir int) string {
	idx := indexOf(presets, cur)
	if idx < 0 {
		idx = 0
	}
	return presets[(idx+dir+len(presets))%len(presets)]
}

func indexOf(slice []string, val string) int {
	for i, s := range slice {
		if s == val {
			return i
		}
	}
	return -1
}

func indexOfInt(slice []int, val int) int {
	for i, v := range slice {
		if v == val {
			return i
		}
	}
	return -1
}
