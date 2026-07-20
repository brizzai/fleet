package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

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
// Presets and the input are one surface: the input is simply the row below the
// last preset, so ↑↓ walks the whole dialog and Enter always acts on whatever
// carries the highlight. Typing from a preset row jumps to the input and keeps
// the keystroke, so the fast path (open, type "2d", Enter) never needs arrows.
type SnoozeDialog struct {
	visible bool
	width   int
	height  int

	title string
	// focus indexes SnoozeDurations, with len(SnoozeDurations) meaning the
	// custom-duration input — i.e. the input is just the row below the last
	// preset, so ↑↓ walks the whole dialog and the highlight always says what
	// Enter will do.
	focus int
	input textinput.Model

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

// snoozeInputRow is the focus index of the custom-duration input: one past the
// last preset.
func snoozeInputRow() int { return len(SnoozeDurations) }

// inputFocused reports whether the custom-duration box owns the keyboard.
func (d *SnoozeDialog) inputFocused() bool { return d.focus == snoozeInputRow() }

// setFocus moves the highlight and keeps the text input's own focus (and so its
// cursor) in step, so the caret only blinks where the highlight is.
func (d *SnoozeDialog) setFocus(i int) {
	if i < 0 {
		i = 0
	}
	if i > snoozeInputRow() {
		i = snoozeInputRow()
	}
	d.focus = i
	if d.inputFocused() {
		d.input.Focus()
	} else {
		d.input.Blur()
	}
}

func (d *SnoozeDialog) Show(title string) {
	d.visible = true
	d.title = title
	d.input.SetValue("")
	d.setFocus(0)
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

func (d *SnoozeDialog) Update(msg tea.Msg) (*SnoozeDialog, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	key := keyMsg.String()
	switch key {
	case "esc", "ctrl+c":
		d.Hide()
		return d, nil
	case "up", "shift+tab":
		d.setFocus(d.focus - 1)
		return d, nil
	case "down", "tab":
		d.setFocus(d.focus + 1)
		return d, nil
	case "enter":
		// Enter acts on whatever is highlighted — the highlight is the promise.
		if !d.inputFocused() {
			sel := SnoozeDurations[d.focus]
			until := sel.Resolve(time.Now())
			d.Hide()
			return d, func() tea.Msg {
				return snoozeSelectedMsg{until: until, durationID: sel.ID}
			}
		}
		dur, err := parseSnoozeDuration(d.input.Value())
		if err != nil {
			// Stay open so the typo is fixable; View renders the reason. Never
			// fall through to a preset — that would snooze for a span the user
			// never asked for.
			return d, nil
		}
		until := time.Now().Add(dur)
		d.Hide()
		return d, func() tea.Msg {
			return snoozeSelectedMsg{until: until, durationID: "custom"}
		}
	}

	// Typing from a preset row jumps to the input and keeps the keystroke, so
	// the fast path (open, type "2d", enter) never needs the arrow keys.
	if !d.inputFocused() && isTypingKey(key) {
		d.setFocus(snoozeInputRow())
	}
	if !d.inputFocused() {
		return d, nil
	}

	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	return d, cmd
}

// isTypingKey reports whether a key press is someone starting to type a
// duration (a bare printable rune) rather than navigation or a chord.
func isTypingKey(key string) bool {
	r := []rune(key)
	return len(r) == 1 && unicode.IsPrint(r[0]) && r[0] != ' '
}

// View returns the bare box (empty when hidden); app.go composites it at
// Position, matching the context menu's overlay pattern.
func (d *SnoozeDialog) View() string {
	if !d.visible {
		return ""
	}
	now := time.Now()

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
		if i == d.focus {
			b.WriteString(" " + selTitle().Render(row))
		} else {
			b.WriteString(" " + DimStyle.Render(row))
		}
		b.WriteString("\n")
	}

	// The input is the row below the presets and carries the same highlight, so
	// the eye tracks one moving selection through the whole dialog.
	b.WriteString("\n")
	label := " or type: "
	if d.inputFocused() {
		b.WriteString(" " + selTitle().Render(label))
	} else {
		b.WriteString(" " + DimStyle.Render(label))
	}
	b.WriteString(d.input.View())
	b.WriteString("\n")

	// Last line is the live verdict on what was typed — the resolved wake time,
	// or why it won't parse. Exactly one line in every state, or the box height
	// jumps mid-keystroke. Each string must fit contentW without wrapping.
	switch {
	case !d.inputFocused():
		b.WriteString(DimStyle.Render("  ↑↓ pick • enter ok • esc cancel"))
	case strings.TrimSpace(d.input.Value()) == "":
		b.WriteString(DimStyle.Render("  type a duration • ↑ for presets"))
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
