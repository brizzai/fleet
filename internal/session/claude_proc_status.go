package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"github.com/brizzai/fleet/internal/proc"
)

// Claude Code keeps a status file per live process, <config dir>/sessions/<pid>.json,
// carrying its OWN view of what the process is doing — the one status signal
// fleet does not have to infer. Since 2.1.327 it writes status "waiting" with a
// waitingFor reason whenever an input the agent is blocked on is pending
// (computed in Claude's RY(): queuedElicitation → "input needed", else the top
// dialog's own reason, else a pending worker/sandbox request), and busy/idle
// otherwise.
//
// It is used ONLY to confirm waiting, never to deny it or to source anything else:
//
//   - Not every prompt declares a reason. Several permission dialogs and plan
//     approval carry a notification but no waitingFor, so the file reads "busy"
//     while they sit on screen. Its silence proves nothing.
//   - "busy"/"idle" are already covered by hooks and the pane, and the file is
//     blind to /compact (reads idle throughout).
//
// Where it earns its place is exactly where fleet's own signals lie: a waiting
// hook overridden to finished because the question had not rendered yet, or a
// queued message's UserPromptSubmit landing after the question's
// PermissionRequest and masking it (snapshot 2026-11-19T13-58-18).

// claudeTrustedWaitingFor is the set of waitingFor reasons that mean the agent
// is blocked on the user. "dialog open" is deliberately absent: Claude also
// reports it for a slash-command dialog the user opened themselves (/model,
// /config left on screen) — isShowingLocalJSXCommand — which is not the agent
// asking for anything, and would put the row into the Space rotation.
var claudeTrustedWaitingFor = map[string]bool{
	"input needed":    true, // AskUserQuestion, MCP elicitations, setup questions
	"sandbox request": true, // a sandboxed command asking for network access
	"worker request":  true,
	"goal proposal":   true,
}

// claudeProcStatus is the subset of Claude's per-process status file fleet reads.
type claudeProcStatus struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	Status     string `json:"status"`
	WaitingFor string `json:"waitingFor"`
}

// ClaudeSessionsDir is where Claude Code writes its per-process status files for
// a session on the given account: under the account's CLAUDE_CONFIG_DIR when it
// has one (Claude creates sessions/ under that dir, measured), else ~/.claude.
// An unresolvable account falls back to ~/.claude, matching sessionEnv, which
// launches such a session on the ambient login.
func ClaudeSessionsDir(account string) string {
	if dir := accountConfigDir(account); dir != "" {
		return filepath.Join(dir, "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "sessions")
}

// readClaudeProcStatus reads one process's status file. Keyed by pid, so the
// read is a single small file rather than a scan of every Claude on the machine.
func readClaudeProcStatus(dir string, pid int) (claudeProcStatus, bool) {
	if dir == "" || pid <= 0 {
		return claudeProcStatus{}, false
	}
	raw, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(pid)+".json"))
	if err != nil {
		return claudeProcStatus{}, false
	}
	var st claudeProcStatus
	if json.Unmarshal(raw, &st) != nil {
		return claudeProcStatus{}, false
	}
	return st, true
}

// trustedWaitingFor returns the reason when st says the conversation sessionID
// is blocked on the user for a trusted reason, else "". The sessionId check is
// what makes a pid-keyed file safe: a recycled pid, or a file another Claude
// wrote after /clear rotated the conversation, names a different session.
func (st claudeProcStatus) trustedWaitingFor(sessionID string) string {
	if sessionID == "" || st.SessionID != sessionID || st.Status != "waiting" {
		return ""
	}
	if !claudeTrustedWaitingFor[st.WaitingFor] {
		return ""
	}
	return st.WaitingFor
}

// claudeWaitingFor reports why Claude's own status file says this session is
// blocked on the user, or "" when it doesn't (or can't be read). Located by the
// pid the hooks report for the owning conversation, so a session with no hook
// yet — or a pre-AgentPID status file — has no signal. The pid must still be
// alive: a crashed Claude leaves its last word behind.
// MUST NOT be called while holding s.mu — it does disk I/O.
func (s *Session) claudeWaitingFor() string {
	if s.claudeWaitingFn != nil {
		return s.claudeWaitingFn()
	}
	s.mu.RLock()
	pid, owner, account := s.ownerPID, s.ownerSessionID, s.Account
	s.mu.RUnlock()

	st, ok := readClaudeProcStatus(ClaudeSessionsDir(account), pid)
	if !ok {
		return ""
	}
	reason := st.trustedWaitingFor(owner)
	if reason == "" || !proc.Alive(pid) {
		return ""
	}
	return reason
}

// ClaudeWaitingFor reports the reason Claude's own status file gave for being
// blocked on the user on the last status pass, or "". A waiting status that
// rests on it holds even when no prompt is on screen yet (the question's
// PermissionRequest lands before the dialog renders), so a reader that checks
// the pane for a prompt must not overrule it.
func (s *Session) ClaudeWaitingFor() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.claudeWaiting
}
