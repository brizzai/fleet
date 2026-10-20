// Package control is the agent-facing view of fleet: reading every session's
// state and acting on one, from a one-shot CLI process rather than the TUI.
//
// It reinvents none of the TUI's machinery. Status comes from the same
// pipeline the TUI's worker runs — the persisted row, the hook status file, a
// pane capture, Session.UpdateStatus — and liveness from the one batched tmux
// call the worker makes. What it adds is what a reader needs and the status
// classifier never did: the prompt a session is blocked on, what sits in its
// input box, and its last exchange from the agent's own transcript.
package control

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/hooks"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/tmux"
)

// PaneState is what is actually running in a session's tmux pane — the fact a
// status alone can hide. An errored session whose pane is back at a shell can
// be restarted in place; one whose tmux is gone needs a full relaunch.
type PaneState string

const (
	PaneAlive     PaneState = "alive"     // the agent is the pane's foreground process
	PaneShell     PaneState = "shell"     // the agent exited; the pane is at a shell prompt
	PaneDead      PaneState = "dead"      // the pane's process exited (kept on screen by remain-on-exit)
	PaneGone      PaneState = "gone"      // no tmux session at all
	PaneSuspended PaneState = "suspended" // fleet tore it down on purpose to free memory
)

// Observation is one session as a supervising agent sees it.
type Observation struct {
	Row     *session.SessionRow
	Session *session.Session
	Agent   agent.Type
	Status  session.Status
	// Since is when the session entered its current state, zero when unknown:
	// the turn's start while running, the hook that put it there otherwise.
	Since        time.Time
	Pane         PaneState
	PaneCmd      string
	LastActivity time.Time // the pane's last output (tmux window_activity)
	HookEvent    string
	HookAt       time.Time
	Root         string // checkout root containing the session's path
	Branch       string
	PR           *github.PR
	Slot         int // hotkey slot, -1 when unbound
	View         session.PaneView
	// Turns are the latest exchanges from the agent's transcript, oldest first
	// (Claude only). LastPrompt falls back to the hook's prompt for the others.
	Turns      []session.Turn
	LastPrompt string
	LastReply  string
	// Note names a known failure the status alone does not explain — today,
	// a resume that could not find its conversation.
	Note string
	// Exit is how a dead pane's process ended ("exit 1", "signal 9").
	Exit string
	// Capture is the raw pane the view was parsed from, empty when nothing was
	// captured (suspended, gone).
	Capture string

	first bool // this observer's first look at the session
}

// Options shapes an observation pass.
type Options struct {
	// Turns is how many transcript turns to read per session. At least one is
	// always read for a Claude session that is observed in full, since the last
	// prompt and reply come from it.
	Turns int
	// PRs is the TUI's PR cache (StateDB.LoadPRCache), keyed by checkout root.
	PRs map[string]*session.PRCacheRow
	// Slots maps hotkey slot to session id (StateDB.LoadSlotBindings).
	Slots map[int]string
}

// Observer recomputes status for a set of sessions. It keeps each Session
// across passes so a poller (`fleet wait`) gets the status pipeline's
// multi-frame state — the confirmations that only accumulate over several
// captures — exactly as the TUI's worker does.
type Observer struct {
	opts     Options
	sessions map[string]*session.Session
	hooksDir string
}

// NewObserver returns an Observer. It turns crash dumps off for the process:
// the TUI's worker owns them, and here every invocation would be a new "life"
// that dumps a dead session again.
func NewObserver(opts Options) *Observer {
	session.DisableCrashDumps()
	return &Observer{opts: opts, sessions: map[string]*session.Session{}, hooksDir: hooks.GetHooksDir()}
}

// Observe reads the given sessions' live state. Costs one `tmux list-panes`
// and one batched `capture-pane` for all of them together, plus small file
// reads per session.
func (o *Observer) Observe(rows []*session.SessionRow) []*Observation {
	tmux.RefreshSessionCache()

	slotOf := make(map[string]int, len(o.opts.Slots))
	for n, id := range o.opts.Slots {
		slotOf[id] = n
	}

	obs := make([]*Observation, 0, len(rows))
	var captureNames []string
	for _, row := range rows {
		s, first := o.session(row)
		ob := &Observation{Row: row, Session: s, Agent: s.Agent, Slot: -1, first: first}
		if n, ok := slotOf[row.ID]; ok {
			ob.Slot = n
		}
		ob.Root, ob.Branch = git.ReadHead(row.ProjectPath)
		ob.PR = o.prFor(ob.Root, row.ProjectPath, ob.Branch)

		ts := s.GetTmuxSession()
		switch {
		case s.GetStatus() == session.StatusSuspended:
			ob.Pane = PaneSuspended
		case !ts.Exists():
			ob.Pane = PaneGone
		case ts.IsPaneDead():
			ob.Pane = PaneDead
		default:
			ob.PaneCmd = tmux.PaneCurrentCommand(ts.Name)
			ob.Pane = PaneAlive
			if tmux.IsShellCommand(ob.PaneCmd) {
				ob.Pane = PaneShell
			}
			captureNames = append(captureNames, ts.Name)
		}
		if act, ok := ts.GetActivity(); ok && act > 0 {
			ob.LastActivity = time.Unix(act, 0)
		}
		obs = append(obs, ob)
	}

	captures := tmux.BatchCapturePanes(captureNames)
	for _, ob := range obs {
		ts := ob.Session.GetTmuxSession()
		if c, ok := captures[ts.Name]; ok {
			ts.SeedCapture(c)
			ob.Capture = c
		}
	}

	for _, ob := range obs {
		o.updateStatus(ob)
	}
	for _, ob := range obs {
		o.fill(ob)
	}
	return obs
}

// session returns the cached Session for row, rebuilding it when the row now
// points at a different tmux session (someone relaunched it).
func (o *Observer) session(row *session.SessionRow) (s *session.Session, first bool) {
	if s, ok := o.sessions[row.ID]; ok && s.TmuxSessionName == row.TmuxSession {
		return s, false
	}
	s = session.FromRow(row)
	o.sessions[row.ID] = s
	return s, true
}

// updateStatus runs the TUI's status pipeline: the hook file into
// UpdateHookStatus, then UpdateStatus against the seeded capture.
func (o *Observer) updateStatus(ob *Observation) {
	s := ob.Session
	if sf, err := hooks.ReadStatusFile(filepath.Join(o.hooksDir, s.ID+".json")); err == nil {
		ob.HookEvent = sf.Event
		ob.HookAt = time.Unix(sf.Timestamp, 0)
		s.UpdateHookStatus(&session.HookStatus{
			Status:          sf.Status,
			SessionID:       sf.SessionID,
			UpdatedAt:       ob.HookAt,
			UserPrompt:      sf.UserPrompt,
			PromptCount:     sf.PromptCount,
			AgentPID:        sf.AgentPID,
			Reason:          sf.Reason,
			PromptSessionID: sf.PromptSessionID,
		}, true)
	}
	if ob.first {
		// This observer has no history of the pane; tmux's activity clock stands
		// in for it, as it would after a few ticks of the TUI's worker.
		s.SeedStableContent(ob.Capture, ob.LastActivity)
	}
	s.UpdateStatus()
	ob.Status = s.GetStatus()
	// The pane, not the hook, decides "blocked on a prompt" — the rule fleet
	// send's guard already follows. Answering a prompt fires no hook, so a
	// waiting hook outlives the prompt it reported, and an observer that never
	// watched it being answered (every new CLI process) cannot tell. No prompt on
	// screen means the agent took the answer and is working. OpenCode has no pane
	// detector, so its plugin's word stands.
	if ob.Status == session.StatusWaiting && ob.Agent != agent.OpenCode && ob.Capture != "" &&
		!session.PaneIndicatesWaiting(ob.Agent, ob.Capture) {
		ob.Status = session.StatusRunning
	}
	if ob.Capture == "" && (ob.Pane == PaneAlive || ob.Pane == PaneShell) {
		// The batch skipped it; UpdateStatus captured it individually.
		ob.Capture, _ = s.GetTmuxSession().CapturePane()
	}
}

// fill adds what a reader needs beyond the status: the pane's prompt and input
// box, the last exchange, and when the current state began.
func (o *Observer) fill(ob *Observation) {
	s := ob.Session
	if ob.Capture != "" && ob.Pane == PaneAlive {
		ob.View = session.ParsePaneView(ob.Agent, ob.Capture)
	}
	if ob.Pane == PaneShell || ob.Pane == PaneDead {
		// Why the agent is gone is usually printed just before the screen it
		// left, and a failed resume clears that screen — read some scrollback.
		// Exited panes are rare, so the extra capture is too.
		history, err := s.GetTmuxSession().CaptureHistory(200)
		if err != nil {
			history = ob.Capture
		}
		ob.Note = exitNote(ob.Pane, history)
	}
	if ob.Pane == PaneDead {
		if dead, status, signal, ok := s.GetTmuxSession().PaneDeadInfo(); ok && dead {
			switch {
			case signal != "" && signal != "0":
				ob.Exit = "signal " + signal
			case status != "":
				ob.Exit = "exit " + status
			}
		}
	}

	if ob.Agent == agent.Claude {
		n := max(1, o.opts.Turns)
		turns, _ := session.ReadClaudeTurns(transcriptPath(s, ob.Row), n)
		ob.Turns = turns
		if len(turns) > 0 {
			last := turns[len(turns)-1]
			ob.LastPrompt, ob.LastReply = last.Prompt, last.Reply
		}
	}
	if ob.LastPrompt == "" {
		ob.LastPrompt = s.FirstPrompt
	}
	ob.Since = sinceFor(ob, s.GetHookStatus())
}

// transcriptPath is the Claude transcript for a session: the conversation the
// hooks say is current, falling back to the persisted id when that one has no
// transcript (yet).
func transcriptPath(s *session.Session, row *session.SessionRow) string {
	if p := session.ClaudeTranscriptPath(s.GetClaudeSessionID(), s.ProjectPath); p != "" && fileExists(p) {
		return p
	}
	return session.ClaudeTranscriptPath(row.ClaudeSessionID, row.ProjectPath)
}

// sinceFor picks when the current state began. A running Claude turn started
// at its prompt; otherwise the hook that moved the session into this state is
// the best clock there is. Zero when nothing says.
func sinceFor(ob *Observation, hookStatus string) time.Time {
	var last *session.Turn
	if len(ob.Turns) > 0 {
		last = &ob.Turns[len(ob.Turns)-1]
	}
	switch ob.Status {
	case session.StatusRunning, session.StatusStarting:
		if last != nil && !last.Done {
			return last.PromptAt
		}
		if hookStatus == "running" {
			return ob.HookAt
		}
	case session.StatusWaiting:
		if hookStatus == "waiting" {
			return ob.HookAt
		}
	case session.StatusFinished, session.StatusIdle:
		if hookStatus == "finished" {
			return ob.HookAt
		}
		if last != nil && last.Done && !last.ReplyAt.IsZero() {
			return last.ReplyAt
		}
	}
	return time.Time{}
}

// prFor returns the cached PR for the session's checkout, but only while the
// cache still describes the branch that is checked out now.
func (o *Observer) prFor(root, projectPath, branch string) *github.PR {
	for _, key := range []string{root, projectPath} {
		if row, ok := o.opts.PRs[key]; ok && row.PR != nil && (branch == "" || row.Branch == branch) {
			return row.PR
		}
	}
	return nil
}

// noConversation is Claude's refusal when `--resume <id>` names a conversation
// it has no transcript for.
const noConversation = "No conversation found with session ID"

// exitNote explains a pane whose agent is gone, from what the pane printed.
func exitNote(pane PaneState, history string) string {
	if ResumeFailed(history) {
		return "resume failed: no conversation found (fleet restart --fresh starts a new one)"
	}
	if pane == PaneShell {
		return "agent exited; pane is at a shell prompt"
	}
	return ""
}

// ResumeFailed reports whether a pane shows Claude refusing to resume.
func ResumeFailed(capture string) bool {
	return strings.Contains(session.StripANSI(capture), noConversation)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
