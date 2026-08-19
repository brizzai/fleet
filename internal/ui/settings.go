package ui

import (
	"fmt"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/editor"
)

// settingsClosedMsg is sent when the settings dialog closes.
type settingsClosedMsg struct{}

var (
	// Only the editors this machine can actually launch — a preset the user picks
	// must not fail with "executable not found" (see internal/editor). Lazy: the
	// PATH walk and /Applications scan behind it would otherwise run at package
	// init in *every* fleet process, including each one-shot `fleet hook-handler`
	// spawned per hook event, for a value only this dialog reads.
	editorPresets = sync.OnceValue(editor.Available)
	tickPresets   = []int{1, 2, 3, 5, 10}
	// Bounded by config.DrawerHeightMax (the UI's hard body cap) — taller can't render.
	drawerHeightPresets = []int{6, 8, 10, 12, 14}
	statusStyleSet      = []string{"icon", "bar"}
	chevronStyleSet     = []string{"triangle", "plusminus"}
	densitySet          = []string{"normal", "compact"}
	enterModeSet        = []string{"attach", "split"}
	defaultAgentSet     = []string{"claude", "codex", "opencode"}
	accountStrategySet  = claudeaccount.Strategies
	telemetryModeSet    = []string{config.TelemetryFull, config.TelemetryMinimal, config.TelemetryOff}
	suspendModeSet      = []string{config.SuspendOff, config.SuspendLight, config.SuspendBalanced, config.SuspendAggressive}
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
	valueW    func() int                       // widest *possible* display width of value; stable across cfg (nil for subheaders)
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
	detailWByCat   []int // natural detail-column width per category (cached; depends only on static labels/presets)
	categoryCursor int
	rowCursor      int
	focus          settingsFocus
}

// NewSettingsDialog creates a settings dialog.
func NewSettingsDialog(cfg *config.Config) *SettingsDialog {
	d := &SettingsDialog{cfg: cfg, categories: buildSettingsCategories()}
	// The natural detail width per category depends only on static label text and
	// the fixed preset sets (via each row's widest *possible* value), so compute
	// it once here rather than on every View() frame.
	d.detailWByCat = make([]int, len(d.categories))
	for i, c := range d.categories {
		d.detailWByCat[i] = categoryContentWidth(c)
	}
	return d
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

// labelColWidth returns the width of the widest interactive (non-subheader)
// label in rows. The detail pane sizes its label column to this so labels never
// wrap (wrapping inflates the rendered line count past the fixed block height,
// which would silently truncate the rows below).
func labelColWidth(rows []settingRow) int {
	w := 0
	for _, r := range rows {
		if r.subheader {
			continue
		}
		if lw := lipgloss.Width(r.label); lw > w {
			w = lw
		}
	}
	return w
}

// rowChrome is the non-label, non-value width of a detail row:
// "  " + "◂" + " " + value + " " + "▸" = 6 cells.
const rowChrome = 6

// categoryContentWidth is the width a category needs to render each of its rows
// on a single line: widest label + chrome + widest *possible* value. It uses the
// widest possible value (not the current one) so the modal width stays constant
// as the user cycles values, and reads no live config — only static labels and
// preset sets — so it's safe to cache.
func categoryContentWidth(c settingsCategory) int {
	labelW := labelColWidth(c.rows)
	maxVal := 0
	for _, r := range c.rows {
		if r.subheader || r.valueW == nil {
			continue
		}
		if vw := r.valueW(); vw > maxVal {
			maxVal = vw
		}
	}
	return labelW + rowChrome + maxVal
}

// maxStrW returns the widest rendered width among ss.
func maxStrW(ss []string) int {
	w := 0
	for _, s := range ss {
		if x := lipgloss.Width(s); x > w {
			w = x
		}
	}
	return w
}

// maxIntStrW returns the widest decimal-rendered width among ns.
func maxIntStrW(ns []int) int {
	w := 0
	for _, n := range ns {
		if x := len(fmt.Sprintf("%d", n)); x > w {
			w = x
		}
	}
	return w
}

// themeValueW is the widest theme display name.
func themeValueW() int {
	w := 0
	for _, p := range BuiltinPalettes {
		if x := lipgloss.Width(PaletteDisplayName(p.Name)); x > w {
			w = x
		}
	}
	return w
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

	// Size the detail column to the current category's widest row so labels render
	// on one line (no wrap → no truncation of the rows below). Per-category so the
	// smaller Appearance column doesn't inherit Behavior's long labels and squeeze
	// out the live preview. It still flexes down to fit narrow panes; the floor
	// keeps values readable. detailW + railW + ruleW never exceeds the content
	// area, so the box stays within d.width.
	detailW := d.detailWByCat[d.categoryCursor]
	if fit := d.width - chrome - railW - ruleW; fit < detailW {
		detailW = fit
	}
	if detailW < 20 {
		detailW = 20
	}

	rail := d.renderRail()
	detail := d.renderDetail(detailW)
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
			style = SelectionPill(true)
			label = " " + label
		case selected:
			style = SelectionPill(false)
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
// detailW is the (possibly terminal-clamped) column width.
func (d *SettingsDialog) renderDetail(detailW int) string {
	var b strings.Builder
	rows := d.curRows()
	labelW := labelColWidth(rows)
	// Narrow-pane clamp: if detailW was clamped below its natural width, shrink the
	// label column so a minimum value width survives (rather than the long label
	// eating the row and clipping the value). Over-long labels are then truncated
	// with … — never wrapped, which would re-inflate the line count past blockH and
	// truncate the rows below.
	const minVal = 4
	if maxLabel := detailW - rowChrome - minVal; maxLabel > 0 && labelW > maxLabel {
		labelW = maxLabel
	}
	for i, r := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		if r.subheader {
			b.WriteString(lipgloss.NewStyle().Foreground(ColorTextDim).Bold(true).Render(r.label))
			continue
		}

		selected := d.focus == focusDetail && i == d.rowCursor
		labelStyle := lipgloss.NewStyle().Width(labelW).Align(lipgloss.Left)
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
		line := labelStyle.Render(truncLabel(r.label, labelW)) + "  " +
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

// truncLabel truncates s to at most w display columns, appending … when it cuts,
// so a clamped label stays on one line. (Letting lipgloss wrap a too-wide label
// would re-inflate the rendered line count past the fixed block height and
// truncate the rows below — the very bug this dialog's sizing exists to avoid.)
func truncLabel(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	target := w - 1 // leave a column for the ellipsis
	var b strings.Builder
	cur := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if cur+rw > target {
			break
		}
		b.WriteRune(r)
		cur += rw
	}
	b.WriteRune('…')
	return b.String()
}

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
			value:  func(c *config.Config) string { return onOff(get(c)) },
			valueW: func() int { return maxStrW([]string{"On", "Off"}) },
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
				valueW: themeValueW,
				cycle:  cycleTheme,
			},
			{
				label:  "Status style",
				value:  func(c *config.Config) string { return c.GetStatusIndicator() },
				valueW: func() int { return maxStrW(statusStyleSet) },
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
			// Self-hides below two accounts, so this only matters once a second
			// subscription is added.
			withLabel("Account usage", toggle(
				(*config.Config).IsShowAccountUsage,
				func(c *config.Config, v bool) { c.ShowAccountUsage = &v }, true)),
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
				label:  "Chevron",
				value:  func(c *config.Config) string { return c.GetChevronStyle() },
				valueW: func() int { return maxStrW(chevronStyleSet) },
				cycle: func(d *SettingsDialog, dir int) {
					d.cfg.ChevronStyle = cycleString(d.cfg.GetChevronStyle(), chevronStyleSet, dir)
					ApplyDisplayConfig(d.cfg)
				},
			},
			{
				label:  "Density",
				value:  func(c *config.Config) string { return c.GetSidebarDensity() },
				valueW: func() int { return maxStrW(densitySet) },
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
				label:  "Editor",
				value:  func(c *config.Config) string { return c.GetEditor() },
				valueW: func() int { return maxStrW(editorPresets()) },
				cycle: func(d *SettingsDialog, dir int) {
					// The presets hold only what this machine can launch, so a config
					// synced from another Mac may name an editor that isn't installed
					// here. cycleString treats an absent value as index 0, which would
					// overwrite it on the first arrow with no way back — so keep it in
					// the ring and step from it instead.
					cur := d.cfg.GetEditor()
					presets := editorPresets()
					if indexOf(presets, cur) < 0 {
						presets = append([]string{cur}, presets...)
					}
					d.cfg.Editor = cycleString(cur, presets, dir)
				},
			},
			{
				label:  "Tick (sec)",
				value:  func(c *config.Config) string { return fmt.Sprintf("%d", c.TickIntervalSec) },
				valueW: func() int { return maxIntStrW(tickPresets) },
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
			withLabel("Confirm before restart", toggle(
				(*config.Config).IsConfirmBeforeRestartEnabled,
				func(c *config.Config, v bool) { c.ConfirmBeforeRestart = &v }, false)),
			{
				label:  "Drawer height (rows)",
				value:  func(c *config.Config) string { return fmt.Sprintf("%d", c.GetDrawerHeight()) },
				valueW: func() int { return maxIntStrW(drawerHeightPresets) },
				cycle: func(d *SettingsDialog, dir int) {
					idx := indexOfInt(drawerHeightPresets, d.cfg.GetDrawerHeight())
					if idx < 0 {
						idx = indexOfInt(drawerHeightPresets, config.DrawerHeightDefault)
					}
					d.cfg.DrawerHeight = drawerHeightPresets[(idx+dir+len(drawerHeightPresets))%len(drawerHeightPresets)]
				},
			},
			withLabel("Origin forget removes worktrees", toggle(
				(*config.Config).GetOriginDeleteRemovesWorktrees,
				func(c *config.Config, v bool) { c.OriginDeleteRemovesWorktrees = &v }, false)),
			{
				label:  "Enter mode",
				value:  func(c *config.Config) string { return c.GetEnterMode() },
				valueW: func() int { return maxStrW(enterModeSet) },
				cycle: func(d *SettingsDialog, dir int) {
					d.cfg.EnterMode = cycleString(d.cfg.GetEnterMode(), enterModeSet, dir)
				},
			},
			{
				label: "Telemetry",
				value: func(c *config.Config) string {
					switch c.GetTelemetryMode() {
					case config.TelemetryMinimal:
						return "Minimal (anon)"
					case config.TelemetryOff:
						return "Off"
					default:
						return "Full"
					}
				},
				cycle: func(d *SettingsDialog, dir int) {
					d.cfg.TelemetryMode = cycleString(d.cfg.GetTelemetryMode(), telemetryModeSet, dir)
					d.cfg.Telemetry = nil // supersede any legacy bool
				},
			},
			{
				label: "Default agent",
				value: func(c *config.Config) string {
					switch c.GetDefaultAgent() {
					case "codex":
						return "Codex"
					case "opencode":
						return "OpenCode"
					default:
						return "Claude"
					}
				},
				valueW: func() int { return maxStrW([]string{"Claude", "Codex", "OpenCode"}) },
				cycle: func(d *SettingsDialog, dir int) {
					d.cfg.DefaultAgent = cycleString(d.cfg.GetDefaultAgent(), defaultAgentSet, dir)
				},
			},
			{
				// Which Claude subscription a new session runs under. Only
				// meaningful once a second account is added (Ctrl+K → Manage
				// Claude Accounts); with one account every mode picks it.
				label: "Claude account",
				value: func(c *config.Config) string {
					return claudeaccount.StrategyLabel(c.GetAccountStrategy())
				},
				valueW: func() int { return maxStrW(claudeaccount.StrategyLabels()) },
				cycle: func(d *SettingsDialog, dir int) {
					d.cfg.AccountStrategy = cycleString(d.cfg.GetAccountStrategy(), accountStrategySet, dir)
				},
			},
			{
				// Auto-hibernate idle sessions under memory pressure to keep the
				// machine (and the shared tmux server) from being OOM-killed.
				// Off / Light (critical-pressure safety net) / Balanced / Aggressive.
				label: "Idle-session suspend",
				value: func(c *config.Config) string {
					switch c.GetSessionSuspendMode() {
					case config.SuspendOff:
						return "Off"
					case config.SuspendBalanced:
						return "Balanced"
					case config.SuspendAggressive:
						return "Aggressive"
					default:
						return "Light"
					}
				},
				valueW: func() int { return maxStrW([]string{"Off", "Light", "Balanced", "Aggressive"}) },
				cycle: func(d *SettingsDialog, dir int) {
					d.cfg.SessionSuspendMode = cycleString(d.cfg.GetSessionSuspendMode(), suspendModeSet, dir)
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
