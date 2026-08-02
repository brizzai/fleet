package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/hooks"
	"github.com/brizzai/fleet/internal/tmux"
)

// PaneCapturer abstracts the tmux session view used by status detection, for testing.
type PaneCapturer interface {
	CapturePane() (string, error)
	IsPaneDead() bool
	GetActivity() (int64, bool) // window_activity unix ts; ok=false when unknown
}

// Status represents the current state of a session.
type Status string

const (
	StatusRunning  Status = "running"
	StatusWaiting  Status = "waiting"
	StatusFinished Status = "finished"
	StatusIdle     Status = "idle"
	StatusError    Status = "error"
	StatusStarting Status = "starting"
	// StatusSuspended is a hibernated session: its agent process and tmux session
	// have been intentionally torn down to free memory, but the conversation is
	// preserved (ClaudeSessionID) and resumes on attach via Restart(). It is a
	// resting state with no live process, so the status pipeline must not touch it.
	StatusSuspended Status = "suspended"
)

// Session represents a managed Claude Code session.
type Session struct {
	ID                   string
	Title                string
	ProjectPath          string
	Agent                agent.Type // which coding agent runs in this session (claude/codex)
	Status               Status
	TmuxSessionName      string
	CreatedAt            time.Time
	LastAccessedAt       time.Time
	Acknowledged         bool
	ClaudeSessionID      string
	AgentSessionName     string    // Transient: the agent's own title (Claude JSONL / Codex state DB), not persisted to SQLite.
	AgentNameLastChecked time.Time // Transient: last time we read the agent's title.
	WorkspaceName        string
	ManuallyRenamed      bool
	FirstPrompt          string
	TitleGenerated       bool
	PromptCount          int
	ForkFromID           string // Transient: if set, start with --resume <id> --fork-session (cleared after start)

	// snoozedUntil mutes this session from the attention surfaces (Space jump,
	// status pills) until the deadline passes. Deliberately NOT a Status: a
	// snoozed session keeps running and keeps its real status — snooze is a
	// lens over the attention surfaces, not a state of the session itself.
	// Guarded by mu (written by the worker's wake sweep, read while rendering).
	snoozedUntil time.Time

	hookStatus       string
	hookUpdatedAt    time.Time
	hookOverriddenAt time.Time // timestamp of hook that was overridden by pane; prevents re-evaluation of same stale hook
	ownerSessionID   string    // Claude session_id that owns this fleet session; hooks from other (nested) Claudes are ignored
	forkParentID     string    // parent's claude session id while this fork hasn't diverged yet; lets us adopt the fork's own id deterministically (cleared on divergence)

	// Negative cache for the rotation check: the last (owner, foreign) pair that
	// sessionRotationVerdict rejected. A persistent foreign id (a nested-child eval
	// harness) would otherwise rescan transcripts every worker cycle; once
	// rejected we short-circuit until the owner or the foreign id changes.
	rotRejectOwner   string
	rotRejectForeign string
	// When we last logged that this pair is still being rejected. The rejection
	// itself is logged once, on the cycle it is cached — but a neg-cached pair then
	// drops every subsequent hook silently, so a session whose hook layer is dead
	// looks identical in the log to one that is simply quiet. Re-announcing it on a
	// throttle is what makes "the hook is frozen because we rejected its id"
	// greppable instead of a whole-pipeline trace.
	rotRejectLoggedAt time.Time
	// When we last paid the transcript I/O to ask whether a rejected pair has become
	// recoverable (ownerConversationSuperseded). The neg-cache exists precisely to
	// keep that scan off the ~500ms worker cycle, so the recovery check rides a
	// slower clock of its own rather than reopening the cost the cache saved.
	rotRejectCheckedAt time.Time

	// Bounded retry tracking for an undecidable (owner, foreign) pair — one
	// sessionRotationVerdict can't yet classify because a transcript hasn't flushed a
	// timestamp. The transient /clear-flush race clears in a cycle or two, but a
	// permanently unreadable/missing owner transcript would stay undecidable forever
	// and rescan transcripts every worker pass. After rotationUndecidedRetryCap
	// consecutive undecided cycles we fall back to the neg-cache.
	rotUndecidedOwner   string
	rotUndecidedForeign string
	rotUndecidedCount   int

	lastContentHash     string
	lastContentChangeAt time.Time

	// waitingPaneConfirmed records that we've structurally observed the permission
	// prompt on screen (detectStatus returned StatusWaiting) for the current waiting
	// hook. Until that happens, a pane that reads "running" is the prompt's own render
	// spinner, not Claude resuming — so we don't trust it to flip waiting→running.
	// Reset whenever a new hook arrives (see UpdateHookStatus).
	waitingPaneConfirmed bool

	deathRecorded bool // crash dump already written for the current life of this session; reset by Restart

	// Transcript tiebreaker cache. conversationActivePastHook runs on the ~500ms fast
	// pass for a session held running through a long turn, and lastLeadTranscriptTimestamp
	// is a full JSONL scan of a growing file. Skip the scan when the transcript is
	// unchanged since the last read (path+size+mtime match). Guarded by s.mu.
	convTranscriptPath  string
	convTranscriptSize  int64
	convTranscriptMtime time.Time
	convLeadTimestamp   time.Time

	tmuxSession  *tmux.Session
	paneCapturer PaneCapturer // optional override for testing; if nil, uses tmuxSession
	convActiveFn func() bool  // optional override for testing; if nil, reads the real transcript
	mu           sync.RWMutex
}

// NewSession creates a new session for the given project path.
func NewSession(title, projectPath string) *Session {
	id := generateID()
	ts := tmux.NewSession(title, projectPath)

	return &Session{
		ID:              id,
		Title:           title,
		ProjectPath:     projectPath,
		Status:          StatusIdle,
		TmuxSessionName: ts.Name,
		CreatedAt:       time.Now(),
		tmuxSession:     ts,
	}
}

// buildAgentCmd returns the launch command for this session's agent, with
// optional resume/fork details. ClaudeSessionID stores the agent's own
// conversation id (Claude or Codex) captured from hooks.
func (s *Session) buildAgentCmd() string {
	return agent.Parse(string(s.Agent)).BuildLaunchCmd(agent.LaunchOpts{
		ResumeID: s.ClaudeSessionID,
		ForkID:   s.ForkFromID,
	})
}

// initialRunStatus is the status to show right after launching the agent.
// Codex and OpenCode fire no event until their first turn, so they start idle
// (sitting at their prompt) rather than flashing running.
func (s *Session) initialRunStatus() Status {
	if s.Agent == agent.Codex || s.Agent == agent.OpenCode {
		return StatusIdle
	}
	return StatusRunning
}

// sessionEnv returns the env vars to set on the tmux session for this fleet session.
func (s *Session) sessionEnv() []string {
	return []string{
		fmt.Sprintf("FLEET_INSTANCE_ID=%s", s.ID),
		"ZSH_DOTENV_PROMPT=false", // Auto-source .env without prompting (oh-my-zsh dotenv plugin).
	}
}

// Start launches the Claude Code session in tmux.
func (s *Session) Start() error {
	debuglog.Logger.Info("session start", "id", s.ID, "title", s.Title, "path", s.ProjectPath)
	s.mu.Lock()
	s.Status = StatusStarting
	s.mu.Unlock()

	cmd := s.buildAgentCmd()
	if err := s.tmuxSession.Start(cmd, s.sessionEnv()...); err != nil {
		s.mu.Lock()
		s.Status = StatusError
		s.mu.Unlock()
		debuglog.Logger.Error("session start failed", "id", s.ID, "title", s.Title, "err", err)
		return err
	}

	s.mu.Lock()
	s.Status = s.initialRunStatus()
	// Remember the fork parent: a `claude --resume <parent> --fork-session` reports
	// the PARENT's id at SessionStart and only switches to its own id once it
	// diverges (first prompt). Keeping the parent id lets UpdateHookStatus adopt the
	// fork's own id deterministically when it appears, instead of relying on the
	// transcript heuristic (which can't see a fork). Cleared on divergence.
	s.forkParentID = s.ForkFromID
	s.ForkFromID = "" // Clear after first start so restarts use session's own ClaudeSessionID.
	s.mu.Unlock()
	debuglog.Logger.Info("session started", "id", s.ID, "title", s.Title)
	return nil
}

// Kill terminates the tmux session.
func (s *Session) Kill() error {
	debuglog.Logger.Info("session kill", "id", s.ID, "title", s.Title)
	err := s.tmuxSession.Kill()
	s.mu.Lock()
	s.Status = StatusError
	s.mu.Unlock()
	return err
}

// GetStatus returns the current status (thread-safe).
func (s *Session) GetStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Status
}

// SetStatus sets the status (thread-safe). Clears Acknowledged on active states.
func (s *Session) SetStatus(status Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = status
	if status == StatusRunning || status == StatusWaiting {
		s.Acknowledged = false
	}
}

// SnoozedUntil returns this session's own snooze deadline (thread-safe). The
// zero time means not snoozed. Note this is the session's *own* snooze — a
// session may also be muted by an umbrella snooze on its repo group, which
// lives in the UI layer since it isn't a property of the session.
func (s *Session) SnoozedUntil() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snoozedUntil
}

// SetSnoozedUntil sets (or, on the zero time, clears) the snooze deadline.
// Status is untouched by design.
func (s *Session) SetSnoozedUntil(until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snoozedUntil = until
}

// IsSnoozed reports whether the session's own snooze is still in effect at now.
func (s *Session) IsSnoozed(now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.snoozedUntil.IsZero() && s.snoozedUntil.After(now)
}

// GetHookStatus returns the raw hook status string (thread-safe).
func (s *Session) GetHookStatus() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hookStatus
}

// getCapturer returns the pane capturer (test override or real tmux session).
func (s *Session) getCapturer() PaneCapturer {
	if s.paneCapturer != nil {
		return s.paneCapturer
	}
	return s.tmuxSession
}

// conversationActivePastHook reports whether the Claude conversation transcript
// shows lead-turn activity that resumed after the current waiting hook — the
// out-of-pane tiebreaker for the between-bursts frame where the pane is
// indistinguishable from a finished idle prompt. Claude only; Codex has no Claude
// transcript. MUST NOT be called while holding s.mu — it does disk I/O.
func (s *Session) conversationActivePastHook() bool {
	if s.convActiveFn != nil {
		return s.convActiveFn()
	}
	s.mu.RLock()
	agentType := s.Agent
	claudeID := s.ClaudeSessionID
	projectPath := s.ProjectPath
	hookUpdatedAt := s.hookUpdatedAt
	cachePath := s.convTranscriptPath
	cacheSize := s.convTranscriptSize
	cacheMtime := s.convTranscriptMtime
	cacheTS := s.convLeadTimestamp
	s.mu.RUnlock()

	if agentType == agent.Codex || claudeID == "" {
		return false
	}

	path := ClaudeTranscriptPath(claudeID, projectPath)
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}

	// Skip the full JSONL scan when the transcript is unchanged since the last read.
	// os.Stat is a cheap syscall; the scan only runs when the file actually grew.
	var last time.Time
	if path == cachePath && fi.Size() == cacheSize && fi.ModTime().Equal(cacheMtime) {
		last = cacheTS
	} else {
		last = lastLeadTranscriptTimestamp(path)
		s.mu.Lock()
		s.convTranscriptPath = path
		s.convTranscriptSize = fi.Size()
		s.convTranscriptMtime = fi.ModTime()
		s.convLeadTimestamp = last
		s.mu.Unlock()
	}
	if last.IsZero() {
		return false
	}
	// Advanced past the hook (the agent did something after the prompt fired) AND
	// still fresh (recently appending → mid-turn, not a finished-with-dropped-Stop).
	return last.After(hookUpdatedAt.Add(conversationActiveSkew)) &&
		time.Since(last) < conversationActiveWindow
}

// IsAlive checks if the tmux session exists.
func (s *Session) IsAlive() bool {
	if s.paneCapturer != nil {
		return !s.paneCapturer.IsPaneDead()
	}
	return s.tmuxSession.Exists()
}

// GetTmuxSession returns the underlying tmux session handle.
func (s *Session) GetTmuxSession() *tmux.Session {
	return s.tmuxSession
}

// MarkAccessed updates the last accessed timestamp.
func (s *Session) MarkAccessed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastAccessedAt = time.Now()
}

// Acknowledge marks the session as seen by the user.
func (s *Session) Acknowledge() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Acknowledged = true
	if s.Status == StatusFinished {
		s.Status = StatusIdle
	}
}

// MarkUnread flags a session as needing attention again (Idle → Finished),
// the inverse of Acknowledge. Only meaningful for a session at rest.
func (s *Session) MarkUnread() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Acknowledged = false
	if s.Status == StatusIdle {
		s.Status = StatusFinished
	}
}

// IdleFor reports how long the session has shown no sign of life — the time since
// the most recent of: last hook event (agent), last pane-content change (output),
// last user access, or creation. Used to pick the most-idle sessions for
// memory-pressure hibernation. Cheap: reads in-memory timestamps only, no I/O.
func (s *Session) IdleFor() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	last := s.CreatedAt // floor: a session can't be more idle than its age
	for _, t := range []time.Time{s.hookUpdatedAt, s.lastContentChangeAt, s.LastAccessedAt} {
		if t.After(last) {
			last = t
		}
	}
	if last.IsZero() {
		return 0
	}
	return time.Since(last)
}

// HookStatus holds decoded status from a hook status file.
// Defined here to avoid import cycle with hooks package.
type HookStatus struct {
	Status      string
	SessionID   string // Claude conversation session ID
	UpdatedAt   time.Time
	UserPrompt  string
	PromptCount int
}

// UpdateHookStatus updates the session's hook-based status.
// Returns true if the hook meaningfully changed (new status or new timestamp),
// so callers can prioritize an immediate status recomputation.
//
// resolveRotation gates the transcript I/O in sessionRotationVerdict. The worker
// passes true (it runs off the Bubble Tea Update() loop); the UI paths
// (hookChangedMsg/statusUpdateMsg) pass false so a foreign id can't block the
// render loop on disk reads — they simply defer it to the next worker cycle
// (~500ms), which adopts it.
func (s *Session) UpdateHookStatus(hs *HookStatus, resolveRotation bool) bool {
	if hs == nil {
		return false
	}

	// Ignore hooks while suspended. Killing the agent to hibernate fires a
	// SessionEnd ("dead") a beat after clearHookState(); storing it would leave a
	// stale dead hook that resurfaces the moment we resume. Resume goes through
	// Restart() (→ StatusStarting), so genuine post-resume hooks are accepted again.
	if s.GetStatus() == StatusSuspended {
		return false
	}

	// Resolve ownership for a non-owner session_id BEFORE taking s.mu — the
	// rotation check reads Claude transcripts and must not run under the lock.
	// Rare path: only fires when an id we don't already own reports.
	//
	// A different Claude session_id means one of two things:
	//   - Claude rotated its session id (/clear, continue, resume, fork). A
	//     legitimate handoff: we MUST adopt the new id, or the in-memory hook
	//     freezes at the old session's last event (e.g. a stale "waiting") and
	//     the resume id / auto-name go stale.
	//   - A nested child `claude` (an eval harness spawning sub-Claudes)
	//     inherited FLEET_INSTANCE_ID. Adopting it would clobber our status and
	//     resume id, so it must be ignored.
	// sessionRotationVerdict distinguishes them; a persistent rejection is cached so
	// a nested child doesn't rescan transcripts every cycle.
	var owner string
	if hs.SessionID != "" {
		s.mu.RLock()
		owner = s.ownerSessionID
		forkParent := s.forkParentID
		projectPath := s.ProjectPath
		negCached := owner != "" && s.rotRejectOwner == owner && s.rotRejectForeign == hs.SessionID
		frozenHook, frozenHookAt := s.hookStatus, s.hookUpdatedAt
		s.mu.RUnlock()
		if owner != "" && hs.SessionID != owner {
			switch {
			case forkParent != "" && owner == forkParent:
				// This session was forked (`claude --resume <parent> --fork-session`).
				// Its SessionStart hook reports the PARENT's id; the fork's own id only
				// appears once it diverges (first prompt). That first id different from
				// the parent IS this fork's own session, so adopt it directly. We can't
				// use sessionRotationVerdict here: fork transcripts carry no logicalParentUuid,
				// and they copy the parent's history verbatim (with the parent's original
				// timestamps), so the timestamp-handoff check measures the parent's whole
				// conversation length, not a handoff gap, and misses for any parent older
				// than ~10s — leaving the fork pinned to the parent id (so forking it
				// re-forks the parent). The window where we own the parent id is before
				// the fork's first turn; clearHookState() clears forkParentID so a restart
				// can't re-open it. The residual gap — a nested child `claude` that fires a
				// hook inside that sub-second pre-divergence window — is narrow enough that
				// we don't pay transcript I/O here to verify descent.
				debuglog.Logger.Info("adopting forked claude session",
					"id", s.ID, "parent", owner, "new", hs.SessionID)
			case negCached:
				// Normally this pair stays rejected for the session's whole life — right
				// for a nested child, wrong for a rotation we simply could not prove.
				// Nothing else releases the owner (an in-process rotation fires no
				// SessionEnd for the id it abandons), so recheck on a slow clock whether
				// the owner conversation has gone silent while this one keeps writing.
				if resolveRotation && s.dueForDeadOwnerRecheck() &&
					ownerConversationSuperseded(projectPath, owner, hs.SessionID) {
					debuglog.Logger.Info("adopting superseded claude session",
						"id", s.ID, "old", owner, "new", hs.SessionID,
						"frozenHookAge", time.Since(frozenHookAt).Truncate(time.Second))
					break // recovered — fall through to the adoption block below
				}
				// Re-announce the rejection on a throttle: this branch is the steady
				// state of a session whose hook layer has gone dead, and dropping every
				// hook in silence is what made that state cost a full pipeline trace to
				// identify.
				// Check and stamp under ONE lock: the worker and the UI path both reach
				// here (app.go calls syncHookStatuses from each), so a snapshot-then-write
				// split lets both pass the interval check and log the same drop twice.
				now := time.Now()
				shouldLog := false
				s.mu.Lock()
				if now.Sub(s.rotRejectLoggedAt) >= rotRejectLogInterval {
					s.rotRejectLoggedAt = now
					shouldLog = true
				}
				s.mu.Unlock()
				if shouldLog {
					// frozenHookAge is the diagnostic: it is the age of the hook we are
					// KEEPING, not of the one we just dropped. A large value next to a
					// fresh dropped status is the signature of this failure.
					debuglog.Logger.Info("hook dropped: claude session id still rejected",
						"id", s.ID, "owner", owner, "foreign", hs.SessionID,
						"dropped", hs.Status, "frozenHook", frozenHook,
						"frozenHookAge", time.Since(frozenHookAt).Truncate(time.Second))
				}
				return false
			case !resolveRotation:
				// UI path: the rotation check reads transcripts off disk, which must not
				// block the render loop. Defer to the next worker cycle (~500ms), which
				// re-evaluates with resolveRotation=true.
				return false
			default:
				// Only a confirmed rotation (rotationYes) may proceed to the adoption
				// block below — every other verdict returns here. Keep this an explicit
				// guard, not a fall-through-by-not-returning: a stray statement after this
				// block or a reordered case must not be able to silently route an adopted
				// rotation to a no-op (the hook would stay frozen on the dead owner id).
				if v := sessionRotationVerdict(projectPath, owner, hs.SessionID); v != rotationYes {
					if v == rotationUnknown && s.bumpUndecidedRotation(owner, hs.SessionID) {
						// The rotated transcript hasn't flushed a timestamped entry yet (its
						// SessionStart hook beat the first user turn to disk). Reject WITHOUT
						// neg-caching so the next cycle re-evaluates once it flushes — neg-caching
						// here would freeze the hook on the dead owner session forever (issue #23).
						debuglog.Logger.Info("deferring undecided claude session",
							"id", s.ID, "owner", owner, "foreign", hs.SessionID)
						return false
					}
					// rotationNo (a genuine nested-child eval harness), or a pair that has
					// stayed undecidable past the retry cap (e.g. a permanently unreadable
					// owner transcript). Neg-cache so a persistent foreign id doesn't rescan
					// transcripts every cycle.
					s.negCacheRotation(owner, hs.SessionID)
					debuglog.Logger.Info("ignoring foreign claude session",
						"id", s.ID, "owner", owner, "foreign", hs.SessionID)
					return false
				}
				debuglog.Logger.Info("adopting rotated claude session",
					"id", s.ID, "old", owner, "new", hs.SessionID)
				// proceed to the adoption block below
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Claim ownership (first hook after launch), adopt a verified rotation, or
	// no-op for the current owner. Re-validate under the write lock: the worker
	// runs syncHookStatuses WITHOUT workerMu (app.go), so it races the UI path —
	// ownership may have changed since the snapshot above. If it changed to a
	// different non-empty id, drop rather than clobber it; the next cycle
	// re-evaluates. (This re-check is load-bearing, not just defensive.)
	if hs.SessionID != "" {
		if cur := s.ownerSessionID; cur != "" && cur != owner && cur != hs.SessionID {
			debuglog.Logger.Debug("owner changed under lock; dropping hook",
				"id", s.ID, "snapshot", owner, "current", cur, "hook", hs.SessionID)
			return false
		}
		s.ownerSessionID = hs.SessionID
		// Once we own an id other than the fork parent, the fork has diverged to its
		// own session — drop the parent link so it can't influence later hooks.
		if s.forkParentID != "" && hs.SessionID != s.forkParentID {
			s.forkParentID = ""
		}
	}

	changed := s.hookStatus != hs.Status || s.hookUpdatedAt != hs.UpdatedAt
	// Reset tracking when hook genuinely changes (new event or new status).
	if changed {
		s.lastContentHash = ""
		s.lastContentChangeAt = time.Time{}
		s.hookOverriddenAt = time.Time{} // allow fresh evaluation of new hook
		s.waitingPaneConfirmed = false   // re-observe the prompt before trusting a pane spinner
		// A non-dead hook means Claude is alive again. Re-arm the crash-dump
		// trigger so the NEXT real death gets a dump even if a prior false
		// transition (e.g. brief stale-hook flash before this fresh hook
		// landed) already consumed the once-per-life dump quota.
		if hs.Status != "" && hs.Status != "dead" {
			s.deathRecorded = false
		}
	}
	s.hookStatus = hs.Status
	s.hookUpdatedAt = hs.UpdatedAt
	if hs.SessionID != "" {
		s.ClaudeSessionID = hs.SessionID
	}
	// Owner ended — release ownership so the next Claude (resume, /clear, or an
	// in-pane relaunch) can claim it.
	if hs.Status == "dead" && hs.SessionID == s.ownerSessionID {
		s.ownerSessionID = ""
	}
	// Track user prompts for auto-naming (always update to latest).
	if hs.PromptCount > s.PromptCount {
		s.PromptCount = hs.PromptCount
		if hs.UserPrompt != "" {
			s.FirstPrompt = hs.UserPrompt
		}
	} else if hs.UserPrompt != "" && s.FirstPrompt == "" {
		s.FirstPrompt = hs.UserPrompt
	}
	return changed
}

// rotationUndecidedRetryCap bounds how many consecutive worker cycles a single
// (owner, foreign) pair may stay undecidable in sessionRotationVerdict before we give
// up and neg-cache it. The transient /clear-flush race clears in a cycle or two, so the
// happy path never approaches the cap; it only catches a pair that can never decide
// (e.g. a permanently unreadable owner transcript) and would otherwise rescan
// transcripts on every pass forever.
const rotationUndecidedRetryCap = 10

// rotRejectLogInterval throttles the "hook dropped" line for an already-rejected
// (owner, foreign) pair. The worker re-offers the same hook every fast cycle
// (~500ms), so this must not log per attempt — but the state can persist for a
// session's whole life, so it must not log only once either: debug.log rotates,
// and the one line explaining a dead hook layer is exactly the line that gets lost.
const rotRejectLogInterval = time.Minute

// bumpUndecidedRotation records that the (owner, foreign) pair was undecidable this
// cycle and reports whether to keep deferring. It returns false once the pair has been
// undecidable for rotationUndecidedRetryCap consecutive cycles, signalling the caller to
// neg-cache instead. A change of pair resets the counter.
func (s *Session) bumpUndecidedRotation(owner, foreign string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rotUndecidedOwner != owner || s.rotUndecidedForeign != foreign {
		s.rotUndecidedOwner = owner
		s.rotUndecidedForeign = foreign
		s.rotUndecidedCount = 0
	}
	s.rotUndecidedCount++
	return s.rotUndecidedCount <= rotationUndecidedRetryCap
}

// deadOwnerRecheckInterval throttles the ownerConversationSuperseded scan for a
// neg-cached pair. The state it recovers from has already lasted minutes by the
// time it is reachable at all (deadOwnerSupersedeMargin), so checking once a
// minute costs nothing and loses nothing.
const deadOwnerRecheckInterval = time.Minute

// dueForDeadOwnerRecheck reports whether enough time has passed to re-examine a
// neg-cached rejection, stamping the clock when it does. Check and stamp under ONE
// lock: the worker calls this on every fast cycle, and a snapshot-then-write split
// would let two cycles both pass and pay the scan twice.
func (s *Session) dueForDeadOwnerRecheck() bool {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Sub(s.rotRejectCheckedAt) < deadOwnerRecheckInterval {
		return false
	}
	s.rotRejectCheckedAt = now
	return true
}

// negCacheRotation records the (owner, foreign) pair as a confirmed non-rotation so
// future hooks for it short-circuit without rescanning transcripts, and clears any
// undecided-retry tracking for it.
func (s *Session) negCacheRotation(owner, foreign string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rotRejectOwner = owner
	s.rotRejectForeign = foreign
	// Stamp the throttle for the NEW pair. The caller logs "ignoring foreign claude
	// session" on this same cycle, so the first negCached hit that follows would only
	// repeat it; starting the interval here makes the throttled line a pure
	// still-happening heartbeat. Stamping per pair also stops a pair rejected moments
	// after another from inheriting the previous pair's spent interval — that silence
	// would land squarely in the window where a user hits `!` and files a report.
	s.rotRejectLoggedAt = time.Now()
	// Same for the recovery clock: a pair rejected this instant cannot yet show a
	// superseded owner (the margin is minutes wide), so start its interval here
	// rather than letting the very next cycle spend a transcript scan proving it.
	s.rotRejectCheckedAt = time.Now()
	s.rotUndecidedOwner = ""
	s.rotUndecidedForeign = ""
	s.rotUndecidedCount = 0
}

// Restart kills and recreates the tmux session with the same config.
func (s *Session) Restart() error {
	debuglog.Logger.Info("session restart", "id", s.ID, "title", s.Title)
	// Kill old tmux session if it still exists.
	if s.tmuxSession.Exists() {
		_ = s.tmuxSession.Kill()
	}

	// Drop the previous Claude's hook state. Without this, fleet's status worker
	// can read the old file's status=dead before the new Claude has fired any
	// hook event, which flips the freshly-restarted session straight back to
	// error and triggers a misleading crash dump.
	s.clearHookState()

	// Recreate tmux session with same config. Mutate s.tmuxSession under
	// s.mu so concurrent triggerCrashDump readers see a consistent pointer.
	newTmux := tmux.NewSession(s.Title, s.ProjectPath)
	s.mu.Lock()
	s.tmuxSession = newTmux
	s.TmuxSessionName = newTmux.Name
	s.Status = StatusStarting
	s.deathRecorded = false
	s.mu.Unlock()

	cmd := s.buildAgentCmd()
	if err := newTmux.Start(cmd, s.sessionEnv()...); err != nil {
		s.mu.Lock()
		s.Status = StatusError
		s.mu.Unlock()
		debuglog.Logger.Error("session restart failed", "id", s.ID, "title", s.Title, "err", err)
		return err
	}

	s.mu.Lock()
	s.Status = s.initialRunStatus()
	s.mu.Unlock()
	debuglog.Logger.Info("session restarted", "id", s.ID, "title", s.Title)
	return nil
}

// RespawnClaude restarts the claude process in an existing tmux session.
func (s *Session) RespawnClaude() error {
	resuming := s.ClaudeSessionID != ""
	debuglog.Logger.Info("session respawn", "id", s.ID, "title", s.Title, "resuming", resuming)
	s.clearHookState()
	s.mu.Lock()
	s.Status = StatusStarting
	s.deathRecorded = false
	s.mu.Unlock()

	cmd := s.buildAgentCmd()
	if err := s.tmuxSession.RespawnPane(cmd, s.sessionEnv()...); err != nil {
		s.mu.Lock()
		s.Status = StatusError
		s.mu.Unlock()
		debuglog.Logger.Error("session respawn failed", "id", s.ID, "title", s.Title, "err", err)
		return err
	}

	// Reset to a baseline status bar; the UI worker will re-apply with
	// active fleet theme + live state on the next tick.
	s.tmuxSession.ApplyStatusBar(tmux.StatusBarOpts{})

	s.mu.Lock()
	s.Status = s.initialRunStatus()
	s.mu.Unlock()
	debuglog.Logger.Info("session respawned", "id", s.ID, "title", s.Title)
	return nil
}

// Suspend hibernates the session to free memory: it tears down the agent process
// and the whole tmux session (freeing the tmux server's pane buffers too), and
// marks the session StatusSuspended. The conversation survives — ClaudeSessionID
// is persisted, so a later Restart() resumes it via `--resume <id>`. This is the
// teardown half of Restart() with no recreate. Idempotent for an already-dead pane.
func (s *Session) Suspend() error {
	debuglog.Logger.Info("session suspend", "id", s.ID, "title", s.Title)
	// Mark suspended BEFORE tearing down tmux. The status pipeline short-circuits
	// on StatusSuspended, so a concurrent UpdateStatus can't flip the row to error
	// via its liveness gates, and UpdateHookStatus won't store the killed agent's
	// "dead" death-rattle, during teardown. Restore the prior status if the kill
	// fails (the session is still alive, so it must not read as suspended).
	oldStatus := s.GetStatus()
	s.SetStatus(StatusSuspended)
	s.clearHookState()
	if s.tmuxSession.Exists() {
		if err := s.tmuxSession.Kill(); err != nil {
			s.SetStatus(oldStatus)
			debuglog.Logger.Error("session suspend: kill failed", "id", s.ID, "title", s.Title, "err", err)
			return err
		}
	}
	return nil
}

// GetClaudeSessionID returns the captured resume id (thread-safe). Empty until the
// agent's first SessionStart hook records it.
func (s *Session) GetClaudeSessionID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ClaudeSessionID
}

// ResolveForkParent returns the conversation id a fork of this session should
// resume, plus the stale id it replaced when one was healed ("" when nothing was
// wrong). Read-only: the source session heals itself through UpdateHookStatus on
// its next hook.
//
// Normally the answer is just ClaudeSessionID. It is not when a foreign id is
// neg-cached against the owner: the pane may be in that other conversation while
// ClaudeSessionID still names the one we froze on, and a fork of a dead id silently
// copies a conversation the user stopped working in an hour ago (issue #226). The
// worker's own recovery can't be relied on here — it only reruns when a hook
// arrives, and a session that rotated and then went quiet fires none.
//
// Does transcript I/O. Call it off the Bubble Tea Update loop.
func (s *Session) ResolveForkParent() (parentID, healedFrom string) {
	s.mu.RLock()
	stored, owner, rejected := s.ClaudeSessionID, s.ownerSessionID, s.rotRejectForeign
	rejectedAgainst, projectPath := s.rotRejectOwner, s.ProjectPath
	s.mu.RUnlock()

	// Only a rejection standing against the *current* owner says anything about the
	// id we are about to fork.
	if rejected == "" || rejected == stored || owner == "" || rejectedAgainst != owner || stored != owner {
		return stored, ""
	}
	if !ownerConversationSuperseded(projectPath, owner, rejected) {
		return stored, ""
	}
	return rejected, stored
}

// clearHookState drops the previous Claude process's hook state — both the
// in-memory cache and the on-disk status file at
// ~/.config/fleet/hooks/<id>.json. Called before relaunching Claude so the
// worker doesn't trust the dead Claude's last hook ("dead", "waiting", etc.)
// during the gap between tmux respawn and the new Claude's first hook event.
func (s *Session) clearHookState() {
	s.mu.Lock()
	s.hookStatus = ""
	s.hookUpdatedAt = time.Time{}
	s.hookOverriddenAt = time.Time{}
	s.ownerSessionID = ""
	s.rotRejectOwner = ""
	s.rotRejectForeign = ""
	// Zero the throttle with the pair it belongs to, so a restart re-arms the
	// "hook dropped" line instead of inheriting a spent interval from the old pair.
	s.rotRejectLoggedAt = time.Time{}
	s.rotRejectCheckedAt = time.Time{}
	// Clear the fork parent link too. Restart()/RespawnClaude() call this without
	// going through Start() (the only place forkParentID is set), so without this an
	// un-diverged fork that resumes the parent id would re-claim owner==forkParent on
	// its next SessionStart and re-arm the unconditional fork-adoption branch — the
	// next foreign hook (incl. a nested child) would then be force-adopted.
	s.forkParentID = ""
	s.mu.Unlock()
	hookFile := filepath.Join(hooks.GetHooksDir(), s.ID+".json")
	if err := os.Remove(hookFile); err != nil && !os.IsNotExist(err) {
		debuglog.Logger.Warn("session: clear hook file failed", "id", s.ID, "path", hookFile, "err", err)
	}
}

// UpdateStatus detects the session status from pane content.
func (s *Session) UpdateStatus() {
	log := debuglog.Logger.With("session", s.ID, "title", s.Title)
	oldStatus := s.GetStatus()

	// Suspended is a terminal resting state we entered on purpose (tmux killed to
	// free memory). Its tmux is gone, so the liveness gates below would flip it to
	// error and write a spurious crash dump every cycle. Leave it untouched until a
	// resume (Restart) moves it to StatusStarting.
	if oldStatus == StatusSuspended {
		return
	}

	if !s.IsAlive() {
		s.SetStatus(StatusError)
		log.Debug("status: not alive", "old", oldStatus, "new", StatusError)
		s.triggerCrashDump("tmux_gone")
		return
	}

	// Check if the pane's process has died (tmux alive but process crashed).
	if s.getCapturer().IsPaneDead() {
		s.SetStatus(StatusError)
		log.Debug("status: pane dead", "old", oldStatus, "new", StatusError)
		s.triggerCrashDump("pane_dead")
		return
	}

	// Hook fast path: hooks are authoritative as long as the session is alive.
	// No time-based expiry — IsAlive/IsPaneDead above handle stale scenarios.
	s.mu.RLock()
	hookStatus := s.hookStatus
	hookAge := time.Since(s.hookUpdatedAt)
	hasHook := hookStatus != "" && !s.hookUpdatedAt.IsZero()
	s.mu.RUnlock()

	// Codex status is hook-driven, but we do NOT run Claude's pane heuristics on
	// it. Codex's pane is authoritative when it shows a definite state: its hooks
	// are incomplete (a wait/approval prompt and a permission approval both fire
	// no hook), so the latest hook can be stale. A working footer (paneRunning)
	// or a wait prompt (paneWaiting) overrides the hook; only an at-rest pane
	// lets the hook decide running/finished/idle. With no hook at all, an at-rest
	// pane settles to idle.
	//
	// OpenCode is purely plugin-driven: its status plugin reports running/waiting/
	// finished authoritatively for the root session (sub-agents are filtered out),
	// including the waiting→running transition after a permission reply, so the
	// hook is always trusted and no pane scraping is needed. Before the first
	// event there is no hook — the session sits idle at its prompt.
	if s.Agent == agent.OpenCode {
		if !hasHook {
			if oldStatus != StatusIdle {
				s.SetStatus(StatusIdle)
				log.Info("status changed (opencode no-hook)", "old", oldStatus, "new", StatusIdle)
			}
			return
		}
		s.applyHookStatus(oldStatus, hookStatus, log)
		return
	}

	if s.Agent == agent.Codex {
		paneWaiting, paneRunning := false, false
		captured := false
		if content, err := s.getCapturer().CapturePane(); err == nil {
			clean := StripANSI(content)
			paneWaiting = codexPaneWaiting(clean)
			paneRunning = codexPaneRunning(clean)
			captured = true
		} else {
			log.Warn("codex pane capture failed", "err", err)
		}
		// A transient capture failure must not downgrade an active session: with
		// no pane signal and no hook to fall back on, preserve the current status.
		if !captured && !hasHook {
			return
		}
		switch {
		case paneWaiting:
			if oldStatus != StatusWaiting {
				s.SetStatus(StatusWaiting)
				log.Info("status changed (codex pane)", "old", oldStatus, "new", StatusWaiting)
			}
		case paneRunning:
			if oldStatus != StatusRunning {
				s.SetStatus(StatusRunning)
				log.Info("status changed (codex pane)", "old", oldStatus, "new", StatusRunning)
			}
		case hasHook:
			// Pane at rest — it can't tell running from finished from idle at the
			// prompt, so the hook decides.
			s.applyHookStatus(oldStatus, hookStatus, log)
		case oldStatus != StatusIdle:
			// At its prompt with no active turn and no hook — settle to idle.
			s.SetStatus(StatusIdle)
			log.Info("status changed (codex no-hook)", "old", oldStatus, "new", StatusIdle)
		}
		return
	}

	if hasHook {
		s.updateStatusFromHook(oldStatus, hookStatus, hookAge, log)
		return
	}

	s.updateStatusFromPane(oldStatus, log)
}

// applyHookStatus maps an authoritative hook/plugin status string
// (running/waiting/finished/dead) onto the session, honoring the acknowledged
// flag (finished→idle once acknowledged) and triggering a crash dump on dead.
// Used by Codex (when its pane is at rest, the hook decides) and OpenCode (which
// is purely plugin-driven with no pane fallback).
func (s *Session) applyHookStatus(oldStatus Status, hookStatus string, log *slog.Logger) {
	hookSaysDead := false
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch hookStatus {
		case "running":
			s.Status = StatusRunning
			s.Acknowledged = false
		case "waiting":
			s.Status = StatusWaiting
			s.Acknowledged = false
		case "finished":
			if s.Acknowledged {
				s.Status = StatusIdle
			} else {
				s.Status = StatusFinished
			}
		case "error":
			// A recoverable in-session error (OpenCode session.error) that didn't
			// kill the pane. Self-corrects on the next running/finished hook.
			s.Status = StatusError
			s.Acknowledged = false
		case "dead":
			s.Status = StatusError
			hookSaysDead = true
		}
		if s.Status != oldStatus {
			log.Info("status changed (agent hook)", "old", oldStatus, "new", s.Status, "hookStatus", hookStatus)
		}
	}()

	if hookSaysDead {
		s.triggerCrashDump("hook_dead")
	}
}

// updateStatusFromHook applies hook-based status with pane overrides.
func (s *Session) updateStatusFromHook(oldStatus Status, hookStatus string, hookAge time.Duration, log *slog.Logger) {
	// Capture pane once for content change detection and pane-based overrides.
	var paneContent string
	var paneStatus Status
	if content, err := s.getCapturer().CapturePane(); err == nil {
		paneContent = StripANSI(content)
		paneStatus = detectStatus(paneContent, log)
	}

	// Out-of-pane tiebreaker: a between-bursts frame (the agent is working but its
	// activity line is momentarily gone, so the persistent ❯ box reads as a finished
	// idle prompt) can't be told from a real finish by pane content alone. Consult
	// the transcript. Read here, before the lock — conversationActivePastHook does
	// disk I/O and must not run under s.mu. Gated tightly so it's a rare read.
	convActive := false
	switch hookStatus {
	case "waiting":
		// Stale waiting hook (a granted permission / answered question fires no resume
		// hook) where the pane doesn't structurally confirm a prompt — either an idle ❯
		// box (StatusFinished) or nothing matched ("") because the activity line was
		// pushed out of the detection window. The transcript decides whether the turn
		// resumed. Exclude paneStatus==waiting (a real prompt on screen) and ==running
		// (the pane already flips it) so genuine prompts never read the transcript.
		if paneStatus != StatusRunning && paneStatus != StatusWaiting {
			convActive = s.conversationActivePastHook()
		}
	case "finished":
		// Stale finished hook (e.g. a SessionStart/resume that kicked off one long
		// turn, no Stop since) about to demote a still-running session on an idle/
		// ambiguous frame. Gate on oldStatus==running so a genuinely-finished session
		// (already settled) never reads the transcript every round-robin cycle; the
		// gate is self-sustaining — keeping it running across between-bursts frames
		// keeps oldStatus running, so a live turn can't be demoted by one bad frame.
		if oldStatus == StatusRunning && paneStatus != StatusRunning && paneStatus != StatusWaiting {
			convActive = s.conversationActivePastHook()
		}
	}

	hookSaysDead := false
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		switch hookStatus {
		case "running":
			s.applyHookRunning(oldStatus, paneContent, paneStatus, log)
		case "waiting":
			s.applyHookWaiting(paneContent, paneStatus, convActive, log)
		case "finished":
			s.applyHookFinished(paneStatus, convActive, log)
		case "dead":
			s.lastContentHash = ""
			s.lastContentChangeAt = time.Time{}
			s.Status = StatusError
			hookSaysDead = true
		}

		if s.Status != oldStatus {
			log.Info("status changed (hook)", "old", oldStatus, "new", s.Status, "hookStatus", hookStatus, "hookAge", hookAge.Round(time.Millisecond))
		}
	}()

	if hookSaysDead {
		s.triggerCrashDump("hook_dead")
	}
}

// applyHookRunning handles hook status "running" with content stability detection.
// Must be called with s.mu held.
func (s *Session) applyHookRunning(oldStatus Status, paneContent string, paneStatus Status, log *slog.Logger) {
	// If this stale "running" hook was already overridden to finished/idle,
	// preserve that decision. A new hook (different timestamp) resets the flag
	// via UpdateHookStatus, allowing fresh evaluation.
	//
	// Without this guard, every pane-content change (survey popups, cursor
	// blinks, scrollback redraws) resets Status=Running at the top of this
	// function and clears Acknowledged, causing idle ↔ running ↔ finished
	// oscillation on stale hooks where no Stop event ever fires (e.g. slash
	// commands, session survey popup).
	//
	// Escape: if pane shows an active running indicator, Claude genuinely
	// resumed (sub-agent output burst, or user-initiated work before a new
	// hook landed). Clear the override and fall through to the normal path so
	// the stability heuristic can re-engage if Claude stops again — otherwise
	// the session would be stuck at Running forever until the next hook.
	if !s.hookOverriddenAt.IsZero() && s.hookOverriddenAt.Equal(s.hookUpdatedAt) {
		if paneStatus == StatusRunning {
			s.hookOverriddenAt = time.Time{}
			s.lastContentHash = ""
			s.lastContentChangeAt = time.Time{}
			log.Info("overridden running hook but pane shows running, resuming")
			// fall through to normal running path
		} else {
			return // preserve whatever state the stability override concluded
		}
	}

	s.Status = StatusRunning
	s.Acknowledged = false

	// Do NOT override to waiting here — pane detection can false-positive on
	// approval pattern strings (e.g. "(Y/n)") in code diffs or conversation text.
	// When hooks say running, trust them. Real permission prompts trigger a
	// PermissionRequest hook within milliseconds, so applyHookWaiting handles it.
	// Team/sub-agent waiting is caught by applyHookFinished (stale parent Stop).

	if paneContent == "" {
		return
	}

	// Content change detection: if content is stable >10s, Claude likely stopped.
	// Unlike the waiting path, this override is NOT given the transcript tiebreaker
	// (conversationActivePastHook): it already requires 10s of hash-stable content
	// AND defers to a fresh tmux window_activity below, so a single between-bursts
	// frame with the activity line missing can't trip a false finished here.
	hash := hashContent(normalizeForHash(paneContent))
	if hash != s.lastContentHash {
		s.lastContentHash = hash
		s.lastContentChangeAt = time.Now()
	} else if !s.lastContentChangeAt.IsZero() && time.Since(s.lastContentChangeAt) > 10*time.Second {
		// Don't flag as stale if tmux reports recent activity (hash normalization
		// may strip the changing content, but tmux still sees raw output).
		if s.tmuxSession != nil {
			if activity, ok := s.tmuxSession.GetActivity(); ok {
				if time.Since(time.Unix(activity, 0)) < 10*time.Second {
					return // activity recent, trust the hook
				}
			}
		}
		// Don't override if pane detection sees running indicators (spinner,
		// whimsical activity). During extended thinking, normalizeForHash strips
		// the whimsical line so the hash stabilizes, but the line is still
		// visible — trust the pane over the stability heuristic.
		if paneStatus == StatusRunning {
			return
		}
		// Content stable >10s while hook says running — hook is stale.
		// Preserve idle if already acknowledged (don't bounce back to finished).
		if oldStatus == StatusIdle {
			s.Status = StatusIdle
			s.Acknowledged = true
		} else {
			s.Status = StatusFinished
		}
		// Memoize the override so subsequent ticks don't re-run this logic and
		// reset Status=Running at the top on every pane-content change.
		s.hookOverriddenAt = s.hookUpdatedAt
		log.Info("content stable >10s, hook says running, assuming finished",
			"stableSince", s.lastContentChangeAt.Format(time.TimeOnly))
	}
}

// applyHookWaiting handles hook status "waiting" with content change detection.
// convActive is the pre-computed transcript tiebreaker (see conversationActivePastHook);
// it is only meaningful when paneStatus == StatusFinished.
// Must be called with s.mu held.
func (s *Session) applyHookWaiting(paneContent string, paneStatus Status, convActive bool, log *slog.Logger) {
	// If this hook was already overridden by pane detection (stale hook),
	// skip re-evaluation. A new hook (different timestamp) resets the flag.
	// The pane stays authoritative for this stale hook: a running spinner means
	// the user approved and Claude resumed (no hook fires for a permission
	// grant), and an idle prompt means the turn finished. Handling both keeps a
	// stale overridden-waiting hook from staying pinned to the last "running" it
	// saw — e.g. when the real Stop hook was dropped after a session-id rotation.
	if !s.hookOverriddenAt.IsZero() && s.hookOverriddenAt.Equal(s.hookUpdatedAt) {
		// The override latches: once hookOverriddenAt is set on a finished frame, no new
		// hook fires on a permission grant, so without this we'd keep settling finished
		// for the rest of the turn — even if convActive was only momentarily false on the
		// frame that latched it (resumed lead entry not yet flushed, or within the skew).
		// Re-check the transcript tiebreaker first: if the lead turn has since advanced
		// past the hook, the turn resumed and we recover to running (convActive is
		// recomputed every tick).
		if convActive {
			s.Status = StatusRunning
			s.Acknowledged = false
			log.Info("overridden waiting hook but transcript active past hook — resuming")
			return
		}
		switch paneStatus {
		case StatusRunning:
			s.Status = StatusRunning
			s.Acknowledged = false
			log.Info("overridden waiting hook but pane shows running, resuming")
		case StatusWaiting:
			// A real permission prompt is structurally on screen now. The latch was set
			// on an earlier finished/running frame of this same stale hook, but the agent
			// is genuinely blocked — the pane stays authoritative for a stale waiting hook
			// (see above), so recover to waiting instead of staying pinned to the last
			// running we saw. Without this case the switch matched nothing and returned,
			// leaving a genuine prompt stuck showing running when a missed session-id
			// rotation froze the hook so no fresh hook ever reset the latch (issue #23).
			s.Status = StatusWaiting
			s.Acknowledged = false
			log.Info("overridden waiting hook but pane shows waiting prompt, recovering to waiting")
		case StatusFinished:
			if s.Acknowledged {
				s.Status = StatusIdle
			} else {
				s.Status = StatusFinished
			}
			// Match the non-overridden settle below: reset content tracking so a
			// later waiting hook measures change from a clean baseline.
			s.lastContentHash = ""
			s.lastContentChangeAt = time.Time{}
			log.Info("overridden waiting hook but pane shows idle prompt, settling to finished")
		}
		return
	}

	// Hook says waiting — trust hooks unless pane clearly shows idle prompt.
	//
	// Why we allow pane override for finished only:
	// - detectWaiting runs before detectFinished in the detection pipeline,
	//   so a real permission prompt yields paneStatus=StatusWaiting, not StatusFinished.
	//   The "❯ 1. Yes" false-positive concern is already handled by detection order.
	// - waiting→running is NOT overridden (stale spinner text can false-positive)
	// - If user approves, UserPromptSubmit hook fires within milliseconds
	// - If user interrupts/escapes, no hook fires — pane override catches this
	// - Content change detection below handles the gap if hooks are delayed
	if paneStatus == StatusFinished {
		if convActive {
			// Pane reads as a finished idle prompt, but the transcript shows the lead
			// turn still appending past this waiting hook — modern Claude keeps the ❯
			// input box on screen while working, and this is a between-bursts frame
			// where the activity line is momentarily gone. Stay running; do NOT set
			// hookOverriddenAt so each tick re-evaluates until the turn truly ends.
			s.Status = StatusRunning
			s.Acknowledged = false
			log.Info("hook=waiting, pane idle, but transcript active past hook — staying running")
			return
		}
		// Pane shows idle prompt — user is no longer being prompted.
		// Preserve idle if already acknowledged to prevent finished↔idle oscillation.
		if s.Acknowledged {
			s.Status = StatusIdle
		} else {
			s.Status = StatusFinished
		}
		s.lastContentHash = ""
		s.lastContentChangeAt = time.Time{}
		s.hookOverriddenAt = s.hookUpdatedAt // prevent re-evaluation of this stale hook
		log.Info("hook says waiting but pane shows idle prompt, overriding to finished")
		return
	}

	// NOTE: window_activity is NOT used here — sub-agents produce output continuously
	// while the permission prompt sits at the bottom, so activity > hookUpdatedAt is
	// always true during agent team work. Content hash change detection below is the
	// correct mechanism for waiting→running (it detects actual pane content changes).

	s.Status = StatusWaiting
	s.Acknowledged = false

	// Out-of-pane resume signal. The pane shows no running spinner (the activity line
	// was pushed out of the detection window by queued messages / long output) AND the
	// normalized content hash is stable (the spinner is stripped), so neither flip
	// below can fire — yet the transcript's LEAD turn has advanced past this waiting
	// hook and is still fresh. The user approved/answered and the lead resumed (no
	// resume hook fires on a permission grant). Flip to running. Unlike window_activity
	// (deliberately unused here — a sub-agent keeps it fresh during a genuine prompt),
	// the LEAD-only transcript stays static while the lead is truly blocked. Don't set
	// hookOverriddenAt: re-evaluate each tick until a real hook lands or it goes stale.
	if convActive {
		s.Status = StatusRunning
		log.Info("hook=waiting but transcript advanced past hook — assuming running")
		return
	}

	if paneContent == "" {
		return
	}

	// Content change detection: if content changed since waiting started, user acted.
	hash := hashContent(normalizeForHash(paneContent))

	// Decide whether a pane=running signal can be trusted to flip waiting→running.
	// The permission prompt may still be painting when its hook fires, and during
	// that window detectStatus reads the streaming spinner as running (the menu's
	// "Esc to cancel" footer hasn't rendered yet). A just-fired waiting hook with a
	// pane spinner is therefore the prompt rendering, NOT Claude resuming — the user
	// hasn't approved yet.
	//
	// The reliable "the prompt has finished painting" signal is detectStatus itself
	// returning StatusWaiting (the menu is structurally on screen). Once we've seen
	// that for this hook, a later pane=running means the menu went away → the user
	// approved → trust it. The render speed no longer matters: we wait for the actual
	// prompt, not a fixed timer. The time backstop only covers prompts detection never
	// confirms structurally and genuinely stale waiting hooks, so neither a slow render
	// nor the hook ts's whole-second granularity can pin the wrong status.
	if paneStatus == StatusWaiting {
		s.waitingPaneConfirmed = true
	}
	trustPane := s.waitingPaneConfirmed || time.Since(s.hookUpdatedAt) > waitingHookRenderBackstop
	if s.lastContentHash == "" {
		// First tick in waiting state — save baseline hash.
		// Don't set lastContentChangeAt here — it should only be set on actual
		// content changes, otherwise the cooldown triggers falsely on the next tick.
		s.lastContentHash = hash
		// Still trust the pane if it shows an active running indicator — the
		// user may have approved and Claude started working immediately.
		// normalizeForHash strips spinner lines so the hash can appear stable
		// even while Claude is actively working; without this the TUI stays in
		// waiting for multiple ticks. Gated on trustPane so the permission
		// prompt's own render spinner can't trip this before the prompt is seen.
		if paneStatus == StatusRunning && trustPane {
			s.Status = StatusRunning
			s.lastContentChangeAt = time.Now()
		}
	} else if hash != s.lastContentHash {
		// Content changed — refresh hash so the next tick measures from the new baseline.
		s.lastContentHash = hash
		// If pane still structurally shows a waiting prompt (e.g. the user is
		// navigating Claude's AskUserQuestion dialog with arrow/Tab keys, which
		// mutates checkbox/cursor cells without leaving the prompt), keep
		// status=waiting. The override below assumes hash drift means "user
		// approved and Claude resumed", but that's wrong when the prompt is
		// still on screen. Don't bump lastContentChangeAt either — that would
		// extend the running cooldown for every keystroke.
		if paneStatus == StatusWaiting {
			return
		}
		// Pane no longer confirms waiting — user likely approved/escaped.
		// Transition to running (approval is the most common action).
		// Hooks will correct to the right status within milliseconds.
		s.lastContentChangeAt = time.Now()
		s.Status = StatusRunning
		log.Info("content changed while waiting, assuming running")
	} else if !s.lastContentChangeAt.IsZero() && time.Since(s.lastContentChangeAt) < 15*time.Second {
		// Content recently changed — stay running to prevent flicker.
		// Claude outputs in bursts; between bursts the hash is the same for a tick,
		// causing oscillation back to waiting. The 15s cooldown covers burst gaps.
		s.Status = StatusRunning
	} else if paneStatus == StatusRunning && trustPane {
		// Content hash is stable (normalizeForHash strips spinner/whimsical lines)
		// and cooldown expired, but pane detection sees an active running indicator
		// (spinner char or whimsical activity). Trust the pane — Claude is working.
		// Self-correcting: a Stop hook or idle prompt will override when done.
		// Gated on trustPane: before the permission prompt has been seen on screen,
		// a stable-hash spinner is the prompt still rendering, not a resumed session.
		s.Status = StatusRunning
		s.lastContentChangeAt = time.Now()
	}
}

// finishedHoldMaxAge bounds how long after a finished hook the activity-bridge below
// may hold a stale state. Long enough to cover a post-finish render burst, short enough
// that a background agent keeping window_activity fresh can't pin the status forever.
const finishedHoldMaxAge = 5 * time.Second

// waitingHookRenderBackstop bounds how long applyHookWaiting waits to observe the
// permission prompt on screen (detectStatus == StatusWaiting) before falling back to
// trusting a pane=running signal. The prompt being seen is the primary gate; this is
// the safety net for prompts detection never structurally confirms and for genuinely
// stale waiting hooks. Sized so a normally-rendered prompt is always observed first,
// leaving render speed and the hook ts's whole-second granularity unable to flip the
// status prematurely.
const waitingHookRenderBackstop = 5 * time.Second

// conversationActiveWindow bounds how recent the last lead-conversation transcript
// entry must be for conversationActivePastHook to count the agent as actively
// working. Sized above the longest observed gap between lead entries during silent
// thinking/tool stretches (~80s) so a live agent is never read as finished, while a
// genuinely dropped Stop hook settles to finished once the transcript ages past it.
const conversationActiveWindow = 120 * time.Second

// conversationActiveSkew is how far past the waiting hook's timestamp the last lead
// entry must advance to prove the agent resumed after the hook (not the entry that
// triggered the hook itself). Matches the snapshot's advanced_past_hook >2s rule.
const conversationActiveSkew = 2 * time.Second

// applyHookFinished handles hook status "finished" with pane override for active spinners.
// convActive is the pre-computed transcript tiebreaker (see conversationActivePastHook);
// it is only meaningful when paneStatus is not running/waiting.
// Must be called with s.mu held.
func (s *Session) applyHookFinished(paneStatus Status, convActive bool, log *slog.Logger) {
	s.lastContentHash = ""
	s.lastContentChangeAt = time.Time{}
	if paneStatus == StatusRunning {
		// Hook says finished (e.g. SessionStart after auto-resume) but pane
		// shows an active spinner — Claude is actually working.
		s.Status = StatusRunning
		s.Acknowledged = false
		log.Info("hook says finished but pane shows running, overriding")
		return
	}
	if paneStatus == StatusWaiting {
		// Hook says finished (e.g. parent Stop when delegating to sub-agent)
		// but pane shows a permission prompt — sub-agent is waiting for approval.
		s.Status = StatusWaiting
		s.Acknowledged = false
		log.Info("hook says finished but pane shows waiting, overriding")
		return
	}
	// Pane detection says "finished" or gave no signal. Before committing, bridge
	// the brief burst-gap right after a finished hook fires (auto-resume, or Stop
	// then a final render) where Claude's TUI momentarily shows no spinner: if the
	// pane was written to in the last few seconds AND the finished hook itself is
	// recent, hold the previous state for a tick instead of flipping.
	//
	// Both conditions are required. window_activity alone can't distinguish "Claude
	// mid-burst" from "a long-running background/sub-agent redrawing the pane" — the
	// latter keeps activity perpetually fresh, so the activity check by itself would
	// pin a stale state forever (it once held `waiting` for 24m). Bounding by hook
	// age releases once the finished hook is no longer fresh: a genuinely-working
	// Claude shows a spinner (handled by the paneStatus==running branch above), so
	// reaching here with a stale finished hook means the turn is really done.
	//
	// applyHookWaiting deliberately does NOT use activity at all (see its note):
	// there activity can't distinguish "user approved" from "user still deciding".
	if cap := s.getCapturer(); cap != nil {
		if activity, ok := cap.GetActivity(); ok &&
			time.Since(time.Unix(activity, 0)) < 3*time.Second &&
			time.Since(s.hookUpdatedAt) < finishedHoldMaxAge {
			log.Info("hook=finished, pane idle/ambiguous, recent activity + recent hook — bridging burst gap",
				"paneStatus", paneStatus, "current", s.Status,
				"hookAge", time.Since(s.hookUpdatedAt).Round(time.Millisecond))
			return
		}
	}
	// Stale finished hook, idle-looking pane — but the transcript shows the lead turn
	// still appending past the hook (a SessionStart/resume that began one long turn,
	// caught on a between-bursts frame where the activity line wasn't rendered). The
	// agent is working; don't demote it to finished.
	if convActive {
		s.Status = StatusRunning
		s.Acknowledged = false
		log.Info("hook=finished, pane idle, but transcript active past hook — staying running")
		return
	}
	if s.Acknowledged {
		s.Status = StatusIdle
	} else {
		s.Status = StatusFinished
	}
}

// updateStatusFromPane detects status from pane capture when no hook data is available.
func (s *Session) updateStatusFromPane(oldStatus Status, log *slog.Logger) {
	log.Debug("no hook data, falling back to pane capture")

	content, err := s.getCapturer().CapturePane()
	if err != nil {
		log.Warn("pane capture failed", "err", err)
		return // Keep previous status on capture failure.
	}

	content = StripANSI(content)
	status := detectStatus(content, log)

	s.mu.Lock()
	defer s.mu.Unlock()

	switch status {
	case StatusRunning:
		s.Status = StatusRunning
		s.Acknowledged = false
	case StatusWaiting:
		s.Status = StatusWaiting
		s.Acknowledged = false
	case StatusFinished:
		if s.Acknowledged {
			s.Status = StatusIdle
		} else {
			s.Status = StatusFinished
		}
	default:
		log.Debug("no pattern matched, keeping previous status", "status", s.Status)
	}

	if s.Status != oldStatus {
		log.Info("status changed (pane)", "old", oldStatus, "new", s.Status, "detected", status)
	}
}

// ToRow converts to a storage row.
func (s *Session) ToRow() *SessionRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &SessionRow{
		ID:              s.ID,
		Title:           s.Title,
		ProjectPath:     s.ProjectPath,
		Agent:           string(s.Agent),
		Status:          string(s.Status),
		TmuxSession:     s.TmuxSessionName,
		CreatedAt:       s.CreatedAt,
		LastAccessed:    s.LastAccessedAt,
		Acknowledged:    s.Acknowledged,
		ClaudeSessionID: s.ClaudeSessionID,
		WorkspaceName:   s.WorkspaceName,
		ManuallyRenamed: s.ManuallyRenamed,
		FirstPrompt:     s.FirstPrompt,
		TitleGenerated:  s.TitleGenerated,
		PromptCount:     s.PromptCount,
		SnoozedUntil:    s.snoozedUntil,
	}
}

// StatusSnapshot holds internal diagnostic data for debugging status mismatches.
type StatusSnapshot struct {
	ID              string
	Title           string
	ProjectPath     string
	TmuxSessionName string
	ClaudeSessionID string

	Status       Status
	Acknowledged bool

	HookStatus       string
	HookUpdatedAt    time.Time
	HookOverriddenAt time.Time

	LastContentHash     string
	LastContentChangeAt time.Time

	DetectedPaneStatus Status // what detectStatus returns on the raw pane right now
}

// SnapshotData captures a point-in-time copy of all internal status fields.
// rawPane should be the ANSI-preserved pane capture. detectStatus is called
// outside the lock to avoid potential deadlocks from logging.
func (s *Session) SnapshotData(rawPane string) StatusSnapshot {
	s.mu.RLock()
	snap := StatusSnapshot{
		ID:                  s.ID,
		Title:               s.Title,
		ProjectPath:         s.ProjectPath,
		TmuxSessionName:     s.TmuxSessionName,
		ClaudeSessionID:     s.ClaudeSessionID,
		Status:              s.Status,
		Acknowledged:        s.Acknowledged,
		HookStatus:          s.hookStatus,
		HookUpdatedAt:       s.hookUpdatedAt,
		HookOverriddenAt:    s.hookOverriddenAt,
		LastContentHash:     s.lastContentHash,
		LastContentChangeAt: s.lastContentChangeAt,
	}
	s.mu.RUnlock()

	snap.DetectedPaneStatus = detectStatus(StripANSI(rawPane), debuglog.Logger)
	return snap
}

// FromRow reconstructs a Session from a storage row, reconnecting to tmux.
func FromRow(row *SessionRow) *Session {
	ts := tmux.ReconnectSession(row.TmuxSession, row.Title, row.ProjectPath)
	status := Status(row.Status)
	// Don't check ts.Exists() here — let background worker detect dead sessions.

	// ownerSessionID is intentionally left unset: ownership is established at
	// runtime by the first hook and released on the owner's own death. Seeding
	// it from the persisted (non-liveness-checked) ClaudeSessionID could re-pin
	// a dead owner across an app restart and drop a relaunched Claude's hooks.
	return &Session{
		ID:              row.ID,
		Title:           row.Title,
		ProjectPath:     row.ProjectPath,
		Agent:           agent.Parse(row.Agent),
		Status:          status,
		TmuxSessionName: row.TmuxSession,
		CreatedAt:       row.CreatedAt,
		LastAccessedAt:  row.LastAccessed,
		Acknowledged:    row.Acknowledged,
		ClaudeSessionID: row.ClaudeSessionID,
		WorkspaceName:   row.WorkspaceName,
		ManuallyRenamed: row.ManuallyRenamed,
		FirstPrompt:     row.FirstPrompt,
		TitleGenerated:  row.TitleGenerated,
		PromptCount:     row.PromptCount,
		// A deadline that lapsed while fleet was closed is dropped here rather
		// than waiting for the wake sweep, so the first frame is already correct.
		snoozedUntil: dropIfPast(row.SnoozedUntil, time.Now()),
		tmuxSession:  ts,
	}
}

// dropIfPast returns the zero time for a deadline that has already lapsed,
// so an expired snooze never has to be reasoned about downstream.
func dropIfPast(deadline, now time.Time) time.Time {
	if deadline.IsZero() || !deadline.After(now) {
		return time.Time{}
	}
	return deadline
}

// --- Status detection ---

var (
	busyPatterns = []string{
		"ctrl+c to interrupt",
		"esc to interrupt",
	}
	spinnerChars = []string{
		"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",
		"✳", "✽", "✶", "✢",
	}
	// approvalPatterns are checked near the bottom of the pane (last 15
	// non-empty lines). Only used as a secondary signal alongside structural
	// checks to avoid false-positives from code diffs in scrollback.
	approvalPatterns = []string{
		"Yes, allow once",
		"No, and tell Claude",
		"Do you trust the files",
		"Allow this MCP server",
	}
	// Patterns in recent lines that indicate Claude is idle at the prompt.
	idlePatterns = []string{
		"⏵⏵",            // Claude Code permission mode bar (appears below the prompt)
		"esc to cancel", // Claude Code text input prompt (commit message, etc.)
		"tab to amend",  // Claude Code text input prompt
	}
)

// codexWaitPatterns mark a Codex prompt blocked on the user. These prompts fire
// no hook, so the pane is the only signal. Checked only in the recent (bottom)
// lines to avoid false-positives from scrollback.
var codexWaitPatterns = []string{
	"enter to submit answer", // request_user_input question menu
	"Press enter to confirm", // command/tool approval + plan-approval menus
}

// codexPaneWaiting reports whether Codex's pane shows a prompt awaiting the user.
func codexPaneWaiting(content string) bool {
	if content == "" {
		return false
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	recent := strings.Join(extractRecentLines(lines, 15), "\n")
	for _, p := range codexWaitPatterns {
		if strings.Contains(recent, p) {
			return true
		}
	}
	return false
}

// codexRunPatterns mark a Codex turn in progress. Codex shows this footer only
// while actively working ("Working (… • esc to interrupt)"), so it's a reliable
// running signal when no hook has arrived. Checked only in the recent (bottom)
// lines to avoid false-positives from scrollback.
var codexRunPatterns = []string{
	"esc to interrupt",
}

// codexPaneRunning reports whether Codex's pane shows an in-progress turn. It is
// the no-hook fallback for the running state, mirroring codexPaneWaiting.
//
// The request_user_input question menu also renders "esc to interrupt" (you can
// interrupt while answering), so a waiting pane takes precedence — running means
// an active turn that is NOT blocked on the user.
func codexPaneRunning(content string) bool {
	if content == "" {
		return false
	}
	if codexPaneWaiting(content) {
		return false
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	recent := strings.Join(extractRecentLines(lines, 15), "\n")
	for _, p := range codexRunPatterns {
		if strings.Contains(recent, p) {
			return true
		}
	}
	return false
}

func detectStatus(content string, log *slog.Logger) Status {
	if content == "" {
		log.Debug("detectStatus: empty content")
		return ""
	}

	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	recentLines := extractRecentLines(lines, 50)
	recentContent := strings.Join(recentLines, "\n")

	if s := detectRunning(recentLines, recentContent, log); s != "" {
		return s
	}
	if s := detectWaiting(recentLines, recentContent, log); s != "" {
		return s
	}
	// Background-agents dock is checked after detectWaiting: if a permission prompt
	// renders alongside live dock rows, waiting (needs attention) must win over the
	// running the dock would otherwise report. It runs before detectFinished so a
	// parked lead with in-flight agents reads running rather than idle.
	if detectBackgroundAgentsRunning(recentLines) {
		log.Debug("detectStatus: matched background-agents dock (in-flight)")
		return StatusRunning
	}
	if s := detectFinished(recentLines, recentContent, log); s != "" {
		return s
	}

	if len(recentLines) > 0 {
		log.Debug("detectStatus: no pattern matched",
			"lastLine", strings.TrimSpace(recentLines[0]),
			"lastLineHex", fmt.Sprintf("%x", strings.TrimSpace(recentLines[0])),
			"recentLineCount", len(recentLines),
		)
	} else {
		log.Debug("detectStatus: no recent lines found")
	}
	return "" // No match — caller keeps previous status.
}

// extractRecentLines returns the last n non-empty lines in reverse order.
func extractRecentLines(lines []string, n int) []string {
	var result []string
	for i := len(lines) - 1; i >= 0 && len(result) < n; i-- {
		line := strings.TrimRight(lines[i], " \t")
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

// backgroundAgentGlyph marks an in-flight agent in Claude Code's background-agents
// dock — the "◯ Explore  <desc>  2m 49s · ↓ 78.1k tokens" rows rendered below the
// input box when the lead delegates to run_in_background agents.
const backgroundAgentGlyph = "◯"

// detectBackgroundAgentsRunning reports whether Claude Code's background-agents dock
// shows at least one in-flight agent. Those agents are doing work even though the
// lead's turn has ended (it fired Stop and is parked until they re-invoke it), so the
// session is Running rather than finished/idle.
//
// Position is the only reliable discriminator. ANSI is stripped before detection, so a
// dock row QUOTED in conversation text (e.g. a session discussing this feature — the
// row is documented verbatim) is byte-identical to a live one; no textual anchor can
// tell them apart. But the live dock always renders at the very bottom of the pane, so
// its rows sit at recentLines[0..2]. A quoted row sits above the idle chrome (~5 lines:
// PR line, status line, border, ❯, border), i.e. index ≥5. So we scan only the bottom
// 3 lines — enough to catch the live dock (its rows are the pane's last lines) while
// staying clear of quoted conversation text. A row looks like:
//
//	"◯ Explore  Map analytics/platform pages   2m 49s · ↓ 78.1k tokens"
//
// Only the in-flight glyph (◯) is matched, so a completed agent (rendered with a
// different glyph) does not keep the session pinned Running after its work ends.
func detectBackgroundAgentsRunning(recentLines []string) bool {
	n := min(3, len(recentLines))
	for _, line := range recentLines[:n] {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, backgroundAgentGlyph) {
			continue
		}
		if strings.Contains(t, "tokens") &&
			(strings.Contains(t, "· ↓") || strings.Contains(t, "· ↑")) {
			return true
		}
	}
	return false
}

// detectRunning checks for busy indicators, spinner chars, and whimsical activity patterns.
func detectRunning(recentLines []string, _ string, log *slog.Logger) Status {
	// Busy patterns only in bottom 5 lines — "ctrl+c to interrupt" always appears
	// near the bottom. Checking all 50 lines false-positives on conversation text
	// that discusses these patterns (meta-problem).
	bottomN := min(5, len(recentLines))
	bottomContent := strings.ToLower(strings.Join(recentLines[:bottomN], "\n"))
	for _, pattern := range busyPatterns {
		if strings.Contains(bottomContent, pattern) {
			log.Debug("detectStatus: matched busy pattern", "pattern", pattern)
			return StatusRunning
		}
	}

	// Spinner chars only in bottom 10 lines — active Claude spinners sit right
	// above the prompt chrome. Checking all 50 lines false-positives on CLI tool
	// output (e.g. braille spinners from `linear`, `npm`) baked into scrollback.
	spinnerN := min(10, len(recentLines))
	for _, line := range recentLines[:spinnerN] {
		for _, sc := range spinnerChars {
			if strings.HasPrefix(strings.TrimSpace(line), sc) {
				// Don't treat spinner chars as running if they're part of a
				// team waiting indicator (e.g. "✢  Waiting for team lead approval").
				lowerLine := strings.ToLower(line)
				if strings.Contains(lowerLine, "waiting for") {
					continue
				}
				log.Debug("detectStatus: matched spinner char", "char", sc, "line", line)
				return StatusRunning
			}
		}
	}

	// Whimsical activity pattern (Claude 2.1.25+) — see isWhimsicalActivity for the
	// two shapes. This is the signal that catches the glyphs spinnerChars above does
	// not cover (`·` U+00B7, `✻` U+273B), so it carries an activity frame whose glyph
	// rotated off that set and which has no token counter to fall back on.
	//
	// Scanned in the bottom 20 lines (not all 50): plan execution can push the
	// activity line down via checklist items rendered below it (deepest known
	// case lands at recentLines[14]), so the limit accommodates that with
	// headroom. Scanning all 50 false-positives on quoted activity lines that
	// can land in scrollback when Claude's prior response embeds an example
	// pane capture or crash-dump snippet — those satisfy every textual guard
	// (leading glyph, ellipsis, real duration, `)` suffix) but are not live
	// indicators.
	whimsicalN := min(20, len(recentLines))
	for _, line := range recentLines[:whimsicalN] {
		if isWhimsicalActivity(line) {
			log.Debug("detectStatus: matched whimsical activity pattern", "line", strings.TrimRight(line, " \t"))
			return StatusRunning
		}
	}
	return ""
}

// activityGutterGlyphs are the structural glyphs Claude uses for tool-result
// gutters and agent-team boxes. The rotating spinner set is deliberately NOT
// enumerated below (Claude keeps extending it), but these are stable chrome, so
// naming them is safe and does not reintroduce that churn.
//
// Excluding them is load-bearing for COUNTER-LESS rows, not tidiness: a gutter row
// carries its own elapsed counter (`⎿  Running… (9s · timeout 5m)` — present
// verbatim in captured panes), and detectRunning runs BEFORE detectWaiting. A bash
// task ticking under a live permission menu would therefore return Running,
// detectWaiting would never run, applyHookWaiting would never set
// waitingPaneConfirmed, and after waitingHookRenderBackstop the pane would pin
// Status to Running — dropping an unanswered prompt out of the Space rotation and
// making Y refuse it.
//
// The protection stops there. A gutter row that DOES carry a token counter
// (`⎿  Running… (12s · ↓ 3.4k tokens)`) is claimed by shape A below before this
// list is ever consulted, so it can still mask Waiting — unchanged from master,
// which had no gutter check at all, and not present in any captured pane. Left
// that way deliberately rather than hoisting this check above shape A, which
// would narrow a shape whose whole purpose is to preserve prior behaviour.
var activityGutterGlyphs = []rune{'⎿', '⏺', '│', '├', '└'}

// isWhimsicalActivity reports whether a line is one of Claude's live activity
// indicators. Two shapes qualify, and they are alternatives on purpose:
//
//	A. a token counter — "Clauding… (53s · ↓ 749 tokens)"
//	                     "✳ Newspapering… (5m 21s · ↓ 3.7k tokens)"
//	B. a glyph + capitalised activity word — "· Improvising… (51s · thinking with xhigh effort)"
//	                                         "✻ Marinating… (33s)"
//	                                         "· Compacting conversation… (2m 27s)"
//
// B exists because Claude renders plenty of activity lines with no counter, and
// spinnerChars covers only part of the rotating glyph set (`·` U+00B7 and `✻`
// U+273B are absent) — so such a frame matched nothing and fell through to the
// prompt check, reading a mid-turn session as finished. That only surfaces when
// the hook layer is also down, but then the status flips frame-to-frame as the
// glyph rotates.
//
// A is kept as an alternative rather than replaced by B: it is the older, proven
// anchor, and it is the shape that still matches if a frame captures with its
// glyph cell blank or clipped. Keeping both means B can afford a strict gate
// without risking a false negative.
//
// A is therefore evaluated on EXACTLY master's terms, and every tightening this
// function adds lives inside B. In particular the ellipsis requirement must not be
// hoisted above A: master required no ellipsis, so `Clauding (53s · ↓ 749 tokens)`
// matched there, and gating A behind a character that a blank-or-clipped frame may
// equally lack would defeat the reason A is kept. Same for `waiting for` — scoping
// it to B keeps A's long-standing behaviour intact.
//
// Used by detectRunning (status detection) and normalizeForHash (content hashing).
func isWhimsicalActivity(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	if !strings.HasSuffix(lower, ")") {
		return false
	}
	if strings.Contains(lower, "tokens") &&
		(strings.Contains(lower, "· ↓") || strings.Contains(lower, "· ↑")) &&
		hasWhimsicalDuration(lower) {
		return true // shape A — master's predicate, unchanged
	}
	// Shape B. The duration must follow the activity word rather than merely appear
	// on the line, or prose quoting a timing figure would satisfy the check. The
	// `waiting for` guard mirrors the spinner branch in detectRunning, which skips a
	// glyph-prefixed team-waiting line for the same reason.
	ellipsis := strings.Index(lower, "…")
	if ellipsis < 0 || !hasWhimsicalDuration(lower[ellipsis:]) {
		return false
	}
	return hasActivityGlyphPrefix(trimmed) && !strings.Contains(lower, "waiting for")
}

// hasActivityGlyphPrefix reports whether s opens the way Claude's activity lines
// do: one non-ASCII glyph, whitespace, then a capitalised activity word.
//
// The glyph is not matched against a list — that set rotates and grows. But it must
// be non-ASCII, because Claude renders its own replies as markdown: a bullet or
// blockquote closing on a duration (`- Ran the suite… (12s)`, `> Marinating… (33s)`)
// otherwise qualifies as "the glyph" and pins an idle session to Running until the
// line scrolls out of the bottom-20 window. Capitalisation alone does not separate
// those from an activity word — a bullet is capitalised too.
//
// Requiring the following word to be capitalised rejects lowercase prose behind a
// real glyph (`· quoted example… (2s)`), and unlike "the ellipsis must terminate the
// first token" it still admits the two-word `Compacting conversation…`. Accented
// prose (`Éclair… (2s)`) is rejected by the separator check: nothing follows the
// first rune but more letters.
func hasActivityGlyphPrefix(s string) bool {
	glyph, size := utf8.DecodeRuneInString(s)
	if glyph == utf8.RuneError || glyph < utf8.RuneSelf {
		return false
	}
	if slices.Contains(activityGutterGlyphs, glyph) {
		return false
	}
	rest := strings.TrimLeft(s[size:], " \t")
	if rest == s[size:] {
		return false // glyph not separated from the word by whitespace
	}
	word, _ := utf8.DecodeRuneInString(rest)
	return unicode.IsUpper(word)
}

// hasWhimsicalDuration checks for Claude's duration counter pattern "(Ns", "(Nm Ns",
// "(Nh Nm" inside the line — e.g. "(53s", "(5m 42s", "(1h 2m". This anchors the
// whimsical check to actual activity lines and prevents false-positives from
// conversation text that coincidentally mentions "tokens" + "· ↓"/"· ↑" + ")".
func hasWhimsicalDuration(lower string) bool {
	// Look for "(<digits><unit>" where unit is s, m, or h.
	for i := 0; i < len(lower)-2; i++ {
		if lower[i] != '(' {
			continue
		}
		// Must have digit after '('.
		j := i + 1
		if j >= len(lower) || lower[j] < '0' || lower[j] > '9' {
			continue
		}
		// Scan digits.
		for j < len(lower) && lower[j] >= '0' && lower[j] <= '9' {
			j++
		}
		// Must end with a time unit.
		if j < len(lower) && (lower[j] == 's' || lower[j] == 'm' || lower[j] == 'h') {
			return true
		}
	}
	return false
}

// detectWaiting checks for permission prompts using structural layout checks.
// Uses specific UI structures rather than text patterns to avoid false-positives
// from code or conversation text containing approval-related strings.
// recentLines is ordered from bottom (index 0 = last non-empty line).
func detectWaiting(recentLines []string, _ string, log *slog.Logger) Status {
	bottomN := 15
	if bottomN > len(recentLines) {
		bottomN = len(recentLines)
	}

	// Structural check: numbered permission menu.
	// Requires three cues: a cursor-on-numbered-option line ("❯ N."), at least
	// one other numbered option line, AND a menu footer nearby. The cursor
	// may sit on any option (1, 2, 3…) depending on what the user has highlighted.
	// The footer ("Esc to cancel" for a standard permission menu, or "approve
	// with this feedback" for the ExitPlanMode plan-approval menu — which has no
	// "Esc to cancel") only appears in Claude's interactive prompts, preventing
	// false-positives from user-typed numbered lists.
	hasCursorOnOption := false
	hasOtherOption := false
	hasMenuFooter := false
	for i := 0; i < bottomN; i++ {
		trimmed := strings.TrimSpace(recentLines[i])
		if strings.HasPrefix(trimmed, "❯ ") && isNumberedMenuOption(strings.TrimPrefix(trimmed, "❯ ")) {
			hasCursorOnOption = true
		} else if isNumberedMenuOption(trimmed) {
			hasOtherOption = true
		}
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "esc to cancel") || strings.Contains(lower, "approve with this feedback") {
			hasMenuFooter = true
		}
	}
	if hasCursorOnOption && hasOtherOption && hasMenuFooter {
		log.Debug("detectStatus: matched permission menu structure")
		return StatusWaiting
	}

	// Structural check: team waiting box.
	// Line starting with box-drawing char │ and containing "Waiting for team lead"
	// — only appears inside the agent team approval box UI.
	for i := 0; i < bottomN; i++ {
		trimmedLine := strings.TrimSpace(recentLines[i])
		if strings.HasPrefix(trimmedLine, "│") && strings.Contains(trimmedLine, "Waiting for team lead") {
			log.Debug("detectStatus: matched team waiting box", "line", trimmedLine)
			return StatusWaiting
		}
	}

	// Structural check: AskUserQuestion tool dialog.
	// The footer "Tab to switch questions" only appears in this tool's UI —
	// no other Claude prompt has tabs between questions. Pair with "Esc to cancel"
	// to avoid matching conversation text that mentions tabs.
	// The cursor `❯` and numbered options here are unstable as the user navigates
	// (Tab moves focus to checkbox-style question rows where `❯` disappears),
	// so the menu structural check above misses these states. The footer is
	// rendered identically on every tick regardless of which question has focus.
	for i := 0; i < bottomN; i++ {
		lower := strings.ToLower(recentLines[i])
		if strings.Contains(lower, "tab to switch questions") && strings.Contains(lower, "esc to cancel") {
			log.Debug("detectStatus: matched askuserquestion footer")
			return StatusWaiting
		}
	}

	// Text pattern fallback: highly specific strings that only appear in
	// Claude's permission UI, not in normal code or conversation.
	bottomContent := strings.Join(recentLines[:bottomN], "\n")
	for _, pattern := range approvalPatterns {
		if strings.Contains(bottomContent, pattern) {
			log.Debug("detectStatus: matched approval pattern", "pattern", pattern)
			return StatusWaiting
		}
	}

	return ""
}

// isNumberedMenuOption reports whether s starts with "<digits>.".
// Used to distinguish permission-menu cursor lines ("❯ 2. Yes…") from
// idle prompt lines ("❯ " or "❯ some user text").
func isNumberedMenuOption(s string) bool {
	sawDigit := false
	for _, r := range s {
		if r >= '0' && r <= '9' {
			sawDigit = true
			continue
		}
		return sawDigit && r == '.'
	}
	return false
}

// detectFinished checks for prompt indicators and idle patterns.
func detectFinished(recentLines []string, recentContent string, log *slog.Logger) Status {
	if len(recentLines) == 0 {
		return ""
	}

	// Scan last few lines for prompt indicator.
	scanLimit := min(5, len(recentLines))
	for i := 0; i < scanLimit; i++ {
		line := strings.TrimSpace(recentLines[i])
		if line == ">" || line == "❯" || strings.HasPrefix(line, "> ") || strings.HasPrefix(line, "❯ ") {
			// Skip permission-menu cursor lines ("❯ 2. Yes…") — those are
			// waiting state, not idle prompts. Defense-in-depth: detectWaiting
			// should have caught these earlier, but if a new menu variant
			// slips past it, we don't want to silently flip to finished.
			remainder := line
			switch {
			case strings.HasPrefix(line, "❯ "):
				remainder = strings.TrimPrefix(line, "❯ ")
			case strings.HasPrefix(line, "> "):
				remainder = strings.TrimPrefix(line, "> ")
			}
			if isNumberedMenuOption(remainder) {
				continue
			}
			log.Debug("detectStatus: matched prompt", "line", line, "linesFromBottom", i)
			return StatusFinished
		}
	}

	// Check idle patterns (e.g. permission mode bar).
	for _, pattern := range idlePatterns {
		if strings.Contains(recentContent, pattern) {
			log.Debug("detectStatus: matched idle pattern", "pattern", pattern)
			return StatusFinished
		}
	}
	return ""
}

// normalizeForHash normalizes pane content for stable hashing.
// Removes spinner lines, trailing whitespace, UI chrome, and collapses blank lines.
func normalizeForHash(content string) string {
	content = StripANSI(content)
	lines := strings.Split(content, "\n")

	// Remove lines containing spinner characters or whimsical activity indicators.
	// These are activity indicator lines with ephemeral whimsical words
	// ("Seasoning…", "Thinking…") that change every few seconds.
	// The whimsical activity check handles extended thinking lines that use
	// · (middle dot) instead of spinner chars and contain changing timers/counters.
	filtered := lines[:0]
	for _, line := range lines {
		if isWhimsicalActivity(line) {
			continue
		}
		hasSpinner := false
		for _, sc := range spinnerChars {
			if strings.Contains(line, sc) {
				hasSpinner = true
				break
			}
		}
		if !hasSpinner {
			filtered = append(filtered, line)
		}
	}
	lines = filtered

	// Strip UI chrome: Claude Code renders input line, separators, and status bar
	// at the bottom with animated elements (creature). Find the second-to-last
	// separator line (the one ABOVE the input line) and cut everything from there.
	// Layout: content | separator₁ | ❯ input | separator₂ | status bar
	cutoff := len(lines)
	separatorsFromBottom := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if isSeparatorLine(lines[i]) {
			separatorsFromBottom++
			if separatorsFromBottom >= 2 {
				cutoff = i
				break
			}
		}
	}
	lines = lines[:cutoff]

	// Trim trailing whitespace per line, strip right-margin animations,
	// and collapse consecutive blank lines.
	var result []string
	prevBlank := false
	for _, line := range lines {
		line = stripRightMargin(line)
		line = strings.TrimRight(line, " \t")
		blank := line == ""
		if blank && prevBlank {
			continue
		}
		result = append(result, line)
		prevBlank = blank
	}
	return strings.Join(result, "\n")
}

// isSeparatorLine checks if a line is predominantly box-drawing characters (─━═╌).
// These separator lines mark the boundary between content and UI chrome in Claude Code.
func isSeparatorLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 20 {
		return false
	}
	boxChars := 0
	total := 0
	for _, r := range trimmed {
		total++
		if r == '─' || r == '━' || r == '═' || r == '╌' {
			boxChars++
		}
	}
	return total > 0 && boxChars*100/total >= 70
}

// stripRightMargin truncates a line at the first run of 20+ consecutive spaces.
// Claude Code renders animated elements (whimsical creature, etc.) at the far
// right of the terminal, separated from real content by long space runs.
// Stripping these prevents animations from affecting content hash stability.
func stripRightMargin(line string) string {
	const threshold = 20
	count := 0
	for i := 0; i < len(line); i++ {
		if line[i] == ' ' {
			count++
			if count >= threshold {
				return line[:i-threshold+1]
			}
		} else {
			count = 0
		}
	}
	return line
}

// hashContent returns a truncated SHA256 hash (16 hex chars) of the content.
func hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h[:8])
}

// StripANSI removes ANSI escape sequences from content.
// Uses O(n) single-pass algorithm to avoid regex backtracking issues.
func StripANSI(content string) string {
	// Fast path: no escape chars.
	if !strings.ContainsRune(content, '\x1b') && !strings.ContainsRune(content, '\x9B') {
		return content
	}

	var b strings.Builder
	b.Grow(len(content))

	i := 0
	for i < len(content) {
		switch content[i] {
		case '\x1b':
			i = skipESCSequence(content, i+1)
		case '\x9B':
			i = skipCSIParams(content, i+1)
		default:
			b.WriteByte(content[i])
			i++
		}
	}

	return b.String()
}

// skipESCSequence skips an escape sequence starting after the ESC byte.
func skipESCSequence(content string, i int) int {
	if i >= len(content) {
		return i
	}
	switch content[i] {
	case '[':
		return skipCSIParams(content, i+1)
	case ']':
		return skipOSCBody(content, i+1)
	default:
		return i + 1 // Other escape: skip one byte after ESC.
	}
}

// skipCSIParams skips CSI parameter bytes and the final byte.
func skipCSIParams(content string, i int) int {
	for i < len(content) && content[i] >= 0x20 && content[i] <= 0x3F {
		i++ // parameter bytes
	}
	if i < len(content) && content[i] >= 0x40 && content[i] <= 0x7E {
		i++ // final byte
	}
	return i
}

// skipOSCBody skips an OSC sequence body until ST (ESC \ or BEL).
func skipOSCBody(content string, i int) int {
	for i < len(content) {
		if content[i] == '\x07' {
			return i + 1
		}
		if content[i] == '\x1b' && i+1 < len(content) && content[i+1] == '\\' {
			return i + 2
		}
		i++
	}
	return i
}

// --- Repo grouping ---

var (
	repoRootCache   = make(map[string]string)
	repoRootCacheMu sync.RWMutex
)

// SeedRepoRoot pre-populates the repo-root cache so GetRepoRoot returns root for
// projectPath without shelling out to git. Used by the sidebar preview to
// resolve its synthetic paths hermetically (no disk access).
func SeedRepoRoot(projectPath, root string) {
	repoRootCacheMu.Lock()
	repoRootCache[projectPath] = root
	repoRootCacheMu.Unlock()
}

// GetRepoRoot returns the git repo root for a path, or the path itself if not a git repo.
func GetRepoRoot(projectPath string) string {
	repoRootCacheMu.RLock()
	if root, ok := repoRootCache[projectPath]; ok {
		repoRootCacheMu.RUnlock()
		return root
	}
	repoRootCacheMu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", projectPath, "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	root := projectPath
	if err == nil {
		root = strings.TrimSpace(string(output))
	}

	repoRootCacheMu.Lock()
	repoRootCache[projectPath] = root
	repoRootCacheMu.Unlock()

	return root
}

// GroupByRepo groups sessions by their git repo root.
func GroupByRepo(sessions []*Session) map[string][]*Session {
	groups := make(map[string][]*Session)
	for _, s := range sessions {
		root := GetRepoRoot(s.ProjectPath)
		groups[root] = append(groups[root], s)
	}
	return groups
}

// GroupByOrigin groups sessions by origin → checkout (repo root) → sessions.
// originOf maps a repo root to a stable origin key (typically a github
// org/repo identity); use [github.com/brizzai/fleet/internal/git.GetOriginKey]
// or a cached lookup. Worktrees of the same repo land under one origin.
func GroupByOrigin(sessions []*Session, originOf func(repoRoot string) string) map[string]map[string][]*Session {
	groups := make(map[string]map[string][]*Session)
	for _, s := range sessions {
		root := GetRepoRoot(s.ProjectPath)
		origin := originOf(root)
		if groups[origin] == nil {
			groups[origin] = make(map[string][]*Session)
		}
		groups[origin][root] = append(groups[origin][root], s)
	}
	return groups
}

func generateID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%d", b, time.Now().Unix())
}

// TitleFromPath generates a session title from a directory path.
func TitleFromPath(path string) string {
	return filepath.Base(path)
}
