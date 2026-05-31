package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// RenderPreview renders the preview pane for the selected session.
// Panel title + border are now drawn by RenderBorderedPanel in the caller, so
// `width` and `height` here describe the inner content area only.
func RenderPreview(s *session.Session, content string, repoInfo *git.RepoInfo, width, height int, focused bool) string {
	if s == nil {
		return DimStyle.Render("  No session selected")
	}

	var b strings.Builder

	// Header: title + status.
	header := fmt.Sprintf("  %s %s  %s",
		StatusSymbol(s.GetStatus()),
		PreviewHeaderStyle.Render(s.Title),
		StatusLabel(s.GetStatus()),
	)
	b.WriteString(header)
	b.WriteString("\n")

	// Metadata.
	metaLine := s.ProjectPath
	if !s.LastAccessedAt.IsZero() {
		metaLine += "  ·  last used " + relativeTime(s.LastAccessedAt)
	}
	b.WriteString(DimStyle.Render(fmt.Sprintf("  %s", metaLine)))
	b.WriteString("\n")

	// Git info line.
	usedLines := 3 // header + path + first content row
	if gitLine := renderGitInfoLine(repoInfo); gitLine != "" {
		b.WriteString("  " + gitLine)
		b.WriteString("\n")
		usedLines++
	}

	// Workspace name.
	if s.WorkspaceName != "" {
		b.WriteString(DimStyle.Render(fmt.Sprintf("  workspace: %s", s.WorkspaceName)))
		b.WriteString("\n")
		usedLines++
	}

	// Last prompt.
	if s.FirstPrompt != "" {
		prompt := s.FirstPrompt
		// Take first line only.
		if idx := strings.IndexByte(prompt, '\n'); idx != -1 {
			prompt = prompt[:idx]
			if len(prompt) < len(s.FirstPrompt) {
				prompt += "…"
			}
		}
		// Truncate to fit width.
		maxLen := width - 6
		if maxLen > 80 {
			maxLen = 80
		}
		if maxLen > 0 && len(prompt) > maxLen {
			prompt = prompt[:maxLen] + "…"
		}
		b.WriteString(DimStyle.Render(fmt.Sprintf("  > %s", prompt)))
		b.WriteString("\n")
		usedLines++
	}

	// Separator.
	sep := strings.Repeat("─", width-2)
	if len(sep) > 0 {
		b.WriteString(DimStyle.Render("  " + sep))
		b.WriteString("\n")
	}

	// Terminal content.
	contentHeight := height - usedLines
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

	// Strip OSC-8 hyperlinks to prevent dotted underlines in preview.
	content = stripOSC8(content)

	// Show last N lines that fit.
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	start := len(lines) - contentHeight
	if start < 0 {
		start = 0
	}

	for i := start; i < len(lines); i++ {
		line := lines[i]
		// Truncate long lines (ANSI-aware to avoid cutting escape sequences).
		if ansi.StringWidth(line) > width-2 {
			line = ansi.Truncate(line, width-2, "")
		}
		// Reset ANSI at end of each line to prevent background color bleed.
		b.WriteString("  " + line + "\x1b[0m")
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}

	return b.String()
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

// renderGitInfoLine renders a line with branch, dirty status, and PR info.
func renderGitInfoLine(info *git.RepoInfo) string {
	if info == nil || info.Branch == "" {
		return ""
	}

	var parts []string

	// Branch.
	parts = append(parts, BranchStyle.Render(branchIcon+" "+info.Branch))

	// Dirty indicator.
	if info.IsDirty {
		parts = append(parts, DirtyStyle.Render("* uncommitted"))
	}

	// PR info.
	if info.PR != nil && info.PR.State != "CLOSED" {
		pr := info.PR
		prText := fmt.Sprintf("PR #%d", pr.Number)

		if pr.State == "MERGED" {
			parts = append(parts, PRMergedStyle.Render(prText+" (merged)"))
		} else {
			var details []string
			if pr.CIStatus == "FAILURE" {
				details = append(details, "CI failing")
			}
			if pr.ReviewDecision == "CHANGES_REQUESTED" {
				details = append(details, "changes requested")
			}
			if pr.ReviewDecision == "APPROVED" {
				details = append(details, "approved")
			}
			if pr.CIStatus == "SUCCESS" && pr.ReviewDecision != "APPROVED" {
				details = append(details, "CI passing")
			}
			if pr.CIStatus == "PENDING" {
				details = append(details, "CI pending")
			}
			if pr.ReviewDecision == "REVIEW_REQUIRED" {
				details = append(details, "review pending")
			}
			if pr.UnresolvedThreads > 0 {
				details = append(details, fmt.Sprintf("%d unresolved", pr.UnresolvedThreads))
			}
			if pr.HasConflicts {
				details = append(details, "conflicts")
			}
			if len(details) > 0 {
				prText += " (" + strings.Join(details, ", ") + ")"
			}

			ciFail := pr.CIStatus == "FAILURE"
			changesReq := pr.ReviewDecision == "CHANGES_REQUESTED"
			approved := pr.ReviewDecision == "APPROVED"
			ciPass := pr.CIStatus == "SUCCESS"
			hasThreads := pr.UnresolvedThreads > 0

			style := PRPendingStyle // default: yellow
			if ciFail || changesReq || hasThreads || pr.HasConflicts {
				style = PRFailStyle
			} else if approved && ciPass {
				style = PROpenStyle
			}
			parts = append(parts, style.Render(prText))
		}
	}

	return strings.Join(parts, "  ")
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
