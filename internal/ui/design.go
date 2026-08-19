package ui

import (
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
)

// Role styles: the small vocabulary every fleet surface renders selection,
// focus and mode with. See docs/design-system.md for the rules; this file is
// their implementation and the only place a background fill may be constructed.
//
// The vocabulary exists because three different facts kept borrowing each
// other's clothes:
//
//   - FOCUS      — which surface owns the keyboard. At most one per screen.
//   - SELECTION  — which row a list's cursor is on. One per list, and a list
//                  keeps its selection while another surface holds focus.
//   - MODE       — which option is currently in effect (an active tab, a
//                  chosen radio). Never focus, never selection.
//
// The command palette drew its active tab with the heaviest treatment there
// is — Bg-on-Accent, the same fill the sidebar uses for the row you are
// standing on — while the row you were actually standing on got the muted
// band. The loudest thing on screen was the least important fact, and the
// three could not be told apart. The rule that prevents a repeat is that
// weight is ordered and the order is fixed: focus outranks selection, which
// outranks mode, and a mode indicator may not fill at all.
//
// These are functions, not the package-level `var …Style` table above them in
// styles.go, and that is deliberate: they read the Color* globals at call
// time, so ApplyPalette does not have to know they exist. Every style in the
// var table has to be written twice — once as its initializer, once inside
// ApplyPalette — and one written only once silently keeps default-pink under
// every other theme. A role style cannot have that bug.

// SelectionFillWidthGuide is the column budget past which an accent fill stops
// reading as a mark and starts reading as a wall — the threshold that decides
// SelectionPill against SelectionBand. It is guidance for choosing at the call
// site rather than a runtime switch, because a surface knows at build time
// whether it fills a label or a row, and a fill that changed character as the
// terminal resized would be worse than either.
const SelectionFillWidthGuide = 40

// SelectionPill is the selected row's fill where the filled region is sized to
// its own content — a sidebar row, a dropdown entry, a settings rail category.
// Accent-filled when the list owns the keyboard, a muted band when it does not,
// so a list that has lost focus still shows where its cursor is without
// competing with whatever took focus.
func SelectionPill(focused bool) lipgloss.Style {
	if focused {
		return lipgloss.NewStyle().Bold(true).Foreground(ColorBg).Background(ColorAccent)
	}
	return lipgloss.NewStyle().Bold(true).Foreground(ColorText).Background(ColorBorder)
}

// SelectionPillSecondary is the same fill for a row's supporting text — a
// count, a status, a tree connector — so a pill built from several renders
// stays one continuous block of color.
func SelectionPillSecondary(focused bool) lipgloss.Style {
	if focused {
		return lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)
	}
	return lipgloss.NewStyle().Foreground(ColorText).Background(ColorBorder)
}

// SelectionBand is the selected row's fill where the fill spans a full row
// wider than SelectionFillWidthGuide — the command palette, whose rows run to
// 96 columns. Always muted, never accent: a solid accent bar that wide is a
// wall of color, and the accent is better spent on SelectionMarker and the
// panel border, which is where the eye lands anyway.
//
// It takes no focused argument because a full-row list is the focused surface
// whenever it is on screen. Give it one the day that stops being true.
func SelectionBand() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(ColorText).Background(ColorBorder)
}

// SelectionBandSecondary is the band's supporting text.
//
// Deliberately ColorText and not ColorTextDim: dim-on-border is the one pairing
// in this palette that genuinely fails to read — #857a8c on #6a4d78 under Fleet
// Pink, and no better under Gruvbox — so the shortcut column on the selected
// row was the least legible text on screen. Dropping the bold, not the color,
// is what keeps the hierarchy.
func SelectionBandSecondary() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(ColorText).Background(ColorBorder)
}

// SelectionMarker styles the ▸ that precedes a selected row. On a banded list
// this carries the accent the band gives up.
func SelectionMarker(focused bool) lipgloss.Style {
	if focused {
		return lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	}
	return lipgloss.NewStyle().Foreground(ColorTextDim)
}

// ModeOn styles the option currently in effect — the active tab, the chosen
// value of a cycler.
//
// Accent text and a rule underneath, never a fill. A mode indicator does not
// hold the keyboard and is not a cursor position; giving it the fill outranks
// both of the things that do, which is exactly the bug this vocabulary exists
// to prevent. Underline is what gives it a boundary without borrowing weight
// it has not earned.
func ModeOn() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Underline(true)
}

// ModeOff styles the options not in effect.
func ModeOff() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(ColorTextDim)
}

// FocusCaret is the block cursor — the single loudest mark on screen, and the
// literal answer to "where does what I type go". Only a surface that receives
// keystrokes may render one.
func FocusCaret() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)
}

// PrimaryAction styles a surface's one primary action — the launchpad's
// "⏎ Add & continue" button. A button may fill where a mode indicator may not:
// it is short, and it is the thing Enter does, so the fill points at the
// keyboard rather than competing with it.
func PrimaryAction() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(ColorBg).Background(ColorAccent)
}

// NewTextInput returns a text input wearing fleet's palette.
//
// Bubbles ships DefaultDarkStyles, which hardcodes a 256-color grey (SGR 38;5;240)
// for the placeholder and the prompt. Every one of fleet's twelve inputs took
// that default, so under Gruvbox or Nord the one widget that shows where your
// keystrokes land was the one widget that ignored the theme — and the caret,
// the loudest focus signal there is, was not the accent color it is everywhere
// else.
//
// The input also owns its own prompt. A dialog that draws a second one beside
// it gets the "> >" the command palette shipped for a release.
func NewTextInput() textinput.Model {
	ti := textinput.New()
	st := ti.Styles()

	st.Focused.Text = lipgloss.NewStyle().Foreground(ColorText)
	st.Focused.Placeholder = lipgloss.NewStyle().Foreground(ColorTextDim)
	st.Focused.Prompt = lipgloss.NewStyle().Foreground(ColorAccent)

	// A blurred input still shows its value, just quietly — it holds state the
	// user typed, and blanking it out would read as the field having been
	// cleared rather than as it having lost focus.
	st.Blurred.Text = lipgloss.NewStyle().Foreground(ColorTextDim)
	st.Blurred.Placeholder = lipgloss.NewStyle().Foreground(ColorTextDim)
	st.Blurred.Prompt = lipgloss.NewStyle().Foreground(ColorTextDim)

	st.Cursor.Color = ColorAccent
	ti.SetStyles(st)
	return ti
}
