package stats

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Action enums for RecordAction. Callers should use these constants; an
// unknown snake_case enum is still accepted and gets a derived label.
const (
	ActionAttach       = "attach" // counted from the attaches table; don't also RecordAction it
	ActionSpaceJump    = "space_jump"
	ActionQuickApprove = "quick_approve"
	ActionFork         = "fork"
	ActionDrawerOpen   = "drawer_open"
	ActionSlotJump     = "slot_jump"
	ActionSlotBind     = "slot_bind"
	ActionSnooze       = "snooze"
	ActionSessionNew   = "session_new"
	ActionWorktreeNew  = "worktree_new"
	ActionRestart      = "restart"
	ActionResume       = "resume"
	ActionRename       = "rename"
	ActionPaletteOpen  = "palette_open"
	ActionPROpen       = "pr_open"
	ActionPRJump       = "pr_jump"
	ActionEditorOpen   = "editor_open"
	ActionMarkUnread   = "mark_unread"
	ActionDelete       = "delete"
	ActionSend         = "send"
	ActionHelpOpen     = "help_open"
	ActionUndo         = "undo"
	ActionCopyPRLink   = "copy_pr_link"
)

var actionLabels = map[string]string{
	ActionAttach:       "Attaches",
	ActionSpaceJump:    "Space jumps",
	ActionQuickApprove: "Quick approves",
	ActionFork:         "Forks",
	ActionDrawerOpen:   "Drawer opens",
	ActionSlotJump:     "Slot jumps",
	ActionSlotBind:     "Slot binds",
	ActionSnooze:       "Snoozes",
	ActionSessionNew:   "Sessions started",
	ActionWorktreeNew:  "Worktrees created",
	ActionRestart:      "Restarts",
	ActionResume:       "Resumes",
	ActionRename:       "Renames",
	ActionPaletteOpen:  "Palette opens",
	ActionPROpen:       "PRs opened",
	ActionPRJump:       "PR jumps",
	ActionEditorOpen:   "Editor opens",
	ActionMarkUnread:   "Marked unread",
	ActionDelete:       "Deletes",
	ActionSend:         "Messages sent",
	ActionHelpOpen:     "Help opens",
	ActionUndo:         "Undos",
	ActionCopyPRLink:   "PR links copied",
}

// ActionLabel is the human label for an action enum; unknown enums are
// sentence-cased ("ticket_start" → "Ticket start").
func ActionLabel(action string) string {
	if l, ok := actionLabels[action]; ok {
		return l
	}
	s := strings.ReplaceAll(action, "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// FormatDuration renders a duration the way Stats shows it: "48s",
// "47m 12s", "41h 12m".
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Round(time.Second)/time.Second))
	case d < time.Hour:
		d = d.Round(time.Second)
		return fmt.Sprintf("%dm %02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
	default:
		d = d.Round(time.Minute)
		return fmt.Sprintf("%dh %02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	}
}

// formatUSD: "$1,284" at ten dollars and above, "$4.20" below.
func formatUSD(x float64) string {
	if x < 10 {
		return fmt.Sprintf("$%.2f", x)
	}
	return "$" + formatInt(int64(math.Round(x)))
}

func formatInt(n int64) string {
	s := fmt.Sprintf("%d", n)
	if n < 0 {
		return "-" + formatInt(-n)
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// formatTokens: "38.6M", "12.3K", "1.2B".
func formatTokens(n int64) string {
	f := float64(n)
	switch {
	case f >= 1e9:
		return fmt.Sprintf("%.1fB", f/1e9)
	case f >= 1e6:
		return fmt.Sprintf("%.1fM", f/1e6)
	case f >= 1e3:
		return fmt.Sprintf("%.1fK", f/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
