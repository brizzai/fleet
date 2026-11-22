package session

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/hooks"
	"github.com/brizzai/fleet/internal/tmux"
)

// Antigravity CLI (`agy`) status, ownership and titles. What its hooks do and
// don't report was measured against agy 1.5.14 on macOS and Linux; the handler
// side is normalizeAntigravityEvent in cmd/fleet/hook_handler.go.

// antigravityHookAccepted decides whether a hook belongs to this session.
// Subagents never reach here — the handler drops their hooks — so a new
// conversation id means the user started one: /clear (or /new) and /resume
// both switch conversations inside the running process. Same-process only: an
// `agy -p` a tool call runs inherits FLEET_INSTANCE_ID, and its conversation
// must not take the session over. An unknown owner (fresh launch, fleet
// reopened) can't be checked, and a dead one has nothing left to protect.
func (s *Session) antigravityHookAccepted(hs *HookStatus) bool {
	if hs.SessionID == "" {
		return true
	}
	s.mu.RLock()
	expected := s.ownerSessionID
	if expected == "" {
		expected = s.ClaudeSessionID
	}
	ownerPID := s.ownerPID
	s.mu.RUnlock()
	if expected == "" || hs.SessionID == expected {
		return true
	}
	if ownerPID == 0 || hs.AgentPID == ownerPID || !agentProcessAlive(ownerPID) {
		debuglog.Logger.Info("adopting antigravity conversation from the session's own process",
			"id", s.ID, "old", expected, "new", hs.SessionID)
		return true
	}
	debuglog.Logger.Debug("hook dropped: foreign antigravity conversation",
		"id", s.ID, "expected", expected, "foreign", hs.SessionID)
	return false
}

// updateAntigravityStatus is the Antigravity branch of UpdateStatus. Its hooks
// say when a turn starts and ends, but nothing fires on a permission prompt,
// an Esc interrupt or /exit, so the pane leads: a prompt reads waiting, the
// working footer reads running, and an at-rest pane lets the hook decide.
func (s *Session) updateAntigravityStatus(oldStatus Status, hasHook bool, hookStatus string, log *slog.Logger) {
	// /exit drops the pane back to the launch shell (exit 0, no hook). The pane
	// alone isn't proof: Ctrl+Z or a split window show a shell over a live agy.
	if hasHook && tmux.IsShellCommand(s.paneCommand()) && s.agentOwnerGone() {
		s.applyHookStatus(oldStatus, "dead", "", log)
		return
	}
	s.updatePaneLedStatus("antigravity", oldStatus, hasHook, hookStatus, "",
		antigravityPaneWaiting, antigravityPaneRunning, s.settleInterruptedAntigravityTurn, log)
}

// settleInterruptedAntigravityTurn is the at-rest pane's say over a running
// hook. Esc mid-turn ends the turn with no hook at all, so the last one stays
// "running" forever; an at-rest pane under it means the turn is over.
//
// That includes a running BackgroundWait hook. Its fullyIdle=false is not
// reliable: agy also sent it as the final Stop of a turn whose subagents had
// all finished (measured), which would have held the session at running for
// good. Subagents that really are working keep the pane out of "at rest"
// (antigravityPaneRunning reads their dock rows), so this never sees them.
//
// One odd frame is not enough: the demotion must be seen
// paneFinishedConfirmFrames times, paneFinishedConfirmDelay apart, as Claude's
// no-hook demotion is.
func (s *Session) settleInterruptedAntigravityTurn(hook string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if hook != "running" {
		s.resetPaneFinishedConfirmLocked()
		return hook
	}
	now := time.Now()
	if s.paneFinishedFrames == 0 {
		s.paneFinishedFirstAt = now
	}
	s.paneFinishedFrames++
	if s.paneFinishedFrames < paneFinishedConfirmFrames || now.Sub(s.paneFinishedFirstAt) < paneFinishedConfirmDelay {
		return hook
	}
	return "finished"
}

// PaneDemotionPendingSince reports a half-built at-rest confirmation: the pane
// read at rest once under a running hook, and a look at least ConfirmDelay
// after since decides it. The TUI's worker takes that look on its next pass; a
// one-shot observer (`fleet status`) has no next pass, so it asks.
func (s *Session) PaneDemotionPendingSince() (since time.Time, pending bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.paneFinishedFirstAt, s.paneFinishedFrames > 0
}

// PaneDemotionConfirmDelay is how far apart the looks confirming an at-rest
// pane must be.
const PaneDemotionConfirmDelay = paneFinishedConfirmDelay

// antigravityFooterLines is how far up the footers below are looked for. Every
// Antigravity prompt ends in its key-hint line and, under it, the status bar;
// a reply quoting the same words further up can't match.
const antigravityFooterLines = 3

// antigravityPaneWaiting reports whether Antigravity is blocked on the user.
// Command, file-create, file-edit and ask_question prompts all draw a
// "↑/↓ Navigate · …" hint line directly above an "esc to cancel" status bar.
// Its own pickers (/hooks, /model, /resume) draw the hint line with no
// "esc to cancel" under it, and are the user browsing, not the agent asking.
// The trust and sign-in menus have no status bar; they are matched by their
// own wording. Fleet launches with the project trusted (AntigravitySettings),
// so the trust menu means that failed.
func antigravityPaneWaiting(content string) bool {
	recent := recentNonBlankLines(content, antigravityFooterLines)
	for i := 0; i+1 < len(recent); i++ {
		if strings.Contains(recent[i], "↑/↓ Navigate") && strings.HasPrefix(strings.TrimSpace(recent[i+1]), "esc to cancel") {
			return true
		}
	}
	return antigravitySubagentNeedsApproval(content) || recentContains(content, antigravityBlockingMenus)
}

var antigravityBlockingMenus = []string{
	"Do you trust the contents of this project?",
	"You are currently not signed in.",
	// The Alt+J subagent manager, opened on a subagent's approval.
	"⚠ Approval Required",
}

// antigravitySubagentNeedsApproval reports a subagent blocked on an approval.
// The parent's pane shows no menu for it — only a quoted banner above the
// input box ("┃ self needs approval for Bash … ┃ ctrl+k approve · alt+j
// manage"), while the status bar still reads as idle. Ctrl+K approves it.
func antigravitySubagentNeedsApproval(content string) bool {
	return recentContains(content, []string{"┃ ctrl+k approve"})
}

// AntigravityApproveKey is the key that approves what an Antigravity pane is
// waiting on: Enter for its own prompts, whose first option ("1. Yes …") is
// highlighted, and Ctrl+K for a subagent's approval banner, where Enter would
// only submit the empty input box.
func AntigravityApproveKey(rawPane string) string {
	clean := StripANSI(rawPane)
	recent := recentNonBlankLines(clean, antigravityFooterLines)
	for i := 0; i+1 < len(recent); i++ {
		if strings.Contains(recent[i], "↑/↓ Navigate") {
			return "Enter"
		}
	}
	if antigravitySubagentNeedsApproval(clean) {
		return "C-k"
	}
	return "Enter"
}

// antigravityPaneRunning reports a turn in progress: the status bar reads
// "esc to cancel" while the agent works (model retries included), and
// "? for shortcuts" — or nothing, with a draft in the box — at rest. A prompt
// shows "esc to cancel" too, so waiting takes precedence.
//
// The agent can also end its own turn while subagents it started keep working;
// its pane then sits at the idle prompt, and only the dock under the input box
// says the session is busy: one "● Agent(…) … · 12s" row per live subagent,
// gone once it finishes (measured). The status bar's "· N subagent(s)" says
// the same, but agy drops it from a pane 50 columns wide or narrower, where
// the left-aligned dock rows survive.
func antigravityPaneRunning(content string) bool {
	if antigravityPaneWaiting(content) {
		return false
	}
	recent := recentNonBlankLines(content, antigravityDockLines)
	if len(recent) == 0 {
		return false
	}
	bar := strings.TrimSpace(recent[len(recent)-1])
	if strings.HasPrefix(bar, "esc to cancel") || strings.HasSuffix(bar, "subagent(s)") {
		return true
	}
	for _, l := range recent {
		if strings.HasPrefix(strings.TrimSpace(l), "● Agent(") {
			return true
		}
	}
	return false
}

// antigravityDockLines is how far up a subagent dock row is looked for: the
// status bar, a rule, the rows, a rule, the input box. Conversation lines
// sit above all of that, so a transcript row can't match.
const antigravityDockLines = 10

// recentNonBlankLines returns the last n non-blank lines of content, in order.
func recentNonBlankLines(content string, n int) []string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append(out, lines[i])
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// antigravityTitleMaxRunes bounds an accepted title: Antigravity's own are a
// few words ("Request To Reply Pong").
const antigravityTitleMaxRunes = 120

// antigravityConversationID is the shape of the ids Antigravity reports
// (a UUID). The id reaches a file path, so nothing else is accepted.
var antigravityConversationID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ReadAntigravitySessionName returns the title Antigravity keeps for a
// conversation, from annotations/<id>.pbtxt (`title:"…"`, a text-format
// protobuf). It holds the model-generated title after the first turn and the
// one set with /rename, so this covers both. "" until it exists.
func ReadAntigravitySessionName(conversationID string) string {
	if !antigravityConversationID.MatchString(conversationID) {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(hooks.AntigravityAppDataDir(), "annotations", conversationID+".pbtxt"))
	if err != nil {
		return ""
	}
	return parseAntigravityTitle(string(data))
}

var antigravityTitleField = regexp.MustCompile(`(?m)^\s*title:\s*("(?:[^"\\]|\\.)*")`)

func parseAntigravityTitle(pbtxt string) string {
	m := antigravityTitleField.FindStringSubmatch(pbtxt)
	if m == nil {
		return ""
	}
	// Text-format strings use C escapes (\", \\, \n, and \ooo octal bytes for
	// non-ASCII), all of which Go's own unquoting reads — except \', which a
	// C++-style writer emits and Go refuses inside double quotes.
	title, err := strconv.Unquote(unescapeSingleQuotes(m[1]))
	if err != nil || !utf8.ValidString(title) {
		return ""
	}
	title = strings.TrimSpace(title)
	if strings.ContainsAny(title, "\n\r") || utf8.RuneCountInString(title) > antigravityTitleMaxRunes {
		return ""
	}
	return title
}

// unescapeSingleQuotes turns each \' escape in a quoted text-format string
// into a bare ', leaving every other escape (an escaped backslash before a
// quote included) for strconv.Unquote.
func unescapeSingleQuotes(quoted string) string {
	if !strings.Contains(quoted, `\'`) {
		return quoted
	}
	var b strings.Builder
	for i := 0; i < len(quoted); i++ {
		if quoted[i] == '\\' && i+1 < len(quoted) {
			if quoted[i+1] == '\'' {
				b.WriteByte('\'')
			} else {
				b.WriteString(quoted[i : i+2])
			}
			i++
			continue
		}
		b.WriteByte(quoted[i])
	}
	return b.String()
}

// resolvedProjectPath is the project directory with symlinks resolved, the
// form Antigravity compares trusted workspaces in (on macOS it shows /tmp as
// /private/tmp). The path as given when it can't be resolved.
func resolvedProjectPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}
