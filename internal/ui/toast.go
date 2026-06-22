package ui

import (
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// ToastLevel tags a toast's severity, which picks the icon + accent color.
type ToastLevel int

const (
	ToastInfo ToastLevel = iota
	ToastSuccess
	ToastWarn
	ToastError
)

const (
	toastMaxStack   = 3
	toastMaxWidth   = 44
	toastTTLInfo    = 3 * time.Second
	toastTTLError   = 5 * time.Second
	toastInnerHPad  = 1
	toastBorderCols = 2 // left + right border cells
)

// Toast is a single transient notification.
type Toast struct {
	Level     ToastLevel
	Message   string
	ExpiresAt time.Time
}

// ToastStack holds the active toast queue. Expired entries are filtered
// lazily inside View so callers don't need to pump a separate timer.
type ToastStack struct {
	toasts []Toast
}

func NewToastStack() *ToastStack { return &ToastStack{} }

// Add queues a toast at the given level. When the stack exceeds toastMaxStack
// the oldest entries drop off.
func (ts *ToastStack) Add(level ToastLevel, message string) {
	ttl := toastTTLInfo
	if level == ToastError {
		ttl = toastTTLError
	}
	ts.toasts = append(ts.toasts, Toast{
		Level:     level,
		Message:   message,
		ExpiresAt: time.Now().Add(ttl),
	})
	if len(ts.toasts) > toastMaxStack {
		ts.toasts = ts.toasts[len(ts.toasts)-toastMaxStack:]
	}
}

// View returns the rendered stack of active toasts, newest at the bottom.
// Returns "" when nothing is active so the caller can skip compositing.
// maxWidth bounds the toast column to the terminal width.
func (ts *ToastStack) View(maxWidth int) string {
	if len(ts.toasts) == 0 {
		return ""
	}
	now := time.Now()
	active := ts.toasts[:0]
	for _, t := range ts.toasts {
		if t.ExpiresAt.After(now) {
			active = append(active, t)
		}
	}
	ts.toasts = active
	if len(active) == 0 {
		return ""
	}

	width := toastMaxWidth
	if maxWidth > 0 && maxWidth < width+2 {
		width = maxWidth - 2
	}
	if width < 12 {
		return ""
	}

	blocks := make([]string, 0, len(active))
	for _, t := range active {
		blocks = append(blocks, renderToast(t, width))
	}
	return lipgloss.JoinVertical(lipgloss.Right, blocks...)
}

// Empty reports whether the stack has any active toasts (ignoring expiry).
// Callers that care about expiry should call View and compare to "".
func (ts *ToastStack) Empty() bool { return len(ts.toasts) == 0 }

func renderToast(t Toast, width int) string {
	color, icon := toastStyle(t.Level)
	return renderHintBox(icon, color, t.Message, "", width)
}

// renderHintBox draws the shared bottom-right box used by both toasts and tips:
// an accent sigil on the first wrapped line, a 2-space hanging indent on the
// rest, an optional dim footer line, all inside a rounded border in `accent`.
// `width` is the final outer width (already clamped by the caller). Toasts pass
// no footer; tips pass the dismiss hint. Keeping both on this one path is what
// guarantees the stacked boxes stay the same width and shape.
func renderHintBox(sigil string, accent color.Color, body, footer string, width int) string {
	// innerWidth is the content area (inside border + padding); reserve 2 cells
	// for the sigil prefix (line 0) or hanging indent (line 1+).
	innerWidth := width - toastBorderCols - 2*toastInnerHPad
	if innerWidth < 6 {
		innerWidth = 6
	}
	bodyWidth := innerWidth - 2

	sigilStyle := lipgloss.NewStyle().Foreground(accent).Bold(true)
	msgStyle := lipgloss.NewStyle().Foreground(ColorText)

	lines := wrapToastLine(strings.ReplaceAll(body, "\n", " "), bodyWidth)

	var b strings.Builder
	for i, line := range lines {
		if i == 0 {
			b.WriteString(sigilStyle.Render(sigil) + " " + msgStyle.Render(line))
		} else {
			b.WriteString("\n  " + msgStyle.Render(line))
		}
	}
	if footer != "" {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(ColorTextDim).Render(footer))
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Padding(0, toastInnerHPad).
		Width(width - toastBorderCols).
		Render(b.String())
}

func toastStyle(level ToastLevel) (color.Color, string) {
	switch level {
	case ToastSuccess:
		return ColorGreen, "✓"
	case ToastWarn:
		return ColorYellow, "!"
	case ToastError:
		return ColorRed, "✕"
	default:
		return ColorAccent, "ℹ"
	}
}

// wrapToastLine greedy-wraps a plain-text message at space boundaries to at
// most width cells per line. Words longer than width are hard-split.
func wrapToastLine(msg string, width int) []string {
	if width < 1 {
		return []string{msg}
	}
	words := strings.Fields(msg)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	var cur string
	flush := func() {
		if cur != "" {
			lines = append(lines, cur)
			cur = ""
		}
	}
	for _, w := range words {
		// Hard-split any single word wider than the column.
		for lipgloss.Width(w) > width {
			runes := []rune(w)
			flush()
			lines = append(lines, string(runes[:width]))
			w = string(runes[width:])
		}
		if cur == "" {
			cur = w
			continue
		}
		if lipgloss.Width(cur)+1+lipgloss.Width(w) <= width {
			cur = cur + " " + w
		} else {
			flush()
			cur = w
		}
	}
	flush()
	return lines
}
