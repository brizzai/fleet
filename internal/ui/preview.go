package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// RenderPreview renders the inner content of the preview pane: a single dim
// italic prompt strip plus the live pane capture. All session metadata
// (name, status, PR, path, last-used) now rides the panel border via
// BuildPreviewTitle / BuildPreviewFooter, freeing 5+ rows of vertical space
// previously eaten by a metadata header block.
func RenderPreview(s *session.Session, content string, repoInfo *git.RepoInfo, width, height int, focused bool) string {
	if s == nil {
		return DimStyle.Render("  No session selected")
	}

	var b strings.Builder

	// Always-on prompt strip — dim italic so it reads as "context, not content".
	promptRow := ""
	if s.FirstPrompt != "" {
		prompt := s.FirstPrompt
		if idx := strings.IndexByte(prompt, '\n'); idx != -1 {
			prompt = prompt[:idx] + "…"
		}
		full := "  > " + prompt
		if lipgloss.Width(full) > width {
			full = ansi.Truncate(full, width, "…")
		}
		promptRow = lipgloss.NewStyle().Foreground(ColorTextDim).Italic(true).Render(full)
		b.WriteString(promptRow)
		b.WriteString("\n")
	}

	contentHeight := height - 1 // 1 row for the prompt strip
	if promptRow == "" {
		contentHeight = height
	}
	if contentHeight < 1 {
		contentHeight = 1
	}

	if content == "" {
		if s.GetStatus() == session.StatusError {
			b.WriteString(ErrorStyle.Render("  Session is not running"))
		} else {
			b.WriteString(DimStyle.Render("  Waiting for output..."))
		}
		return b.String()
	}

	content = stripOSC8(content)
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	start := len(lines) - contentHeight
	if start < 0 {
		start = 0
	}
	for i := start; i < len(lines); i++ {
		line := lines[i]
		if ansi.StringWidth(line) > width-2 {
			line = ansi.Truncate(line, width-2, "")
		}
		b.WriteString("  " + line + "\x1b[0m")
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// BuildPreviewTitle renders the rich top-border title for the preview panel:
// "Preview · <session> · <status> · <PR>". Pre-styled with the per-segment
// colors; RenderBorderedPanel passes it through verbatim. Truncates the
// session title from the end if the result is too wide for the border.
func BuildPreviewTitle(s *session.Session, repoInfo *git.RepoInfo, focused bool, maxWidth int) string {
	previewLabel := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("Preview")
	if s == nil {
		return previewLabel
	}
	sep := DimStyle.Render(" · ")

	statusSeg := StatusStyle(s.GetStatus()).Render(string(s.GetStatus()))

	prSeg := ""
	var pr *github.PR
	if repoInfo != nil {
		pr = repoInfo.PR
	}
	if pr != nil {
		if txt := previewPRSummary(pr); txt != "" {
			prSeg = prBadgeStyle(pr).Render(txt)
		}
	}

	focusSeg := ""
	if focused {
		focusSeg = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("focus")
	}

	// Title is truncated to whatever budget remains after the fixed segments.
	used := lipgloss.Width(previewLabel) + lipgloss.Width(sep)
	used += lipgloss.Width(statusSeg) + lipgloss.Width(sep)
	if prSeg != "" {
		used += lipgloss.Width(prSeg) + lipgloss.Width(sep)
	}
	if focusSeg != "" {
		used += lipgloss.Width(focusSeg) + lipgloss.Width(sep)
	}
	titleBudget := maxWidth - used
	if titleBudget < 6 {
		titleBudget = 6
	}
	rawTitle := s.Title
	if lipgloss.Width(rawTitle) > titleBudget {
		rawTitle = ansi.Truncate(rawTitle, titleBudget, "…")
	}
	titleSeg := PreviewHeaderStyle.Render(rawTitle)

	parts := []string{previewLabel, titleSeg, statusSeg}
	if prSeg != "" {
		parts = append(parts, prSeg)
	}
	if focusSeg != "" {
		parts = append(parts, focusSeg)
	}
	return strings.Join(parts, sep)
}

// previewPRSummary returns a verbose human-readable PR summary like
// "PR #2874 (CI failing, 3 unresolved, conflicts)". Glyph-only badges work
// in the sidebar because there's a learning curve users opt into; in the
// preview title we spell the problems out so newcomers can read state
// without a legend.
func previewPRSummary(pr *github.PR) string {
	if pr == nil || pr.State == "CLOSED" {
		return ""
	}
	base := fmt.Sprintf("PR #%d", pr.Number)

	if pr.State == "MERGED" {
		return base + " (merged)"
	}

	var details []string
	if pr.CIStatus == "FAILURE" {
		details = append(details, "CI failing")
	}
	if pr.HasConflicts {
		details = append(details, "conflicts")
	}
	if pr.ReviewDecision == "CHANGES_REQUESTED" {
		details = append(details, "changes requested")
	}
	if pr.UnresolvedThreads > 0 {
		details = append(details, fmt.Sprintf("%d unresolved", pr.UnresolvedThreads))
	}
	if pr.CIStatus == "PENDING" && len(details) == 0 {
		details = append(details, "CI pending")
	}
	if pr.ReviewDecision == "REVIEW_REQUIRED" && len(details) == 0 {
		details = append(details, "review pending")
	}
	if pr.CIStatus == "SUCCESS" && pr.ReviewDecision == "APPROVED" && len(details) == 0 {
		return base + " (ready)"
	}

	if len(details) == 0 {
		return base
	}
	return base + " (" + strings.Join(details, ", ") + ")"
}

// BuildPreviewFooter renders the rich bottom-border footer for the preview
// panel: "<path> · <relative-time>". Path is shortened to use ~ for home and
// truncated from the start ("…trailing/components") when too long.
func BuildPreviewFooter(s *session.Session, maxWidth int) string {
	if s == nil {
		return ""
	}
	path := shortenPath(s.ProjectPath)
	if maxWidth > 4 && lipgloss.Width(path) > maxWidth {
		path = "…" + ansi.Truncate(reverseString(path), maxWidth-1, "")
		path = reverseString(path)
	}
	parts := []string{DimStyle.Render(path)}
	if !s.LastAccessedAt.IsZero() {
		parts = append(parts, DimStyle.Render(relativeTime(s.LastAccessedAt)))
	}
	return strings.Join(parts, DimStyle.Render(" · "))
}

// shortenPath replaces the user's home dir with "~". Falls back to the
// original path if HOME isn't set or doesn't match.
func shortenPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(p, home); ok {
			return "~" + rest
		}
	}
	return p
}

// reverseString reverses a string rune-by-rune. Used by BuildPreviewFooter to
// truncate paths from the start while preserving rune boundaries.
func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// relativeTime formats a time as a human-readable relative duration (e.g., "5m ago", "2h ago").
func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", h)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

// stripOSC8 removes OSC-8 hyperlink sequences while preserving the visible link text.
// OSC-8 format: ESC]8;params;uri ST ... visible text ... ESC]8;;ST
// where ST is BEL (\x07) or ESC\ (\x1b\x5c).
func stripOSC8(content string) string {
	if !strings.Contains(content, "\x1b]8;") {
		return content
	}

	var b strings.Builder
	b.Grow(len(content))

	i := 0
	for i < len(content) {
		// Look for ESC ] 8 ;
		if i+3 < len(content) && content[i] == '\x1b' && content[i+1] == ']' && content[i+2] == '8' && content[i+3] == ';' {
			// Skip until ST (BEL or ESC\).
			j := i + 4
			for j < len(content) {
				if content[j] == '\x07' {
					j++
					break
				}
				if content[j] == '\x1b' && j+1 < len(content) && content[j+1] == '\\' {
					j += 2
					break
				}
				j++
			}
			i = j
			continue
		}
		b.WriteByte(content[i])
		i++
	}

	return b.String()
}
