package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// readerSession is everything the reader knows about the agent working on this
// pull request.
//
// A snapshot handed in by the app, not a session the reader holds: the reader
// owns no storage and no lifecycle, exactly as it owns no comments store. Frame
// is the pane's live screen, already rendered — the emulator and its tmux
// stream live on Home beside the drawer's, because forking `tmux -C attach` is
// not something a dialog does.
type readerSession struct {
	Present bool
	Title   string
	Status  string
	Frame   string
	// Starting is true between asking for a session and its pane existing, so
	// the tab can say something is happening rather than repeating the offer.
	Starting bool
}

// reviewSessionMsg asks the app to act on this review's agent.
type reviewSessionMsg struct {
	key   reviewKey
	start bool // start one rather than attach to the one that exists
}

// SetSession installs what the app knows about the review's agent.
func (d *ReaderDialog) SetSession(s readerSession) { d.session = s }

// renderSession draws the third tab.
func (d *ReaderDialog) renderSession(width, rows int) []string {
	if !d.session.Present {
		return d.renderNoSession(width, rows)
	}

	out := make([]string, rows)
	// The frame arrives already sized to this panel — the emulator is resized
	// to it — so it is placed line for line rather than wrapped, which is what
	// keeps a spinner in the same column between frames.
	for i, line := range strings.Split(d.session.Frame, "\n") {
		if i >= rows {
			break
		}
		out[i] = ansi.Truncate(line, width, "")
	}
	return out
}

// renderNoSession is the common case: most reviews are read without ever
// starting an agent, so this tab is usually an offer rather than a terminal.
func (d *ReaderDialog) renderNoSession(width, rows int) []string {
	out := make([]string, rows)
	line := 0
	put := func(s string) {
		if line < rows {
			out[line] = s
			line++
		}
	}

	put("")
	if d.session.Starting {
		put("  " + DimStyle.Render("Starting an agent on this review…"))
		return out
	}
	put("  " + TitleStyle.Render("No agent on this review yet"))
	put("")
	for _, l := range []string{
		"Nothing is working on #" + itoa(d.pr) + ". Reading a pull request",
		"does not need one, which is why there usually isn't.",
	} {
		put("  " + SessionItemStyle.Render(ansi.Truncate(l, max(width-4, 8), "…")))
	}
	put("")
	put("  " + HelpKeyStyle.Render("⏎") + DimStyle.Render(" start one in the review worktree"))
	return out
}

// sessionStatus is what the panel's top-right corner says.
func (d *ReaderDialog) sessionStatus() string {
	if !d.session.Present {
		return ""
	}
	return statusDot(d.session.Status) + " " + d.session.Status
}

// statusDot reuses the sidebar's vocabulary: the same glyph means the same
// thing here as it does on the row you came from.
func statusDot(status string) string {
	switch status {
	case "running", "finished":
		return "●"
	case "waiting":
		return "◐"
	case "error":
		return "✕"
	}
	return "○"
}

// sessionTitle names the pane in the panel's border.
func (d *ReaderDialog) sessionTitle(width int) string {
	if !d.session.Present {
		return "Session"
	}
	return ansi.Truncate(d.session.Title, max(width-24, 12), "…")
}
