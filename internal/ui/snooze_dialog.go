package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// snoozeSelectedMsg carries the chosen deadline back to app.go. The dialog
// resolves the duration itself so the handler never re-parses.
type snoozeSelectedMsg struct {
	until time.Time
	// durationID is the analytics dimension: a preset id ("1h") or "custom".
	durationID string
}

// snoozeMaxDuration bounds the custom input. Past a month a snooze is really
// "forever", and it keeps a fat-fingered `200d` from silently burying work.
const snoozeMaxDuration = 30 * 24 * time.Hour

// SnoozeDialog picks how long to mute the row under the cursor: a preset list
// plus a free-text duration. Anchored to its row like the context menu rather
// than centered — it's a dropdown from the thing it acts on, not a takeover.
//
// Presets and the input are one surface: arrow onto a preset, or just start
// typing. Typing a valid duration takes over Enter, so the fast path (open,
// type "2d", Enter) never touches the list.
type SnoozeDialog struct {
	visible bool
	width   int
	height  int

	title  string
	cursor int
	input  textinput.Model

	// anchor mirrors ContextMenuDialog's: the sidebar column and the row to
	// hang from, plus the first y the footer owns.
	anchorX     int
	rowY        int
	bottomLimit int
}

func NewSnoozeDialog() *SnoozeDialog {
	ti := textinput.New()
	ti.Placeholder = "e.g. 15m, 3h, 2d"
	ti.CharLimit = 8
	ti.SetWidth(18)
	return &SnoozeDialog{input: ti}
}

func (d *SnoozeDialog) Show(title string) {
	d.visible = true
	d.title = title
	d.cursor = 0
	d.input.SetValue("")
	d.input.Focus()
}

func (d *SnoozeDialog) Hide() {
	d.visible = false
	d.input.Blur()
}

func (d *SnoozeDialog) IsVisible() bool { return d.visible }

func (d *SnoozeDialog) SetSize(w, h int) { d.width, d.height = w, h }

// SetAnchor positions the dropdown against its sidebar row, same contract as
// ContextMenuDialog.SetAnchor.
func (d *SnoozeDialog) SetAnchor(x, rowY, bottomLimit int) {
	d.anchorX, d.rowY, d.bottomLimit = x, rowY, bottomLimit
}

// customDuration returns the parsed custom input, if the box holds a valid one.
func (d *SnoozeDialog) customDuration() (time.Duration, bool) {
	dur, err := parseSnoozeDuration(d.input.Value())
	return dur, err == nil
}

func (d *SnoozeDialog) Update(msg tea.Msg) (*SnoozeDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	switch keyMsg.String() {
	case "esc", "ctrl+c":
		d.Hide()
		return d, nil
	case "up":
		if d.cursor > 0 {
			d.cursor--
		}
		return d, nil
	case "down":
		if d.cursor < len(SnoozeDurations)-1 {
			d.cursor++
		}
		return d, nil
	case "enter":
		// A valid custom duration wins: the user typed it on purpose, and the
		// preset cursor is only where it was left.
		if dur, ok := d.customDuration(); ok {
			until := time.Now().Add(dur)
			d.Hide()
			return d, func() tea.Msg {
				return snoozeSelectedMsg{until: until, durationID: "custom"}
			}
		}
		// A non-empty but unparseable box is a typo, not a request for the
		// highlighted preset — refuse rather than snooze for the wrong span.
		if strings.TrimSpace(d.input.Value()) != "" {
			return d, nil
		}
		sel := SnoozeDurations[d.cursor]
		until := sel.Resolve(time.Now())
		d.Hide()
		return d, func() tea.Msg {
			return snoozeSelectedMsg{until: until, durationID: sel.ID}
		}
	}

	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	return d, cmd
}

// View returns the bare box (empty when hidden); app.go composites it at
// Position, matching the context menu's overlay pattern.
func (d *SnoozeDialog) View() string {
	if !d.visible {
		return ""
	}
	now := time.Now()
	typing := strings.TrimSpace(d.input.Value()) != ""

	// Fixed width so the box doesn't resize or reflow under the cursor as the
	// verdict line swaps between the hint and a wake time. Sized so the widest
	// static line (the hint) fits on one row after DialogStyle's border and
	// padding — if it wraps, the box grows a row the moment you type.
	const contentW = 42
	boxW := contentW
	if max := d.width - 6; boxW > max {
		boxW = max
	}

	var b strings.Builder
	b.WriteString(TitleStyle.Render(ansi.Truncate(d.title, boxW, "…")))
	b.WriteString("\n\n")

	for i, dur := range SnoozeDurations {
		wake := formatSnoozeWake(dur.Resolve(now), now)
		// Pad the RAW text before styling — padding a styled string counts the
		// ANSI escape bytes and the columns come out ragged.
		row := fmt.Sprintf(" %-12s %10s ", dur.Label, wake)
		// While a custom duration is being typed it owns Enter, so the preset
		// highlight would be lying about what Enter does — drop it.
		if i == d.cursor && !typing {
			b.WriteString(" " + selTitle().Render(row))
		} else {
			b.WriteString(" " + DimStyle.Render(row))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(DimStyle.Render("or type: "))
	b.WriteString(d.input.View())
	b.WriteString("\n")

	// Third line is the live verdict on what was typed: the resolved wake time,
	// or why it won't parse. Always rendered so the box height never jumps.
	switch {
	case !typing:
		// Must fit contentW on one line, or the box grows a row and the height
		// jumps the moment the user starts typing.
		b.WriteString(DimStyle.Render("  ↑↓ pick • enter ok • esc cancel"))
	default:
		if dur, err := parseSnoozeDuration(d.input.Value()); err == nil {
			b.WriteString(PROpenStyle.Render("  → wakes " + formatSnoozeWake(now.Add(dur), now)))
		} else {
			b.WriteString(ErrorStyle.Render("  " + err.Error()))
		}
	}

	return DialogStyle.Width(boxW).Render(b.String())
}

// Position places the dropdown below its row, flipping above near the footer —
// same rules as ContextMenuDialog.Position.
func (d *SnoozeDialog) Position(boxW, boxH int) (int, int) {
	x := d.anchorX
	if x+boxW > d.width {
		x = d.width - boxW
	}
	if x < 0 {
		x = 0
	}
	y := d.rowY + 1
	if y+boxH > d.bottomLimit {
		y = d.rowY - boxH
	}
	if y < 0 {
		y = 0
	}
	return x, y
}

// parseSnoozeDuration reads a single-unit duration: a positive integer followed
// by m, h, or d. Deliberately NOT time.ParseDuration — that accepts combos
// ("1h30m"), sub-minute units ("5s"), and has no notion of days, so its error
// messages would describe a syntax we don't actually offer.
//
// The returned errors are user-facing: they render straight into the dialog.
func parseSnoozeDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("enter a duration")
	}

	unit := s[len(s)-1]
	var mult time.Duration
	switch unit {
	case 'm':
		mult = time.Minute
	case 'h':
		mult = time.Hour
	case 'd':
		mult = 24 * time.Hour
	default:
		return 0, fmt.Errorf("end with m, h or d")
	}

	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil {
		return 0, fmt.Errorf("try 15m, 3h or 2d")
	}
	if n <= 0 {
		return 0, fmt.Errorf("must be more than 0")
	}

	total := time.Duration(n) * mult
	if total > snoozeMaxDuration {
		return 0, fmt.Errorf("max 30d")
	}
	return total, nil
}
