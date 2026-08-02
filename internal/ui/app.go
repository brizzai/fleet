package ui

import (
	"context"
	"fmt"
	"image/color"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/agent"
	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/chrome"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/discovery"
	"github.com/brizzai/fleet/internal/editor"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/hooks"
	"github.com/brizzai/fleet/internal/naming"
	"github.com/brizzai/fleet/internal/perfwatch"
	"github.com/brizzai/fleet/internal/proc"
	"github.com/brizzai/fleet/internal/releasenotes"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/shell"
	"github.com/brizzai/fleet/internal/tmux"
	"github.com/brizzai/fleet/internal/vterm"
	"github.com/brizzai/fleet/internal/workspace"
)

const (
	tickInterval           = 2 * time.Second
	activeStatusInterval   = 500 * time.Millisecond // fast pane re-check for active sessions
	previewTickInterval    = 500 * time.Millisecond
	whatsNewTickInterval   = 60 * time.Millisecond // shimmer cadence for the What's New badge
	previewCacheTTL        = 500 * time.Millisecond
	layoutBreakpointSingle = 50
	layoutBreakpointDual   = 80
	helpBarHeight          = 1 // single row of contextual hotkeys, no top rule
	focusFilterFooterRows  = 2 // border + content line — focus mode / filter active
	statusRoundRobin       = 5 // sessions per tick
	undoDeleteTimeout      = 5 * time.Second

	// agentNameRecheckInterval is the steady-state cadence for re-reading a
	// session's title from the agent (Claude's growing JSONL transcript, Codex's
	// state DB).
	agentNameRecheckInterval = 30 * time.Second
	// agentNameFreshPollWindow is how long after creation a still-untitled
	// session is polled every cycle so it adopts its ai-title promptly. Past
	// this window the transcript is large enough that scanning it every tick is
	// wasteful, so we fall back to agentNameRecheckInterval.
	agentNameFreshPollWindow = 2 * time.Minute
)

// PendingDelete holds state for a deferred session deletion (undo window).
type PendingDelete struct {
	Nonce         string              // unique ID for timer matching
	Session       *session.Session    // kept alive (tmux still running)
	Row           *session.SessionRow // DB snapshot for re-insert
	RepoPath      string
	DestroyWS     bool
	WorkspaceName string
	UnpinRepo     bool // unpin the repo from the sidebar on finalize (header delete)
	DeletedAt     time.Time
}

// Message types.
type (
	tickMsg          time.Time
	hookChangedMsg   struct{} // HookWatcher detected a status file change
	statusUpdateMsg  struct{ attachedSessionID string }
	sessionDeleteMsg struct {
		id               string
		err              error
		destroyWorkspace bool
		workspaceName    string
		unpinRepo        bool
		repoPath         string
	}
	// repoDeleteMsg deletes every session in a repo/worktree group (header delete).
	repoDeleteMsg struct {
		repoPath         string
		destroyWorkspace bool
	}
	// originDeleteMsg forgets a whole origin group: every checkout under it
	// (the main repo plus any worktrees) and all their sessions. Each target
	// carries its own destroy decision, snapshotted at show-time so the ~2s
	// gitInfo refresh can't flip a worktree classification between the user
	// ticking the checkbox and execution.
	originDeleteMsg struct {
		targets []originDeleteTarget
	}
	// originDeleteTarget is one checkout in an origin-forget, with the resolved
	// decision of whether its worktree directory is removed from disk.
	originDeleteTarget struct {
		repoPath string
		destroy  bool
	}
	pendingDeleteExpireMsg struct {
		nonce string
	}
	sessionRestartMsg struct {
		id  string
		err error
	}
	// sessionsSuspendedMsg reports that the sweep (auto=true) or a manual command
	// (auto=false) just hibernated n idle sessions, so Update can rebuild the list
	// and show a toast. Persistence + action-log happen at the suspend site.
	sessionsSuspendedMsg struct {
		n    int
		auto bool
	}
	sessionRestartConfirmedMsg struct{ id string }
	sessionCreateResultMsg     struct {
		session *session.Session
		err     error
	}
	previewMsg struct {
		sessionID string
		content   string
	}
	loadSessionsMsg struct {
		sessions     []*session.Session
		shells       []*shell.Shell
		slotBindings map[int]string
		ghAvailable  bool
		warning      string
		prCache      map[string]*session.PRCacheRow
		err          error
	}
	openEditorMsg        struct{ err error }
	openPRMsg            struct{ err error }
	quickApproveMsg      struct{ err error }
	spinnerTickMsg       struct{}
	whatsNewTickMsg      struct{}
	previewTickMsg       time.Time
	focusTickMsg         time.Time
	slotAssignTimeoutMsg struct{}
	reloadAllResultMsg   struct {
		restarted int
		skipped   int
		errors    []string
	}
	// bootstrapDoneMsg fires when the initial parallel git/origin probe
	// finishes (or hits its deadline). It dismisses the boot splash and
	// hands control to the steady-state status worker.
	bootstrapDoneMsg struct{}
	// gitInfoUpdateMsg carries a per-repo refresh result to Update, which
	// applies it via writeGitInfo. nil info is allowed (signals "no data
	// yet"); Update overwrites whatever was in the cache for `repo`.
	//
	// Update is NOT the only writer of h.gitInfoCache: the git+PR fan-out
	// writes it directly (writeGitInfo is a lock-free COW swap, safe from
	// any goroutine) because routing payloads through Update blocks on
	// Tea's unbuffered channel for the length of an attach. Readers that
	// span several h.gitInfo() loads across one decision must therefore
	// take a single snapshot and index it — see confirmDeleteOrigin.
	gitInfoUpdateMsg struct {
		repo string
		info *git.RepoInfo
	}
	// gitRepaintMsg tells Update that a worker already wrote fresh git info
	// into the cache and the sidebar should reflect it. Deliberately carries
	// no data: the git+PR fan-out publishes via writeGitInfo and only signals
	// here, so a dropped message costs a repaint, never correctness.
	gitRepaintMsg struct {
		structural bool // origin/worktree changed — needs a full flat-item rebuild
	}
	// splashFrameMsg advances the boot-splash spinner. Self-rescheduled
	// while !booted; not emitted after bootstrapDoneMsg.
	splashFrameMsg time.Time
	// shutdownFrameMsg advances the shutdown-overlay spinner. Self-rescheduled
	// while `quitting`, until the teardown command emits tea.Quit.
	shutdownFrameMsg time.Time
	// discoveryMsg carries the launchpad's scan of ~/.claude/projects back to
	// Update. Fired once, only on an empty fleet.
	discoveryMsg struct {
		items []discovery.Recent
	}
)

func spinnerTickCmd() tea.Msg {
	time.Sleep(100 * time.Millisecond)
	return spinnerTickMsg{}
}

// whatsNewTickCmd paces the What's New badge shimmer. Self-rescheduled only
// while the badge is visible (see the whatsNewTickMsg handler), so it burns no
// CPU when the badge is hidden.
func whatsNewTickCmd() tea.Cmd {
	return tea.Tick(whatsNewTickInterval, func(time.Time) tea.Msg { return whatsNewTickMsg{} })
}

// Home is the main Bubble Tea model.
type Home struct {
	width  int
	height int

	sessions    []*session.Session
	sessionByID map[string]*session.Session
	storage     *session.StateDB
	flatItems   []SidebarItem

	cursor     int
	viewOffset int

	isAttaching atomic.Bool
	err         error
	errTime     time.Time
	infoMsg     string
	infoTime    time.Time

	newDialog             *NewSessionDialog
	confirmDialog         *ConfirmDialog
	renameDialog          *RenameDialog
	helpOverlay           *HelpOverlay
	settingsDialog        *SettingsDialog
	worktreeDialog        *WorktreeDialog
	createWorkspaceDialog *CreateWorkspaceDialog
	branchDialog          *BranchCheckoutDialog
	commandPalette        *CommandPaletteDialog
	contextMenu           *ContextMenuDialog
	snoozeDialog          *SnoozeDialog
	// The row the open context menu was built for. Its entries resolve their
	// subject from h.cursor, which an async rebuild can move — see contextMenuTarget.
	contextMenuTarget   contextMenuTarget
	sessionCreateDialog *SessionCreateDialog
	consentDialog       *ConsentDialog
	onboardingDialog    *OnboardingDialog
	releaseNotes        *ReleaseNotesDialog

	// What's New badge: an animated top-right indicator shown while an unseen
	// highlighted release exists. cachedReleases is loaded once at startup so
	// the badge can be computed without opening the dialog.
	cachedReleases     []releasenotes.Release
	hasUnseenWhatsNew  bool
	whatsNewFrame      int  // shimmer animation frame
	whatsNewShimmering bool // whether the ~60ms shimmer loop is currently armed

	// launchpad is the first-run experience shown when the fleet is empty:
	// recent repos mined from Claude Code history, ready to resume.
	// launchpadDismissed lets the user Esc out to the bare empty state.
	launchpad          *Launchpad
	launchpadDismissed bool

	pendingWorkspaces []*PendingWorkspace // in-flight workspace creations
	pendingForkCtx    *forkContext        // set while Shift+F worktree picker is open; consumed on pick/cancel
	pendingDeletes    []PendingDelete     // undo stack for deferred deletions
	// finalizingDeletes holds entries whose undo window has expired but whose
	// background cleanup (tmux kill, hook removal, workspace destroy) is still
	// running. Quit drains both this list and pendingDeletes so an in-flight
	// kill isn't lost when fleet exits mid-cleanup.
	finalizingDeletes []PendingDelete
	pinnedRepos       map[string]bool // pinned repo paths (persist in SQLite)

	// failedWorktreeRemovals holds worktree repo paths whose destroy failed
	// (something is still holding the directory). Such repos are re-pinned and
	// flagged in the sidebar so the user can press d to retry; cleared on a
	// successful retry. In-memory only.
	failedWorktreeRemovals map[string]bool

	// holderScanGen monotonically tags each async process scan that backs the
	// delete dialog's "will terminate …" warning, so a stale result can't write
	// onto a later prompt.
	holderScanGen int

	repoExpanded     map[string]bool // checkout-path / "origin:<key>" -> expanded state (default expanded when missing; persisted in SQLite)
	previewCache     map[string]string
	previewCacheTime map[string]time.Time
	statusRRIndex    int       // round-robin index for status updates
	lastHeavyCycleAt time.Time // wall-clock gate: heavy worker work runs at most every tickInterval
	// lastSuspendSweepAt throttles the memory-pressure idle-suspend sweep so it
	// runs on its own slow cadence inside the ~2s heavy pass.
	lastSuspendSweepAt time.Time

	// groupSnooze maps a sidebar group key ("origin:<key>" or a checkout path)
	// to its umbrella snooze deadline. Read only while building the sidebar
	// tree and written only by the wake sweep — both on the Update goroutine —
	// so a plain map needs no lock. (Deliberately NOT on the worker: unlike the
	// idle-suspend sweep, which lives there because it probes sysctl, expiry is
	// clock arithmetic and a tiny SQLite write.)
	groupSnooze map[string]time.Time
	// snoozeMuted is the resolved "is this session attention-muted" answer for
	// every session, recomputed in rebuildFlatItems. Exists so fleet-wide
	// consumers (statusCountsLine) consult the same resolution the sidebar rows
	// were stamped with instead of re-deriving the precedence rule — see
	// snoozeState's doc. Keyed by session ID.
	snoozeMuted map[string]bool
	// lastSnoozeSweepAt throttles that sweep, mirroring lastSuspendSweepAt.
	lastSnoozeSweepAt time.Time
	// attachAfterResumeID: when a suspended session is resumed via Enter, the
	// resume runs async; this holds its id so the sessionRestartMsg handler
	// attaches once the tmux is live again. Empty otherwise.
	attachAfterResumeID string
	// attachedSessionID is the session the user is currently attached to (empty
	// when detached). Guarded by workerMu; the memory-pressure sweep reads it to
	// avoid hibernating the pane the user is sitting in. Set/cleared in attachSession.
	attachedSessionID string

	// Status-worker liveness stamps (unix-nano), written by statusWorkerCycle and
	// read lock-free by the snapshot + the dev-only watchdog. lastWorkerCycleNano
	// is the last COMPLETED cycle; workerCycleStartNano is the in-flight cycle's
	// start (0 when idle). A stale lastWorkerCycleNano means the worker wedged —
	// the failure mode where every session's status freezes while the UI stays
	// responsive (it waits synchronously on the per-cycle git+PR fan-out).
	lastWorkerCycleNano  atomic.Int64
	workerCycleStartNano atomic.Int64
	// Same pair for gitWorker. The git+PR fan-out is the blocking work the
	// watchdog exists to catch, and it no longer runs on statusWorker — without
	// its own stamps a permanently wedged gitWorker would leave branch/dirty/PR
	// frozen while workerHeartbeat() still read healthy off statusWorker's.
	lastGitCycleNano  atomic.Int64
	gitCycleStartNano atomic.Int64
	// attachStartedAt is when the current tea.Exec attach began (0 when not
	// attached). The watchdog subtracts this window instead of skipping its
	// check, so an attach can't manufacture a stall — nor hide a real one.
	attachStartedAt atomic.Int64

	// lastTmuxStatusBar tracks the most recent (status, theme) tuple applied
	// to each session's tmux status bar so the worker can skip no-op
	// re-applies. Map is only read/written from the worker goroutine — no
	// lock needed.
	lastTmuxStatusBar map[string]string // sessionID -> "status|theme"

	// gitInfoCache holds the latest known git+PR state per repo. Stored as
	// an atomic pointer to an immutable map so reads are lock-free and
	// writes are copy-on-write: workers carry-forward by reading the
	// loaded snapshot; Update applies gitInfoUpdateMsg by copying the
	// current map, mutating the copy, and atomically swapping it in. The
	// loaded map MUST NOT be mutated by readers — always make a fresh map
	// for any write. Replaces the prior `map[string]*RepoInfo` + workerMu
	// pair, which deadlocked when callers double-locked via rebuildFlatItems.
	gitInfoCache  atomic.Pointer[map[string]*git.RepoInfo]
	repoLastHotAt map[string]time.Time // repo root -> last time a session in it was Running (worker-only access)
	ghAvailable   bool                 // cached gh CLI availability

	hookWatcher *hooks.HookWatcher

	// Focus mode (split view).
	focusMode     bool
	controlClient *tmux.ControlClient
	cachedSidebar string // cached sidebar render for focus mode
	sidebarDirty  bool   // true when sidebar needs rebuild

	// Filter.
	filterInput  textinput.Model
	filterActive bool
	filterText   string

	// Terminal drawer (shells). Shells are non-agent terminals scoped to a
	// repo/worktree, shown in the drawer (never the sidebar). The drawer is
	// either hidden or focused (always-typing: keystrokes go to the active
	// shell, a few Ctrl chords drive the chrome). See drawer.go.
	shells            []*shell.Shell // all live shells, all repos (mirror of the shells table)
	drawerMode        drawerMode     // drawerHidden / drawerTyping
	drawerProgress    float64        // animated slide 0 (closed) → 1 (open)
	drawerTarget      float64        // where the slide is heading
	drawerActiveTab   int            // index into shellsForActiveRepo()
	drawerHeight      int            // max body rows when open (from config; clamped at render)
	drawerCloseArmed  bool           // ⌃W pressed once on a running shell; confirm close with ⌃W again
	drawerTickRunning bool           // a drawerTypeTick loop is live; guards against spawning duplicates on fast toggling
	drawerRepo        string         // repo the drawer is scoped to (frozen at open)
	drawerInnerW      int            // last-rendered drawer body inner width (drives the live emulator size)
	drawerInnerH      int            // last-rendered drawer body inner height (stable terminal rows)

	// Live terminal stream for the active shell: a tmux control-mode reader
	// streams the pane's %output into an insulated emulator, rendered each frame
	// (replaces the old capture-pane polling). shellStreamTarget is the tmux
	// session the reader is attached to (changes on tab switch / restart).
	shellReader        *tmux.OutputReader
	shellTerm          *vterm.Terminal
	shellStreamTarget  string
	shellStreamPending string      // target of an in-flight async attach ("" = none); dedups dispatches
	shellReseedPending bool        // a capture-pane re-seed of a blank emulator is in flight
	shellWake          atomic.Bool // coalesces output→render wakes (true = one shellOutputMsg in flight)

	// Slot hotkeys (RTS-style quick access: digit=jump, double-digit=attach, alt+digit or =<digit>=bind).
	slotBindings      map[int]string // slot (0-9) -> session ID
	lastSlotTapSlot   int            // -1 when no pending tap
	lastSlotTapAt     time.Time
	slotAssignMode    int // 0=off, 1=bind pending (=<digit>), 2=unbind pending (==<digit>)
	slotAssignExpires time.Time

	// Floating toast overlay (bottom-right).
	toasts *ToastStack

	// Contextual tips (bottom-right hint box). See tips.go.
	tipEpisodeDismissed map[string]bool          // recurring tips dismissed this episode (in-memory)
	tipVisibleFor       map[string]time.Duration // tipOnce: cumulative time actually on screen
	lastTipTickAt       time.Time                // wall clock of the previous refreshTips, for the delta
	activeTipID         string                   // tip currently rendered (set by refreshTips)

	// macOS TCC: tmux can't read ~/Documents/~/Desktop/~/Downloads once its server
	// daemonizes. Probed lazily (once per protected root per launch) when a session
	// or shell is created under one; drives the tcc-blocked tip. Read/written on the
	// Update goroutine only. See probeTCCCmd / tips.go.
	tccProbed       map[string]bool // protected root -> probe dispatched this launch
	tccBlockedRoots map[string]bool // protected root -> tmux read blocked

	// Recently picked palette item IDs (most recent first). In-memory only.
	recentPaletteIDs []string

	// Config.
	cfg     *config.Config
	version string

	// Pre-resolved per-install identity (device hash + git name/email +
	// OS version). Discovered in main.go before the TUI starts so Init
	// doesn't shell out on the Bubble Tea Update() thread.
	identity analytics.Identity

	// Bug report / diagnostics.
	errorHistory *ErrorHistory
	actionLog    *ActionLog
	bugReport    *BugReportDialog

	// Background worker for async status/git/PR updates.
	statusTrigger         chan struct{} // buffered(1), triggers worker
	gitTrigger            chan struct{} // buffered(1), triggers gitWorker out of band
	priorityStatusUpdates chan string   // buffered, session IDs with fresh hook changes — drained before round-robin
	// workerMu now protects only h.sessions (worker snapshots the slice at
	// the top of each cycle; Update mutates on add/remove/restore). The
	// git/PR cache moved off the lock entirely — see h.gitInfoCache /
	// h.gitInfo / h.writeGitInfo, which use an atomic.Pointer with
	// copy-on-write writes for fully lock-free reads.
	workerMu      sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	workerStarted bool

	// program is the running tea.Program, injected by cmd/fleet/main.go via
	// SetProgram before p.Run(). Worker goroutines push state updates back
	// to Update via h.send(msg) so Update remains the writer of model fields
	// — no lock contracts to honor at call sites. The exception is
	// gitInfoCache, which the git+PR fan-out writes directly via its atomic
	// COW swap; h.send blocks for the whole of an attach, which is too long
	// to hold a fan-out goroutine. Nil during tests that drive Update
	// directly; send() is a no-op in that case.
	program *tea.Program

	startTime time.Time // app start time for uptime tracking

	// Throttles the "gh rate-limited" WARN log so it doesn't fire every
	// 2s tick. Reset to time.Time{} when a refresh comes back clean.
	lastRateLimitWarn time.Time

	// Boot splash. `booted` is false until the bootstrap fan-out resolves
	// every visible repo's OriginKey (or the 4s deadline expires); while
	// false, View() returns the splash and the steady-state worker has
	// not started.
	booted            bool
	splashFrame       int
	bootstrapRepos    int          // total repos the bootstrap is waiting on (for progress UI)
	bootstrapResolved atomic.Int32 // goroutines that have finished within the current bootstrap fan-out

	// Shutdown overlay. Once `quitting` is set, View() renders `frozenFrame`
	// (the last body, captured at quit time) dimmed with a "Shutting down…"
	// box overlaid, while the blocking teardown runs off the Update loop.
	// `shutdownFrame` advances the box's spinner.
	quitting      bool
	shutdownFrame int
	frozenFrame   string

	// Rendering diagnostics (accumulated counters for bug reports).
	renderStats RenderStats
}

// NewHome creates the main TUI model.
func NewHome(storage *session.StateDB, cfg *config.Config, version string, identity analytics.Identity) *Home {
	ctx, cancel := context.WithCancel(context.Background())

	fi := textinput.New()
	fi.Placeholder = "filter..."
	fi.CharLimit = 64
	fi.SetWidth(20)

	// Apply theme — PaletteByName falls back to the flagship default when
	// cfg.Theme is empty or unknown.
	ApplyPalette(PaletteByName(cfg.Theme))
	ApplyDisplayConfig(cfg)

	h := &Home{
		storage:                storage,
		sessionByID:            make(map[string]*session.Session),
		repoExpanded:           make(map[string]bool),
		groupSnooze:            make(map[string]time.Time),
		lastTmuxStatusBar:      make(map[string]string),
		slotBindings:           make(map[int]string),
		lastSlotTapSlot:        -1,
		toasts:                 NewToastStack(),
		tipEpisodeDismissed:    make(map[string]bool),
		tipVisibleFor:          make(map[string]time.Duration),
		tccProbed:              make(map[string]bool),
		tccBlockedRoots:        make(map[string]bool),
		pinnedRepos:            make(map[string]bool),
		failedWorktreeRemovals: make(map[string]bool),
		newDialog:              NewNewSessionDialog(),
		confirmDialog:          NewConfirmDialog(),
		renameDialog:           NewRenameDialog(),
		helpOverlay:            NewHelpOverlay(),
		settingsDialog:         NewSettingsDialog(cfg),
		worktreeDialog:         NewWorktreeDialog(),
		createWorkspaceDialog:  NewCreateWorkspaceDialog(),
		branchDialog:           NewBranchCheckoutDialog(),
		commandPalette:         NewCommandPaletteDialog(),
		contextMenu:            NewContextMenuDialog(),
		snoozeDialog:           NewSnoozeDialog(),
		sessionCreateDialog:    NewSessionCreateDialog(),
		consentDialog:          NewConsentDialog(),
		onboardingDialog:       NewOnboardingDialog(cfg),
		releaseNotes:           NewReleaseNotesDialog(),
		launchpad:              NewLaunchpad(),
		bugReport:              NewBugReportDialog(),
		previewCache:           make(map[string]string),
		previewCacheTime:       make(map[string]time.Time),
		repoLastHotAt:          make(map[string]time.Time),
		filterInput:            fi,
		cfg:                    cfg,
		version:                version,
		identity:               identity,
		errorHistory:           NewErrorHistory(50),
		actionLog:              NewActionLog(100),
		statusTrigger:          make(chan struct{}, 1),
		gitTrigger:             make(chan struct{}, 1),
		priorityStatusUpdates:  make(chan string, 256),
		ctx:                    ctx,
		cancel:                 cancel,
		startTime:              time.Now(),
	}
	h.drawerHeight = cfg.GetDrawerHeight()
	// Seed the What's New "seen" version only on a genuinely fresh install, so a
	// brand-new install doesn't light up the badge for releases that predate it.
	// An existing user meeting this feature for the first time also has an empty
	// seen version, but IsFirstRun is false for them — so they still get the
	// current release's highlights (including What's New's own announcement)
	// instead of being silently stamped as caught-up.
	if cfg.IsFirstRun() && cfg.GetReleaseNotesSeenVersion() == "" {
		cfg.MarkReleaseNotesSeen(releasenotes.NormalizeVersion(version))
	}
	emptyCache := make(map[string]*git.RepoInfo)
	h.gitInfoCache.Store(&emptyCache)
	return h
}

// Init implements tea.Model.
func (h *Home) Init() tea.Cmd {
	return tea.Batch(
		h.loadSessions,
		h.tick(),
		h.previewTick(),
		h.loadReleaseNotes(), // compute the What's New badge without opening the dialog
	)
}

// SetProgram wires up the running tea.Program so worker goroutines can
// push state updates back to Update via h.send. Called once from
// cmd/fleet/main.go after tea.NewProgram and before p.Run().
func (h *Home) SetProgram(p *tea.Program) {
	h.program = p
}

// gitInfo returns the current immutable snapshot of the git/PR cache.
// Safe to read from any goroutine. The returned map MUST NOT be mutated —
// readers are guaranteed it won't change underneath them only because
// writers copy-on-write via writeGitInfo.
func (h *Home) gitInfo() map[string]*git.RepoInfo {
	if p := h.gitInfoCache.Load(); p != nil {
		return *p
	}
	return nil
}

// writeGitInfo COW-updates the cache: copies the current map, runs the
// mutation, and CAS-swaps the new map in. CAS retries on contention so
// concurrent writers (production: only Update; tests: bootstrap per-repo
// goroutines) can all succeed without locks. If mutate returns false the
// swap is skipped.
func (h *Home) writeGitInfo(mutate func(m map[string]*git.RepoInfo) bool) {
	for {
		oldP := h.gitInfoCache.Load()
		var old map[string]*git.RepoInfo
		if oldP != nil {
			old = *oldP
		}
		next := make(map[string]*git.RepoInfo, len(old)+1)
		for k, v := range old {
			next[k] = v
		}
		if mutate != nil && !mutate(next) {
			return
		}
		if h.gitInfoCache.CompareAndSwap(oldP, &next) {
			return
		}
	}
}

// send pushes a message to the Update loop from a background goroutine.
// In production h.program is wired by main.go; tests skip that, leaving this a
// no-op — every remaining sender already applies its own state and uses the
// message purely as a repaint/toast hint.
//
// Callers on the status or git worker MUST guard this with isAttaching:
// program.Send is a rendezvous on Tea's unbuffered msgs channel, and tea.Exec
// suspends the loop that drains it for the whole of an attach.
func (h *Home) send(msg tea.Msg) {
	if h.program != nil {
		h.program.Send(msg)
		return
	}
}

// Update implements tea.Model.
func (h *Home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if perfwatch.Enabled() {
		tok := perfwatch.MarkUpdateStart(fmt.Sprintf("%T", msg))
		defer perfwatch.MarkUpdateEnd(tok)
	}
	// While shutting down, the blocking teardown runs off-loop (see beginQuit);
	// keep the spinner animating and swallow everything else so no key or worker
	// message mutates state the teardown goroutine is touching concurrently. A
	// second Ctrl+C is the escape hatch: if teardown wedges (a stuck worktree
	// remove, a wedged telemetry drain), force an immediate exit rather than
	// spinning forever with no way out.
	if h.quitting {
		switch m := msg.(type) {
		case shutdownFrameMsg:
			h.shutdownFrame++
			return h, h.shutdownTick()
		case tea.KeyPressMsg:
			if m.String() == "ctrl+c" {
				return h, tea.Quit
			}
		}
		return h, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h.renderStats.RecordResize(msg.Width, msg.Height)
		// Only log resizes after the initial one (startup always sends one).
		if h.width > 0 && (msg.Width != h.width || msg.Height != h.height) {
			debuglog.Logger.Info("window resized",
				"from", fmt.Sprintf("%dx%d", h.width, h.height),
				"to", fmt.Sprintf("%dx%d", msg.Width, msg.Height),
				"resize_count", h.renderStats.ResizeCount,
			)
		}
		h.width = msg.Width
		h.height = msg.Height
		h.sidebarDirty = true
		h.newDialog.SetSize(msg.Width, msg.Height)
		h.confirmDialog.SetSize(msg.Width, msg.Height)
		h.renameDialog.SetSize(msg.Width, msg.Height)
		h.helpOverlay.SetSize(msg.Width, msg.Height)
		h.settingsDialog.SetSize(msg.Width, msg.Height)
		h.worktreeDialog.SetSize(msg.Width, msg.Height)
		h.createWorkspaceDialog.SetSize(msg.Width, msg.Height)
		h.branchDialog.SetSize(msg.Width, msg.Height)
		h.commandPalette.SetSize(msg.Width, msg.Height)
		h.contextMenu.SetSize(msg.Width, msg.Height)
		h.snoozeDialog.SetSize(msg.Width, msg.Height)
		h.sessionCreateDialog.SetSize(msg.Width, msg.Height)
		h.consentDialog.SetSize(msg.Width, msg.Height)
		h.onboardingDialog.SetSize(msg.Width, msg.Height)
		h.bugReport.SetSize(msg.Width, msg.Height)
		h.releaseNotes.SetSize(msg.Width, msg.Height)
		h.syncViewport()
		// The dropdown is pinned to a sidebar row, and a resize moves that row
		// (syncViewport may also re-scroll). Re-anchor so it doesn't strand.
		if h.contextMenu.IsVisible() {
			h.contextMenu.SetAnchor(h.contextMenuAnchor())
		}
		if h.snoozeDialog.IsVisible() {
			h.snoozeDialog.SetAnchor(h.contextMenuAnchor())
		}
		return h, nil

	case tea.KeyPressMsg:
		return h.handleKey(msg)

	case tea.PasteMsg: // cmd+v — v2 delivers paste as its own message, not a KeyMsg
		return h.handlePaste(msg)

	case drawerAnimTickMsg: // drive the terminal-drawer slide
		h.syncShellStream() // attach/resize the live stream as the drawer opens
		if h.drawerStep() {
			return h, h.drawerAnimTick()
		}
		return h, nil

	case drawerTypeTickMsg: // backstop: keep the live stream attached + sized while open
		if h.drawerMode != drawerTyping {
			h.drawerTickRunning = false // stop the loop (teardown ran on close); a reopen starts a fresh one
			return h, nil
		}
		h.syncShellStream()
		return h, h.drawerTypeTick()

	case shellOutputMsg:
		// The reader pushed new bytes into the emulator; clear the wake latch so
		// the next chunk can schedule another frame. Returning re-renders the View.
		h.shellWake.Store(false)
		return h, nil

	case shellStreamReadyMsg:
		// An async stream attach completed; install it if still wanted.
		h.applyShellStream(msg)
		return h, nil

	case shellReseedMsg:
		// A capture-pane re-seed of a blank emulator completed off-thread. Apply
		// only if the same shell is still streaming AND still blank, so we never
		// clobber content the live reader has since delivered.
		h.shellReseedPending = false
		if len(msg.seed) > 0 && h.shellTerm != nil && h.shellStreamTarget == msg.target &&
			strings.TrimSpace(h.shellTerm.Render()) == "" {
			h.shellTerm.Write(msg.seed)
			h.shellWake.Store(false)
		}
		return h, nil

	case shellCreateResultMsg:
		if msg.err != nil {
			h.setError(msg.err)
			return h, nil
		}
		h.workerMu.Lock()
		h.shells = append(h.shells, msg.sh)
		h.workerMu.Unlock()
		if err := h.storage.SaveShell(msg.sh.ToRow()); err != nil {
			debuglog.Logger.Error("storage: SaveShell", "id", msg.sh.ID, "err", err)
		}
		// Focus the new tab if it's in the drawer's current scope.
		shells := h.shellsForActiveRepo()
		for i, s := range shells {
			if s.ID == msg.sh.ID {
				h.drawerActiveTab = i
			}
		}
		h.syncShellStream() // attach the live stream to the newly-focused shell
		// Probe for a TCC-blocked folder against the now-live tmux server.
		return h, h.probeTCCCmd(msg.sh.RepoPath)

	case shellRestartMsg:
		if msg.err != nil {
			h.setError(msg.err)
			return h, nil
		}
		if err := h.storage.UpdateShellTmuxName(msg.id, msg.tmuxName); err != nil {
			debuglog.Logger.Error("storage: UpdateShellTmuxName", "id", msg.id, "err", err)
		}
		h.syncShellStream() // re-attach the live stream to the restarted session
		return h, nil

	case tickMsg:
		return h.handleTick()

	case hookChangedMsg:
		// HookWatcher detected a status file change. Do immediate hook-only sync,
		// then hand sessions whose hook changed to the worker via the priority queue
		// so they get a full UpdateStatus() within ~100ms instead of waiting for round-robin.
		h.workerMu.Lock()
		changed := h.syncHookStatuses(h.sessions, false) // UI loop: no rotation I/O
		h.workerMu.Unlock()
		h.rebuildFlatItems()
		h.enqueuePriorityUpdates(changed)
		return h, h.listenForHookChanges

	case statusUpdateMsg:
		// Returned after detaching from session.
		h.isAttaching.Store(false)
		// Immediate hook sync (data already in HookWatcher from hooks that fired during attach).
		h.workerMu.Lock()
		changed := h.syncHookStatuses(h.sessions, false) // UI loop: no rotation I/O
		h.workerMu.Unlock()
		h.rebuildFlatItems()
		h.enqueuePriorityUpdates(changed)
		// Also trigger full background refresh for pane captures, git, etc.
		// Both workers: git skipped every cycle during the attach we just left,
		// so branch/dirty/PR are as stale as the attach was long.
		h.triggerGitRefresh()
		select {
		case h.statusTrigger <- struct{}{}:
		default:
		}
		return h, nil

	case sessionCreateMsg:
		return h.handleSessionCreate(msg)

	case forkSessionMsg:
		ag := msg.agent
		if ag == "" {
			ag = agent.Parse(h.cfg.GetDefaultAgent())
		}
		analytics.Track(analytics.EventForkSession, map[string]interface{}{"agent": string(ag)})
		if ag == agent.Codex {
			if err := hooks.EnsureCodexDirTrust(hooks.GetCodexConfigDir(), msg.path); err != nil {
				debuglog.Logger.Error("codex dir trust seeding failed", "path", msg.path, "err", err)
			}
		}
		s := session.NewSession(msg.title, msg.path)
		s.WorkspaceName = msg.workspaceName
		s.ForkFromID = msg.parentClaudeSessionID
		s.Agent = ag
		// Record which conversation we're forking from. The fork resumes the
		// source session's ClaudeSessionID; if that id is stale (a missed
		// rotation), the fork opens the wrong conversation (issue #142). Logging
		// the source id + parent claude id here lets a bug report pin whether the
		// parent we forked was the source's current conversation.
		debuglog.Logger.Info("forking session",
			"newID", s.ID,
			"newTitle", msg.title,
			"sourceID", msg.sourceSessionID,
			"sourceTitle", msg.sourceTitle,
			"parentClaude", msg.parentClaudeSessionID,
			"destPath", msg.path)
		parentSessionID := msg.parentClaudeSessionID
		sourcePath := msg.sourcePath
		destPath := msg.path
		return h, func() tea.Msg {
			// Stage the parent's Claude transcript into the destination cwd's
			// project dir so `claude --resume <id> --fork-session` finds it.
			// Only needed when forking into a different cwd — same-cwd forks
			// already have the transcript in place.
			if sourcePath != "" && sourcePath != destPath {
				if err := session.CopyClaudeForkTranscript(parentSessionID, sourcePath, destPath); err != nil {
					return sessionCreateResultMsg{err: fmt.Errorf("stage parent transcript: %w", err)}
				}
			}
			if err := s.Start(); err != nil {
				return sessionCreateResultMsg{err: err}
			}
			return sessionCreateResultMsg{session: s}
		}

	case sessionCreateResultMsg:
		return h.handleSessionCreateResult(msg)

	case tccProbeResultMsg:
		if !msg.determined {
			// Inconclusive (no server yet / tmux error) — unlatch so a later create
			// re-probes instead of caching a false "not blocked" for the whole launch.
			delete(h.tccProbed, msg.root)
			return h, nil
		}
		h.tccBlockedRoots[msg.root] = msg.blocked
		if msg.blocked {
			debuglog.Logger.Info("tcc: tmux blocked from protected folder", "root", msg.root)
		}
		return h, nil

	case slotAssignTimeoutMsg:
		if h.slotAssignMode != 0 && !time.Now().Before(h.slotAssignExpires) {
			h.slotAssignMode = 0
		}
		return h, nil

	case sessionDeleteMsg:
		if msg.err != nil {
			h.setError(msg.err)
			return h, nil
		}
		// Engagement signals: lifetime, prompt count, and orphaned-flag tell us
		// whether the session was actually used before being thrown away.
		if s, ok := h.sessionByID[msg.id]; ok {
			lifetime := time.Since(s.CreatedAt).Seconds()
			analytics.Distribution(analytics.MetricSessionLifetimeSeconds, lifetime, nil)
			if s.PromptCount > 0 {
				analytics.Distribution(analytics.MetricSessionPromptsPerSession, float64(s.PromptCount), nil)
			}
			if s.LastAccessedAt.IsZero() {
				analytics.Track(analytics.EventSessionOrphaned, map[string]interface{}{
					"lifetime_seconds": int(lifetime),
				})
			}
		}
		analytics.Track(analytics.EventSessionDeleted, nil)
		return h.deferDelete(msg)

	case repoDeleteMsg:
		return h.deferDeleteRepo(msg)

	case originDeleteMsg:
		return h.deferDeleteOrigin(msg)

	case pendingDeleteExpireMsg:
		return h.handlePendingDeleteExpire(msg)

	case sessionRestartMsg:
		if msg.err != nil {
			h.setError(fmt.Errorf("restart failed: %w", msg.err))
		}
		// Update storage with new status and tmux session name.
		if s, ok := h.sessionByID[msg.id]; ok {
			if err := h.storage.UpdateStatus(s.ID, string(s.GetStatus())); err != nil {
				debuglog.Logger.Error("storage: UpdateStatus after restart", "id", s.ID, "err", err)
			}
			if err := h.storage.UpdateTmuxSession(s.ID, s.TmuxSessionName); err != nil {
				debuglog.Logger.Error("storage: UpdateTmuxSession after restart", "id", s.ID, "err", err)
			}
		}
		h.rebuildFlatItems()
		// A resume-from-suspend requested an attach once the tmux was live again.
		if h.attachAfterResumeID == msg.id {
			h.attachAfterResumeID = ""
			if msg.err != nil {
				// Resume failed — return the row to Suspended so Enter can retry,
				// instead of stranding it in Starting/Error (which Enter can't wake).
				if s, ok := h.sessionByID[msg.id]; ok {
					s.SetStatus(session.StatusSuspended)
					if err := h.storage.UpdateStatus(s.ID, string(session.StatusSuspended)); err != nil {
						debuglog.Logger.Error("storage: UpdateStatus after failed resume", "id", s.ID, "err", err)
					}
					h.rebuildFlatItems()
				}
				return h, nil
			}
			// Attach the session we actually resumed — the cursor may have moved
			// during the async Restart, so attachSelected() (cursor-based) could
			// drop the user into a different session.
			return h, h.attachSessionByID(msg.id)
		}

	case sessionsSuspendedMsg:
		// Persistence + action-log already happened at the suspend site (worker or
		// manual cmd goroutine). Here we just refresh the list and confirm to the user.
		h.rebuildFlatItems()
		if msg.n > 0 {
			noun := "session"
			if msg.n > 1 {
				noun = "sessions"
			}
			if msg.auto {
				h.setInfo(fmt.Sprintf("Suspended %d idle %s to free memory · enter to resume", msg.n, noun))
			} else {
				h.setInfo(fmt.Sprintf("Suspended %d %s · enter to resume", msg.n, noun))
			}
		}

	case sessionRestartConfirmedMsg:
		if s, ok := h.sessionByID[msg.id]; ok {
			return h, h.restartSession(s)
		}
		return h, nil

	case commandPaletteMsg:
		return h.dispatchPaletteSelection(msg)

	case snoozeSelectedMsg:
		// Same discipline as contextMenuMsg: the picker names a row, and an
		// async rebuild may have moved the cursor while it was open, so put the
		// cursor back before applying — otherwise a session finishing mid-pick
		// could snooze a row the dialog never named.
		if !h.focusContextMenuTarget() {
			h.setInfo("That row is gone — nothing to snooze")
			return h, nil
		}
		sc, ok := h.snoozeScopeAtCursor()
		if !ok {
			return h, nil
		}
		h.applySnoozeUntil(sc, msg.until, msg.durationID)
		return h, nil

	case contextMenuMsg:
		// Put the cursor back on the row the menu was opened for before dispatching —
		// an async rebuild may have moved it while the menu was up, and every handler
		// resolves its subject from the cursor.
		if !h.focusContextMenuTarget() {
			h.setInfo("That row is gone — nothing to act on")
			return h, nil
		}
		h.actionLog.Add("context menu: "+msg.id, "", true)
		return h.dispatchCommand(msg.id)

	case reloadAllResultMsg:
		for _, s := range h.sessions {
			if err := h.storage.UpdateStatus(s.ID, string(s.GetStatus())); err != nil {
				debuglog.Logger.Error("storage: UpdateStatus after reload all", "id", s.ID, "err", err)
			}
			if err := h.storage.UpdateTmuxSession(s.ID, s.TmuxSessionName); err != nil {
				debuglog.Logger.Error("storage: UpdateTmuxSession after reload all", "id", s.ID, "err", err)
			}
		}
		h.rebuildFlatItems()
		// Trigger immediate status refresh.
		select {
		case h.statusTrigger <- struct{}{}:
		default:
		}
		if len(msg.errors) > 0 {
			h.setError(fmt.Errorf("reloaded %d sessions, %d failed: %s",
				msg.restarted, len(msg.errors), strings.Join(msg.errors, ", ")))
		} else if msg.restarted > 0 {
			h.setInfo(fmt.Sprintf("Reloaded %d sessions (%d skipped)", msg.restarted, msg.skipped))
		}
		return h, nil

	case sessionRenameMsg:
		if s, ok := h.sessionByID[msg.id]; ok {
			// Manual rename on a previously auto-named session = signal that
			// our auto-title heuristic produced a bad name.
			if s.TitleGenerated && !s.ManuallyRenamed {
				analytics.Track(analytics.EventManualRenameAfterAuto, nil)
			}
			s.Title = msg.newTitle
			s.ManuallyRenamed = true
			analytics.Track(analytics.EventSessionRenamed, nil)
			if err := h.storage.UpdateTitle(s.ID, msg.newTitle); err != nil {
				debuglog.Logger.Error("storage: UpdateTitle (rename)", "id", s.ID, "err", err)
			}
			if err := h.storage.MarkManuallyRenamed(s.ID); err != nil {
				debuglog.Logger.Error("storage: MarkManuallyRenamed", "id", s.ID, "err", err)
			}
			h.rebuildFlatItems()
		}
		return h, nil

	case settingsClosedMsg:
		// Re-read tick interval from config after settings change.
		// Also reconcile the live analytics client with the (possibly
		// changed) telemetry mode — otherwise the change only takes
		// effect on next launch. If the user just enabled telemetry
		// mid-session, mark them active today so DAU catches them now.
		analytics.SyncEnabled(h.cfg.GetTelemetryMode(), h.version, h.identity)
		analytics.Heartbeat()
		// Re-read the drawer height (clamped) so a change takes effect without a
		// relaunch; the next render/sync resizes the live stream to match.
		h.drawerHeight = h.cfg.GetDrawerHeight()
		// Display toggles are live, but the density flag changes BuildFlatItems
		// output (inter-group spacer rows), so rebuild the flattened list.
		h.rebuildFlatItems()
		return h, nil

	case onboardingClosedMsg:
		// Theme was applied live during the picker; nothing to re-read.
		return h, nil

	case consentResultMsg:
		// Accept → full (usage + git identity); decline → minimal (anonymous
		// daily-active ping only, no identity). "Off" is Settings-only. Persist
		// the mode so we don't ask again, clearing any legacy bool so it can't
		// shadow the new field, then run startup analytics in the chosen mode.
		mode := config.TelemetryMinimal
		if msg.accepted {
			mode = config.TelemetryFull
		}
		h.cfg.TelemetryMode = mode
		h.cfg.Telemetry = nil
		h.cfg.AnalyticsConsentSeen = true
		if err := h.cfg.Save(); err != nil {
			debuglog.Logger.Error("config: save after consent", "err", err)
		}
		// Both outcomes initialize the client and emit the anonymous DAU
		// signals; only full mode additionally attaches identity + rich usage.
		h.workerMu.Lock()
		repoCount := len(session.GroupByRepo(h.sessions))
		h.workerMu.Unlock()
		h.fireStartupAnalytics(repoCount)
		// First-run theme onboarding follows the consent prompt.
		h.maybeShowOnboarding()
		return h, nil

	case releaseNotesLoadedMsg:
		h.releaseNotes.SetData(msg.releases, msg.err)
		if msg.err == nil {
			h.cachedReleases = msg.releases
			h.recomputeWhatsNew()
		}
		return h, h.ensureWhatsNewShimmer()

	case bugReportClosedMsg:
		return h, nil

	case bugReportOpenErrMsg:
		h.bugReport.submitting = false
		h.setError(msg.err)
		return h, nil

	case openEditorMsg:
		if msg.err != nil {
			h.setError(fmt.Errorf("editor: %w", msg.err))
		}
		return h, nil

	case openPRMsg:
		if msg.err != nil {
			h.setError(msg.err)
		}
		return h, nil

	case quickApproveMsg:
		if msg.err != nil {
			h.setError(fmt.Errorf("approve: %w", msg.err))
		}
		return h, nil

	case branchListMsg:
		if msg.err != nil {
			h.branchDialog.ShowError(msg.err.Error())
			return h, nil
		}
		h.branchDialog.Show(msg.branches, msg.repoPath, msg.isDirty, msg.userEmail)
		return h, nil

	case branchCheckoutMsg:
		h.branchDialog.Hide()
		if msg.err != nil {
			h.setError(fmt.Errorf("checkout: %w", msg.err))
			return h, nil
		}
		// Refresh git info off the Update goroutine — RefreshGitInfo shells
		// out to git, which would otherwise block repaint/key handling until
		// it returns. The existing gitInfoUpdateMsg pipeline writes the new
		// info into the lock-free cache and rebuilds the sidebar.
		repoPath := msg.repoPath
		refresh := func() tea.Msg {
			return gitInfoUpdateMsg{repo: repoPath, info: git.RefreshGitInfo(repoPath)}
		}
		// Trigger PR refresh for new branch. Git lives on its own worker now, so
		// this needs gitTrigger — statusTrigger would only wake pane detection.
		h.triggerGitRefresh()
		select {
		case h.statusTrigger <- struct{}{}:
		default:
		}
		return h, refresh

	case statusSnapshotMsg:
		if msg.err != nil {
			h.setError(fmt.Errorf("snapshot: %w", msg.err))
		} else {
			h.setInfo("Snapshot saved: " + msg.path)
		}
		return h, nil

	case reportSnapshotMsg:
		// Silent on failure: the dialog renders the reason inline, and a toast
		// behind a modal would only stack noise the reporter can't act on.
		h.bugReport.SetSnapshot(msg.snap)
		return h, nil

	case statusMisdetectedMsg:
		analytics.Track(analytics.EventStatusMisdetected, map[string]interface{}{
			"shown":            msg.shown,
			"expected":         msg.expected,
			"agent":            msg.agent,
			"hook_status":      msg.hookStatus,
			"pane_detected":    msg.paneDetect,
			"mismatch":         msg.mismatch,
			"included_content": msg.wroteContent,
		})
		return h, nil

	case previewMsg:
		h.previewCache[msg.sessionID] = msg.content
		h.previewCacheTime[msg.sessionID] = time.Now()
		return h, nil

	case workspaceListMsg:
		if msg.err != nil {
			h.worktreeDialog.Hide()
			h.clearPendingFork()
			h.setError(fmt.Errorf("worktree list: %w", msg.err))
			return h, nil
		}
		if msg.provider.IsCustom() {
			// Custom provider: go straight to create workspace dialog.
			h.worktreeDialog.Hide()
			h.createWorkspaceDialog.Show(msg.provider, msg.repoPath)
			return h, nil
		}
		// Seed gitInfoCache for the normalized main-clone path when it isn't
		// already tracked, so the Creating… phantom (and the anti-flicker seed in
		// workspaceCreateResultMsg) group under the real origin instead of a
		// spurious local:<dir> header. Harmless when the path is already known.
		if msg.originKey != "" {
			h.writeGitInfo(func(next map[string]*git.RepoInfo) bool {
				if _, ok := next[msg.repoPath]; ok {
					return false
				}
				next[msg.repoPath] = &git.RepoInfo{OriginKey: msg.originKey}
				return true
			})
		}
		h.worktreeDialog.Show(msg.workspaces, h.sessions, msg.provider, msg.repoPath, msg.defaultBranch)
		return h, nil

	case workspaceSelectedMsg:
		if ctx := h.pendingForkCtx; ctx != nil {
			h.clearPendingFork()
			return h, h.dispatchForkToWorktree(ctx, msg.info.Path, msg.info.Name)
		}
		return h.handleSessionCreate(sessionCreateMsg{
			path:          msg.info.Path,
			title:         msg.info.Name,
			workspaceName: msg.info.Name,
		})

	case showCreateWorkspaceMsg:
		h.worktreeDialog.Hide()
		h.createWorkspaceDialog.Show(msg.provider, msg.repoPath)
		return h, nil

	case showWorktreeDialogMsg:
		h.createWorkspaceDialog.Hide()
		// Re-fetch worktree list for the same repo.
		if msg.repoPath != "" {
			h.worktreeDialog.ShowLoading()
			return h, tea.Batch(h.fetchWorkspaceListForRepo(msg.repoPath), spinnerTickCmd)
		}
		return h, nil

	case workspaceCreateMsg:
		// Close dialog immediately — creation runs in background.
		h.createWorkspaceDialog.Hide()
		analytics.Track(analytics.EventWorkspaceCreated, map[string]interface{}{
			"provider": func() string {
				if msg.provider.IsCustom() {
					return "shell"
				}
				return "git"
			}(),
		})

		pw := &PendingWorkspace{
			ID:       generatePendingID(),
			Name:     msg.name,
			RepoPath: msg.repoPath,
		}
		h.pendingWorkspaces = append(h.pendingWorkspaces, pw)

		// Expand the origin group and the checkout, then rebuild sidebar so the
		// phantom is visible — when creation is triggered from an origin header the
		// group may be collapsed, which would otherwise hide the phantom row.
		h.setExpanded(OriginExpandKey(h.originOf(msg.repoPath)), true)
		h.setExpanded(msg.repoPath, true)
		h.rebuildFlatItems()

		// Auto-select the phantom entry.
		for i, item := range h.flatItems {
			if item.Pending != nil && item.Pending.ID == pw.ID {
				h.cursor = i
				h.syncViewport()
				break
			}
		}

		pendingID := pw.ID
		provider := msg.provider
		repoPath := msg.repoPath
		name := msg.name
		branch := msg.branch
		baseBranch := msg.baseBranch
		copyClaudeSettings := h.cfg.IsCopyClaudeSettingsEnabled() && !provider.IsCustom()
		return h, tea.Batch(func() tea.Msg {
			info, err := provider.Create(repoPath, name, branch, baseBranch)
			if err == nil && info != nil && info.Path != "" {
				if copyClaudeSettings {
					copyClaudeSettingsFile(repoPath, info.Path)
				}
				workspace.CopyConfiguredFiles(repoPath, info.Path)
			} else if err == nil && info != nil && info.Path == "" {
				debuglog.Logger.Debug("workspace create returned empty path — skipping file copies",
					"repo", repoPath, "name", name)
			}
			return workspaceCreateResultMsg{info: info, err: err, pendingID: pendingID, repoPath: repoPath}
		}, spinnerTickCmd)

	case workspaceCreateResultMsg:
		h.removePendingWorkspace(msg.pendingID)

		if msg.err != nil {
			h.setError(fmt.Errorf("workspace create failed: %w", msg.err))
			analytics.Track(analytics.EventGitCommandFailure, map[string]interface{}{"command": "worktree_create"})
			h.clearPendingFork()
			h.rebuildFlatItems()
			// Clamp cursor if it was on the removed phantom.
			if h.cursor >= len(h.flatItems) && len(h.flatItems) > 0 {
				h.cursor = len(h.flatItems) - 1
			}
			return h, nil
		}
		// Seed gitInfoCache for the new checkout from the source repo's
		// OriginKey so the first sidebar render groups it under the
		// parent origin instead of flickering through a "local:<dir>"
		// header until the worker observes the new path. If the source
		// itself isn't resolved yet (cold start), skip — RefreshGitInfo
		// will fill it in on the next cycle.
		if msg.info != nil && msg.info.Path != "" {
			h.writeGitInfo(func(next map[string]*git.RepoInfo) bool {
				src, ok := next[msg.repoPath]
				if !ok || src == nil || src.OriginKey == "" {
					return false
				}
				next[msg.info.Path] = &git.RepoInfo{
					OriginKey:      src.OriginKey,
					IsWorktreeRepo: true,
				}
				return true
			})
		}

		if ctx := h.pendingForkCtx; ctx != nil {
			h.clearPendingFork()
			return h, h.dispatchForkToWorktree(ctx, msg.info.Path, msg.info.Name)
		}
		return h.handleSessionCreate(sessionCreateMsg{
			path:          msg.info.Path,
			title:         msg.info.Name,
			workspaceName: msg.info.Name,
		})

	case deleteCleanupDoneMsg:
		for i, pd := range h.finalizingDeletes {
			if pd.Session.ID == msg.sessionID {
				h.finalizingDeletes = append(h.finalizingDeletes[:i], h.finalizingDeletes[i+1:]...)
				break
			}
		}
		if msg.destroyAttempted {
			h.handleWorktreeDestroyResult(msg)
		} else if msg.workspaceErr != nil {
			h.setError(fmt.Errorf("workspace destroy: %w", msg.workspaceErr))
		}
		return h, nil

	case worktreeHoldersScannedMsg:
		names := uniqueCommands(msg.holders)
		line := "No background processes to stop"
		if len(names) > 0 {
			line = fmt.Sprintf("Will terminate %d process(es): %s", len(msg.holders), strings.Join(names, ", "))
		}
		h.confirmDialog.SetScan(msg.gen, line)
		return h, nil

	case previewTickMsg:
		// Fast preview-only tick — skips status/git work, just refreshes the preview pane.
		if h.focusMode {
			return h, h.previewTick() // focus mode has its own faster tick
		}
		cmds := []tea.Cmd{h.previewTick()}
		if sel := h.selectedSession(); sel != nil && sel.IsAlive() {
			cmds = append(cmds, h.fetchPreview(sel))
		}
		return h, tea.Batch(cmds...)

	case focusTickMsg:
		if !h.focusMode {
			return h, nil
		}
		s := h.selectedSession()
		if s == nil || !s.IsAlive() {
			h.focusMode = false
			h.sidebarDirty = true
			return h, nil
		}
		return h, tea.Batch(h.fetchPreviewFresh(s), h.focusTick())

	case spinnerTickMsg:
		// Advance spinner in whichever dialog is active.
		if h.worktreeDialog.IsVisible() && h.worktreeDialog.loading {
			h.worktreeDialog.frame++
			return h, spinnerTickCmd
		}
		if h.branchDialog.IsVisible() && h.branchDialog.loading {
			h.branchDialog.frame++
			return h, spinnerTickCmd
		}
		if h.createWorkspaceDialog.IsVisible() && h.createWorkspaceDialog.creating {
			h.createWorkspaceDialog.frame++
			return h, spinnerTickCmd
		}
		// Animate pending workspace spinners in sidebar.
		if len(h.pendingWorkspaces) > 0 {
			for _, pw := range h.pendingWorkspaces {
				pw.Frame++
			}
			return h, spinnerTickCmd
		}
		return h, nil

	case whatsNewTickMsg:
		// Advance the badge shimmer; stop the loop (and stop burning CPU) the
		// moment the badge is hidden or a modal takes the screen.
		if !h.hasUnseenWhatsNew || h.modalOpen() {
			h.whatsNewShimmering = false
			return h, nil
		}
		h.whatsNewFrame++
		return h, whatsNewTickCmd()

	case gitInfoUpdateMsg:
		// Workers push per-repo refresh results here. Update is the sole
		// writer of gitInfoCache and goes through writeGitInfo's COW so
		// concurrent readers (View, worker carry-forward) see a stable
		// immutable snapshot. Empty repo paths are filtered defensively.
		//
		// Only origin grouping and the worktree flag feed BuildFlatItems;
		// branch/dirty/PR are rendered live from the cache. So rebuild the
		// flat item list only when one of those structural fields actually
		// changed — otherwise a refresh just marks the sidebar dirty for a
		// re-render. This collapses the per-cycle, all-repos burst (one
		// message per repo) from N full rebuilds down to, in steady state,
		// zero.
		if msg.repo != "" {
			prev := h.gitInfo()[msg.repo]
			h.writeGitInfo(func(next map[string]*git.RepoInfo) bool {
				next[msg.repo] = msg.info
				return true
			})
			if structuralGitChange(prev, msg.info) {
				h.rebuildFlatItems()
			} else {
				h.sidebarDirty = true
			}
		}
		return h, nil

	case gitRepaintMsg:
		// The cache is already current (the fan-out wrote it); this only
		// decides how much of the sidebar has to be recomputed.
		if msg.structural {
			h.rebuildFlatItems()
		} else {
			h.sidebarDirty = true
		}
		return h, nil

	case bootstrapDoneMsg:
		h.booted = true
		h.rebuildFlatItems()
		// Land the cursor on the first actionable row (a session) instead
		// of the first origin header — first keystroke does something
		// useful immediately.
		if idx := FirstSessionItem(h.flatItems); idx >= 0 {
			h.cursor = idx
			h.syncViewport()
		}
		go h.statusWorker()
		// Started here, not earlier: gitWorker and the bootstrap probe both
		// touch repoLastHotAt unlocked, and bootstrap is done by now.
		go h.gitWorker()
		return h, nil

	case splashFrameMsg:
		if h.booted {
			return h, nil
		}
		h.splashFrame++
		return h, h.splashTick()

	case discoveryMsg:
		h.launchpad.SetItems(msg.items)
		// The boot splash stayed up during the scan; reveal the UI now in one
		// transition (launchpad if we found anything, bare empty state if not).
		if !h.booted {
			h.booted = true
			go h.statusWorker()
			// Empty fleet still needs git/PR refresh: this path never emits
			// bootstrapDoneMsg, so without this gitWorker would never start and
			// branch/dirty/PR would stay blank for the whole process lifetime.
			// No bootstrap probe runs here, so the repoLastHotAt reasoning holds.
			go h.gitWorker()
		}
		if len(msg.items) > 0 {
			analytics.Track(analytics.EventOnboardingFirstLaunch, map[string]interface{}{
				"discovered_repos": len(msg.items),
			})
		}
		return h, nil

	case loadSessionsMsg:
		if msg.err != nil {
			h.setError(msg.err)
			return h, nil
		}
		if msg.warning != "" {
			h.setError(fmt.Errorf("%s", msg.warning))
		}
		h.sessions = msg.sessions
		h.shells = msg.shells
		h.rebuildSessionMap()
		// Keep only bindings whose session is present in the loaded view. Do
		// NOT delete absent bindings from storage here: FLEET_DEMO_PREFIX and
		// similar filters shrink the session set transiently, and writing back
		// would permanently destroy real bindings. The FK cascade on session
		// delete handles the only case where a binding should actually vanish.
		if msg.slotBindings != nil {
			h.slotBindings = make(map[int]string, len(msg.slotBindings))
			for slot, id := range msg.slotBindings {
				if _, ok := h.sessionByID[id]; ok {
					h.slotBindings[slot] = id
				}
			}
		}
		// Load pinned repos from storage.
		if pinnedPaths, err := h.storage.LoadPinnedRepos(); err == nil {
			for _, p := range pinnedPaths {
				h.pinnedRepos[p] = true
			}
		}
		// Restore persisted collapse state before defaulting — the default
		// loops below only fill missing keys, so loaded collapses survive.
		if collapsed, err := h.storage.LoadCollapsedGroups(); err == nil {
			for _, key := range collapsed {
				h.repoExpanded[key] = false
			}
		}
		// Restore group snoozes, dropping any that lapsed while fleet was
		// closed so the very first frame is already correct (waiting for the
		// sweep would paint a stale dim group for up to one interval).
		if snoozed, err := h.storage.LoadSnoozedGroups(); err == nil {
			now := time.Now()
			for key, until := range snoozed {
				if until.After(now) {
					h.groupSnooze[key] = until
					continue
				}
				if err := h.storage.SetGroupSnooze(key, time.Time{}); err != nil {
					debuglog.Logger.Error("failed to clear expired group snooze", "key", key, "err", err)
				}
				// Undo the auto-collapse, exactly as maybeWakeSnoozed does on
				// the live path. Without this the two wake paths disagree: a
				// "Tomorrow" snooze — the preset most likely to span a restart —
				// would come back folded forever, with no marker left to
				// explain why. Runs after the persisted-collapse restore above,
				// so it deliberately overrides the row this snooze wrote.
				h.setExpanded(key, true)
			}
		}
		// Default all repos to expanded on first load.
		groups := session.GroupByRepo(h.sessions)
		for repo := range groups {
			if _, exists := h.repoExpanded[repo]; !exists {
				h.repoExpanded[repo] = true
			}
		}
		// Also expand pinned repos that have no sessions.
		for repo := range h.pinnedRepos {
			if _, exists := h.repoExpanded[repo]; !exists {
				h.repoExpanded[repo] = true
			}
		}
		h.ghAvailable = msg.ghAvailable
		// Hydrate gitInfoCache from the persisted PR cache before bootstrap
		// fires. The branch-match guard in refreshAllGitAndPR will drop
		// entries whose checkout has moved since the cache was written;
		// we apply a 24h age cap here as a coarse outer bound — a PR that
		// hasn't been touched for a day shouldn't outlast its row.
		if msg.prCache != nil {
			cutoff := 24 * time.Hour
			now := time.Now()
			h.writeGitInfo(func(next map[string]*git.RepoInfo) bool {
				wrote := false
				for repo, row := range msg.prCache {
					if now.Sub(row.LastPRRefresh) > cutoff {
						continue
					}
					next[repo] = &git.RepoInfo{
						Branch:          row.Branch,
						OriginKey:       row.OriginKey,
						PR:              row.PR,
						LastPRRefresh:   row.LastPRRefresh,
						PRRateLimitedAt: row.PRRateLimitedAt,
					}
					wrote = true
				}
				return wrote
			})
		}
		h.rebuildFlatItems()
		if len(h.flatItems) > 0 && h.cursor == 0 {
			h.cursor = FirstSelectableItem(h.flatItems)
		}

		// Start hook watcher.
		if h.hookWatcher == nil {
			if watcher, err := hooks.NewHookWatcher(); err == nil {
				h.hookWatcher = watcher
				go watcher.Start()
			}
		}

		// Kick off the bootstrap fan-out: resolve every visible repo's
		// origin/branch/dirty info in parallel before the steady-state
		// worker starts. While this runs, View() shows the boot splash.
		// First-launch consent flow: prompt iff (a) the user hasn't
		// been asked yet AND (b) they haven't already opted out via
		// FLEET_TELEMETRY_DISABLED / DO_NOT_TRACK. When env opt-out is
		// set there's nothing to ask about — analytics.Init would
		// refuse to send anyway. We do NOT persist AnalyticsConsentSeen
		// here so the prompt re-appears if the user later unsets the
		// env var. fireStartupAnalytics is still safe to call: Init
		// re-checks opt-out and creates a disabled client.
		var startupCmd tea.Cmd
		if !h.workerStarted {
			h.workerStarted = true
			repos := h.bootstrapRepoSet()
			h.bootstrapRepos = len(repos)
			if len(repos) == 0 {
				// Empty fleet → first-run launchpad. Keep the boot splash up as
				// the single loading page while we scan Claude history, then
				// flip to the launchpad in one transition (see discoveryMsg).
				// This avoids a splash → "scanning" → list flicker. booted and
				// the status worker are deferred to discoveryMsg.
				startupCmd = tea.Batch(h.scanDiscovery(), h.splashTick())
			} else {
				startupCmd = tea.Batch(h.bootstrapGitInfo(repos), h.splashTick())
			}

			switch {
			case h.cfg.AnalyticsConsentSeen:
				h.fireStartupAnalytics(len(groups))
			case analytics.IsOptedOutByEnv():
				h.fireStartupAnalytics(len(groups))
			case h.cfg.TelemetryConfigured():
				// User already set a telemetry preference in config (e.g. from a
				// pre-consent-dialog version). Don't re-prompt; honor the
				// configured mode (GetTelemetryMode migrates the legacy bool).
				h.fireStartupAnalytics(len(groups))
			default:
				h.consentDialog.Show()
			}

			// The theme onboarding is a brand-new-user experience. When the
			// consent prompt is shown it follows that (see consentResultMsg).
			// When no consent prompt is shown we decide here based on whether
			// this is a genuinely fresh install: a brand-new user who skipped
			// consent only because they're env-opted-out of telemetry still
			// deserves onboarding; an existing/returning install must never be
			// surprised by it (it could overwrite their theme), so mark it seen.
			if !h.consentDialog.IsVisible() {
				if h.cfg.IsFirstRun() {
					h.maybeShowOnboarding()
				} else {
					h.markOnboardingSeen()
				}
			}
		}

		// Start listening for hook changes.
		if h.hookWatcher != nil {
			if startupCmd != nil {
				return h, tea.Batch(startupCmd, h.listenForHookChanges)
			}
			return h, h.listenForHookChanges
		}
		return h, startupCmd
	}

	return h, nil
}

// View implements tea.Model.
func (h *Home) View() tea.View {
	if h.isAttaching.Load() {
		return h.chrome("")
	}
	if h.width == 0 {
		return h.chrome(lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render("   fleet"))
	}
	// Shutting down: dim the last frame and float a "Shutting down…" box while the
	// blocking teardown runs off-loop. Same overlay pipeline as the command palette.
	if h.quitting {
		base := dimBackdrop(h.frozenFrame)
		box := renderShutdownBox(h.shutdownFrame)
		x := (h.width - lipgloss.Width(box)) / 2
		y := (h.height - lipgloss.Height(box)) / 2
		return h.chrome(overlayAt(box, base, x, y))
	}
	if !h.booted {
		return h.chrome(RenderSplash(h.width, h.height, h.bootProgress(), h.splashFrame))
	}
	base := h.renderBody()
	// Animated "What's New" badge, top-right of the header row. Only when there
	// are unseen highlights and no modal owns the screen (a modal makes
	// renderBody return its own full-screen view).
	if h.hasUnseenWhatsNew && !h.modalOpen() {
		badge := renderWhatsNewBadge(h.whatsNewFrame)
		base = overlayAt(badge, base, h.width-lipgloss.Width(badge)-1, 0)
	}
	// Command palette is a true overlay: render it on top of the main UI so
	// the sidebar/preview stay visible behind the dialog box. The base is
	// dimmed first so the palette visually lifts above the content.
	if h.commandPalette.IsVisible() {
		base = dimBackdrop(base)
		pv := h.commandPalette.View()
		x := (h.width - lipgloss.Width(pv)) / 2
		y := (h.height - lipgloss.Height(pv)) / 2
		base = overlayAt(pv, base, x, y)
	}
	// Context menu: a dropdown pinned to the cursor's sidebar row. Deliberately
	// no dimBackdrop — it's a small box sitting beside the row it acts on, and
	// dimming the whole app for it would read as a modal takeover.
	if mv := h.contextMenu.View(); mv != "" {
		x, y := h.contextMenu.Position(lipgloss.Width(mv), lipgloss.Height(mv))
		base = overlayAt(mv, base, x, y)
	}
	// Snooze picker: same row-anchored dropdown treatment as the context menu.
	if sv := h.snoozeDialog.View(); sv != "" {
		x, y := h.snoozeDialog.Position(lipgloss.Width(sv), lipgloss.Height(sv))
		base = overlayAt(sv, base, x, y)
	}
	// Contextual tip box — suppressed while a modal owns the screen so a sticky
	// tip never paints over a dialog.
	tip := ""
	if !h.modalOpen() {
		tip = h.tipView()
	}
	toast := h.toasts.View(h.width)
	if tip == "" && toast == "" {
		return h.chrome(base)
	}
	// Stack toast(s) above the tip, then anchor the block bottom-right with a
	// 1-cell right margin and a 1-row lift so it clears the help-bar baseline.
	stack := toast
	if tip != "" {
		if stack != "" {
			stack = lipgloss.JoinVertical(lipgloss.Right, toast, tip)
		} else {
			stack = tip
		}
	}
	x := h.width - lipgloss.Width(stack) - 1
	y := h.height - lipgloss.Height(stack) - 1
	return h.chrome(overlayAt(stack, base, x, y))
}

// chrome wraps rendered content into the tea.View that carries fleet's terminal
// modes. Bubble Tea v2 reads AltScreen/MouseMode off the View every frame, so
// every View() return path must set them or the renderer would toggle
// alt-screen/mouse off mid-run. (v1 set these once via NewProgram options.)
func (h *Home) chrome(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// overlayAt composites top over base with top's top-left at (x, y) using Lip
// Gloss v2's layer compositor. Replaces rmhubbert/bubbletea-overlay, which has
// no v2 release — its maintainer points to Lip Gloss compositing instead.
func overlayAt(top, base string, x, y int) string {
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(top).X(x).Y(y).Z(1),
	).Render()
}

// modalOpen reports whether any full-screen view currently owns the screen.
// Mirrors the early-returns in renderBody (plus the command-palette overlay) so
// a sticky bottom-right tip is never composited on top of a dialog.
func (h *Home) modalOpen() bool {
	return h.consentDialog.IsVisible() ||
		h.onboardingDialog.IsVisible() ||
		h.helpOverlay.IsVisible() ||
		h.releaseNotes.IsVisible() ||
		h.bugReport.IsVisible() ||
		h.settingsDialog.IsVisible() ||
		h.createWorkspaceDialog.IsVisible() ||
		h.worktreeDialog.IsVisible() ||
		h.branchDialog.IsVisible() ||
		h.sessionCreateDialog.IsVisible() ||
		h.newDialog.IsVisible() ||
		h.confirmDialog.IsVisible() ||
		h.renameDialog.IsVisible() ||
		h.commandPalette.IsVisible() ||
		h.contextMenu.IsVisible() ||
		h.snoozeDialog.IsVisible() ||
		h.launchpadActive()
}

func (h *Home) renderBody() string {
	// Modals take priority. Consent goes first — it gates analytics init
	// and must be the user's first interaction with the TUI.
	if h.consentDialog.IsVisible() {
		return h.consentDialog.View()
	}
	if h.onboardingDialog.IsVisible() {
		return h.onboardingDialog.View()
	}
	if h.helpOverlay.IsVisible() {
		return h.helpOverlay.View()
	}
	if h.releaseNotes.IsVisible() {
		return h.releaseNotes.View()
	}
	if h.bugReport.IsVisible() {
		return h.bugReport.View()
	}
	if h.settingsDialog.IsVisible() {
		return h.settingsDialog.View()
	}
	if h.createWorkspaceDialog.IsVisible() {
		return h.createWorkspaceDialog.View()
	}
	if h.worktreeDialog.IsVisible() {
		return h.worktreeDialog.View()
	}
	if h.branchDialog.IsVisible() {
		return h.branchDialog.View()
	}
	if h.sessionCreateDialog.IsVisible() {
		return h.sessionCreateDialog.View()
	}
	if h.newDialog.IsVisible() {
		return h.newDialog.View()
	}
	if h.confirmDialog.IsVisible() {
		return h.confirmDialog.View()
	}
	if h.renameDialog.IsVisible() {
		return h.renameDialog.View()
	}

	// First-run launchpad owns the screen while the fleet is empty.
	if h.launchpadActive() {
		return h.launchpad.View(h.width, h.height)
	}

	var b strings.Builder

	// Load the immutable git/PR snapshot. Lock-free — writers COW into a
	// new map and atomic-swap, so the returned map is stable for the rest
	// of this render pass.
	gitInfoSnap := h.gitInfo()

	// Header.
	header := h.renderHeader()
	b.WriteString(header)
	b.WriteString("\n") // line break that starts panel row 0 — NOT a blank padding row

	// Content area. Header is 1 row, footer is helpBarHeight rows (or
	// focusFilterFooterRows when focus/filter UI is showing the border+content
	// stack), leaving the rest for the panels.
	footerH := h.footerHeight()
	contentHeight := h.height - 1 - footerH
	if contentHeight < 1 {
		contentHeight = 1
	}

	mode := h.layoutMode()

	// Terminal drawer placement depends on the layout. In dual it splits the
	// right column (rendered inside that branch, so it doesn't steal global
	// content height). In single/stacked there is no right column, so it falls
	// back to a full-width band at the bottom — contentHeight shrinks to make
	// room and the panels above appear pushed up.
	bottomDrawer := ""
	if h.drawerVisible() && mode != "dual" {
		bottomDrawer = h.renderDrawer(h.width, h.height-1-footerH-3)
		contentHeight -= lipgloss.Height(bottomDrawer)
		if contentHeight < 1 {
			contentHeight = 1
		}
	}

	// Status counts ride the Sessions panel's top-right border (inset into
	// the title border, after the "Sessions" label). The top app bar no
	// longer renders them.
	statusTitle := h.statusCountsLine(nil)

	// When the drawer is closed, the selected repo's shells ride the bottom
	// border of whichever panel the drawer would open from (preview in
	// dual/stacked, sidebar in single). "" while the drawer is open/absent.
	shellChips := h.collapsedShellChips()

	switch mode {
	case "single":
		// Inner content area = total - 2 for the border on each side.
		innerW := h.width - 2
		innerH := contentHeight - 2
		sidebar := RenderSidebar(h.flatItems, h.sessions, gitInfoSnap, h.slotBindings, h.cursor, h.viewOffset, innerW, innerH, !h.drawerHasFocus())
		sidebar = ensureExactHeight(sidebar, innerH)
		sidebar = ensureExactWidth(sidebar, innerW)
		// Single layout: the sidebar is the only (bottom-most) panel, so the
		// collapsed shell chips ride its bottom border.
		b.WriteString(RenderBorderedPanelInsets(sidebar, "Sessions", statusTitle, shellChips, "", h.width, contentHeight, h.focusMode))
	case "stacked":
		sidebarHeight := (contentHeight * 55) / 100
		if sidebarHeight < 3 {
			sidebarHeight = 3
		}
		previewHeight := contentHeight - sidebarHeight - 1 // 1 for gap row
		innerW := h.width - 2

		sidebarInner := RenderSidebar(h.flatItems, h.sessions, gitInfoSnap, h.slotBindings, h.cursor, h.viewOffset, innerW, sidebarHeight-2, !h.drawerHasFocus())
		sidebarInner = ensureExactHeight(sidebarInner, sidebarHeight-2)
		sidebarInner = ensureExactWidth(sidebarInner, innerW)
		b.WriteString(RenderBorderedPanelTopRight(sidebarInner, "Sessions", statusTitle, h.width, sidebarHeight, h.focusMode))
		b.WriteString("\n\n")

		s, content := h.selectedPreview()
		previewRepoInfo := h.repoInfoFromSnap(gitInfoSnap)
		previewInner := RenderPreview(s, content, previewRepoInfo, innerW, previewHeight-2, h.focusMode)
		previewInner = ensureExactHeight(previewInner, previewHeight-2)
		previewInner = ensureExactWidth(previewInner, innerW)
		previewTitle := BuildPreviewTitle(s, previewRepoInfo, h.focusMode, h.width-6)
		previewFooter := BuildPreviewFooter(s, h.width-6)
		// Stacked: preview is the bottom-most panel, so it carries the chips.
		b.WriteString(RenderBorderedPanelInsets(previewInner, previewTitle, "", shellChips, previewFooter, h.width, previewHeight, h.focusMode))
	default: // dual
		gap := 1 // single-column gap between the two bordered panels
		// Sidebar wants a ~target absolute width that's comfortable for long
		// branch names and session titles — so on a Mac 14" (~150 cols) it's
		// ~40%, on a wide monitor (~250 cols) it shrinks to ~25% so the
		// preview keeps its share. Cap at 45% of total so it never dominates
		// a small terminal; floor at 22 cols so the headers don't collapse.
		const sidebarTargetCols = 65
		sidebarWidth := sidebarTargetCols
		if cap := h.width * 45 / 100; sidebarWidth > cap {
			sidebarWidth = cap
		}
		if sidebarWidth < 22 {
			sidebarWidth = 22
		}
		previewWidth := h.width - sidebarWidth - gap

		sidebarInnerW := sidebarWidth - 2
		previewInnerW := previewWidth - 2
		innerH := contentHeight - 2

		// In focus mode, reuse cached sidebar to avoid expensive rebuild on every keystroke.
		var leftPanel string
		if h.focusMode && !h.sidebarDirty && h.cachedSidebar != "" {
			leftPanel = h.cachedSidebar
		} else {
			inner := RenderSidebar(h.flatItems, h.sessions, gitInfoSnap, h.slotBindings, h.cursor, h.viewOffset, sidebarInnerW, innerH, !h.drawerHasFocus())
			inner = ensureExactHeight(inner, innerH)
			inner = ensureExactWidth(inner, sidebarInnerW)
			leftPanel = RenderBorderedPanelTopRight(inner, "Sessions", statusTitle, sidebarWidth, contentHeight, h.focusMode)
			h.cachedSidebar = leftPanel
			h.sidebarDirty = false
		}

		// Right column: preview on top; the terminal drawer (when open) splits
		// the bottom of the same column, leaving the session list untouched.
		previewPanelH := contentHeight
		rightDrawer := ""
		if h.drawerVisible() {
			rightDrawer = h.renderDrawer(previewWidth, contentHeight-drawerMinPreviewRows)
			previewPanelH = contentHeight - lipgloss.Height(rightDrawer)
			if previewPanelH < drawerMinPreviewRows {
				previewPanelH = drawerMinPreviewRows
			}
		}
		previewInnerH := previewPanelH - 2
		if previewInnerH < 1 {
			previewInnerH = 1
		}

		s, content := h.selectedPreview()
		previewRepoInfo := h.repoInfoFromSnap(gitInfoSnap)
		previewInner := RenderPreview(s, content, previewRepoInfo, previewInnerW, previewInnerH, h.focusMode)
		previewInner = ensureExactHeight(previewInner, previewInnerH)
		previewInner = ensureExactWidth(previewInner, previewInnerW)
		previewTitle := BuildPreviewTitle(s, previewRepoInfo, h.focusMode, previewWidth-6)
		previewFooter := BuildPreviewFooter(s, previewWidth-6)
		// Dual: the drawer opens from the bottom of this right column, so its
		// collapsed chips ride the preview's bottom-left border (shellChips is
		// "" while the drawer is open, since the drawer then shows the shells).
		rightPanel := RenderBorderedPanelInsets(previewInner, previewTitle, "", shellChips, previewFooter, previewWidth, previewPanelH, h.focusMode)
		if rightDrawer != "" {
			rightPanel = lipgloss.JoinVertical(lipgloss.Left, rightPanel, rightDrawer)
		}

		// Build the single-column gap as transparent spaces.
		gapLines := make([]string, contentHeight)
		for i := range gapLines {
			gapLines[i] = " "
		}
		gapCol := strings.Join(gapLines, "\n")

		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, gapCol, rightPanel))
	}

	// Terminal drawer band, below the panels (single/stacked only; dual renders
	// the drawer inside its right column above).
	if bottomDrawer != "" {
		b.WriteString("\n")
		b.WriteString(bottomDrawer)
	}

	// Pad to fill content area.
	lineCount := strings.Count(b.String(), "\n") + 1
	for lineCount < h.height-footerH {
		b.WriteString("\n")
		lineCount++
	}

	// Focus mode bar / Filter bar / Help bar.
	if h.focusMode {
		border := lipgloss.NewStyle().Foreground(ColorAccent).Render(strings.Repeat("─", h.width))
		b.WriteString("\n")
		b.WriteString(border + "\n")
		b.WriteString(" " + HelpKeyStyle.Render("esc") + " " + HelpDescStyle.Render("Unfocus") + "  " +
			DimStyle.Render("all keys forwarded to session"))
		lineCount += 2 // border + shortcut line
	} else if h.filterActive {
		border := lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("─", h.width))
		b.WriteString("\n")
		b.WriteString(border + "\n")
		b.WriteString(" " + HelpKeyStyle.Render("/") + " " + h.filterInput.View())
		lineCount += 2
	} else if h.filterText != "" {
		// Show active filter indicator even when not typing.
		border := lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("─", h.width))
		b.WriteString("\n")
		b.WriteString(border + "\n")
		b.WriteString(" " + HelpKeyStyle.Render("/") + " " + DimStyle.Render(h.filterText) + "  " + DimStyle.Render("(/ to edit, esc to clear)"))
		lineCount += 2
	} else {
		// Help bar: 1 row of contextual hotkeys. The leading \n in
		// renderHelpBar's output is the row break from the panel bottom;
		// the row of keys itself is the only visible content.
		b.WriteString(h.renderHelpBar())
		lineCount++
	}

	// Track height mismatches (counter for bug report, log only on first occurrence).
	// Uses incremental lineCount instead of re-scanning the output.
	if h.height > 0 && lineCount != h.height {
		diff := lineCount - h.height
		prevCount := h.renderStats.HeightMismatchCount
		h.renderStats.RecordHeightMismatch(diff)
		if prevCount == 0 {
			debuglog.Logger.Warn("View height mismatch detected",
				"output_lines", lineCount,
				"expected", h.height,
				"diff", diff,
				"layout", h.layoutMode(),
			)
		}
	}

	return b.String()
}

// --- Key handling ---

// routeToModal forwards msg to the topmost visible modal dialog/overlay and
// reports whether one consumed it. Shared by handleKey and handlePaste so text
// dialogs receive both key presses and bracketed paste (tea.PasteMsg, which is
// not a KeyMsg in Bubble Tea v2 and so never flows through handleKey).
func (h *Home) routeToModal(msg tea.Msg) (tea.Cmd, bool) {
	switch {
	case h.helpOverlay.IsVisible():
		overlay, cmd := h.helpOverlay.Update(msg)
		h.helpOverlay = overlay
		return cmd, true
	case h.releaseNotes.IsVisible():
		dialog, cmd := h.releaseNotes.Update(msg)
		h.releaseNotes = dialog
		return cmd, true
	case h.consentDialog.IsVisible():
		dialog, cmd := h.consentDialog.Update(msg)
		h.consentDialog = dialog
		return cmd, true
	case h.onboardingDialog.IsVisible():
		dialog, cmd := h.onboardingDialog.Update(msg)
		h.onboardingDialog = dialog
		return cmd, true
	case h.bugReport.IsVisible():
		dialog, cmd := h.bugReport.Update(msg)
		h.bugReport = dialog
		return cmd, true
	case h.settingsDialog.IsVisible():
		dialog, cmd := h.settingsDialog.Update(msg)
		h.settingsDialog = dialog
		return cmd, true
	case h.createWorkspaceDialog.IsVisible():
		dialog, cmd := h.createWorkspaceDialog.Update(msg)
		h.createWorkspaceDialog = dialog
		// User cancelled with ESC — drop fork ctx. Submit (Enter) also hides
		// the dialog but emits a workspaceCreateMsg that consumes the ctx,
		// so we must NOT clear on every Hide.
		if isEscKey(msg) && !h.createWorkspaceDialog.IsVisible() && !h.worktreeDialog.IsVisible() {
			h.clearPendingFork()
		}
		return cmd, true
	case h.worktreeDialog.IsVisible():
		dialog, cmd := h.worktreeDialog.Update(msg)
		h.worktreeDialog = dialog
		// Same reasoning as createWorkspaceDialog above: only ESC clears.
		if isEscKey(msg) && !h.worktreeDialog.IsVisible() && !h.createWorkspaceDialog.IsVisible() {
			h.clearPendingFork()
		}
		return cmd, true
	case h.branchDialog.IsVisible():
		dialog, cmd := h.branchDialog.Update(msg)
		h.branchDialog = dialog
		return cmd, true
	case h.commandPalette.IsVisible():
		dialog, cmd := h.commandPalette.Update(msg)
		h.commandPalette = dialog
		return cmd, true
	case h.snoozeDialog.IsVisible():
		dialog, cmd := h.snoozeDialog.Update(msg)
		h.snoozeDialog = dialog
		return cmd, true
	case h.contextMenu.IsVisible():
		dialog, cmd := h.contextMenu.Update(msg)
		h.contextMenu = dialog
		return cmd, true
	case h.sessionCreateDialog.IsVisible():
		dialog, cmd := h.sessionCreateDialog.Update(msg)
		h.sessionCreateDialog = dialog
		return cmd, true
	case h.newDialog.IsVisible():
		dialog, cmd := h.newDialog.Update(msg)
		h.newDialog = dialog
		return cmd, true
	case h.confirmDialog.IsVisible():
		dialog, cmd := h.confirmDialog.Update(msg)
		h.confirmDialog = dialog
		return cmd, true
	case h.renameDialog.IsVisible():
		dialog, cmd := h.renameDialog.Update(msg)
		h.renameDialog = dialog
		return cmd, true
	}
	return nil, false
}

// handlePaste routes bracketed paste (cmd+v → tea.PasteMsg) to whatever owns
// text input: a modal dialog, the focused split session, the drawer's shell, or
// the sidebar filter. v2 delivers paste as its own message, not a KeyMsg, so it
// bypasses handleKey.
func (h *Home) handlePaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	if cmd, handled := h.routeToModal(msg); handled {
		return h, cmd
	}
	if h.focusMode {
		if s := h.selectedSession(); s != nil && s.IsAlive() {
			if cc := h.getControlClient(); cc != nil {
				_ = cc.SendLiteralKeys(s.GetTmuxSession().Name, msg.Content)
			}
		}
		return h, nil
	}
	if h.drawerHasFocus() {
		if sh := h.activeShell(); sh != nil {
			if cc := h.getControlClient(); cc != nil {
				_ = cc.SendLiteralKeys(sh.TmuxName(), msg.Content)
			}
		}
		return h, nil
	}
	if h.filterActive {
		var cmd tea.Cmd
		h.filterInput, cmd = h.filterInput.Update(msg)
		h.filterText = h.filterInput.Value()
		h.rebuildFlatItems()
		if len(h.flatItems) > 0 {
			h.cursor = FirstSelectableItem(h.flatItems)
		} else {
			h.cursor = 0
		}
		h.syncViewport()
		if previewCmd := h.fetchPreviewForSelected(); previewCmd != nil {
			return h, tea.Batch(cmd, previewCmd)
		}
		return h, cmd
	}
	return h, nil
}

func (h *Home) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Route to the active modal dialog/overlay first (keys + paste share this).
	if cmd, handled := h.routeToModal(msg); handled {
		return h, cmd
	}

	// First-run launchpad: drive the recent-repos picker. Space multi-selects,
	// Enter launches the checked set (or the cursor row). Unhandled keys
	// (n to type a path, ?, S, q, …) fall through to the main switch below.
	if h.launchpadActive() {
		switch msg.String() {
		case "j", "down":
			h.launchpad.Move(1)
			return h, nil
		case "k", "up":
			h.launchpad.Move(-1)
			return h, nil
		case "space":
			h.launchpad.Toggle()
			return h, nil
		case "A":
			h.launchpad.ToggleAll()
			return h, nil
		case "enter":
			// Consume the launchpad as we fire the set: launching is async, so
			// without this a second Enter (before the new sessions register)
			// would re-launch the whole set and duplicate every resumed
			// conversation.
			set := h.launchpad.LaunchSet()
			h.launchpadDismissed = true
			return h, h.launchLaunchpadSet(set)
		case "esc":
			h.launchpadDismissed = true
			return h, nil
		}
	}

	// Focus mode: forward keys to tmux session.
	if h.focusMode {
		return h.handleFocusKey(msg)
	}

	// Filter mode: route keys to filter input.
	if h.filterActive {
		switch msg.String() {
		case "esc":
			h.filterActive = false
			h.filterText = ""
			h.filterInput.SetValue("")
			h.filterInput.Blur()
			h.rebuildFlatItems()
			// Reset cursor to first item.
			if len(h.flatItems) > 0 {
				h.cursor = FirstSelectableItem(h.flatItems)
			}
			h.syncViewport()
			return h, h.fetchPreviewForSelected()
		case "enter":
			// Accept filter and exit filter mode.
			h.filterActive = false
			h.filterInput.Blur()
			return h, nil
		default:
			var cmd tea.Cmd
			h.filterInput, cmd = h.filterInput.Update(msg)
			h.filterText = h.filterInput.Value()
			h.rebuildFlatItems()
			// Reset cursor when filter changes.
			if len(h.flatItems) > 0 {
				h.cursor = FirstSelectableItem(h.flatItems)
			} else {
				h.cursor = 0
			}
			h.syncViewport()
			if previewCmd := h.fetchPreviewForSelected(); previewCmd != nil {
				return h, tea.Batch(cmd, previewCmd)
			}
			return h, cmd
		}
	}

	// While the terminal drawer is focused it captures keys (tab nav, type-mode,
	// lifecycle) so they don't also drive the session list. Sits after the focus
	// (split) route above so split focus mode wins if somehow both are set.
	if h.drawerHasFocus() {
		return h.handleTypingKey(msg)
	}

	// Snapshot and clear the double-tap window: only a consecutive digit press
	// should attach, so any other key falling through this switch invalidates
	// the window for free. The digit case restores the snapshot before jumping.
	prevSlotTapSlot := h.lastSlotTapSlot
	prevSlotTapAt := h.lastSlotTapAt
	h.lastSlotTapSlot = -1

	switch msg.String() {
	case "`": // open the terminal drawer + move focus into it
		return h, h.openDrawerTyping()
	case "j", "down":
		h.cursor = NextSelectableItem(h.flatItems, h.cursor, 1)
		h.syncViewport()
		return h, h.fetchPreviewForSelected()
	case "k", "up":
		h.cursor = NextSelectableItem(h.flatItems, h.cursor, -1)
		h.syncViewport()
		return h, h.fetchPreviewForSelected()
	case "shift+down":
		h.jumpToHeader(1)
		analytics.Track(analytics.EventHeaderJump, nil)
		return h, h.fetchPreviewForSelected()
	case "shift+up":
		h.jumpToHeader(-1)
		analytics.Track(analytics.EventHeaderJump, nil)
		return h, h.fetchPreviewForSelected()
	case "pgdown":
		target := h.cursor + h.sidebarPanelRows()
		if target > len(h.flatItems)-1 {
			target = len(h.flatItems) - 1
		}
		if target < 0 {
			target = 0
		}
		h.cursor = target
		h.syncViewport()
		return h, h.fetchPreviewForSelected()
	case "pgup":
		target := h.cursor - h.sidebarPanelRows()
		if target < 0 {
			target = 0
		}
		h.cursor = target
		h.syncViewport()
		return h, h.fetchPreviewForSelected()
	case "enter":
		// Toggle repo group or attach session.
		if h.cursor >= 0 && h.cursor < len(h.flatItems) && h.flatItems[h.cursor].IsRepoHeader {
			h.toggleRepoGroup()
			return h, nil
		}
		// A suspended session has no live tmux — resume it (recreates tmux +
		// `--resume`), then attach once it's up. Overrides split mode: there is no
		// live preview to focus.
		if s := h.selectedSession(); s != nil && s.GetStatus() == session.StatusSuspended {
			return h, h.resumeSelected(s)
		}
		if h.cfg.GetEnterMode() == "split" {
			return h, h.enterFocusMode()
		}
		if s := h.selectedSession(); s != nil {
			h.actionLog.Add("attach session", s.Title, true)
			analytics.Track(analytics.EventSessionAttached, map[string]interface{}{"agent": string(s.Agent)})
			if analytics.MarkOnboardingMilestone(analytics.MilestoneFirstAttach) {
				analytics.Track(analytics.EventOnboardingFirstAttach, map[string]interface{}{
					"seconds_since_install": int(analytics.SecondsSinceInstall()),
				})
			}
		}
		return h, h.attachSelected()
	case "tab":
		if h.cursor >= 0 && h.cursor < len(h.flatItems) && h.flatItems[h.cursor].IsRepoHeader {
			return h, nil
		}
		if h.cfg.GetEnterMode() == "split" {
			if s := h.selectedSession(); s != nil {
				h.actionLog.Add("attach session", s.Title, true)
			}
			return h, h.attachSelected()
		}
		return h, h.enterFocusMode()
	case "space":
		// Jump to next waiting (or finished) session.
		h.jumpToNextAttentionSession()
		analytics.Track(analytics.EventSpaceJump, nil)
		return h, h.fetchPreviewForSelected()
	case "left", "h":
		h.collapseRepoAtCursor()
		return h, nil
	case "right", "l":
		h.expandRepoAtCursor()
		return h, nil
	case "a":
		// Instant session at current repo path.
		repoPath := h.resolveCurrentRepo()
		if repoPath == "" {
			h.newDialog.Show()
			return h, nil
		}
		repoName := filepath.Base(repoPath)
		h.actionLog.Add("create session", repoPath, true)
		return h.handleSessionCreate(sessionCreateMsg{
			path:  repoPath,
			title: repoName,
		})
	case "A":
		// Session creation dialog with agent picker.
		repoPath := h.resolveCurrentRepo()
		if repoPath == "" {
			h.newDialog.Show()
			return h, nil
		}
		h.sessionCreateDialog.Show(repoPath, filepath.Base(repoPath), agent.Parse(h.cfg.GetDefaultAgent()))
		return h, nil
	case "n":
		// New session at any repo path.
		h.newDialog.Show()
		return h, nil
	case "w":
		// New worktree session (works on a session, checkout header, or origin header).
		repoPath := h.resolveWorktreeBaseRepo()
		if repoPath == "" {
			h.setError(fmt.Errorf("no repo selected"))
			return h, nil
		}
		h.worktreeDialog.ShowLoading()
		return h, tea.Batch(h.fetchWorkspaceListForRepo(repoPath), spinnerTickCmd)
	case "f":
		return h, h.forkSelected()
	case "F":
		return h, h.forkToWorktreeSelected()
	case "z":
		// Scope follows the cursor, like `d`. On an already-snoozed row this
		// wakes immediately rather than re-opening the picker — the common
		// follow-up to "I snoozed this" is "actually, bring it back".
		sc, ok := h.snoozeScopeAtCursor()
		if !ok {
			return h, nil
		}
		if h.snoozed(sc) {
			h.clearSnooze(sc)
			return h, nil
		}
		return h.dispatchCommand("snooze")
	case ".":
		// Context menu for the row under the cursor. No items means there's nothing
		// actionable there (spacer, pending workspace, empty fleet) — stay silent
		// rather than pop an empty box.
		title, items := h.buildContextMenuItems()
		if len(items) == 0 {
			return h, nil
		}
		// Remember which row this menu speaks for, so the pick can't land on a
		// different one if an async rebuild moves the cursor meanwhile.
		h.contextMenuTarget = h.targetForCursor()
		h.contextMenu.SetAnchor(h.contextMenuAnchor())
		h.contextMenu.Show(title, items)
		return h, nil
	case "d":
		return h, h.deleteAtCursor()
	case "u":
		return h.undoDelete()
	case "r":
		return h, h.confirmRestartSelected()
	case "R":
		return h, h.renameSelected()
	case "m":
		if s := h.selectedSession(); s != nil {
			h.actionLog.Add("mark unread", s.Title, true)
		}
		h.markUnreadSelected()
		return h, nil
	case "e":
		if s := h.selectedSession(); s != nil {
			h.actionLog.Add("open editor", fmt.Sprintf("%q at %s", h.cfg.GetEditor(), s.ProjectPath), true)
			analytics.Track(analytics.EventEditorOpened, map[string]interface{}{"editor": h.cfg.GetEditor()})
		}
		return h, h.openEditorSelected()
	case "p":
		h.actionLog.Add("open PR", "", true)
		analytics.Track(analytics.EventPROpened, nil)
		return h, h.openPRInBrowser()
	case "Y":
		if s := h.selectedSession(); s != nil {
			h.actionLog.Add("quick approve", s.Title, true)
			analytics.Track(analytics.EventQuickApprove, nil)
		}
		return h, h.quickApproveSelected()
	case "b":
		repoPath := h.resolveCurrentRepo()
		if repoPath == "" {
			return h, nil
		}
		h.branchDialog.ShowLoading()
		return h, tea.Batch(h.fetchBranchList(repoPath), spinnerTickCmd)
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		digit := int(msg.String()[0] - '0')
		switch h.slotAssignMode {
		case 1:
			h.slotAssignMode = 0
			h.bindCurrentSessionToSlot(digit)
			return h, nil
		case 2:
			h.slotAssignMode = 0
			h.unbindSlot(digit)
			return h, nil
		}
		// Restore double-tap state so two consecutive digit presses attach.
		h.lastSlotTapSlot = prevSlotTapSlot
		h.lastSlotTapAt = prevSlotTapAt
		return h.jumpToSlot(digit)
	case "alt+0", "alt+1", "alt+2", "alt+3", "alt+4", "alt+5", "alt+6", "alt+7", "alt+8", "alt+9":
		s := msg.String()
		digit := int(s[len(s)-1] - '0')
		h.bindCurrentSessionToSlot(digit)
		return h, nil
	case "=":
		switch h.slotAssignMode {
		case 0:
			h.slotAssignMode = 1
			h.setInfo("Slot: digit=bind · = again=unbind · Esc=cancel")
		case 1:
			h.slotAssignMode = 2
			h.setInfo("Unbind slot: digit=clear · Esc=cancel")
		default:
			h.slotAssignMode = 0
			h.setInfo("Slot assign cancelled")
			return h, nil
		}
		h.slotAssignExpires = time.Now().Add(2 * time.Second)
		return h, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return slotAssignTimeoutMsg{} })
	case "/":
		h.filterActive = true
		h.filterInput.Focus()
		analytics.Track(analytics.EventFilterUsed, nil)
		return h, nil
	case "esc":
		// Cancel pending slot-assign leader.
		if h.slotAssignMode != 0 {
			h.slotAssignMode = 0
			h.setInfo("Slot assign cancelled")
			return h, nil
		}
		// Clear active filter.
		if h.filterText != "" {
			h.filterText = ""
			h.filterInput.SetValue("")
			h.rebuildFlatItems()
			if len(h.flatItems) > 0 {
				h.cursor = FirstSelectableItem(h.flatItems)
			}
			h.syncViewport()
			return h, h.fetchPreviewForSelected()
		}
		return h, nil
	case "ctrl+k":
		h.commandPalette.Show(h.buildPaletteItems(), h.recentPaletteIDs)
		h.cfg.NoteFeatureUsed(tipCmdPaletteID, tipLearnedThreshold) // retire the discovery tip once they know it
		analytics.Track(analytics.EventCommandPalette, nil)
		return h, nil
	case "S":
		h.settingsDialog.Show()
		analytics.Track(analytics.EventSettingsOpened, nil)
		return h, nil
	case "W":
		return h.openReleaseNotes(true)
	case "X":
		h.dismissActiveTip()
		return h, nil
	case "!":
		return h.openBugReport()
	case "D":
		s := h.selectedSession()
		if s == nil {
			return h, nil
		}
		h.actionLog.Add("status snapshot", s.Title, true)
		hb := h.workerHeartbeat()
		return h, func() tea.Msg {
			snap := captureStatusSnapshot(s, s.ID, hb, true, true)
			return statusSnapshotMsg{path: snap.path, err: snap.err}
		}
	case "?":
		h.helpOverlay.Show()
		return h, nil
	case "ctrl+c":
		return h, h.beginQuit("ctrl+c")
	}

	return h, nil
}

// shutdownTick schedules the next shutdown-overlay spinner advance (~80ms,
// matching splashTick). Self-rescheduled by Update while `quitting`, until the
// teardown command emits tea.Quit and the program exits.
func (h *Home) shutdownTick() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg {
		return shutdownFrameMsg(t)
	})
}

// beginQuit starts the shutdown sequence. It flips into the "Shutting down…"
// overlay (freezing the current frame behind it) and hands the blocking teardown
// off to performShutdown, so a frame renders and the spinner animates instead of
// the UI appearing frozen. Shared by Ctrl+C and the command-palette Quit command.
func (h *Home) beginQuit(source string) tea.Cmd {
	if h.quitting {
		return nil // already tearing down; ignore repeat presses
	}
	debuglog.Logger.Info("quit requested", "source", source)
	h.quitting = true
	// Capture the frame to dim behind the overlay, mirroring View()'s guard
	// order so a quit during boot freezes what the user is actually looking at
	// (splash) rather than a half-built body — and never runs renderBody with a
	// negative inner width in the pre-first-WindowSizeMsg (width == 0) window.
	switch {
	case h.width == 0:
		h.frozenFrame = "" // nothing meaningful to freeze yet
	case !h.booted:
		h.frozenFrame = RenderSplash(h.width, h.height, h.bootProgress(), h.splashFrame)
	default:
		h.frozenFrame = h.renderBody()
	}
	h.cancel() // stop the worker now so it stops feeding Update
	return tea.Batch(h.shutdownTick(), h.performShutdown())
}

// performShutdown runs the full teardown off the Update loop — close the hook
// watcher / control client / drawer stream, finalize pending deletes, emit quit
// analytics, drain telemetry — then returns tea.Quit. Runs in a command
// goroutine; the Update loop is guarded (see the h.quitting short-circuit) so
// nothing else touches the state this mutates concurrently.
func (h *Home) performShutdown() tea.Cmd {
	return func() tea.Msg {
		if h.hookWatcher != nil {
			h.hookWatcher.Stop()
		}
		if h.controlClient != nil {
			h.controlClient.Close()
		}
		h.teardownShellStream()
		// Finalize all pending deletes before quitting.
		h.finalizeAllPendingDeletes()

		uptime := time.Since(h.startTime).Seconds()

		// h.cancel() (in beginQuit) only signals the status worker — it can
		// still be mid-cycle, holding workerMu and mutating h.sessions. Take
		// the lock for the direct reads; collectSnapshot and anyAttached
		// self-lock.
		h.workerMu.Lock()
		runningCount := 0
		waitingCount := 0
		for _, s := range h.sessions {
			switch s.GetStatus() {
			case session.StatusRunning:
				runningCount++
			case session.StatusWaiting:
				waitingCount++
			}
		}
		sessionCount := len(h.sessions)
		h.workerMu.Unlock()

		analytics.Track(analytics.EventAppQuit, map[string]interface{}{
			"uptime_seconds": int(uptime),
			"session_count":  sessionCount,
		})
		analytics.Distribution(analytics.MetricAppUptimeSeconds, uptime, nil)
		analytics.EmitSnapshot(h.collectSnapshot())

		if runningCount > 0 || waitingCount > 0 {
			analytics.Track(analytics.EventQuitWithRunningSessions, map[string]interface{}{
				"running_count": runningCount,
				"waiting_count": waitingCount,
			})
		}

		if analytics.MarkOnboardingMilestone(analytics.MilestoneFirstQuit) {
			analytics.Track(analytics.EventOnboardingFirstQuit, map[string]interface{}{
				"uptime_seconds":         int(uptime),
				"session_count":          sessionCount,
				"attached_at_least_once": h.anyAttached(),
			})
		}

		analytics.Shutdown()
		return tea.Quit()
	}
}

// --- Session operations ---

func (h *Home) markSessionAccessed(s *session.Session) {
	s.MarkAccessed()
	if err := h.storage.UpdateLastAccessed(s.ID); err != nil {
		debuglog.Logger.Error("storage: UpdateLastAccessed", "id", s.ID, "err", err)
	}
}

func (h *Home) attachSelected() tea.Cmd {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) || h.flatItems[h.cursor].IsRepoHeader {
		return nil
	}
	return h.attachSession(h.flatItems[h.cursor].Session)
}

// attachSessionByID attaches a specific session regardless of the cursor. Used by
// resume-from-suspend, where the cursor may have moved during the async Restart —
// attaching the cursor's row would drop the user into a session they didn't pick.
func (h *Home) attachSessionByID(id string) tea.Cmd {
	s, ok := h.sessionByID[id]
	if !ok {
		return nil
	}
	return h.attachSession(s)
}

func (h *Home) attachSession(s *session.Session) tea.Cmd {
	if s == nil || !s.IsAlive() {
		return nil
	}

	h.markSessionAccessed(s)
	s.Acknowledge()
	if err := h.storage.SetAcknowledged(s.ID, true); err != nil {
		debuglog.Logger.Error("storage: SetAcknowledged", "id", s.ID, "err", err)
	}

	h.isAttaching.Store(true)
	h.attachStartedAt.Store(time.Now().UnixNano())
	// Record the attached session so the memory-pressure sweep never hibernates
	// the pane the user is sitting in (LastAccessedAt doesn't refresh mid-attach).
	// Guarded by workerMu — the worker reads it.
	h.workerMu.Lock()
	h.attachedSessionID = s.ID
	h.workerMu.Unlock()
	attachStart := time.Now()

	return tea.Exec(attachCmd{session: s.GetTmuxSession()}, func(err error) tea.Msg {
		// CRITICAL: Clear isAttaching before returning the message.
		// Prevents race where View() returns empty string after detach.
		h.isAttaching.Store(false)
		h.attachStartedAt.Store(0)
		h.workerMu.Lock()
		h.attachedSessionID = ""
		h.workerMu.Unlock()
		// Only record uptime when the attach actually entered the session
		// and exited via a normal detach (Ctrl+Q). A non-nil err here means
		// the attach failed before / during entry (tmux gone, etc.) — the
		// near-zero "uptime" would be noise in the distribution.
		if err == nil {
			analytics.Distribution(analytics.MetricAttachedSessionUptimeSecs,
				time.Since(attachStart).Seconds(), nil)
		}
		return statusUpdateMsg{attachedSessionID: s.ID}
	})
}

type attachCmd struct {
	session *tmux.Session
}

func (a attachCmd) Run() error {
	return a.session.Attach(context.Background())
}

func (a attachCmd) SetStdin(r io.Reader)  {}
func (a attachCmd) SetStdout(w io.Writer) {}
func (a attachCmd) SetStderr(w io.Writer) {}

// scanDiscovery reads the user's Claude Code history off the UI thread and
// feeds the launchpad. Best-effort: a nil/empty result just leaves the bare
// empty state in place.
func (h *Home) scanDiscovery() tea.Cmd {
	return func() tea.Msg {
		return discoveryMsg{items: discovery.RecentRepos(8)}
	}
}

// launchpadActive reports whether the launchpad should own the screen: a truly
// empty fleet (no sessions, no pinned repos), not dismissed, with the scan
// having found something. The scan completes before booted flips (the splash
// covers it), so by the time this can return true the items are already in;
// an empty scan steps aside for the bare empty state.
func (h *Home) launchpadActive() bool {
	if !h.booted || h.launchpadDismissed {
		return false
	}
	if len(h.sessions) > 0 || len(h.pinnedRepos) > 0 {
		return false
	}
	return h.launchpad.HasItems()
}

// maybeShowOnboarding shows the one-time first-run theme/onboarding screen,
// unless the user has already seen it. Called after the consent flow resolves
// so the order is consent → theme onboarding → launchpad.
func (h *Home) maybeShowOnboarding() {
	if !h.cfg.DisplayOnboardingSeen {
		h.onboardingDialog.Show()
	}
}

// markOnboardingSeen records that onboarding need not appear, without showing
// it — used for existing/returning installs so an upgrade never surprises them
// with the first-run theme picker (which would overwrite their chosen theme).
func (h *Home) markOnboardingSeen() {
	if h.cfg.DisplayOnboardingSeen {
		return
	}
	h.cfg.DisplayOnboardingSeen = true
	if err := h.cfg.Save(); err != nil {
		debuglog.Logger.Error("config: save onboarding-seen", "err", err)
	}
}

func (h *Home) handleSessionCreate(msg sessionCreateMsg) (tea.Model, tea.Cmd) {
	// Empty agent → configured default.
	ag := msg.agent
	if ag == "" {
		ag = agent.Parse(h.cfg.GetDefaultAgent())
	}
	if _, err := exec.LookPath(ag.Binary()); err != nil {
		h.setError(fmt.Errorf("%s CLI not found — install %s to create sessions", ag.Binary(), ag.DisplayName()))
		return h, nil
	}
	// Codex prompts to trust a new directory on first launch; pre-seed trust so
	// the session opens straight to the prompt.
	if ag == agent.Codex {
		if err := hooks.EnsureCodexDirTrust(hooks.GetCodexConfigDir(), msg.path); err != nil {
			debuglog.Logger.Error("codex dir trust seeding failed", "path", msg.path, "err", err)
		}
	}
	msg.agent = ag
	return h, h.startSessionCmd(msg)
}

// tccProbeResultMsg carries the outcome of a lazy macOS-TCC access probe.
// determined is false when the probe was inconclusive (no server / tmux error).
type tccProbeResultMsg struct {
	root       string
	blocked    bool
	determined bool
}

// protectedRoot reports whether path lives inside a macOS TCC-protected user
// folder (~/Documents, ~/Desktop, ~/Downloads) and returns that root. A
// daemonized tmux server is denied access to these unless the user grants Full
// Disk Access, so anything fleet spawns there fails with "Operation not
// permitted". Returns ("", false) for any other location.
func protectedRoot(path string) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil || path == "" {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	// Resolve symlinks so a repo reached via a link into a protected folder is
	// still caught. Best-effort — EvalSymlinks fails on a path that doesn't exist
	// yet (e.g. a worktree mid-creation), so fall back to abs.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	for _, name := range []string{"Documents", "Desktop", "Downloads"} {
		root := filepath.Join(home, name)
		// Case-insensitive compare: the default macOS APFS/HFS+ volume is
		// case-insensitive, so ~/documents is the same folder as ~/Documents.
		if strings.EqualFold(abs, root) ||
			strings.HasPrefix(strings.ToLower(abs), strings.ToLower(root)+string(os.PathSeparator)) {
			return root, true
		}
	}
	return "", false
}

// probeTCCCmd returns a command that checks — once per protected root per launch
// — whether tmux is blocked from reading a macOS-protected folder that path sits
// under. Returns nil when path isn't under such a root or its root was already
// probed. The probe is blocking I/O (a tmux run-shell), so it runs off the Update
// goroutine and reports back via tccProbeResultMsg. Scheduled from the session/
// shell *result* handlers, after Start() has spun up the tmux server, so the
// probe always runs against a live server (no cold-start race).
func (h *Home) probeTCCCmd(path string) tea.Cmd {
	root, ok := protectedRoot(path)
	if !ok || h.tccProbed[root] {
		return nil
	}
	h.tccProbed[root] = true // guard against concurrent duplicate probes
	return func() tea.Msg {
		blocked, determined := tmux.DirAccessBlocked(root)
		return tccProbeResultMsg{root: root, blocked: blocked, determined: determined}
	}
}

// openFullDiskAccessSettings opens the macOS Full Disk Access settings pane so
// the user can grant access to their terminal + tmux in one hop. The URL scheme
// is the Ventura+/Tahoe form; older macOS used
// x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles.
func openFullDiskAccessSettings() tea.Cmd {
	return func() tea.Msg {
		_ = exec.Command("open", "x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_AllFiles").Start()
		return nil
	}
}

// anyTCCBlocked reports whether any probed protected root is blocked. Pure read
// of tccBlockedRoots (Update goroutine only) — drives the tcc-blocked tip.
func (h *Home) anyTCCBlocked() bool {
	for _, blocked := range h.tccBlockedRoots {
		if blocked {
			return true
		}
	}
	return false
}

// firstTCCBlockedRoot returns the lowest-sorted blocked root (stable across
// renders when several are blocked), or "" if none.
func (h *Home) firstTCCBlockedRoot() string {
	roots := make([]string, 0, len(h.tccBlockedRoots))
	for root, blocked := range h.tccBlockedRoots {
		if blocked {
			roots = append(roots, root)
		}
	}
	if len(roots) == 0 {
		return ""
	}
	sort.Strings(roots)
	return roots[0]
}

// startSessionCmd builds the tea.Cmd that creates and starts one session off
// the UI thread. Shared by the new-session flow and the launchpad (which fires
// several at once). A non-empty resumeClaudeID makes buildClaudeCmd() launch
// `claude --resume <id>` to continue an existing conversation. An empty agent
// falls back to agent.Default (Claude) at launch.
func (h *Home) startSessionCmd(msg sessionCreateMsg) tea.Cmd {
	debuglog.Logger.Info("creating session", "title", msg.title, "path", msg.path, "agent", msg.agent, "resume", msg.resumeClaudeID)
	s := session.NewSession(msg.title, msg.path)
	s.WorkspaceName = msg.workspaceName
	s.Agent = msg.agent
	if msg.resumeClaudeID != "" {
		s.ClaudeSessionID = msg.resumeClaudeID
	}
	return func() tea.Msg {
		if err := s.Start(); err != nil {
			debuglog.Logger.Error("session Start() failed", "title", msg.title, "path", msg.path, "err", err)
			analytics.Track(analytics.EventTmuxCommandFailure, map[string]interface{}{"command": "new_session"})
			return sessionCreateResultMsg{err: err}
		}
		return sessionCreateResultMsg{session: s}
	}
}

// launchLaunchpadSet starts every chosen recent project at once, resuming each
// one's most recent Claude conversation. This is the launchpad's payoff:
// rehydrate a whole working set in a single keystroke.
func (h *Home) launchLaunchpadSet(items []discovery.Recent) tea.Cmd {
	if len(items) == 0 {
		return nil
	}
	if _, err := exec.LookPath("claude"); err != nil {
		h.setError(fmt.Errorf("claude CLI not found — install Claude Code to create sessions"))
		return nil
	}
	analytics.Track(analytics.EventOnboardingFirstSessionCreated, map[string]interface{}{"count": len(items)})
	cmds := make([]tea.Cmd, 0, len(items))
	for _, it := range items {
		h.actionLog.Add("launchpad add", it.Path, true)
		cmds = append(cmds, h.startSessionCmd(sessionCreateMsg{
			path:           it.Path,
			title:          it.Title,
			resumeClaudeID: it.ClaudeSessionID,
		}))
	}
	return tea.Batch(cmds...)
}

func (h *Home) handleSessionCreateResult(msg sessionCreateResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		h.setError(fmt.Errorf("failed to start session: %w", msg.err))
		return h, nil
	}

	analytics.Track(analytics.EventSessionCreated, map[string]interface{}{"agent": string(msg.session.Agent)})
	if analytics.MarkOnboardingMilestone(analytics.MilestoneFirstSession) {
		analytics.Track(analytics.EventOnboardingFirstSessionCreated, map[string]interface{}{
			"seconds_since_install": int(analytics.SecondsSinceInstall()),
		})
	}

	s := msg.session
	h.workerMu.Lock()
	h.sessions = append(h.sessions, s)
	h.rebuildSessionMap()
	h.workerMu.Unlock()

	// Ensure the repo group is expanded for the new session and pin it.
	repo := session.GetRepoRoot(s.ProjectPath)
	h.setExpanded(repo, true)
	if !h.pinnedRepos[repo] {
		h.pinnedRepos[repo] = true
		if err := h.storage.PinRepo(repo); err != nil {
			debuglog.Logger.Error("failed to pin repo", "repo", repo, "err", err)
		}
	}
	h.rebuildFlatItems()

	// Save to storage.
	if err := h.storage.SaveSession(s.ToRow()); err != nil {
		h.setError(fmt.Errorf("failed to save session: %w", err))
	}

	// Auto-select the new session.
	for i, item := range h.flatItems {
		if !item.IsRepoHeader && item.Session != nil && item.Session.ID == s.ID {
			h.cursor = i
			h.syncViewport()
			break
		}
	}

	// Probe for a TCC-blocked folder now that Start() has spun up the tmux server
	// (covers both new sessions and forks, which flow through here).
	return h, tea.Batch(h.probeTCCCmd(s.ProjectPath), h.fetchPreviewForSelected())
}

// deleteAtCursor runs the delete whose scope follows the cursor: an origin
// header forgets the whole group, any other header acts on the container
// (§ confirmDeleteHeader), and a session row deletes just that session. Shared by
// the `d` key and the context menu's delete entry so the two can't drift.
func (h *Home) deleteAtCursor() tea.Cmd {
	if h.cursor >= 0 && h.cursor < len(h.flatItems) && h.flatItems[h.cursor].IsRepoHeader {
		item := h.flatItems[h.cursor]
		if item.IsOriginHeader {
			return h.confirmDeleteOrigin(item)
		}
		return h.confirmDeleteHeader(item)
	}
	if s := h.selectedSession(); s != nil {
		h.actionLog.Add("delete session", s.Title, true)
	}
	return h.confirmDeleteSelected()
}

func (h *Home) confirmDeleteSelected() tea.Cmd {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) || h.flatItems[h.cursor].IsRepoHeader {
		return nil
	}
	s := h.flatItems[h.cursor].Session
	if s == nil {
		return nil
	}

	id := s.ID
	repoPath := session.GetRepoRoot(s.ProjectPath)

	details := []string{
		"Press u to undo within 5s",
	}
	// Discoverability nudge: when this is the last session in a destroyable
	// worktree, the worktree dir is kept — point the user at the header.
	if s.WorkspaceName != "" && h.countSessionsForRepo(repoPath) == 1 &&
		workspace.ResolveProvider(repoPath).CanDestroy() {
		details = append(details, "Worktree kept — press d on its header to remove it")
	}

	h.confirmDialog.ShowDanger("Delete Session?", s.Title, details, func() tea.Msg {
		return sessionDeleteMsg{id: id}
	})
	return nil
}

// worktreeHoldersScannedMsg carries the result of the async process scan that
// backs the delete dialog's "will terminate …" warning (part C).
type worktreeHoldersScannedMsg struct {
	gen     int
	holders []proc.Holder
}

// confirmDeleteHeader handles `d` pressed on a repo/worktree header. Scope is the
// container: a worktree header removes the worktree dir + its sessions; a real-repo
// header "forgets" the repo from fleet (deletes its sessions + unpins, folder kept).
func (h *Home) confirmDeleteHeader(item SidebarItem) tea.Cmd {
	repoPath := item.RepoPath
	count := h.countSessionsForRepo(repoPath)
	// A worktree whose removal previously failed leaves an orphaned dir that git
	// may no longer classify as a worktree; treat it as one so the retry routes
	// to destroy (not instant-unpin) and actually removes the leftover.
	isWorktree := h.repoIsWorktree(repoPath) || h.failedWorktreeRemovals[repoPath]

	// Empty plain repo: instant unpin, no dialog (unchanged behavior).
	if count == 0 && !isWorktree {
		return h.unpinRepoHeader(repoPath)
	}

	base := filepath.Base(repoPath)
	var title string
	var details []string
	switch {
	case isWorktree && count > 0:
		title = "Remove Worktree?"
		details = append([]string{fmt.Sprintf("Deletes %d session(s) + the worktree directory", count)}, h.worktreeDeleteWarnings(repoPath)...)
		details = append(details, "Press u to undo within 5s")
	case isWorktree: // empty worktree
		title = "Remove Worktree?"
		details = append([]string{"Removes the worktree directory"}, h.worktreeDeleteWarnings(repoPath)...)
	default: // real repo with sessions
		title = "Remove repo from fleet?"
		details = []string{
			fmt.Sprintf("Deletes %d session(s) — folder untouched", count),
			"Press u to undo within 5s",
		}
	}

	h.actionLog.Add("delete "+map[bool]string{true: "worktree", false: "repo"}[isWorktree], base, true)
	h.confirmDialog.ShowDanger(title, base, details, func() tea.Msg {
		return repoDeleteMsg{repoPath: repoPath, destroyWorkspace: isWorktree}
	})

	// Removing a worktree kills the dev processes still holding it. The lsof
	// scan is ~0.5s — too slow for the Update loop — so fill that warning in
	// asynchronously; SetScan ignores the result if the prompt has moved on.
	if isWorktree {
		gen := h.nextHolderScanGen()
		h.confirmDialog.StartScan(gen, "Checking for running processes…")
		editor := h.cfg.GetEditor()
		return func() tea.Msg {
			holders, _ := proc.FindHolders(repoPath, []string{editor})
			return worktreeHoldersScannedMsg{gen: gen, holders: holders}
		}
	}
	return nil
}

// checkoutsForOrigin returns every known checkout (repo root) that maps to the
// given origin key, scanning the same three sources the sidebar renders from
// (sessions, pinned repos, pending workspaces). Deduped. Origin-level delete must
// reach every checkout even when the origin group is collapsed — collapsed
// checkouts aren't present in flatItems.
func (h *Home) checkoutsForOrigin(origin string) []string {
	return h.checkoutsForOriginIn(h.gitInfo(), origin)
}

// checkoutsForOriginIn resolves against a caller-supplied snapshot — see originOfIn.
func (h *Home) checkoutsForOriginIn(m map[string]*git.RepoInfo, origin string) []string {
	if origin == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	add := func(repo string) {
		if repo == "" || originOfIn(m, repo) != origin {
			return
		}
		if _, ok := seen[repo]; ok {
			return
		}
		seen[repo] = struct{}{}
		out = append(out, repo)
	}
	for _, s := range h.sessions {
		add(session.GetRepoRoot(s.ProjectPath))
	}
	for repo := range h.pinnedRepos {
		add(repo)
	}
	for _, pw := range h.pendingWorkspaces {
		if pw != nil {
			add(pw.RepoPath)
		}
	}
	return out
}

// confirmDeleteOrigin handles `d` pressed on an origin header. Scope is the whole
// group: every checkout under the origin (the main repo plus any worktrees) and
// all their sessions. Because that wipes a group at once, the confirm is gated
// behind a checkbox. Whether worktree directories are removed from disk follows
// the origin_delete_removes_worktrees setting (default on); the main repo's
// folder is always kept.
func (h *Home) confirmDeleteOrigin(item SidebarItem) tea.Cmd {
	// One git snapshot for the whole decision. The git+PR fan-out swaps this
	// map from its own goroutine, so re-loading per lookup would let the set of
	// checkouts and the per-checkout destroy decisions come from two different
	// generations — the dialog counts N worktrees while a different set gets
	// `git worktree remove`. Most reachable right after boot or just after a
	// worktree is created, when OriginKey/IsWorktreeRepo are still settling.
	gitSnap := h.gitInfo()

	checkouts := h.checkoutsForOriginIn(gitSnap, item.OriginKey)
	if len(checkouts) == 0 {
		return nil
	}
	removeWorktrees := h.cfg.GetOriginDeleteRemovesWorktrees()
	inScope := make(map[string]bool, len(checkouts))
	for _, repo := range checkouts {
		inScope[repo] = true
	}

	// Snapshot each checkout's destroy decision now, at show-time, so the ~2s
	// gitInfo refresh can't flip a worktree classification between the user
	// ticking the box and execution. destroyDirs (the dirs actually removed) and
	// the dirty count are derived from the same snapshot the user approves.
	var targets []originDeleteTarget
	var destroyDirs []string
	dirtyWorktrees := 0
	hasImmediateDestroy := false
	for _, repo := range checkouts {
		isWorktree := repoIsWorktreeIn(gitSnap, repo) || h.failedWorktreeRemovals[repo]
		destroy := removeWorktrees && isWorktree
		targets = append(targets, originDeleteTarget{repoPath: repo, destroy: destroy})
		if destroy {
			destroyDirs = append(destroyDirs, repo)
			if info := gitSnap[repo]; info != nil && info.IsDirty {
				dirtyWorktrees++
			}
			// An empty worktree is removed immediately (deferDeleteRepo's
			// len(sess)==0 branch) — that removal is not undoable.
			if h.countSessionsForRepo(repo) == 0 {
				hasImmediateDestroy = true
			}
		}
	}

	// Session totals across the whole group in one pass (mirrors the per-repo
	// warnings worktreeDeleteWarnings surfaces for a single checkout).
	sessionCount, runningCount := 0, 0
	for _, s := range h.sessions {
		if !inScope[session.GetRepoRoot(s.ProjectPath)] {
			continue
		}
		sessionCount++
		switch s.GetStatus() {
		case session.StatusRunning, session.StatusWaiting, session.StatusStarting:
			runningCount++
		}
	}

	details := []string{
		fmt.Sprintf("Forgets %d checkout(s) · %d session(s)", len(checkouts), sessionCount),
	}
	if len(destroyDirs) > 0 {
		details = append(details, fmt.Sprintf("Removes %d worktree director(ies) from disk", len(destroyDirs)))
		// Only reassure about a main folder when a non-worktree checkout is
		// actually in scope (not when every checkout is a worktree).
		if len(checkouts) > len(destroyDirs) {
			details = append(details, "Main repo folder kept")
		}
	} else {
		details = append(details, "No directories removed — checkouts just un-tracked")
	}
	// Surface the same safety warnings the single-checkout path shows, aggregated.
	if dirtyWorktrees > 0 {
		details = append(details, fmt.Sprintf("%d worktree(s) have uncommitted changes", dirtyWorktrees))
	}
	if runningCount > 0 {
		details = append(details, fmt.Sprintf("%d session(s) here are still running", runningCount))
	}
	// List the checkouts so a local:<basename> collision (two unrelated repos
	// sharing a folder name → one origin) is visible before the box is ticked.
	details = append(details, checkoutPathLines(checkouts)...)
	// Undo only restores deferred session deletes; an empty worktree is removed
	// immediately and an all-empty group is just unpinned — neither is undoable.
	// Match confirmDeleteHeader: promise undo only when something undoable is in scope.
	if sessionCount > 0 && !hasImmediateDestroy {
		details = append(details, "Press u to undo within 5s")
	}

	label := item.OriginLabel
	if label == "" {
		label = labelForOrigin(item.OriginKey)
	}
	h.actionLog.Add("delete origin", label, true)
	h.confirmDialog.ShowDanger("Forget entire origin?", label, details, func() tea.Msg {
		return originDeleteMsg{targets: targets}
	})
	h.confirmDialog.RequireCheckbox("Yes, forget the whole origin group")

	// Best-effort: list the dev processes that removing the worktrees will
	// force-stop. Aggregate across every worktree under the origin into the one
	// async scan line, deduping by PID so a daemon holding two sibling worktrees
	// isn't double-counted (reuses the worktreeHoldersScannedMsg handler).
	if len(destroyDirs) > 0 {
		gen := h.nextHolderScanGen()
		h.confirmDialog.StartScan(gen, "Checking for running processes…")
		editor := h.cfg.GetEditor()
		dirs := destroyDirs
		return func() tea.Msg {
			seen := make(map[int]bool)
			var holders []proc.Holder
			for _, dir := range dirs {
				hs, _ := proc.FindHolders(dir, []string{editor})
				for _, hd := range hs {
					if seen[hd.PID] {
						continue
					}
					seen[hd.PID] = true
					holders = append(holders, hd)
				}
			}
			return worktreeHoldersScannedMsg{gen: gen, holders: holders}
		}
	}
	return nil
}

// checkoutPathLines renders up to 4 home-shortened checkout paths as bullet
// detail lines, collapsing the remainder into a "+N more" line. Lets the user
// eyeball which checkouts a group-forget actually covers.
func checkoutPathLines(checkouts []string) []string {
	const max = 4
	var lines []string
	for i, repo := range checkouts {
		if i == max {
			lines = append(lines, fmt.Sprintf("…and %d more", len(checkouts)-max))
			break
		}
		lines = append(lines, shortenPath(repo))
	}
	return lines
}

// deferDeleteOrigin forgets a whole origin group by replaying the show-time
// snapshot through deferDeleteRepo per checkout — so each checkout follows its
// own rule (sessions → undoable deferred delete, worktree → optional
// `git worktree remove`, empty main repo → unpin) and `u`-undo keeps working per
// session (LIFO), against exactly the destroy decisions the user approved.
func (h *Home) deferDeleteOrigin(msg originDeleteMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	for _, t := range msg.targets {
		_, cmd := h.deferDeleteRepo(repoDeleteMsg{repoPath: t.repoPath, destroyWorkspace: t.destroy})
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return h, tea.Batch(cmds...)
}

func (h *Home) nextHolderScanGen() int {
	h.holderScanGen++
	return h.holderScanGen
}

// worktreeDeleteWarnings returns the instant (cache-backed) pre-flight warnings
// for removing a worktree: uncommitted changes and still-running sessions. The
// process-kill warning is filled in asynchronously (see confirmDeleteHeader).
func (h *Home) worktreeDeleteWarnings(repoPath string) []string {
	var w []string
	if info := h.gitInfo()[repoPath]; info != nil && info.IsDirty {
		w = append(w, "Uncommitted changes will be discarded")
	}
	active := 0
	for _, s := range h.sessions {
		if session.GetRepoRoot(s.ProjectPath) != repoPath {
			continue
		}
		switch s.GetStatus() {
		case session.StatusRunning, session.StatusWaiting, session.StatusStarting:
			active++
		}
	}
	if active > 0 {
		w = append(w, fmt.Sprintf("%d session(s) here are still running", active))
	}
	if n := h.countShellsForRepo(repoPath); n > 0 {
		w = append(w, fmt.Sprintf("%d terminal(s) here will be closed", n))
	}
	return w
}

// countShellsForRepo returns how many shells are scoped to repoPath.
func (h *Home) countShellsForRepo(repoPath string) int {
	n := 0
	for _, sh := range h.shells {
		if sh.RepoPath == repoPath {
			n++
		}
	}
	return n
}

// killShellsForRepo forgets every shell scoped to repoPath and returns a tea.Cmd
// that kills their tmux sessions + deletes their rows. The in-memory removal and
// live-stream re-point happen synchronously on the Update goroutine (single
// writer of h.shells/stream), but the actual `tmux kill-session` subprocess per
// shell + DB delete run in the returned command OFF the Update goroutine (per the
// no-blocking-I/O rule). Because a shell's dev server holds the worktree dir open
// (like a session's detached daemon, and proc.FindHolders spares shells), the
// caller must run this command BEFORE `git worktree remove` — see deferDeleteRepo,
// which tea.Sequence()s it ahead of the destroy. Returns nil when nothing is
// scoped here. No undo (shells carry no state).
func (h *Home) killShellsForRepo(repoPath string) tea.Cmd {
	var doomed []*shell.Shell
	h.workerMu.Lock()
	kept := make([]*shell.Shell, 0, len(h.shells))
	for _, sh := range h.shells {
		if sh.RepoPath == repoPath {
			doomed = append(doomed, sh)
		} else {
			kept = append(kept, sh)
		}
	}
	h.shells = kept
	h.workerMu.Unlock()
	// If the active drawer shell was among the doomed, re-point (or tear down) the
	// live stream now instead of leaving it on a killed session until the next tick.
	h.syncShellStream()
	if len(doomed) == 0 {
		return nil
	}
	storage := h.storage
	return func() tea.Msg {
		for _, sh := range doomed {
			if err := storage.DeleteShell(sh.ID); err != nil {
				debuglog.Logger.Error("storage: DeleteShell", "id", sh.ID, "err", err)
			}
			_ = sh.Kill()
		}
		return nil
	}
}

// repoIsWorktree reports whether a repo group is a git worktree, reading from
// the lock-free git/PR snapshot. Cold cache → false. bootstrapGitInfo pre-warms
// every visible repo within 6s of launch and the steady-state worker covers
// every session-repo each cycle, so the realistic cold-cache window is empty.
// If a brand-new worktree header is somehow hit before the worker resolves it,
// `d` falls through to "forget repo" (instant unpin, no disk touch) instead of
// "Remove Worktree?" — the worktree directory stays put and the user can
// `git worktree remove` it manually. Never shells out, so Update() stays
// blocking-I/O-free per project rules.
func (h *Home) repoIsWorktree(repoPath string) bool {
	return repoIsWorktreeIn(h.gitInfo(), repoPath)
}

// repoIsWorktreeIn resolves against a caller-supplied snapshot — see originOfIn.
func repoIsWorktreeIn(m map[string]*git.RepoInfo, repoPath string) bool {
	if info := m[repoPath]; info != nil {
		return info.IsWorktreeRepo
	}
	return false
}

// unpinRepoHeader unpins a repo from the sidebar and fixes the cursor.
func (h *Home) unpinRepoHeader(repoPath string) tea.Cmd {
	if !h.pinnedRepos[repoPath] {
		return nil
	}
	delete(h.pinnedRepos, repoPath)
	if err := h.storage.UnpinRepo(repoPath); err != nil {
		debuglog.Logger.Error("failed to unpin repo", "repo", repoPath, "err", err)
	}
	h.forgetCollapse(repoPath)
	h.forgetSnooze(repoPath)
	h.actionLog.Add("unpin repo", filepath.Base(repoPath), true)
	h.rebuildFlatItems()
	if h.cursor >= len(h.flatItems) {
		h.cursor = len(h.flatItems) - 1
	}
	if h.cursor < 0 {
		h.cursor = 0
	}
	return nil
}

// deferDeleteRepo deletes every session in a repo/worktree group, routing through
// the per-session deferred-delete machinery so `u`-undo still works (LIFO). The last
// session carries the container-level side effects (unpin, optional worktree destroy).
// An empty worktree (no sessions) is removed directly in the background.
func (h *Home) deferDeleteRepo(msg repoDeleteMsg) (tea.Model, tea.Cmd) {
	var sess []*session.Session
	for _, s := range h.sessions {
		if session.GetRepoRoot(s.ProjectPath) == msg.repoPath {
			sess = append(sess, s)
		}
	}

	// Remove the worktree's shells up front so their tmux sessions (and the dev
	// servers they host) don't pin the dir against `git worktree remove`. Only
	// when the directory is actually being destroyed — a plain "forget repo"
	// leaves the folder (and its shells) alone. The in-memory removal is
	// synchronous; killShellsForRepo returns a command for the (off-Update) kills.
	var killShellsCmd tea.Cmd
	if msg.destroyWorkspace {
		killShellsCmd = h.killShellsForRepo(msg.repoPath)
	}

	var cmds []tea.Cmd
	if len(sess) == 0 {
		// Empty worktree: unpin + background `git worktree remove` (not undoable;
		// the confirm dialog is the safety gate, nothing live to lose).
		h.unpinRepoHeader(msg.repoPath)
		if msg.destroyWorkspace {
			repoPath := msg.repoPath
			name := filepath.Base(repoPath)
			editor := h.cfg.GetEditor()
			destroyCmd := func() tea.Msg {
				remaining, err := destroyWorktree(repoPath, name, editor, 2*time.Second)
				return deleteCleanupDoneMsg{
					workspaceErr:     err,
					repoPath:         repoPath,
					workspaceName:    name,
					remainingHolders: remaining,
					destroyAttempted: true,
				}
			}
			// Kill the shells FIRST (frees the dir), THEN remove the worktree.
			// tea.Sequence drops a nil killShellsCmd, so this is just destroyCmd
			// when there were no shells.
			cmds = append(cmds, tea.Sequence(killShellsCmd, destroyCmd))
		}
		return h, tea.Batch(cmds...)
	}

	// Non-empty path: the per-session finalize→destroyWorktree is 5s-deferred, so
	// the async kills complete well before it — just fire them alongside.
	if killShellsCmd != nil {
		cmds = append(cmds, killShellsCmd)
	}

	for i, s := range sess {
		dm := sessionDeleteMsg{id: s.ID}
		if i == len(sess)-1 {
			dm.unpinRepo = true
			dm.repoPath = msg.repoPath
			if msg.destroyWorkspace {
				dm.destroyWorkspace = true
				// Destroy matches the worktree by path, but finalizeDelete guards on a
				// non-empty name; fall back to the dir basename when unset.
				dm.workspaceName = s.WorkspaceName
				if dm.workspaceName == "" {
					dm.workspaceName = filepath.Base(msg.repoPath)
				}
			}
		}
		_, cmd := h.deferDelete(dm)
		cmds = append(cmds, cmd)
	}
	return h, tea.Batch(cmds...)
}

// confirmRestartSelected gates restart behind a y/n confirm (default), or
// restarts immediately when confirm_before_restart is disabled.
func (h *Home) confirmRestartSelected() tea.Cmd {
	s := h.selectedSession()
	if s == nil {
		return nil
	}
	if !h.cfg.IsConfirmBeforeRestartEnabled() {
		return h.restartSession(s)
	}
	id := s.ID
	h.confirmDialog.ShowWarning("Restart Session?", s.Title,
		[]string{"Kills the running process and starts it fresh"},
		func() tea.Msg { return sessionRestartConfirmedMsg{id: id} })
	return nil
}

// restartSession respawns (or full-restarts) the given session. Action-log and
// analytics fire here so they reflect an actual restart — not a cancelled
// confirm — regardless of entry point (key, palette, confirmed dialog).
func (h *Home) restartSession(s *session.Session) tea.Cmd {
	h.actionLog.Add("restart session", s.Title, true)
	analytics.Track(analytics.EventSessionRestarted, nil)
	h.markSessionAccessed(s)
	id := s.ID
	title := s.Title
	debuglog.Logger.Info("restarting session", "id", id, "title", title)
	return func() tea.Msg {
		var err error
		if s.IsAlive() && !s.GetTmuxSession().IsPaneDead() {
			// Tmux session alive, just respawn the pane.
			err = s.RespawnClaude()
			if err != nil {
				debuglog.Logger.Error("RespawnClaude failed", "id", id, "err", err)
			}
		} else {
			// Tmux session dead or pane dead — full restart.
			err = s.Restart()
			if err != nil {
				debuglog.Logger.Error("Restart failed", "id", id, "err", err)
			}
		}
		return sessionRestartMsg{id: id, err: err}
	}
}

// resumeSelected wakes a suspended session: it recreates the tmux session and
// resumes the conversation (Restart() → `--resume <ClaudeSessionID>`), then
// attaches once the pane is live (via attachAfterResumeID in the sessionRestartMsg
// handler). Optimistically shows StatusStarting so the ⏸ row clears immediately.
func (h *Home) resumeSelected(s *session.Session) tea.Cmd {
	h.actionLog.Add("resume session", s.Title, true)
	analytics.Track(analytics.EventSessionRestarted, nil)
	h.markSessionAccessed(s)
	h.attachAfterResumeID = s.ID
	s.SetStatus(session.StatusStarting)
	h.rebuildFlatItems()
	id := s.ID
	title := s.Title
	debuglog.Logger.Info("resuming suspended session", "id", id, "title", title)
	return func() tea.Msg {
		// Suspended tmux is gone, so this is always a full Restart (which resumes
		// because ClaudeSessionID is set), never an in-place respawn.
		err := s.Restart()
		if err != nil {
			debuglog.Logger.Error("resume (Restart) failed", "id", id, "err", err)
		}
		return sessionRestartMsg{id: id, err: err}
	}
}

// Memory-pressure idle-suspend tunables.
const (
	// suspendSweepInterval throttles the sweep to its own slow cadence inside the
	// ~2s heavy worker pass — probing pressure and shedding every 2s would suspend
	// in bursts; ~20s lets pressure settle between batches.
	suspendSweepInterval = 20 * time.Second
	// suspendSweepBatch caps how many sessions one auto-sweep hibernates, so a
	// fleet sheds gradually and re-checks pressure before shedding more.
	suspendSweepBatch = 5
)

// maybeSuspendIdleSessions hibernates the most-idle sessions under memory pressure,
// per the configured aggressiveness mode, so a full fleet can't OOM-kill the shared
// tmux server. Runs on the worker goroutine (it shells out to sysctl). Only
// StatusIdle sessions are ever suspended.
func (h *Home) maybeSuspendIdleSessions(sessions []*session.Session) {
	mode := h.cfg.GetSessionSuspendMode()
	if mode == config.SuspendOff {
		return
	}
	if !h.lastSuspendSweepAt.IsZero() && time.Since(h.lastSuspendSweepAt) < suspendSweepInterval {
		return
	}
	h.lastSuspendSweepAt = time.Now()

	// Never hibernate the session the user is currently attached to — its
	// LastAccessedAt doesn't refresh mid-attach, so it would look idle and get
	// killed out from under them, ejecting them into a dead pane.
	h.workerMu.Lock()
	attached := h.attachedSessionID
	h.workerMu.Unlock()

	// Gather idle candidates first (cheap, no I/O) so we can skip the sysctl probe
	// entirely when there's nothing we could shed.
	type cand struct {
		s    *session.Session
		idle time.Duration
	}
	var cands []cand
	for _, s := range sessions {
		if s.GetStatus() != session.StatusIdle || s.ID == attached {
			continue
		}
		// No captured resume id → Restart() would launch a fresh claude with no
		// --resume and silently discard the conversation. "Nothing is lost" only
		// holds when we can actually resume it, so never auto-park these.
		if s.GetClaudeSessionID() == "" {
			continue
		}
		cands = append(cands, cand{s, s.IdleFor()})
	}
	if len(cands) == 0 {
		return
	}

	level, swapFreeMB := perfwatch.MemoryPressure()
	// Whether low free swap escalates the level is a per-platform judgment:
	// trusted outright on macOS (demand-grown swap, Jetsam lags thrash), but on
	// Linux only when PSI already reports warning — fixed partitions sit
	// partially used on healthy boxes.
	if perfwatch.SwapEscalatesPressure(level, swapFreeMB) {
		level = perfwatch.PressureCritical
	}
	minIdle, act := suspendIdleThreshold(mode, level)
	if !act {
		// Debug-only (FLEET_DEBUG): answers "why isn't auto-suspend firing?" —
		// idle sessions exist but the current pressure/mode doesn't warrant action.
		// Info would spam every ~20s; this stays quiet unless you're debugging.
		debuglog.Logger.Debug("memory sweep: below threshold, no action",
			"mode", mode, "pressure", level, "swapFreeMB", swapFreeMB, "idleCandidates", len(cands))
		return
	}

	sort.Slice(cands, func(i, j int) bool { return cands[i].idle > cands[j].idle }) // most-idle first

	n := 0
	for _, c := range cands {
		if n >= suspendSweepBatch || c.idle < minIdle {
			break // sorted desc: once one is too fresh, so is everything after it
		}
		// Re-validate right before the irreversible kill: the user may have
		// attached to or reactivated this session (Update thread) since we gathered
		// candidates, so a stale snapshot must not kill live work or eject an attach.
		h.workerMu.Lock()
		attachedNow := h.attachedSessionID
		h.workerMu.Unlock()
		if c.s.GetStatus() != session.StatusIdle || c.s.ID == attachedNow {
			continue
		}
		if err := c.s.Suspend(); err != nil { // Suspend() logs the per-session event
			debuglog.Logger.Error("suspend idle session failed", "id", c.s.ID, "title", c.s.Title, "err", err)
			continue
		}
		if err := h.storage.UpdateStatus(c.s.ID, string(session.StatusSuspended)); err != nil {
			debuglog.Logger.Error("storage: UpdateStatus after suspend", "id", c.s.ID, "err", err)
		}
		h.actionLog.Add("suspend session", c.s.Title, true)
		n++
	}
	if n > 0 {
		// One Info summary per acting sweep — the "why" (mode + live pressure) trail,
		// on top of the per-session `session suspend` lines from Suspend().
		debuglog.Logger.Info("memory sweep: suspended idle sessions",
			"count", n, "mode", mode, "pressure", level, "swapFreeMB", swapFreeMB, "minIdle", minIdle.String())
		// Runs on the status worker, so this send must not be able to park:
		// program.Send is a rendezvous on Tea's unbuffered channel and tea.Exec
		// suspends the loop that drains it, which would freeze every session's
		// status for the rest of the attach — the exact wedge this file's
		// git fan-out was moved off this goroutine to avoid.
		//
		// Skipping costs only the toast. Status was already written under
		// workerMu by Suspend(), so the ~2s tick re-derives the sidebar anyway;
		// nothing here carries state the UI cannot recover on its own.
		if !h.isAttaching.Load() {
			h.send(sessionsSuspendedMsg{n: n, auto: true})
		}
	} else {
		// act==true but nothing was idle enough (e.g. balanced's 4h housekeeping
		// with no session that old). Debug-only, so no ~20s Info churn.
		debuglog.Logger.Debug("memory sweep: eligible but none idle enough",
			"mode", mode, "pressure", level, "minIdle", minIdle.String(),
			"idleCandidates", len(cands), "maxIdle", cands[0].idle.Round(time.Second).String())
	}
}

// suspendIdleThreshold returns the minimum idle duration a StatusIdle session must
// exceed to be auto-suspended in the given mode at the given memory-pressure level,
// and whether any suspension should happen at all right now.
func suspendIdleThreshold(mode string, level int) (minIdle time.Duration, act bool) {
	warn := level >= perfwatch.PressureWarning
	critical := level >= perfwatch.PressureCritical
	// Every mode gates on memory pressure — nothing is suspended on a healthy
	// machine. The mode picks how long-idle a session must be once pressure hits.
	switch mode {
	case config.SuspendLight:
		if critical {
			return 24 * time.Hour, true
		}
	case config.SuspendBalanced:
		if warn {
			return 4 * time.Hour, true
		}
	case config.SuspendAggressive:
		if warn {
			return 1 * time.Hour, true
		}
	}
	return 0, false
}

// suspendIdleNow (manual palette command) hibernates every currently-idle session
// immediately, ignoring the pressure/idle gates — the user explicitly asked to free
// memory. Runs the tmux teardown off the Update loop.
func (h *Home) suspendIdleNow() tea.Cmd {
	var targets []*session.Session
	for _, s := range h.sessions {
		// Only idle, and only if resumable — parking a session with no captured
		// ClaudeSessionID would lose its conversation on the next Restart.
		if s.GetStatus() == session.StatusIdle && s.GetClaudeSessionID() != "" {
			targets = append(targets, s)
		}
	}
	if len(targets) == 0 {
		h.setInfo("No idle sessions to suspend")
		return nil
	}
	// Optimistically mark Suspended on the Update thread BEFORE the background
	// teardown, so the status worker sees Suspended and short-circuits instead of
	// racing tmux death into a spurious StatusError (and the ◌ shows immediately).
	for _, s := range targets {
		s.SetStatus(session.StatusSuspended)
		if err := h.storage.UpdateStatus(s.ID, string(session.StatusSuspended)); err != nil {
			debuglog.Logger.Error("storage: UpdateStatus (pre-suspend)", "id", s.ID, "err", err)
		}
	}
	h.rebuildFlatItems()
	return func() tea.Msg {
		n := 0
		for _, s := range targets {
			if err := s.Suspend(); err != nil {
				debuglog.Logger.Error("manual suspend failed", "id", s.ID, "title", s.Title, "err", err)
				continue
			}
			h.actionLog.Add("suspend session", s.Title, true)
			n++
		}
		return sessionsSuspendedMsg{n: n, auto: false}
	}
}

// suspendSelected (manual palette command) hibernates the selected session if it's
// at rest (idle or finished). Refuses running/starting sessions so an in-progress
// turn is never silently killed.
func (h *Home) suspendSelected() tea.Cmd {
	s := h.selectedSession()
	if s == nil {
		return nil
	}
	switch s.GetStatus() {
	case session.StatusIdle, session.StatusFinished:
		// at rest — ok to park
	case session.StatusSuspended:
		h.setInfo("Session already suspended — enter to resume")
		return nil
	default:
		h.setInfo("Only idle or finished sessions can be suspended")
		return nil
	}
	if s.GetClaudeSessionID() == "" {
		// No resume id yet → resuming would discard the conversation. Refuse rather
		// than silently lose it.
		h.setInfo("Can't suspend yet — no resumable session id captured")
		return nil
	}
	// Optimistically mark Suspended on the Update thread before the teardown so the
	// status worker short-circuits instead of racing tmux death into StatusError.
	s.SetStatus(session.StatusSuspended)
	if err := h.storage.UpdateStatus(s.ID, string(session.StatusSuspended)); err != nil {
		debuglog.Logger.Error("storage: UpdateStatus (pre-suspend)", "id", s.ID, "err", err)
	}
	h.rebuildFlatItems()
	id, title := s.ID, s.Title
	return func() tea.Msg {
		if err := s.Suspend(); err != nil {
			debuglog.Logger.Error("manual suspend failed", "id", id, "title", title, "err", err)
			return sessionsSuspendedMsg{n: 0, auto: false}
		}
		h.actionLog.Add("suspend session", title, true)
		return sessionsSuspendedMsg{n: 1, auto: false}
	}
}

func (h *Home) forkSelected() tea.Cmd {
	s := h.selectedSession()
	if s == nil {
		h.setError(fmt.Errorf("cannot fork: no session selected"))
		return nil
	}
	if !s.Agent.SupportsFork() {
		h.setError(fmt.Errorf("cannot fork: %s has no fork command", s.Agent.DisplayName()))
		return nil
	}
	if s.ClaudeSessionID == "" {
		h.setError(fmt.Errorf("cannot fork: session has no Claude conversation ID yet"))
		return nil
	}
	title := s.Title + " (fork)"
	claudeSessionID := s.ClaudeSessionID
	sourceID := s.ID
	sourceTitle := s.Title
	path := s.ProjectPath
	workspaceName := s.WorkspaceName
	parentAgent := s.Agent
	return func() tea.Msg {
		return forkSessionMsg{
			parentClaudeSessionID: claudeSessionID,
			sourceSessionID:       sourceID,
			sourceTitle:           sourceTitle,
			sourcePath:            path,
			path:                  path,
			title:                 title,
			workspaceName:         workspaceName,
			agent:                 parentAgent,
		}
	}
}

// forkContext holds the parent-session fields captured when Shift+F opens the
// worktree picker, so the deferred result handlers can build a forkSessionMsg
// targeting the chosen destination.
type forkContext struct {
	parentClaudeSessionID string
	parentSessionID       string // fleet id of the session being forked (for diagnostics)
	parentProjectPath     string
	parentTitle           string
}

// clearPendingFork resets fork-target picker state. Safe to call when nothing
// is pending.
func (h *Home) clearPendingFork() {
	h.pendingForkCtx = nil
}

// isEscKey reports whether msg is a KeyMsg representing the ESC key.
func isEscKey(msg tea.Msg) bool {
	km, ok := msg.(tea.KeyPressMsg)
	return ok && km.Code == tea.KeyEsc
}

// dispatchForkToWorktree builds the forkSessionMsg for a resolved destination
// (existing or newly-created worktree). Title includes the destination
// workspace name so the sidebar tells the user where the fork landed.
func (h *Home) dispatchForkToWorktree(ctx *forkContext, destPath, destWorkspaceName string) tea.Cmd {
	title := ctx.parentTitle + " (fork)"
	if destWorkspaceName != "" {
		title = ctx.parentTitle + " (" + destWorkspaceName + ")"
	}
	parentClaudeSessionID := ctx.parentClaudeSessionID
	sourceID := ctx.parentSessionID
	sourceTitle := ctx.parentTitle
	sourcePath := ctx.parentProjectPath
	return func() tea.Msg {
		return forkSessionMsg{
			parentClaudeSessionID: parentClaudeSessionID,
			sourceSessionID:       sourceID,
			sourceTitle:           sourceTitle,
			sourcePath:            sourcePath,
			path:                  destPath,
			title:                 title,
			workspaceName:         destWorkspaceName,
		}
	}
}

// forkToWorktreeSelected stashes parent context and opens the worktree picker
// in fork-target mode.
func (h *Home) forkToWorktreeSelected() tea.Cmd {
	s := h.selectedSession()
	if s == nil {
		h.setError(fmt.Errorf("cannot fork to worktree: no session selected"))
		return nil
	}
	// Fork-to-worktree stages the parent's Claude transcript into the new cwd so
	// `claude --resume --fork-session` finds it — a Claude-only mechanism. Codex
	// and OpenCode resume from their own stores and have no such staging, so
	// reject any non-Claude agent here rather than dropping the agent and
	// launching a broken Claude fork. Plain 'f' (in-place fork) handles them.
	if s.Agent != agent.Claude {
		h.setError(fmt.Errorf("fork to worktree is Claude-only; use 'f' to fork this session in place"))
		return nil
	}
	if s.ClaudeSessionID == "" {
		h.setError(fmt.Errorf("cannot fork to worktree: session has no Claude conversation ID yet"))
		return nil
	}
	repoPath := session.GetRepoRoot(s.ProjectPath)
	if repoPath == "" {
		h.setError(fmt.Errorf("cannot fork to worktree: session is not inside a git repo"))
		return nil
	}
	h.pendingForkCtx = &forkContext{
		parentClaudeSessionID: s.ClaudeSessionID,
		parentSessionID:       s.ID,
		parentProjectPath:     s.ProjectPath,
		parentTitle:           s.Title,
	}
	h.worktreeDialog.ShowLoading()
	return tea.Batch(h.fetchWorkspaceListForRepo(repoPath), spinnerTickCmd)
}

// expandKeyFor returns the repoExpanded key for the header at cursor — the
// origin key (prefixed) for origin headers, the repo path for checkouts.
// Empty when the cursor isn't on a header.
func (h *Home) expandKeyFor(item SidebarItem) string {
	switch {
	case item.IsOriginHeader:
		return OriginExpandKey(item.OriginKey)
	case item.IsCheckoutHeader:
		return item.RepoPath
	case item.Session != nil:
		return session.GetRepoRoot(item.Session.ProjectPath)
	}
	return ""
}

// setExpanded updates the in-memory expand state for a sidebar group key and
// persists it so the choice survives restarts. A storage row exists only while
// the group is collapsed (absence = expanded). Every mutation that reflects a
// user intent (toggle, expand/collapse all, revealing a freshly-created
// session) routes through here so the persisted set never drifts from the map.
// Purely transient reveals (jump navigation) deliberately write the map
// directly so they don't overwrite a deliberate collapse on disk.
func (h *Home) setExpanded(key string, expanded bool) {
	if key == "" {
		return
	}
	h.repoExpanded[key] = expanded
	if err := h.storage.SetGroupCollapsed(key, !expanded); err != nil {
		debuglog.Logger.Error("failed to persist collapse state", "key", key, "expanded", expanded, "err", err)
	}
}

// clearCollapse drops a key's collapse state entirely — both the in-memory
// entry (back to the expanded default) and any persisted row.
func (h *Home) clearCollapse(key string) {
	delete(h.repoExpanded, key)
	if err := h.storage.SetGroupCollapsed(key, false); err != nil {
		debuglog.Logger.Error("failed to clear collapse state", "key", key, "err", err)
	}
}

// forgetCollapse drops collapse state for a checkout being forgotten, so a
// later re-add starts expanded rather than resurrecting a stale collapse. If
// the checkout was the last one under its origin, the origin's row is dropped
// too — otherwise it would orphan with no checkout left to render beneath it.
func (h *Home) forgetCollapse(repoPath string) {
	h.clearCollapse(repoPath)
	origin := h.originOf(repoPath)
	if origin == "" || h.originHasCheckoutExcept(origin, repoPath) {
		return
	}
	h.clearCollapse(OriginExpandKey(origin))
}

// forgetSnooze drops group-snooze state for a checkout being forgotten, exactly
// mirroring forgetCollapse (same key space, same last-checkout rule for the
// origin). The two tables share a keyspace by design, so they have to share a
// lifecycle: without this, snoozing a worktree and then removing it leaves the
// row behind, and a worktree later created at the same path is born muted —
// dimmed, skipped by Space, absent from the pills — with a countdown nobody set.
func (h *Home) forgetSnooze(repoPath string) {
	h.clearGroupSnooze(repoPath)
	origin := h.originOf(repoPath)
	if origin == "" || h.originHasCheckoutExcept(origin, repoPath) {
		return
	}
	h.clearGroupSnooze(OriginExpandKey(origin))
}

// clearGroupSnooze drops a group key's snooze from memory and storage. No-op
// when the key isn't snoozed, so it's safe on every delete path.
func (h *Home) clearGroupSnooze(key string) {
	if key == "" {
		return
	}
	delete(h.groupSnooze, key)
	if err := h.storage.SetGroupSnooze(key, time.Time{}); err != nil {
		debuglog.Logger.Error("failed to clear group snooze", "key", key, "err", err)
	}
}

// originHasCheckoutExcept reports whether any currently-known checkout other
// than `except` maps to `origin`, scanning the same sources the sidebar renders
// from (sessions, pinned repos, pending workspaces). Sessions sharing `except`'s
// repo root are excluded, so it answers correctly even mid-deferred-delete when
// the forgotten checkout's sessions are still in the list.
func (h *Home) originHasCheckoutExcept(origin, except string) bool {
	match := func(repo string) bool {
		return repo != "" && repo != except && h.originOf(repo) == origin
	}
	for _, s := range h.sessions {
		if match(session.GetRepoRoot(s.ProjectPath)) {
			return true
		}
	}
	for repo := range h.pinnedRepos {
		if match(repo) {
			return true
		}
	}
	for _, pw := range h.pendingWorkspaces {
		if pw != nil && match(pw.RepoPath) {
			return true
		}
	}
	return false
}

func (h *Home) toggleRepoGroup() {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return
	}
	item := h.flatItems[h.cursor]
	if !item.IsOriginHeader && !item.IsCheckoutHeader {
		return
	}
	key := h.expandKeyFor(item)
	if key == "" {
		return
	}
	h.setExpanded(key, !IsExpanded(h.repoExpanded, key))
	h.rebuildFlatItems()
	// Keep cursor on the same header.
	for i, it := range h.flatItems {
		if (it.IsOriginHeader && it.OriginKey == item.OriginKey && item.IsOriginHeader) ||
			(it.IsCheckoutHeader && it.RepoPath == item.RepoPath && item.IsCheckoutHeader) {
			h.cursor = i
			break
		}
	}
	h.syncViewport()
}

func (h *Home) expandRepoAtCursor() {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return
	}
	key := h.expandKeyFor(h.flatItems[h.cursor])
	if key == "" || IsExpanded(h.repoExpanded, key) {
		return
	}
	h.setExpanded(key, true)
	h.rebuildFlatItems()
	h.syncViewport()
}

func (h *Home) collapseRepoAtCursor() {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return
	}
	item := h.flatItems[h.cursor]
	key := h.expandKeyFor(item)
	if key == "" || !IsExpanded(h.repoExpanded, key) {
		return
	}
	h.setExpanded(key, false)
	h.rebuildFlatItems()
	// Move cursor to the matching header row.
	for i, fi := range h.flatItems {
		if item.IsOriginHeader && fi.IsOriginHeader && fi.OriginKey == item.OriginKey {
			h.cursor = i
			break
		}
		if !item.IsOriginHeader && fi.IsCheckoutHeader && fi.RepoPath == h.expandKeyFor(item) {
			h.cursor = i
			break
		}
	}
	h.syncViewport()
}

// jumpToHeader moves the cursor to the nearest group header (origin or checkout)
// in `direction`, clamping to the edge of the list when there is none.
//
// Unlike the other jumps below, this needs no reveal/rebuild: a collapsed group
// still emits its header row, so every target is already in flatItems.
func (h *Home) jumpToHeader(direction int) {
	h.cursor = NextHeaderItem(h.flatItems, h.cursor, direction)
	h.syncViewport()
}

// jumpToNextAttentionSession moves the cursor to the next session needing
// attention — waiting first, then finished — cycling in on-screen (tree) order
// and wrapping.
//
// Collapse semantics: a COLLAPSED ORIGIN is muted — its sessions are never jump
// targets. A collapsed CHECKOUT (branch) under an expanded origin is NOT muted:
// jump reaches its sessions and expands just that checkout to reveal the target.
// Filtered-out sessions are skipped. Silent no-op when nothing qualifies.
func (h *Home) jumpToNextAttentionSession() {
	// Candidate order: the tree with every checkout force-expanded but origins
	// keeping their real collapse state. So a target in a folded checkout is
	// reachable, while one inside a folded origin stays excluded.
	cand := h.buildJumpTree()
	n := len(cand)
	if n == 0 {
		return
	}

	// Anchor at the current row's position in the candidate order — including
	// header rows, so jumping from an origin/checkout header continues from
	// that header instead of restarting at the top. A collapsed origin header
	// has no children in cand, so the scan simply moves on to the next group.
	start := -1
	if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		cur := h.flatItems[h.cursor]
		for i, it := range cand {
			switch {
			case cur.Session != nil && it.Session != nil && it.Session.ID == cur.Session.ID:
				start = i
			case cur.IsOriginHeader && it.IsOriginHeader && it.OriginKey == cur.OriginKey:
				start = i
			case cur.IsCheckoutHeader && it.IsCheckoutHeader && it.RepoPath == cur.RepoPath:
				start = i
			}
			if start != -1 {
				break
			}
		}
	}

	findNext := func(status session.Status) *session.Session {
		for off := 1; off <= n; off++ {
			it := cand[(start+off+n)%n] // +n keeps the index non-negative when start == -1
			// Snoozed sessions are muted from the rotation — that IS the
			// feature. BuildFlatItems already resolved the group umbrella, so
			// this needs no origin/checkout lookup of its own.
			if it.Snooze.Muted {
				continue
			}
			if !it.IsRepoHeader && it.Session != nil && it.Session.GetStatus() == status {
				return it.Session
			}
		}
		return nil
	}

	target := findNext(session.StatusWaiting)
	if target == nil {
		target = findNext(session.StatusFinished)
	}
	if target == nil {
		debuglog.Logger.Debug("spacejump: no waiting/finished target outside collapsed origins", "cursor", h.cursor)
		return // Silent no-op.
	}

	// Reveal just the target's checkout (its origin is already expanded — a
	// collapsed origin would have excluded it above), then land on it.
	h.repoExpanded[session.GetRepoRoot(target.ProjectPath)] = true
	h.rebuildFlatItems()
	for i, it := range h.flatItems {
		if !it.IsRepoHeader && it.Session != nil && it.Session.ID == target.ID {
			debuglog.Logger.Debug("spacejump: landed", "targetID", target.ID, "newCursor", i)
			h.cursor = i
			h.syncViewport()
			return
		}
	}
}

// buildJumpTree returns the sidebar tree as jump should see it: origins keep
// their real collapse state (collapsed origins are muted), but checkouts are
// forced expanded so a session in a folded branch is still reachable.
func (h *Home) buildJumpTree() []SidebarItem {
	exp := make(map[string]bool, len(h.repoExpanded))
	for k, v := range h.repoExpanded {
		if strings.HasPrefix(k, originExpandPrefix) {
			exp[k] = v // keep origin collapse; drop checkout keys → default expanded
		}
	}
	originOf, isWorktreeOf := h.originResolvers()
	return BuildFlatItems(h.sessions, h.pendingWorkspaces, exp, h.filterText, h.pinnedRepos, h.failedWorktreeRemovals, h.groupSnooze, time.Now(), originOf, isWorktreeOf)
}

func (h *Home) renameSelected() tea.Cmd {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) || h.flatItems[h.cursor].IsRepoHeader {
		return nil
	}
	s := h.flatItems[h.cursor].Session
	if s == nil {
		return nil
	}
	h.renameDialog.Show(s.ID, s.Title)
	return nil
}

func (h *Home) openEditorSelected() tea.Cmd {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) || h.flatItems[h.cursor].IsRepoHeader {
		return nil
	}
	s := h.flatItems[h.cursor].Session
	if s == nil {
		return nil
	}
	spec := h.cfg.GetEditor()
	projectPath := s.ProjectPath
	return func() tea.Msg {
		if err := editor.Launch(spec, projectPath); err != nil {
			debuglog.Logger.Error("editor launch failed", "editor", spec, "path", projectPath, "err", err)
			return openEditorMsg{err: err}
		}
		return openEditorMsg{}
	}
}

func (h *Home) quickApproveSelected() tea.Cmd {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) || h.flatItems[h.cursor].IsRepoHeader {
		return nil
	}
	s := h.flatItems[h.cursor].Session
	if s == nil || !s.IsAlive() {
		return nil
	}
	if s.GetStatus() != session.StatusWaiting {
		h.setError(fmt.Errorf("session not waiting for approval"))
		return nil
	}
	h.markSessionAccessed(s)
	ts := s.GetTmuxSession()
	debuglog.Logger.Info("quick approve", "id", s.ID, "title", s.Title)
	return func() tea.Msg {
		// Send "y" then Enter: menu-style prompts ignore "y" and Enter confirms;
		// (Y/n) and (y/N) prompts accept "y" as approval, Enter submits.
		_ = ts.SendKeys("y")
		err := ts.SendKeys("Enter")
		return quickApproveMsg{err: err}
	}
}

// --- Focus mode (split preview) ---

func (h *Home) getControlClient() *tmux.ControlClient {
	if h.controlClient == nil || h.controlClient.IsClosed() {
		cc, err := tmux.NewControlClient()
		if err != nil {
			debuglog.Logger.Error("failed to create control client", "err", err)
			return nil
		}
		h.controlClient = cc
	}
	return h.controlClient
}

func (h *Home) enterFocusMode() tea.Cmd {
	s := h.selectedSession()
	if s == nil || !s.IsAlive() {
		h.setError(fmt.Errorf("cannot focus: session not running"))
		return nil
	}
	h.focusMode = true
	h.sidebarDirty = true // separator color changes
	h.actionLog.Add("focus preview", s.Title, true)
	return h.focusTick()
}

func (h *Home) focusTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return focusTickMsg(t)
	})
}

func (h *Home) handleFocusKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := h.selectedSession()
	if s == nil || !s.IsAlive() {
		h.focusMode = false
		h.sidebarDirty = true
		return h, nil
	}

	if msg.Code == tea.KeyEsc {
		h.focusMode = false
		h.sidebarDirty = true
		h.actionLog.Add("unfocus preview", s.Title, true)
		return h, nil
	}

	cc := h.getControlClient()
	if cc == nil {
		h.setError(fmt.Errorf("failed to connect to tmux"))
		h.focusMode = false
		h.sidebarDirty = true
		return h, nil
	}

	target := s.GetTmuxSession().Name

	switch msg.Code {
	case tea.KeyEnter:
		cc.SendKeys(target, "Enter")
	case tea.KeyBackspace:
		cc.SendKeys(target, "BSpace")
	case tea.KeyTab:
		cc.SendKeys(target, "Tab")
	case tea.KeySpace:
		cc.SendKeys(target, "Space")
	case tea.KeyUp:
		cc.SendKeys(target, "Up")
	case tea.KeyDown:
		cc.SendKeys(target, "Down")
	case tea.KeyLeft:
		cc.SendKeys(target, "Left")
	case tea.KeyRight:
		cc.SendKeys(target, "Right")
	case tea.KeyHome:
		cc.SendKeys(target, "Home")
	case tea.KeyEnd:
		cc.SendKeys(target, "End")
	case tea.KeyPgUp:
		cc.SendKeys(target, "PageUp")
	case tea.KeyPgDown:
		cc.SendKeys(target, "PageDown")
	case tea.KeyDelete:
		cc.SendKeys(target, "DC")
	default:
		// Ctrl chords (⌃C/⌃D/⌃A/⌃U/⌃L/⌃W/⌃K/…) map to tmux "C-x" so the
		// session's own line-editing keeps working; printable text passes
		// through literally.
		if c, ok := ctrlChord(msg.String()); ok {
			cc.SendKeys(target, c)
		} else if msg.Text != "" {
			cc.SendLiteralKeys(target, msg.Text)
		}
	}
	return h, nil
}

func (h *Home) fetchPreviewFresh(s *session.Session) tea.Cmd {
	id := s.ID
	ts := s.GetTmuxSession()
	return func() tea.Msg {
		content, _ := ts.CapturePaneFresh()
		return previewMsg{sessionID: id, content: content}
	}
}

func (h *Home) openPRInBrowser() tea.Cmd {
	repo := h.resolveCurrentRepo()
	if repo == "" {
		debuglog.Logger.Debug("openPR: no repo selected")
		h.setError(fmt.Errorf("no repo selected"))
		return nil
	}

	info := h.gitInfo()[repo]
	if info == nil || info.PR == nil || info.PR.URL == "" {
		debuglog.Logger.Debug("openPR: no PR for branch", "repo", repo)
		h.setError(fmt.Errorf("no PR for this branch"))
		return nil
	}

	prURL := info.PR.URL
	repoName := filepath.Base(repo)

	return func() tea.Msg {
		// Try Chrome extension first.
		client := &chrome.Client{}
		cmd := &chrome.Command{
			ID:     fmt.Sprintf("pr-%d", time.Now().UnixNano()),
			Action: chrome.ActionOpenOrFocus,
			URL:    prURL,
			Group:  repoName,
		}

		_, err := client.Send(cmd)
		if err != nil {
			// Fallback to opening in the default browser (open / xdg-open).
			debuglog.Logger.Debug("chrome extension unavailable, falling back to browser (open/xdg-open)", "err", err)
			if openErr := openURL(prURL); openErr != nil {
				debuglog.Logger.Error("failed to open PR in browser", "url", prURL, "err", openErr)
				return openPRMsg{err: fmt.Errorf("open PR: %w", openErr)}
			}
		}
		return openPRMsg{}
	}
}

// deferDelete removes a session from the UI and DB but defers tmux/hook/workspace
// cleanup for the undo window. Returns a tick command for expiry.
func (h *Home) deferDelete(msg sessionDeleteMsg) (tea.Model, tea.Cmd) {
	s, ok := h.sessionByID[msg.id]
	if !ok {
		return h, nil
	}

	debuglog.Logger.Info("deferred delete", "id", msg.id, "title", s.Title)

	// Snapshot DB row before deleting.
	row := s.ToRow()
	repoPath := session.GetRepoRoot(s.ProjectPath)

	// Delete from SQLite immediately (crash-safe).
	if err := h.storage.DeleteSession(msg.id); err != nil {
		debuglog.Logger.Error("failed to delete session from storage", "id", msg.id, "err", err)
	}

	// Handle repo unpin if requested.
	if msg.unpinRepo {
		delete(h.pinnedRepos, msg.repoPath)
		if err := h.storage.UnpinRepo(msg.repoPath); err != nil {
			debuglog.Logger.Error("failed to unpin repo", "repo", msg.repoPath, "err", err)
		}
		h.forgetCollapse(msg.repoPath)
		h.forgetSnooze(msg.repoPath)
	}

	// Clear any slot binding pointing at this session. FK cascade drops the
	// DB row (triggered by the DeleteSession above), but the in-memory map
	// needs explicit cleanup so the [N] badge disappears from the sidebar.
	// Slot bindings do NOT survive undo: restoring the session via `u` leaves
	// it unbound, and the user can re-press Alt+<N> to rebind.
	for slot, sid := range h.slotBindings {
		if sid == msg.id {
			delete(h.slotBindings, slot)
		}
	}
	if h.lastSlotTapSlot >= 0 {
		if sid, ok := h.slotBindings[h.lastSlotTapSlot]; !ok || sid == msg.id {
			h.lastSlotTapSlot = -1
		}
	}

	// Remove from in-memory session list.
	var remaining []*session.Session
	for _, sess := range h.sessions {
		if sess.ID != msg.id {
			remaining = append(remaining, sess)
		}
	}
	h.sessions = remaining
	h.rebuildSessionMap()
	h.rebuildFlatItems()

	// Fix cursor.
	if h.cursor >= len(h.flatItems) {
		h.cursor = len(h.flatItems) - 1
	}
	if h.cursor < 0 {
		h.cursor = 0
	}
	if len(h.flatItems) > 0 && h.flatItems[h.cursor].IsRepoHeader {
		h.cursor = NextSelectableItem(h.flatItems, h.cursor, 1)
	}

	// Generate nonce for timer matching.
	nonce := fmt.Sprintf("%s-%d", msg.id, time.Now().UnixNano())

	// Push onto undo stack.
	h.pendingDeletes = append(h.pendingDeletes, PendingDelete{
		Nonce:         nonce,
		Session:       s,
		Row:           row,
		RepoPath:      repoPath,
		DestroyWS:     msg.destroyWorkspace,
		WorkspaceName: msg.workspaceName,
		UnpinRepo:     msg.unpinRepo,
		DeletedAt:     time.Now(),
	})

	// Show undo flash.
	h.setInfo(h.buildUndoFlashMessage())

	// Start expiry timer.
	return h, tea.Tick(undoDeleteTimeout, func(t time.Time) tea.Msg {
		return pendingDeleteExpireMsg{nonce: nonce}
	})
}

// undoDelete restores the most recent pending delete.
func (h *Home) undoDelete() (tea.Model, tea.Cmd) {
	if len(h.pendingDeletes) == 0 {
		return h, nil
	}

	// Pop most recent.
	pd := h.pendingDeletes[len(h.pendingDeletes)-1]
	h.pendingDeletes = h.pendingDeletes[:len(h.pendingDeletes)-1]

	debuglog.Logger.Info("undo delete", "id", pd.Session.ID, "title", pd.Session.Title)

	// Re-insert into SQLite.
	if err := h.storage.SaveSession(pd.Row); err != nil {
		h.setError(fmt.Errorf("undo failed: %w", err))
		return h, nil
	}

	// Re-pin repo if it was unpinned.
	if pd.UnpinRepo {
		h.pinnedRepos[pd.RepoPath] = true
		if err := h.storage.PinRepo(pd.RepoPath); err != nil {
			debuglog.Logger.Error("failed to re-pin repo on undo", "repo", pd.RepoPath, "err", err)
		}
	}

	// Re-add to session list (tmux is still alive).
	h.workerMu.Lock()
	h.sessions = append(h.sessions, pd.Session)
	h.rebuildSessionMap()
	h.workerMu.Unlock()

	// Expand repo group and rebuild sidebar.
	h.repoExpanded[pd.RepoPath] = true
	h.rebuildFlatItems()

	// Move cursor to restored session.
	for i, item := range h.flatItems {
		if !item.IsRepoHeader && item.Session != nil && item.Session.ID == pd.Session.ID {
			h.cursor = i
			h.syncViewport()
			break
		}
	}

	h.actionLog.Add("undo delete", pd.Session.Title, true)
	h.setInfo(fmt.Sprintf("Restored %q", pd.Session.Title))
	return h, nil
}

// handlePendingDeleteExpire finalizes a deferred delete after the undo window.
func (h *Home) handlePendingDeleteExpire(msg pendingDeleteExpireMsg) (tea.Model, tea.Cmd) {
	// Find by nonce.
	idx := -1
	for i, pd := range h.pendingDeletes {
		if pd.Nonce == msg.nonce {
			idx = i
			break
		}
	}
	if idx < 0 {
		return h, nil // already undone
	}

	pd := h.pendingDeletes[idx]
	h.pendingDeletes = append(h.pendingDeletes[:idx], h.pendingDeletes[idx+1:]...)
	// Move into finalizingDeletes so an in-flight cleanup is visible to
	// finalizeAllPendingDeletes if the user quits mid-finalize.
	h.finalizingDeletes = append(h.finalizingDeletes, pd)

	return h, h.finalizeDelete(pd)
}

// finalizeDelete schedules cleanup (tmux kill, hook removal, optional
// workspace destruction) on a background goroutine. All steps shell out or
// hit the filesystem, so they must stay off the Bubble Tea Update loop —
// otherwise an undo-window expiry blocks keystroke processing for the
// duration of `tmux kill-session`. Always returns deleteCleanupDoneMsg so
// the entry can be removed from finalizingDeletes.
func (h *Home) finalizeDelete(pd PendingDelete) tea.Cmd {
	editor := h.cfg.GetEditor() // capture on the Update loop; the goroutine must not touch h
	return func() tea.Msg {
		debuglog.Logger.Info("finalizing delete", "id", pd.Session.ID, "title", pd.Session.Title)

		if pd.Session.IsAlive() {
			if err := pd.Session.Kill(); err != nil {
				debuglog.Logger.Error("failed to kill tmux session", "id", pd.Session.ID, "err", err)
			}
		}

		if err := os.Remove(filepath.Join(hooks.GetHooksDir(), pd.Session.ID+".json")); err != nil && !os.IsNotExist(err) {
			debuglog.Logger.Error("failed to remove hook status file", "id", pd.Session.ID, "err", err)
		}

		var workspaceErr error
		var remaining []string
		attempted := pd.DestroyWS && pd.WorkspaceName != ""
		if attempted {
			remaining, workspaceErr = destroyWorktree(pd.RepoPath, pd.WorkspaceName, editor, 2*time.Second)
		}
		return deleteCleanupDoneMsg{
			sessionID:        pd.Session.ID,
			workspaceErr:     workspaceErr,
			repoPath:         pd.RepoPath,
			workspaceName:    pd.WorkspaceName,
			remainingHolders: remaining,
			destroyAttempted: attempted,
		}
	}
}

// destroyWorktree terminates the dev-stack processes still holding a worktree
// (they detached from the session's tmux pane, so killing the pane didn't stop
// them) and then removes the worktree. Editors, language servers, and shells
// are spared via the proc denylist plus the configured editor. On failure it
// re-scans and returns the remaining holder names so the caller can surface
// what's still blocking removal. Runs on a background goroutine — must not
// touch Home.
func destroyWorktree(repoPath, workspaceName, editor string, grace time.Duration) (remaining []string, err error) {
	provider := workspace.ResolveProvider(repoPath)
	if provider == nil || !provider.CanDestroy() {
		return nil, nil
	}

	// killHolders terminates the dev daemons (process-compose, air, vite/node)
	// still holding the worktree, sparing editors/shells via the proc denylist.
	killHolders := func() {
		holders, ferr := proc.FindHolders(repoPath, []string{editor})
		if ferr != nil || len(holders) == 0 {
			return
		}
		pids := make([]int, len(holders))
		for i, hd := range holders {
			pids[i] = hd.PID
		}
		debuglog.Logger.Info("killing worktree holders before destroy", "repo", repoPath, "procs", strings.Join(uniqueCommands(holders), ", "))
		_ = proc.Kill(pids, grace)
	}

	// Kill holders before removal so nothing re-creates files mid-delete.
	killHolders()

	if err = provider.Destroy(repoPath, workspaceName); err == nil {
		return nil, nil
	}

	// Destroy failed. If the worktree is now an orphan — git's linkage is gone
	// (a failed `git worktree remove` deletes the admin entry before the rmdir
	// that fails) but the directory persists — git can no longer remove it, so
	// fall back to a direct RemoveAll. Guarded on IsWorktree==false so a healthy
	// worktree (which keeps .git intact) can never be force-removed on a
	// transient git error.
	if _, statErr := os.Stat(repoPath); statErr == nil && !git.IsWorktree(repoPath) {
		killHolders() // re-kill: a holder may have respawned during Destroy
		if rmErr := os.RemoveAll(repoPath); rmErr != nil {
			debuglog.Logger.Error("orphan worktree RemoveAll failed", "repo", repoPath, "err", rmErr)
		}
		if _, statErr := os.Stat(repoPath); statErr != nil {
			return nil, nil // gone now — success
		}
	}

	// Still failed — re-scan to report what's holding it (for part B's message).
	if holders, ferr := proc.FindHolders(repoPath, []string{editor}); ferr == nil {
		remaining = uniqueCommands(holders)
	}
	return remaining, err
}

// uniqueCommands returns the distinct command names from holders, order-stable.
func uniqueCommands(holders []proc.Holder) []string {
	seen := make(map[string]bool, len(holders))
	var out []string
	for _, hd := range holders {
		if hd.Command == "" || seen[hd.Command] {
			continue
		}
		seen[hd.Command] = true
		out = append(out, hd.Command)
	}
	return out
}

// handleWorktreeDestroyResult applies part B. On a failed worktree destroy it
// re-pins the worktree (so its header reappears as an empty checkout to retry
// with d) and flags it for a persistent sidebar marker + error; on success it
// clears any prior failure flag.
func (h *Home) handleWorktreeDestroyResult(msg deleteCleanupDoneMsg) {
	if msg.workspaceErr == nil {
		if h.failedWorktreeRemovals[msg.repoPath] {
			delete(h.failedWorktreeRemovals, msg.repoPath)
			h.rebuildFlatItems()
		}
		return
	}

	// Re-pin so the worktree reappears in the sidebar as a retry target.
	if !h.pinnedRepos[msg.repoPath] {
		h.pinnedRepos[msg.repoPath] = true
		if err := h.storage.PinRepo(msg.repoPath); err != nil {
			debuglog.Logger.Error("failed to re-pin failed worktree", "repo", msg.repoPath, "err", err)
		}
	}
	h.failedWorktreeRemovals[msg.repoPath] = true
	analytics.Track(analytics.EventGitCommandFailure, map[string]interface{}{"command": "worktree_remove"})
	h.rebuildFlatItems()

	name := filepath.Base(msg.repoPath)
	if len(msg.remainingHolders) > 0 {
		h.setError(fmt.Errorf("couldn't remove worktree %q — still held by %s; d to retry", name, strings.Join(msg.remainingHolders, ", ")))
	} else {
		h.setError(fmt.Errorf("couldn't remove worktree %q: %w; d to retry", name, msg.workspaceErr))
	}
}

// finalizeAllPendingDeletes synchronously drains both pendingDeletes (undo
// window still open) and finalizingDeletes (cleanup goroutine in flight) on
// quit. tmux kill and hook-file removal are idempotent and run for both lists.
// Workspace Destroy is NOT idempotent (GitWorktreeProvider.Destroy errors when
// the worktree is already gone, and a concurrent run would race with the
// in-flight goroutine's `git worktree remove`), so it only runs for
// pendingDeletes — finalizingDeletes entries already have a goroutine
// responsible for the destroy.
func (h *Home) finalizeAllPendingDeletes() {
	editor := h.cfg.GetEditor()
	finalize := func(pd PendingDelete, destroyWorkspace bool) {
		debuglog.Logger.Info("finalizing pending delete on quit", "id", pd.Session.ID, "title", pd.Session.Title)
		if pd.Session.IsAlive() {
			if err := pd.Session.Kill(); err != nil {
				debuglog.Logger.Error("failed to kill tmux session on quit", "id", pd.Session.ID, "err", err)
			}
		}
		if err := os.Remove(filepath.Join(hooks.GetHooksDir(), pd.Session.ID+".json")); err != nil && !os.IsNotExist(err) {
			debuglog.Logger.Error("failed to remove hook status file on quit", "id", pd.Session.ID, "err", err)
		}
		if destroyWorkspace && pd.DestroyWS && pd.WorkspaceName != "" {
			// Route through destroyWorktree so leftover dev daemons are killed
			// before removal — grace 0 (immediate SIGKILL) so quit isn't delayed.
			if _, err := destroyWorktree(pd.RepoPath, pd.WorkspaceName, editor, 0); err != nil {
				debuglog.Logger.Error("failed to destroy workspace on quit", "id", pd.Session.ID, "workspace", pd.WorkspaceName, "err", err)
			}
		}
	}

	for _, pd := range h.pendingDeletes {
		finalize(pd, true)
	}
	for _, pd := range h.finalizingDeletes {
		finalize(pd, false)
	}
	h.pendingDeletes = nil
	h.finalizingDeletes = nil
}

// buildUndoFlashMessage builds the flash message for the undo prompt.
func (h *Home) buildUndoFlashMessage() string {
	n := len(h.pendingDeletes)
	if n == 0 {
		return ""
	}
	last := h.pendingDeletes[n-1]
	title := last.Session.Title
	if n == 1 {
		return fmt.Sprintf("Deleted %q. u to undo", title)
	}
	return fmt.Sprintf("Deleted %q. u to undo (%d pending)", title, n)
}

// countSessionsForRepo counts live sessions for a given repo path.
func (h *Home) countSessionsForRepo(repoPath string) int {
	count := 0
	for _, s := range h.sessions {
		if session.GetRepoRoot(s.ProjectPath) == repoPath {
			count++
		}
	}
	return count
}

// --- Tick / status ---

func (h *Home) tick() tea.Cmd {
	interval := tickInterval
	if h.cfg != nil && h.cfg.TickIntervalSec > 0 {
		interval = time.Duration(h.cfg.TickIntervalSec) * time.Second
	}
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (h *Home) previewTick() tea.Cmd {
	return tea.Tick(previewTickInterval, func(t time.Time) tea.Msg {
		return previewTickMsg(t)
	})
}

// listenForHookChanges blocks until the HookWatcher signals a status change,
// then returns a hookChangedMsg. Runs as a tea.Cmd in its own goroutine.
func (h *Home) listenForHookChanges() tea.Msg {
	if h.hookWatcher == nil {
		return nil
	}
	select {
	case <-h.hookWatcher.Changes():
		return hookChangedMsg{}
	case <-h.ctx.Done():
		return nil
	}
}

func (h *Home) handleTick() (tea.Model, tea.Cmd) {
	// Trigger background worker (non-blocking).
	select {
	case h.statusTrigger <- struct{}{}:
	default: // worker busy, skip
	}

	// Wake anything whose snooze has expired. Self-throttled, and it rebuilds
	// the tree itself when something changed — so it runs before the
	// unconditional rebuild below rather than after, and the woken rows are
	// already un-dimmed on this frame.
	h.maybeWakeSnoozed()

	h.rebuildFlatItems()
	h.refreshTips(!h.modalOpen())

	// Daily-active heartbeat: no-op until the calendar day rolls over (or the
	// client is disabled), so this is cheap to call every tick. Keeps DAU
	// accurate for instances left running for days without a restart.
	analytics.Heartbeat()

	// Age out the What's New badge against its 7-day window: recompute each tick
	// so a highlighted release that crosses the boundary while fleet keeps
	// running flips hasUnseenWhatsNew back to false. Without this the badge (and
	// its 60ms shimmer loop) would never retire on a long-lived instance — the
	// verdict is otherwise only sampled at load time (Init / explicit opens).
	h.recomputeWhatsNew()
	// Preview is now handled by the faster previewTick, no need to fetch here.
	// Re-arm the badge shimmer if it should be running but isn't (e.g. it
	// stopped while a modal was open, and the modal has since closed).
	return h, tea.Batch(h.tick(), h.ensureWhatsNewShimmer())
}

// bootstrapRepoSet returns the union of repo roots derived from sessions and
// the user's pinned-repo set — i.e. every repo the sidebar will display on
// the first non-splash frame. Output order is non-deterministic; the
// bootstrap fan-out treats it as a set.
func (h *Home) bootstrapRepoSet() []string {
	seen := make(map[string]bool)
	var repos []string
	for _, s := range h.sessions {
		root := session.GetRepoRoot(s.ProjectPath)
		if !seen[root] {
			seen[root] = true
			repos = append(repos, root)
		}
	}
	for repo := range h.pinnedRepos {
		if !seen[repo] {
			seen[repo] = true
			repos = append(repos, repo)
		}
	}
	return repos
}

// allExpandKeys returns every origin + checkout key the sidebar could render
// right now, derived from sessions + pinned repos + pending workspaces (the
// same sources BuildFlatItems consumes). Used by Expand All / Collapse All
// so they can force-write every entry to a known value — IsExpanded defaults
// missing keys to true, which means iterating only the existing map entries
// would leave un-toggled groups in the wrong state.
func (h *Home) allExpandKeys() []string {
	checkoutSeen := make(map[string]bool)
	originSeen := make(map[string]bool)
	var keys []string

	addCheckout := func(repo string) {
		if repo == "" || checkoutSeen[repo] {
			return
		}
		checkoutSeen[repo] = true
		keys = append(keys, repo)
	}
	addOrigin := func(origin string) {
		if origin == "" || originSeen[origin] {
			return
		}
		originSeen[origin] = true
		keys = append(keys, OriginExpandKey(origin))
	}

	originFor := func(repo string) string {
		info := h.gitInfo()[repo]
		if info != nil && info.OriginKey != "" {
			return info.OriginKey
		}
		return "local:" + filepath.Base(repo)
	}

	for _, s := range h.sessions {
		repo := session.GetRepoRoot(s.ProjectPath)
		addCheckout(repo)
		addOrigin(originFor(repo))
	}
	for repo := range h.pinnedRepos {
		addCheckout(repo)
		addOrigin(originFor(repo))
	}
	for _, pw := range h.pendingWorkspaces {
		if pw == nil {
			continue
		}
		addCheckout(pw.RepoPath)
		addOrigin(originFor(pw.RepoPath))
	}
	return keys
}

// bootstrapGitInfo fans out git+PR refresh across `repos` with high
// parallelism (8 workers — these calls are network-bound) and a 6-second
// wall-clock deadline. Goroutines that finish after the deadline still
// write their result to the cache; the next refresh tick will pick them
// up. Returns bootstrapDoneMsg as soon as every goroutine finishes OR
// the deadline elapses.
func (h *Home) bootstrapGitInfo(repos []string) tea.Cmd {
	return func() tea.Msg {
		const maxParallel = 8
		const deadline = 6 * time.Second

		h.bootstrapResolved.Store(0)
		h.refreshAllGitAndPR(repos, maxParallel, deadline, &h.bootstrapResolved)
		return bootstrapDoneMsg{}
	}
}

// bootProgress reports how much of the bootstrap has resolved, in [0,1].
// Reads an atomic counter that bootstrapGitInfo's per-repo goroutines bump
// as they finish — NOT len(gitInfoCache), which can be non-zero from the
// SQLite PR-cache hydration in loadSessionsMsg (the bar would otherwise
// start at 100% on a warm-cache launch).
//
// Returns 0 before bootstrapRepos has been initialized — there's a one-frame
// window between the first paint and loadSessionsMsg arriving where the
// splash renders without yet knowing the repo count. The empty-fleet path
// keeps the splash up (bar at 0, spinner animating) through the Claude-history
// scan and reveals the launchpad when discoveryMsg lands — one transition, no
// splash → scanning → list flicker.
func (h *Home) bootProgress() float64 {
	if h.bootstrapRepos <= 0 {
		return 0
	}
	resolved := int(h.bootstrapResolved.Load())
	if resolved > h.bootstrapRepos {
		resolved = h.bootstrapRepos
	}
	return float64(resolved) / float64(h.bootstrapRepos)
}

// splashTick schedules the next splash-spinner advance (~80ms cadence).
func (h *Home) splashTick() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg {
		return splashFrameMsg(t)
	})
}

// statusWorker runs in its own goroutine, performing all blocking I/O
// (tmux, git, gh) outside the Bubble Tea Update() loop.
func (h *Home) statusWorker() {
	// Dev-only watchdog: auto-dumps goroutine stacks if this worker ever stops
	// completing cycles. Gated to `make run` (version=="dev") builds — it's a
	// debugging aid, not something to run on user machines.
	if h.version == "dev" {
		go h.workerWatchdog()
	}

	ticker := time.NewTicker(activeStatusInterval)
	defer ticker.Stop()

	for {
		select {
		case <-h.ctx.Done():
			return
		case <-h.statusTrigger:
		case <-ticker.C:
		}

		h.statusWorkerCycle()
	}
}

// syncHookStatuses reads the latest hook statuses from the HookWatcher and applies
// them to the given sessions. Caller must ensure thread-safe access to sessions.
// Returns the IDs of sessions whose hook meaningfully changed (new status or timestamp);
// callers can forward these to priorityStatusUpdates for immediate UpdateStatus().
//
// resolveRotation must be true ONLY on the worker goroutine: a foreign/rotated
// session id triggers blocking transcript I/O (rotation detection), which must
// never run on the Bubble Tea Update() loop. UI-path callers pass false; the
// worker adopts a rotation within one fast cycle (~500ms).
func (h *Home) syncHookStatuses(sessions []*session.Session, resolveRotation bool) []string {
	if h.hookWatcher == nil {
		return nil
	}
	var changed []string
	for _, s := range sessions {
		hs := h.hookWatcher.GetStatus(s.ID)
		if hs != nil {
			oldClaudeSessionID := s.ClaudeSessionID
			oldFirstPrompt := s.FirstPrompt
			oldPromptCount := s.PromptCount
			if s.UpdateHookStatus(&session.HookStatus{
				Status:      hs.Status,
				SessionID:   hs.SessionID,
				UpdatedAt:   hs.UpdatedAt,
				UserPrompt:  hs.UserPrompt,
				PromptCount: hs.PromptCount,
			}, resolveRotation) {
				changed = append(changed, s.ID)
			}
			// Persist new Claude session ID if it changed.
			if s.ClaudeSessionID != oldClaudeSessionID && s.ClaudeSessionID != "" {
				if err := h.storage.UpdateClaudeSessionID(s.ID, s.ClaudeSessionID); err != nil {
					debuglog.Logger.Error("storage: UpdateClaudeSessionID", "id", s.ID, "err", err)
				}
			}
			// Persist prompt changes. Re-titling on later prompts is driven by
			// Claude's own ai-title (read from the JSONL in the worker cycle),
			// not by re-running the heuristic per prompt.
			if s.PromptCount != oldPromptCount {
				h.markSessionAccessed(s)
				if err := h.storage.UpdatePromptCount(s.ID, s.PromptCount); err != nil {
					debuglog.Logger.Error("storage: UpdatePromptCount", "id", s.ID, "err", err)
				}
			}
			if s.FirstPrompt != "" && s.FirstPrompt != oldFirstPrompt {
				if err := h.storage.UpdateFirstPrompt(s.ID, s.FirstPrompt); err != nil {
					debuglog.Logger.Error("storage: UpdateFirstPrompt", "id", s.ID, "err", err)
				}
			}
		}
	}
	return changed
}

// updateAndPersistStatus runs a full UpdateStatus() on the session and persists
// the result to storage if the status changed. Called from the worker goroutine.
// Returns true if the status changed (so callers can repaint just the sessions
// that flipped).
func (h *Home) updateAndPersistStatus(s *session.Session) bool {
	oldStatus := s.GetStatus()
	s.UpdateStatus()
	newStatus := s.GetStatus()
	if oldStatus != newStatus {
		if err := h.storage.UpdateStatus(s.ID, string(newStatus)); err != nil {
			debuglog.Logger.Error("storage: UpdateStatus", "id", s.ID, "status", newStatus, "err", err)
		}
		return true
	}
	return false
}

// enqueuePriorityUpdates pushes session IDs into the worker's priority queue
// and kicks the worker to drain them immediately. Safe to call from any goroutine.
func (h *Home) enqueuePriorityUpdates(ids []string) {
	if len(ids) == 0 {
		return
	}
	for _, id := range ids {
		select {
		case h.priorityStatusUpdates <- id:
		default:
			// Queue full — next worker cycle's round-robin will still catch it.
		}
	}
	select {
	case h.statusTrigger <- struct{}{}:
	default:
	}
}

// heavyCycleDue reports whether the worker should run its heavy ~2s pass this
// cycle. A zero lastHeavy (fresh worker) is always due, so the first cycle runs
// full init.
func heavyCycleDue(lastHeavy, now time.Time) bool {
	return now.Sub(lastHeavy) >= tickInterval
}

// fastPassSessions returns the active sessions the ~500ms fast pass should
// pane-recheck — Running/Waiting/Starting — skipping any already handled by the
// priority queue this cycle. These states transition via pane content (no hook
// fires when a permission is approved), so they must not wait for the 2s
// round-robin. Idle/Finished/Error stay on the round-robin; their →running
// transitions ride the hook priority queue.
func fastPassSessions(sessions []*session.Session, processed map[string]bool) []*session.Session {
	var active []*session.Session
	for _, s := range sessions {
		if processed[s.ID] {
			continue
		}
		switch s.GetStatus() {
		case session.StatusRunning, session.StatusWaiting, session.StatusStarting:
			active = append(active, s)
		}
	}
	return active
}

// roundRobinBatch returns up to `budget` sessions not already handled this cycle,
// scanning forward from `start` (wrapping), and the cursor to resume from next
// cycle. The cursor advances only past sessions actually examined — not by a
// fixed window — so when the list is dominated by active (already-processed)
// sessions the scan still reaches the idle/finished sessions instead of stepping
// over them and starving them of their periodic pane re-check.
func roundRobinBatch(sessions []*session.Session, processed map[string]bool, start, budget int) (picked []*session.Session, next int) {
	n := len(sessions)
	if n == 0 {
		return nil, start
	}
	examined := 0
	for examined < n && len(picked) < budget {
		idx := (start + examined) % n
		examined++
		if s := sessions[idx]; !processed[s.ID] {
			picked = append(picked, s)
		}
	}
	return picked, (start + examined) % n
}

// seedActiveCaptures captures every given session's pane in one batched tmux
// call and seeds each session's capture cache, collapsing N per-session
// capture-pane shell-outs into a single invocation on the hot ~500ms fast path.
// Sessions the batch omits keep a cold cache and capture individually inside
// UpdateStatus, so coverage is never lost — only the batch speedup.
func (h *Home) seedActiveCaptures(active []*session.Session) {
	if len(active) < 2 {
		return // a single session saves nothing over its own direct capture
	}
	byName := make(map[string]*session.Session, len(active))
	names := make([]string, 0, len(active))
	for _, s := range active {
		ts := s.GetTmuxSession()
		if ts == nil {
			continue
		}
		byName[ts.Name] = s
		names = append(names, ts.Name)
	}
	for name, content := range tmux.BatchCapturePanes(names) {
		if s := byName[name]; s != nil {
			if ts := s.GetTmuxSession(); ts != nil {
				ts.SeedCapture(content)
			}
		}
	}
}

func (h *Home) statusWorkerCycle() {
	// Liveness stamps for the snapshot + watchdog. Registered first so it runs
	// LAST (after the recover below), recording completion even if the body
	// panics. A healthy worker refreshes lastWorkerCycleNano every ~500ms; a
	// stale value is the wedge signal.
	h.workerCycleStartNano.Store(time.Now().UnixNano())
	defer func() {
		h.lastWorkerCycleNano.Store(time.Now().UnixNano())
		h.workerCycleStartNano.Store(0)
	}()

	// Recover from panics to keep the worker alive.
	defer func() {
		if r := recover(); r != nil {
			debuglog.Logger.Error("statusWorkerCycle panic recovered", "panic", r)
		}
	}()

	// Heavy work (full round-robin over idle sessions, git/PR fan-out,
	// auto-naming, full status-bar repaint) runs at most every tickInterval.
	// The fast path (priority hook updates + active-session pane re-checks)
	// runs every invocation (~activeStatusInterval) so waiting<->running flips
	// — which fire no Claude hook on permission approval — surface within
	// ~500ms instead of starving on the 2s round-robin.
	heavy := heavyCycleDue(h.lastHeavyCycleAt, time.Now())
	if heavy {
		h.lastHeavyCycleAt = time.Now()
	}

	// Take a snapshot of sessions + shells under lock.
	h.workerMu.Lock()
	sessions := make([]*session.Session, len(h.sessions))
	copy(sessions, h.sessions)
	shells := make([]*shell.Shell, len(h.shells))
	copy(shells, h.shells)
	h.workerMu.Unlock()

	if len(sessions) == 0 && len(shells) == 0 {
		return
	}

	// Refresh the tmux activity + pane-dead cache every cycle. This is a single
	// `tmux list-panes -a` call — O(1) in session count, not O(N) — and feeds
	// Exists/GetActivity/IsPaneDead for every session, so per-session
	// UpdateStatus calls below never shell out for those. It also keeps the
	// 3s/10s running/finished hold heuristics fresh on the fast (~500ms) path;
	// gating it to the 2s heavy cadence would let them read activity up to ~2s
	// stale, shrinking the 3s hold to ~1s and flipping busy sessions to finished
	// prematurely.
	tmux.RefreshSessionCache()

	// Refresh shell statuses from the just-updated cache (cache-only reads,
	// plus one bounded PaneDeadInfo on an exited pane). Shells aren't in
	// h.sessions, so they get their own pass.
	for _, sh := range shells {
		sh.RefreshStatus()
	}

	// Label each shell with the latest command it ran. One `ps` call resolves
	// every shell's foreground child process from its cached pane pid; a shell
	// at a prompt has no child, so its last command persists. Heavy cadence
	// only — a tab label doesn't need sub-second freshness.
	if heavy && len(shells) > 0 {
		byPID := make(map[int]*shell.Shell, len(shells))
		pids := make([]int, 0, len(shells))
		for _, sh := range shells {
			if pid := tmux.PanePID(sh.TmuxName()); pid > 0 {
				byPID[pid] = sh
				pids = append(pids, pid)
			}
		}
		for pid, cmd := range proc.ForegroundCommands(pids) {
			byPID[pid].SetLastCommand(cmd)
		}
	}

	// 3. Sync hook status (fast: in-memory map lookups; worker may resolve a
	// session-id rotation here — off the UI loop, so its transcript I/O is safe).
	//
	// The returned IDs MUST be carried into this cycle's priority set, and the merge
	// below stays adjacent to this call on purpose. syncHookStatuses diffs against
	// the session's in-memory hook state and then overwrites it, so a transition is
	// consume-once: whichever caller syncs first eats it. During an attach tea.Exec
	// suspends the Update loop, so hookChangedMsg never runs and this worker call is
	// always the first to observe — dropping the result here left the session on the
	// round-robin (≈26s at 65 sessions), and the post-detach catch-up sync in
	// statusUpdateMsg found nothing left to enqueue.
	//
	// Nothing may come between the consume and the merge: the cycle's defer recover()
	// swallows a panic from anything in between (step 3b reads transcripts and writes
	// SQLite), and the transitions this call already ate would be gone for good —
	// the same failure reached a different way.
	hookChanged := h.syncHookStatuses(sessions, true)
	priorityIDs := make(map[string]bool, len(hookChanged))
	for _, id := range hookChanged {
		priorityIDs[id] = true
	}

	// 3b. Auto-name: generate title for ONE session per cycle (heavy cadence).
	// Priority: manual (R key) > the agent's own title (Claude custom-title, then
	// ai-title; Codex `/rename`) > last prompt heuristic.
	if heavy && h.cfg.IsAutoNameEnabled() {
		for _, s := range sessions {
			if s.ManuallyRenamed {
				continue
			}

			// Re-read the agent's own title (Claude's JSONL, Codex's state DB). A
			// freshly-created session with no title yet is polled every cycle so
			// it adopts its ai-title promptly; otherwise (already titled, or old
			// enough that its transcript is large) we re-check ~every 30s to
			// follow custom-title/ai-title/`/rename` drift without re-scanning a
			// growing file each tick.
			recheck := agentNameRecheckInterval
			if s.AgentSessionName == "" && time.Since(s.CreatedAt) < agentNameFreshPollWindow {
				recheck = 0
			}
			if s.ClaudeSessionID != "" && time.Since(s.AgentNameLastChecked) >= recheck {
				s.AgentNameLastChecked = time.Now()
				name := session.ReadAgentSessionName(s.Agent, s.ClaudeSessionID, s.ProjectPath)
				if name != "" && name != s.AgentSessionName {
					s.AgentSessionName = name
					s.Title = name
					if err := h.storage.UpdateTitle(s.ID, name); err != nil {
						debuglog.Logger.Error("storage: UpdateTitle (agent name)", "id", s.ID, "err", err)
					}
					s.TitleGenerated = true
					if err := h.storage.MarkTitleGenerated(s.ID); err != nil {
						debuglog.Logger.Error("storage: MarkTitleGenerated", "id", s.ID, "err", err)
					}
				}
			}
			if s.AgentSessionName != "" {
				continue
			}

			// Fallback: prompt-based title heuristic.
			if s.FirstPrompt != "" && !s.TitleGenerated {
				title := naming.GenerateTitle(s.FirstPrompt)
				if title != "" && title != s.Title {
					s.Title = title
					if err := h.storage.UpdateTitle(s.ID, title); err != nil {
						debuglog.Logger.Error("storage: UpdateTitle (auto-name)", "id", s.ID, "err", err)
					}
				}
				s.TitleGenerated = true
				if err := h.storage.MarkTitleGenerated(s.ID); err != nil {
					debuglog.Logger.Error("storage: MarkTitleGenerated", "id", s.ID, "err", err)
				}
				break // one per cycle
			}
		}
	}

	// 4. Priority updates first — sessions whose hook just changed, seeded above
	// from this cycle's own sync and topped up here from the UI path's queue.
	// These bypass round-robin so the UI reflects fresh hook status within
	// ~100ms of the hook firing (vs. up to (N/statusRoundRobin)*tickInterval seconds).
drainPriority:
	for {
		select {
		case id := <-h.priorityStatusUpdates:
			priorityIDs[id] = true
		default:
			break drainPriority
		}
	}
	processed := make(map[string]bool, len(priorityIDs))
	// Sessions whose status flipped on the fast (non-heavy) path; their tmux
	// status bars are repainted immediately below so the in-pane pill tracks
	// the sidebar within ~500ms instead of lagging until the next heavy cycle.
	var changedBars []*session.Session
	prio := make([]*session.Session, 0, len(priorityIDs))
	for _, s := range sessions {
		if priorityIDs[s.ID] {
			prio = append(prio, s)
		}
	}
	// Batch-capture their panes in one tmux call, exactly as the fast pass does
	// below. This loop marks them processed and fastPassSessions skips processed,
	// so without this they never reach that batch and each shells out to its own
	// capture-pane inside UpdateStatus — captureCacheTTL (400ms) is already stale
	// against the ~500ms cycle. That cost scales with hook bursts, not session
	// count: the startup pass (loadExisting seeds every session's hook file before
	// the first cycle, so every session reports changed) or Reload All Sessions
	// would fork one capture-pane per session, serially, inside a single cycle.
	h.seedActiveCaptures(prio)
	for _, s := range prio {
		if h.updateAndPersistStatus(s) {
			changedBars = append(changedBars, s)
		}
		processed[s.ID] = true
	}

	// Fast active-session pass (every cycle): re-check Running/Waiting/Starting
	// sessions. These transition via pane content (no hook fires when the user
	// approves a permission), so without this they'd only refresh on the ~2s
	// round-robin — up to ceil(N/statusRoundRobin)*tickInterval behind at high
	// session counts (≈18s at N=43). Idle/finished stay on the round-robin
	// below; their important transitions (→running) ride the hook priority queue.
	// Batch-capture the active sessions' panes in a single tmux invocation and
	// seed each capture cache, so the per-session UpdateStatus calls below read
	// warm caches instead of each shelling out to capture-pane. Sessions the
	// batch misses (a dead pane aborts the chain) keep a cold cache and fall
	// back to a live capture inside UpdateStatus.
	active := fastPassSessions(sessions, processed)
	h.seedActiveCaptures(active)
	for _, s := range active {
		if h.updateAndPersistStatus(s) {
			changedBars = append(changedBars, s)
		}
		processed[s.ID] = true
	}

	// Everything below is heavy work — only on the ~2s cadence. On fast cycles
	// repaint just the status bars whose session flipped, then bail.
	// refreshTmuxStatusBars is cache-guarded, so the next heavy cycle's full
	// repaint won't redundantly re-apply these.
	if !heavy {
		if len(changedBars) > 0 {
			h.refreshTmuxStatusBars(changedBars)
		}
		return
	}

	// 5. Round-robin status updates (pane capture — blocking) over the sessions
	// not already handled this cycle, capped at statusRoundRobin real updates.
	batch, next := roundRobinBatch(sessions, processed, h.statusRRIndex, statusRoundRobin)
	for _, s := range batch {
		h.updateAndPersistStatus(s)
	}
	h.statusRRIndex = next

	// 5b. Memory-pressure idle-session sweep — hibernate the most-idle sessions
	// when the system is under memory pressure, so a full fleet can't OOM-kill the
	// shared tmux server. Self-throttled to a slow cadence; probes sysctl (blocking)
	// which is why it lives here on the worker, not the Update loop.
	h.maybeSuspendIdleSessions(sessions)

	// 5. Git+PR refresh used to run here, inline. It now lives on its own
	// goroutine (gitWorker) — see the comment there for why sharing this
	// one was a bug.

	// 6. Repaint tmux status bars for sessions whose state changed since the
	// last cycle. Sessions whose status + theme key matches the last applied
	// value are skipped — a single tmux set-option round-trip is fast but
	// running it for 100 idle sessions every 2s is wasteful.
	h.refreshTmuxStatusBars(sessions)
}

// gitWorker refreshes per-repo git + PR state on its own goroutine and cadence.
//
// This is deliberately NOT part of statusWorkerCycle. The fan-out is unbounded:
// N repos at 4-wide, each costing a serial git refresh plus up to two 15s `gh`
// calls, and every result has to reach the Update loop. Sharing a goroutine
// with the ~500ms fast pass meant a slow fan-out starved the active-session
// pane re-checks — and a permission grant fires no hook, so waiting→running is
// pane-only. A 16-minute cycle once froze every session's status for its whole
// duration.
func (h *Home) gitWorker() {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-h.ctx.Done():
			return
		case <-h.gitTrigger:
		case <-ticker.C:
		}

		h.gitWorkerCycle()
	}
}

// triggerGitRefresh asks gitWorker for an out-of-band cycle. Non-blocking: the
// channel is buffered(1), so a pending request coalesces with this one.
func (h *Home) triggerGitRefresh() {
	select {
	case h.gitTrigger <- struct{}{}:
	default:
	}
}

func (h *Home) gitWorkerCycle() {
	// Liveness stamps for the watchdog, mirroring statusWorkerCycle. Registered
	// first so the deferred completion stamp runs last.
	h.gitCycleStartNano.Store(time.Now().UnixNano())
	defer func() {
		h.lastGitCycleNano.Store(time.Now().UnixNano())
		h.gitCycleStartNano.Store(0)
	}()

	defer func() {
		if r := recover(); r != nil {
			debuglog.Logger.Error("gitWorkerCycle panic recovered", "panic", r)
		}
	}()

	// Nothing to refresh for while attached: tea.Exec has taken over the
	// terminal, View() renders blank, and the detach path (statusUpdateMsg)
	// rebuilds the sidebar wholesale anyway. Skipping also keeps the fan-out
	// away from a Tea loop that is not draining messages.
	if h.isAttaching.Load() {
		return
	}

	h.workerMu.Lock()
	sessions := make([]*session.Session, len(h.sessions))
	copy(sessions, h.sessions)
	h.workerMu.Unlock()

	if len(sessions) == 0 {
		return
	}

	// Stamp `repoLastHotAt` so the TTL classifier (next call) can see who's
	// active right now. A repo is "hot" if any of its sessions is currently
	// Running — checked every cycle, cheap. repoLastHotAt is only ever touched
	// from this cycle and from bootstrap; gitWorker starts on bootstrapDoneMsg,
	// so the two never run concurrently and no lock is needed.
	now := time.Now()
	for _, s := range sessions {
		if s.GetStatus() == session.StatusRunning {
			h.repoLastHotAt[session.GetRepoRoot(s.ProjectPath)] = now
		}
	}

	// Fan out bounded to 4 concurrent goroutines so subprocess load stays flat.
	// Branch/dirty lands within the 2s tick; PR refresh respects the per-repo
	// TTL gate (60s hot / 2 min cold) inside the goroutine.
	h.refreshAllGitAndPR(h.uniqueRepoPathsFromSessions(sessions), 4, 0, nil)
}

// workerStallThreshold is how long the status worker may go without completing a
// cycle before the dev-only watchdog treats it as wedged and dumps goroutine
// stacks. It must clear the worst-case *bounded* cycle so a merely-slow run
// doesn't false-fire: a single repo's git refresh is serial (~40s of 8s
// timeouts) and GetPRForBranch chains two gh calls — `gh pr view` (15s) plus
// the `gh api graphql` thread-count query (15s) ≈ 30s — so one repo can
// legitimately take ~70s even fanned out 4-wide. 90s sits above that, firing
// only when a call ignores its deadline or a real deadlock occurs.
const workerStallThreshold = 90 * time.Second

// attachAdjustedStall converts a raw since-last-cycle latency into one that
// excludes the current attach window.
//
// An attach suspends the Tea loop, and both workers deliberately skip sends (and
// gitWorker skips its whole cycle) while it lasts — so latency measured across
// one says nothing about a wedge. Skipping the check outright was the first
// attempt and it was wrong in the other direction: a genuine wedge starting
// mid-attach went uncaptured, which is exactly the freeze this watchdog is for.
// Subtracting the window keeps both properties.
func attachAdjustedStall(stalledFor time.Duration, attachStart int64, now time.Time) time.Duration {
	if attachStart == 0 {
		return stalledFor
	}
	attached := now.Sub(time.Unix(0, attachStart))
	if attached <= 0 {
		return stalledFor
	}
	if attached >= stalledFor {
		return 0
	}
	return stalledFor - attached
}

// workerWatchdog auto-captures a goroutine dump when either worker stops
// completing cycles — the failure mode where status (or branch/dirty/PR) freezes
// while the UI stays responsive, because a worker waits synchronously on
// blocking I/O. Launched only on dev (`make run`) builds.
func (h *Home) workerWatchdog() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// One episode stamp per watched worker: the stamp is frozen while wedged, so
	// dump once and wait for it to advance (worker recovered) before re-arming.
	dumpedFor := map[string]int64{}
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
		}

		now := time.Now()
		attachStart := h.attachStartedAt.Load()

		// Both workers are watched. The git+PR fan-out — the blocking work this
		// watchdog was written for — now lives on gitWorker, so watching only
		// statusWorker would report healthy through a permanent git wedge.
		for _, w := range []struct {
			name  string
			last  int64
			start int64
		}{
			{"status", h.lastWorkerCycleNano.Load(), h.workerCycleStartNano.Load()},
			{"git", h.lastGitCycleNano.Load(), h.gitCycleStartNano.Load()},
		} {
			// Normally measure the stall from the last completed cycle. Before the
			// first cycle ever completes (last==0), fall back to the in-flight
			// cycle's start so a wedge on the very first cycle is still caught.
			episode := w.last
			var stalledFor time.Duration
			switch {
			case w.last != 0:
				stalledFor = now.Sub(time.Unix(0, w.last))
			case w.start != 0:
				episode = w.start
				stalledFor = now.Sub(time.Unix(0, w.start))
			default:
				continue // worker hasn't started a cycle yet
			}
			if attachAdjustedStall(stalledFor, attachStart, now) < workerStallThreshold {
				continue
			}
			if episode == dumpedFor[w.name] {
				continue
			}
			dumpedFor[w.name] = episode

			var cycleStart time.Time
			if w.start != 0 {
				cycleStart = time.Unix(0, w.start)
			}
			dir := writeWorkerStallDump(stalledFor, cycleStart)
			debuglog.Logger.Warn("worker stalled — goroutine dump written",
				"worker", w.name, "stalled_for", stalledFor.Round(time.Millisecond), "dir", dir)
		}
	}
}

// workerHeartbeat snapshots the status worker's liveness stamps for a status
// snapshot. Reads lock-free atomics, safe from any goroutine.
func (h *Home) workerHeartbeat() workerHeartbeat {
	var hb workerHeartbeat
	if n := h.lastWorkerCycleNano.Load(); n != 0 {
		hb.LastCycleAt = time.Unix(0, n)
	}
	if n := h.workerCycleStartNano.Load(); n != 0 {
		hb.CycleStartAt = time.Unix(0, n)
	}
	if n := h.lastGitCycleNano.Load(); n != 0 {
		hb.LastGitCycleAt = time.Unix(0, n)
	}
	if n := h.gitCycleStartNano.Load(); n != 0 {
		hb.GitCycleStartAt = time.Unix(0, n)
	}
	return hb
}

// refreshTmuxStatusBars re-applies the theme + state pill on every session's
// tmux chrome whose (status, theme) key has changed since the last refresh.
// Called from the status worker, so map access is single-threaded — no lock.
func (h *Home) refreshTmuxStatusBars(sessions []*session.Session) {
	if len(sessions) == 0 {
		return
	}
	themeKey := colorHex(ColorBg) + colorHex(ColorAccent) // any theme-change invalidates the cache
	gitSnap := h.gitInfo()

	for _, s := range sessions {
		if s == nil {
			continue
		}
		tmuxS := s.GetTmuxSession()
		if tmuxS == nil {
			continue
		}
		status := s.GetStatus()
		opts := h.buildTmuxStatusBarOpts(s, gitSnap[session.GetRepoRoot(s.ProjectPath)])
		// Cache key must cover every input to ApplyStatusBar, not just status
		// and theme. A branch switch / PR refresh / auto-rename changes
		// opts.DisplayName/Origin/Branch/PRSummary/Path but leaves status
		// alone — without those in the key we'd skip the re-apply and the
		// pane chrome would go stale.
		key := strings.Join([]string{
			string(status), themeKey,
			opts.DisplayName, opts.Origin, opts.Branch, opts.PRSummary, opts.Path,
		}, "\x00")
		if last, ok := h.lastTmuxStatusBar[s.ID]; ok && last == key {
			continue
		}
		// Apply in a fresh goroutine so a slow tmux server can't stall the
		// worker cycle. Each session is its own goroutine; tmux serialises
		// set-option per server, so concurrent calls just queue.
		go tmuxS.ApplyStatusBar(opts)
		h.lastTmuxStatusBar[s.ID] = key
	}
}

// buildTmuxStatusBarOpts converts the live theme + repo state into the
// tmux-agnostic struct ApplyStatusBar consumes. The chrome inside the pane
// surfaces only info you can't see from inside the tool — origin, branch,
// PR status, path — so you stay oriented without detaching.
func (h *Home) buildTmuxStatusBarOpts(s *session.Session, info *git.RepoInfo) tmux.StatusBarOpts {
	branch := ""
	origin := ""
	prSummary := ""
	prColor := colorHex(ColorTextDim)
	if info != nil {
		branch = info.Branch
		origin = strings.TrimPrefix(info.OriginKey, "local:")
		if info.PR != nil {
			if txt := previewPRSummary(info.PR); txt != "" {
				prSummary = txt
				// Reuse the sidebar/preview badge color logic so draft, merged,
				// fail, approved, and pending stay consistent across the UI.
				if fg := prBadgeStyle(info.PR).GetForeground(); fg != nil {
					prColor = colorHex(fg)
				}
			}
		}
	}
	return tmux.StatusBarOpts{
		StripBg:     colorHex(ColorBorder),
		StripFg:     colorHex(ColorText),
		Dim:         colorHex(ColorTextDim),
		BorderColor: colorHex(ColorBorder),
		AccentColor: colorHex(ColorAccent),
		Origin:      origin,
		PRSummary:   prSummary,
		PRColor:     prColor,
		DisplayName: s.Title,
		Branch:      branch,
		Path:        shortenPath(s.ProjectPath),
		DetachHint:  "ctrl+q detach",
	}
}

// PR-refresh TTL constants. Hot repos refresh fast so an actively-used
// branch's badge stays responsive; cold repos refresh slowly so a 24-repo
// fleet doesn't burn through the gh rate limit.
const (
	prTTLHot         = 60 * time.Second
	prTTLCold        = 2 * time.Minute
	prHotnessWindow  = 15 * time.Minute
	prRateLimitedFor = 5 * time.Minute
)

// repoTTLFor returns the per-repo PR refresh TTL. A repo is hot if any of
// its sessions was Running within `prHotnessWindow`; cold otherwise.
// repoLastHotAt is worker-only state — no synchronization required.
func (h *Home) repoTTLFor(repo string, now time.Time) time.Duration {
	if t, ok := h.repoLastHotAt[repo]; ok && now.Sub(t) < prHotnessWindow {
		return prTTLHot
	}
	return prTTLCold
}

// refreshAllGitAndPR runs the per-repo git+PR refresh across `repos` with
// bounded parallelism, optionally capped by a wall-clock deadline.
//
// Mirrors the lock-and-merge pattern from the original round-robin step 5b:
// each goroutine reads any cached PR forward, writes the merged result back
// atomically under workerMu.
//
//   - maxParallel <= 0 collapses to 1 (no fan-out).
//   - deadline <= 0 waits for every goroutine to finish.
//   - deadline > 0 returns early when it elapses; in-flight goroutines keep
//     running and their results land in the cache when they finish — the
//     next refresh tick picks them up via rebuildFlatItems.
func (h *Home) refreshAllGitAndPR(repos []string, maxParallel int, deadline time.Duration, progress *atomic.Int32) {
	if len(repos) == 0 {
		return
	}
	if maxParallel < 1 {
		maxParallel = 1
	}
	cycleStart := time.Now()
	var prFetches atomic.Int32
	var rateLimitedHits atomic.Int32

	// Precompute per-repo TTL once so the per-repo goroutines don't all
	// hammer repoLastHotAt. The map is only ever touched from the
	// bootstrap or worker cycle that called us — both single-goroutine.
	// Bootstrap and any cycle where `repoLastHotAt` is empty produce
	// all-cold TTLs; that's fine — bootstrap honors carried-forward
	// LastPRRefresh values from the persisted cache the same way.
	repoTTL := make(map[string]time.Duration, len(repos))
	for _, r := range repos {
		repoTTL[r] = h.repoTTLFor(r, cycleStart)
	}

	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	// Move dispatch into the gated goroutine so the deadline preempts BOTH
	// the per-repo wait AND the dispatch loop. Otherwise, with many repos
	// and slow gh calls, the for-loop sits on `sem <- struct{}{}` for the
	// entire batch and the deadline select below never runs — the splash
	// hangs past the intended 6s cutoff. Goroutines spawned before the
	// deadline keep running; their results land in the cache and the
	// steady-state worker carries them forward.
	dispatchAndWait := func() {
		for _, repo := range repos {
			wg.Add(1)
			sem <- struct{}{}
			go func(r string) {
				defer wg.Done()
				defer func() { <-sem }()
				if progress != nil {
					defer progress.Add(1)
				}
				defer func() {
					if pErr := recover(); pErr != nil {
						debuglog.Logger.Error("bootstrap: panic in repo goroutine", "repo", r, "panic", fmt.Sprintf("%v", pErr), "stack", string(debug.Stack()))
					}
				}()
				info := git.RefreshGitInfo(r)

				// Carry PR + last-refresh stamp forward from the cache, but
				// ONLY if the cached row's branch matches what RefreshGitInfo
				// just observed. A branch switch (interactive or external)
				// invalidates the cached PR for that checkout — better to
				// re-fetch than show a badge for the wrong branch.
				//
				// `LastPRRefresh` must be preserved even when `old.PR == nil`,
				// because a recent "no PR for this branch" result is still a
				// completed check that resets the TTL. Otherwise repos with
				// no open PR re-fetch on every 2s tick → blows the gh rate
				// limit the moment we fan out across all repos in parallel.
				// Carry-forward PR data is now a lock-free atomic load —
				// h.gitInfo() returns an immutable map snapshot.
				if old, ok := h.gitInfo()[r]; ok && old != nil && old.Branch == info.Branch {
					info.PR = old.PR
					info.LastPRRefresh = old.LastPRRefresh
					info.PRRateLimitedAt = old.PRRateLimitedAt
				}

				ttl := repoTTL[r]
				if ttl == 0 {
					ttl = prTTLHot
				}
				if h.ghAvailable && shouldRefreshPR(info, ttl) {
					prFetches.Add(1)
					git.RefreshPRInfo(info, r, workspace.IgnorePatterns(r))
					if !info.PRRateLimitedAt.IsZero() && time.Since(info.PRRateLimitedAt) < time.Second {
						rateLimitedHits.Add(1)
					}
				}

				// Publish the result by writing the cache directly. It is a
				// lock-free COW swap, safe from any goroutine, and it makes
				// the data live immediately for View and for the next cycle's
				// carry-forward read.
				//
				// This must NOT go through h.send. Bubble Tea's msgs channel
				// is UNBUFFERED (v2 tea.go: `msgs: make(chan Msg)`) and Send
				// is a bare rendezvous with no deadline — while tea.Exec has
				// the loop suspended for an attach, nothing drains it. Sending
				// here parked one goroutine per repo for the whole attach,
				// each holding a semaphore slot, wedging the fan-out until
				// detach. Compare inside the mutation so the before/after read
				// is atomic with the write.
				var structural bool
				h.writeGitInfo(func(next map[string]*git.RepoInfo) bool {
					structural = structuralGitChange(next[r], info)
					next[r] = info
					return true
				})

				// Persist to SQLite so the next launch can carry this forward
				// instead of re-firing gh. Storage method logs errors itself;
				// a failed write doesn't affect the in-memory cache.
				//
				// Ordered BEFORE the send deliberately: the send can still park
				// on a suspended loop (see below), and a fleet killed during a
				// long attach would otherwise lose these rows and re-fire gh for
				// every repo on the next launch.
				_ = h.storage.SavePRCacheRow(&session.PRCacheRow{
					RepoPath:        r,
					Branch:          info.Branch,
					OriginKey:       info.OriginKey,
					PR:              info.PR,
					LastPRRefresh:   info.LastPRRefresh,
					PRRateLimitedAt: info.PRRateLimitedAt,
				})

				// Update still owns the sidebar rebuild. The message carries no
				// data — the cache write above already published it — so if this
				// is dropped or delayed the cost is a late repaint, never stale
				// state. Skipped while attached: the sidebar isn't on screen, and
				// the detach path rebuilds it wholesale.
				//
				// This check-then-send is not atomic: an attach starting between
				// the Load and the Send parks this goroutine until detach. That
				// window is left open on purpose — closing it needs a different
				// transport than program.Send, and the cost is now bounded to
				// this decoupled worker (status detection is unaffected) with the
				// hint droppable by construction.
				if !h.isAttaching.Load() {
					h.send(gitRepaintMsg{structural: structural})
				}
			}(repo)
		}
		wg.Wait()
	}

	if deadline <= 0 {
		dispatchAndWait()
	} else {
		done := make(chan struct{})
		go func() {
			dispatchAndWait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(deadline):
		}
	}

	// One-line cycle summary at DEBUG. Off by default — set FLEET_DEBUG=1
	// to surface it. Helps diagnose "PR badges missing" without grepping
	// per-repo lines.
	rl := rateLimitedHits.Load()
	debuglog.Logger.Debug("git+PR refresh cycle",
		"repos", len(repos),
		"pr_fetches", prFetches.Load(),
		"rate_limited", rl,
		"duration_ms", time.Since(cycleStart).Milliseconds(),
	)
	if rl > 0 {
		h.maybeWarnRateLimited()
	}
}

// shouldRefreshPR decides whether the worker should call gh for this repo
// this cycle. Returns false when we're in the rate-limit back-off window —
// otherwise compares LastPRRefresh against the supplied `ttl` (which the
// caller picks based on per-repo hotness).
func shouldRefreshPR(info *git.RepoInfo, ttl time.Duration) bool {
	if !info.PRRateLimitedAt.IsZero() && time.Since(info.PRRateLimitedAt) < prRateLimitedFor {
		return false
	}
	if info.LastPRRefresh.IsZero() {
		return true
	}
	return time.Since(info.LastPRRefresh) > ttl
}

// maybeWarnRateLimited emits a single WARN line per cooldown window when
// any repo in the last cycle was rate-limited by gh. Without the throttle
// every 2s tick would re-warn until the hourly reset.
func (h *Home) maybeWarnRateLimited() {
	const cooldown = 5 * time.Minute
	// lastRateLimitWarn is only touched from background workers (status
	// cycle + bootstrap) that never run concurrently. No lock needed.
	last := h.lastRateLimitWarn
	now := time.Now()
	if !last.IsZero() && now.Sub(last) < cooldown {
		return
	}
	h.lastRateLimitWarn = now
	debuglog.Logger.Warn("gh rate-limited — PR badges paused; resets hourly. Run: gh api rate_limit --jq .resources.graphql")
}

// uniqueRepoPathsFromSessions returns distinct repo root paths from the given sessions.
func (h *Home) uniqueRepoPathsFromSessions(sessions []*session.Session) []string {
	seen := make(map[string]bool)
	var repos []string
	for _, s := range sessions {
		root := session.GetRepoRoot(s.ProjectPath)
		if !seen[root] {
			seen[root] = true
			repos = append(repos, root)
		}
	}
	return repos
}

func (h *Home) resolveCurrentRepo() string {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return ""
	}
	item := h.flatItems[h.cursor]
	if item.IsRepoHeader {
		return item.RepoPath
	}
	if item.Session != nil {
		return session.GetRepoRoot(item.Session.ProjectPath)
	}
	return ""
}

// resolveWorktreeBaseRepo resolves the repo a new worktree should be based on for
// the current cursor. It extends resolveCurrentRepo with origin-header support:
// an origin header carries no RepoPath (it stands for the whole group), so it
// resolves to a checkout of that origin group instead. fetchWorkspaceListForRepo
// then normalizes the result to the main worktree, so a worktree checkout still
// yields correct sibling naming.
func (h *Home) resolveWorktreeBaseRepo() string {
	if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		if item := h.flatItems[h.cursor]; item.IsOriginHeader {
			return h.originBaseRepo(item.OriginKey)
		}
	}
	return h.resolveCurrentRepo()
}

// originBaseRepo returns a checkout to base a new worktree on for an origin
// group, preferring the main clone over a linked worktree (both resolve to the
// same main worktree, but the main clone is the more stable pick). Returns "" if
// the origin has no known checkouts. Works even when the origin is collapsed —
// checkoutsForOrigin scans sessions/pinned/pending, not just visible rows.
func (h *Home) originBaseRepo(origin string) string {
	checkouts := h.checkoutsForOrigin(origin)
	if len(checkouts) == 0 {
		return ""
	}
	// checkoutsForOrigin gathers pinnedRepos via map iteration, so sort the fresh
	// slice for a stable pick when an origin has multiple non-worktree checkouts.
	sort.Strings(checkouts)
	_, isWorktreeOf := h.originResolvers()
	for _, repo := range checkouts {
		if !isWorktreeOf(repo) {
			return repo
		}
	}
	return checkouts[0]
}

// drawerScopeRepo returns the repo the terminal drawer is scoped to: frozen to
// drawerRepo while open (set when the drawer was opened), else the repo under
// the cursor (used by createShell before the drawer opens).
func (h *Home) drawerScopeRepo() string {
	if h.drawerMode != drawerHidden && h.drawerRepo != "" {
		return h.drawerRepo
	}
	return h.resolveCurrentRepo()
}

// shellsForActiveRepo returns the drawer's shells for its current scope repo,
// in creation order. The drawer only ever shows one repo's shells at a time.
func (h *Home) shellsForActiveRepo() []*shell.Shell {
	repo := h.drawerScopeRepo()
	if repo == "" {
		return nil
	}
	out := make([]*shell.Shell, 0, len(h.shells))
	for _, sh := range h.shells {
		if sh.RepoPath == repo {
			out = append(out, sh)
		}
	}
	return out
}

func (h *Home) fetchBranchList(repoPath string) tea.Cmd {
	return func() tea.Msg {
		branches, err := git.ListBranches(repoPath)
		isDirty := git.HasUncommittedChanges(repoPath)
		userEmail := git.GetUserEmail(repoPath)
		return branchListMsg{branches: branches, repoPath: repoPath, isDirty: isDirty, userEmail: userEmail, err: err}
	}
}

func (h *Home) fetchWorkspaceListForRepo(repoPath string) tea.Cmd {
	return func() tea.Msg {
		provider := workspace.ResolveProvider(repoPath)
		// For the built-in git provider, resolve to the main worktree: a new
		// worktree is a sibling of the main repo, so its name must derive from the
		// main repo — not from whichever linked worktree is currently selected,
		// else names snowball (issue #168). Custom shell providers own their own
		// naming and run relative to the selected repo, so leave them untouched.
		var originKey string
		if !provider.IsCustom() {
			repoPath = git.GetMainWorktreePath(repoPath)
			// The normalized main-clone path may not be a tracked gitInfoCache key
			// (origin with only a worktree tracked), which would misgroup the
			// Creating… phantom under a spurious local:<dir> header. Resolve the
			// origin here (worker goroutine, blocking git is fine) so the handler
			// can seed the cache.
			originKey = git.GetOriginKey(repoPath)
		}
		workspaces, err := provider.List(repoPath)
		defaultBranch := git.GetDefaultBranch(repoPath)
		return workspaceListMsg{workspaces: workspaces, provider: provider, repoPath: repoPath, defaultBranch: defaultBranch, originKey: originKey, err: err}
	}
}

// copyClaudeSettingsFile copies .claude/settings.local.json from srcRepo to dstRepo.
func copyClaudeSettingsFile(srcRepo, dstRepo string) {
	srcFile := filepath.Join(srcRepo, ".claude", "settings.local.json")
	data, err := os.ReadFile(srcFile)
	if err != nil {
		return // source doesn't exist, nothing to copy
	}
	dstDir := filepath.Join(dstRepo, ".claude")
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		debuglog.Logger.Error("copyClaudeSettings: failed to create .claude dir", "dst", dstDir, "err", err)
		return
	}
	if err := os.WriteFile(filepath.Join(dstDir, "settings.local.json"), data, 0600); err != nil {
		debuglog.Logger.Error("copyClaudeSettings: failed to write settings file", "dst", dstRepo, "err", err)
	}
}

func (h *Home) fetchPreview(s *session.Session) tea.Cmd {
	id := s.ID
	ts := s.GetTmuxSession()
	return func() tea.Msg {
		content, _ := ts.CapturePane()
		return previewMsg{sessionID: id, content: content}
	}
}

// fetchPreviewForSelected returns a tea.Cmd that fetches the preview for the
// currently selected session, or nil if no live session is selected.
func (h *Home) fetchPreviewForSelected() tea.Cmd {
	sel := h.selectedSession()
	if sel == nil || !sel.IsAlive() {
		return nil
	}
	return h.fetchPreview(sel)
}

// --- Rendering helpers ---

func (h *Home) renderHeader() string {
	logo := lipgloss.NewStyle().Foreground(ColorBrand).Bold(true).Render("❯_")
	title := logo + " " + TitleStyle.Render("fleet")

	breadcrumb := h.cursorBreadcrumb(nil)

	left := title
	if breadcrumb != "" {
		sep := lipgloss.NewStyle().Foreground(ColorTextDim).Render("  ›  ")
		left += sep + breadcrumb
	}

	// Status counts moved to the Sessions panel's top-right border.
	return HeaderBarStyle.Render(left)
}

// cursorBarContext maps the cursor's flatItem to a BarContext so the footer
// can show only the relevant subset of keys.
func (h *Home) cursorBarContext() BarContext {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return BarContextEmpty
	}
	item := h.flatItems[h.cursor]
	switch {
	case item.IsOriginHeader:
		return BarContextOrigin
	case item.IsCheckoutHeader:
		return BarContextCheckout
	case item.Session != nil:
		return BarContextSession
	default:
		return BarContextEmpty
	}
}

// cursorBreadcrumb returns "origin › checkout › session-title" for the row
// currently under the cursor. Empty string if there's no useful path (boot,
// empty fleet). Honours bg so it inlays into the header surface cleanly.
func (h *Home) cursorBreadcrumb(bg color.Color) string {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return ""
	}
	item := h.flatItems[h.cursor]
	if item.IsSpacer {
		return ""
	}
	segStyle := lipgloss.NewStyle().Foreground(ColorText).Background(bg)
	dimSeg := lipgloss.NewStyle().Foreground(ColorTextDim).Background(bg)
	sep := dimSeg.Render(" › ")

	var parts []string
	if item.OriginKey != "" {
		// Show the full origin (e.g. "brizzai/fleet") so the breadcrumb
		// names both the owner AND the repo, not just the repo. Local
		// repos use the bare folder basename after stripping "local:".
		origin := item.OriginKey
		if rest, ok := strings.CutPrefix(origin, "local:"); ok {
			origin = rest
		}
		parts = append(parts, segStyle.Render(origin))
	}

	if item.RepoPath != "" && !item.IsOriginHeader {
		branch := ""
		if info := h.gitInfo()[item.RepoPath]; info != nil {
			branch = info.Branch
		}
		if branch == "" {
			branch = filepath.Base(item.RepoPath)
		}
		if idx := strings.LastIndex(branch, "/"); idx != -1 {
			branch = branch[idx+1:]
		}
		parts = append(parts, segStyle.Render(branch))
	}

	if item.Session != nil {
		title := item.Session.Title
		if len(title) > 40 {
			title = title[:39] + "…"
		}
		parts = append(parts, segStyle.Bold(true).Render(title))
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, sep)
}

// statusCountsLine renders the global session-status pill embedded in the
// Sessions panel's top border. Uses the same glyph language as the per-row
// origin/checkout pills (renderStatusSummary) so the visual language stays
// consistent across the sidebar. Idle keeps its "N idle" text — knowing the
// total fleet size is useful info, but it doesn't deserve a glyph.
func (h *Home) statusCountsLine(bg color.Color) string {
	counts := make(map[session.Status]int)
	for _, s := range h.sessions {
		// Muted sessions are absent from the attention glyphs here for the
		// same reason they're absent from the per-header pills — otherwise the
		// global line contradicts the sidebar it sits above. Snooze gets no
		// counter of its own: it is a sidebar-only signal by design.
		//
		// Read from the map rebuildFlatItems stamped rather than calling
		// snoozeState again: the precedence rule lives in exactly one place, and
		// this runs on every View frame.
		if h.snoozeMuted[s.ID] {
			continue
		}
		counts[s.GetStatus()]++
	}
	summary := renderStatusSummaryOpts(counts, true)
	idleN := counts[session.StatusIdle]
	if summary == "" && idleN == 0 {
		return ""
	}
	if idleN == 0 {
		return summary
	}
	idleText := lipgloss.NewStyle().Foreground(ColorTextDim).Render(fmt.Sprintf("%d idle", idleN))
	if summary == "" {
		return idleText
	}
	sep := lipgloss.NewStyle().Foreground(ColorTextDim).Render(" · ")
	return summary + sep + idleText
}

func (h *Home) renderHelpBar() string {
	// When the terminal drawer owns the keyboard, the help bar shows its keys.
	if h.drawerHasFocus() {
		return h.renderDrawerHelpBar()
	}
	contextKeys, globalKeys := HelpBarBindingsFor(h.cursorBarContext(), h.cfg.GetEnterMode())

	var parts []string
	for _, kd := range contextKeys {
		parts = append(parts, HelpKeyStyle.Render(kd.Key)+" "+HelpDescStyle.Render(kd.Desc))
	}
	sep := HelpSepStyle.Render(" │ ")
	left := strings.Join(parts, "  ")

	var gparts []string
	for _, kd := range globalKeys {
		gparts = append(gparts, HelpKeyStyle.Render(kd.Key)+" "+HelpDescStyle.Render(kd.Desc))
	}
	right := strings.Join(gparts, "  ")

	// Leading \n: end the panel's last row and place the hotkey row on the
	// next line. Without it the keys would be appended to the bottom border
	// of the Sessions panel.
	return "\n " + left + sep + right
}

// renderDrawerHelpBar shows the drawer's Ctrl-chord key hints in the footer.
func (h *Home) renderDrawerHelpBar() string {
	pairs := [][2]string{
		{"keys", "→ shell"}, {"⌃T", "new"}, {"⌃W", "close"},
		{"⌃PgUp/Dn", "tab"}, {"⌃G", "full"}, {"`", "close drawer"},
	}
	var parts []string
	for _, p := range pairs {
		parts = append(parts, HelpKeyStyle.Render(p[0])+" "+HelpDescStyle.Render(p[1]))
	}
	return "\n " + strings.Join(parts, "  ")
}

func (h *Home) layoutMode() string {
	if h.width < layoutBreakpointSingle {
		return "single"
	}
	if h.width < layoutBreakpointDual {
		return "stacked"
	}
	return "dual"
}

// bindCurrentSessionToSlot persists the selected session under the given slot,
// replacing any prior binding for either the slot or the session. Re-binding
// the same session to its existing slot toggles the binding off (unbind).
func (h *Home) bindCurrentSessionToSlot(slot int) {
	s := h.selectedSession()
	if s == nil {
		h.setError(fmt.Errorf("select a session first"))
		return
	}
	if existing, ok := h.slotBindings[slot]; ok && existing == s.ID {
		h.unbindSlot(slot)
		return
	}
	if err := h.storage.BindSlot(slot, s.ID); err != nil {
		h.setError(fmt.Errorf("bind slot: %w", err))
		return
	}
	for k, v := range h.slotBindings {
		if v == s.ID {
			delete(h.slotBindings, k)
		}
	}
	h.slotBindings[slot] = s.ID
	h.actionLog.Add("bind slot", fmt.Sprintf("%d → %s", slot, s.Title), true)
	h.setInfo(fmt.Sprintf("Slot %d → %s", slot, s.Title))
	h.sidebarDirty = true
}

// unbindSlot clears the given slot's binding, if any.
func (h *Home) unbindSlot(slot int) {
	id, ok := h.slotBindings[slot]
	if !ok {
		h.setInfo(fmt.Sprintf("Slot %d already unbound", slot))
		return
	}
	title := id
	if s, ok := h.sessionByID[id]; ok {
		title = s.Title
	}
	if err := h.storage.UnbindSlot(slot); err != nil {
		h.setError(fmt.Errorf("unbind slot: %w", err))
		return
	}
	delete(h.slotBindings, slot)
	if h.lastSlotTapSlot == slot {
		h.lastSlotTapSlot = -1
	}
	h.actionLog.Add("unbind slot", fmt.Sprintf("%d (was %s)", slot, title), true)
	h.setInfo(fmt.Sprintf("Slot %d cleared", slot))
	h.sidebarDirty = true
}

// jumpToSlot moves the cursor to the session bound at the given slot.
// A second press of the same slot within 400ms also attaches.
func (h *Home) jumpToSlot(slot int) (tea.Model, tea.Cmd) {
	sessID, ok := h.slotBindings[slot]
	if !ok {
		h.setInfo(fmt.Sprintf("Slot %d unbound", slot))
		return h, nil
	}
	s, ok := h.sessionByID[sessID]
	if !ok {
		delete(h.slotBindings, slot)
		_ = h.storage.UnbindSlot(slot)
		h.setError(fmt.Errorf("slot %d was stale, cleared", slot))
		return h, nil
	}

	// Reveal the session: expand both its origin group and its checkout if
	// collapsed, so a bound session folded away under either header still
	// becomes visible and selectable. (Expanding only the checkout left a
	// collapsed origin hiding the row — it then read as "hidden by filter".)
	repo := session.GetRepoRoot(s.ProjectPath)
	h.revealCheckout(repo)
	h.rebuildFlatItems()

	idx := -1
	for i, item := range h.flatItems {
		if !item.IsRepoHeader && item.Session != nil && item.Session.ID == sessID {
			idx = i
			break
		}
	}
	if idx < 0 {
		// Likely hidden by an active filter.
		h.setInfo(fmt.Sprintf("Slot %d hidden by filter", slot))
		return h, nil
	}

	isDoubleTap := h.lastSlotTapSlot == slot &&
		time.Since(h.lastSlotTapAt) < 400*time.Millisecond
	h.cursor = idx
	h.syncViewport()
	if isDoubleTap {
		h.lastSlotTapSlot = -1
		h.actionLog.Add("attach via slot", fmt.Sprintf("%d", slot), true)
		// A suspended session has no live tmux — attachSelected() would no-op
		// silently. Wake it the same way Enter does.
		if s.GetStatus() == session.StatusSuspended {
			return h, h.resumeSelected(s)
		}
		return h, h.attachSelected()
	}
	h.lastSlotTapSlot = slot
	h.lastSlotTapAt = time.Now()
	return h, h.fetchPreviewForSelected()
}

func (h *Home) selectedSession() *session.Session {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) || h.flatItems[h.cursor].IsRepoHeader {
		return nil
	}
	return h.flatItems[h.cursor].Session
}

func (h *Home) selectedPreview() (*session.Session, string) {
	s := h.selectedSession()
	if s == nil {
		return nil, ""
	}
	content := h.previewCache[s.ID]
	return s, content
}

// repoInfoFromSnap returns repo info for the selected session using a snapshot
// of gitInfoCache. Safe to call from View() without holding workerMu.
func (h *Home) repoInfoFromSnap(snap map[string]*git.RepoInfo) *git.RepoInfo {
	s := h.selectedSession()
	if s == nil {
		return nil
	}
	return snap[session.GetRepoRoot(s.ProjectPath)]
}

// --- Internal helpers ---

// originResolvers builds the origin/worktree lookup closures from the
// immutable git/PR snapshot. h.gitInfo() is a lock-free atomic load — writers
// always publish a fresh map, so the entries can't shift underneath us. Shared
// by rebuildFlatItems and the jump tree so both group identically.
func (h *Home) originResolvers() (OriginOf, IsWorktreeOf) {
	snap := h.gitInfo()
	originSnap := make(map[string]string, len(snap))
	worktreeSnap := make(map[string]bool, len(snap))
	for k, v := range snap {
		if v == nil {
			continue
		}
		if v.OriginKey != "" {
			originSnap[k] = v.OriginKey
		}
		if v.IsWorktreeRepo {
			worktreeSnap[k] = true
		}
	}
	originOf := func(repoRoot string) string {
		if key, ok := originSnap[repoRoot]; ok {
			return key
		}
		return "local:" + filepath.Base(repoRoot)
	}
	isWorktreeOf := func(repoRoot string) bool {
		return worktreeSnap[repoRoot]
	}
	return originOf, isWorktreeOf
}

func (h *Home) rebuildFlatItems() {
	originOf, isWorktreeOf := h.originResolvers()
	now := time.Now()
	h.flatItems = BuildFlatItems(h.sessions, h.pendingWorkspaces, h.repoExpanded, h.filterText, h.pinnedRepos, h.failedWorktreeRemovals, h.groupSnooze, now, originOf, isWorktreeOf)

	// Resolve the attention-mute for EVERY session, not just the visible ones,
	// so callers that count the whole fleet (statusCountsLine) read the same
	// answer the sidebar rows do rather than re-deriving the precedence rule.
	// Deliberately not read off flatItems: that omits children of collapsed
	// groups, and the global pill is fleet-wide by design.
	//
	// Resolving here rather than at each call site also moves the work from
	// per-View-frame to per-rebuild — statusCountsLine runs on every frame,
	// including the ~60ms What's New shimmer tick, and originResolvers() takes
	// the gitInfo lock and builds three maps each time.
	muted := make(map[string]bool, len(h.sessions))
	for _, s := range h.sessions {
		repo := session.GetRepoRoot(s.ProjectPath)
		if snoozeState(s, originOf(repo), repo, h.groupSnooze, now).Muted {
			muted[s.ID] = true
		}
	}
	h.snoozeMuted = muted

	h.sidebarDirty = true
}

// structuralGitChange reports whether a per-repo git refresh changed a field
// that BuildFlatItems groups or sorts on (origin identity or the worktree
// flag). Branch, dirty, and PR changes are render-only — they don't reshape the
// sidebar tree — so they don't warrant a flat-item rebuild. A nil on either
// side (first sighting of a repo, or a missing result) is treated as structural
// so the initial grouping always lands.
func structuralGitChange(prev, next *git.RepoInfo) bool {
	if prev == nil || next == nil {
		return true
	}
	return prev.OriginKey != next.OriginKey || prev.IsWorktreeRepo != next.IsWorktreeRepo
}

// originOf maps a repo root to its stable origin key, falling back to
// "local:<basename>" for repos whose RepoInfo hasn't been refreshed yet.
// Lock-free read from the atomic gitInfo snapshot.
func (h *Home) originOf(repoRoot string) string {
	return originOfIn(h.gitInfo(), repoRoot)
}

// originOfIn resolves against a caller-supplied snapshot. Callers that make one
// decision out of several lookups must use this with a single h.gitInfo() load:
// the git+PR fan-out swaps the map from its own goroutine, so two loads can
// straddle a refresh and disagree.
func originOfIn(m map[string]*git.RepoInfo, repoRoot string) string {
	if info := m[repoRoot]; info != nil && info.OriginKey != "" {
		return info.OriginKey
	}
	return "local:" + filepath.Base(repoRoot)
}

// revealCheckout expands both header levels that contain a checkout — the origin
// group and the checkout itself — so a row inside them becomes visible in the
// flat tree. The two levels collapse independently (see IsExpanded /
// OriginExpandKey), so revealing a row means expanding both keys. Callers must
// rebuildFlatItems afterward.
func (h *Home) revealCheckout(repo string) {
	h.repoExpanded[OriginExpandKey(h.originOf(repo))] = true
	h.repoExpanded[repo] = true
}

func (h *Home) removePendingWorkspace(id string) {
	for i, pw := range h.pendingWorkspaces {
		if pw.ID == id {
			h.pendingWorkspaces = append(h.pendingWorkspaces[:i], h.pendingWorkspaces[i+1:]...)
			return
		}
	}
}

func (h *Home) rebuildSessionMap() {
	h.sessionByID = make(map[string]*session.Session, len(h.sessions))
	for _, s := range h.sessions {
		h.sessionByID[s.ID] = s
	}
}

// sidebarListHeight returns the height of the sidebar panel in the current
// layout — i.e. the value View() passes to RenderSidebar as `height`, before
// chrome rows (title + underline) and scroll indicators are subtracted.
// Stacked mode gives the sidebar ~55% of the content area; single/dual give
// it the full content area. Mirrors the arithmetic in View() (app.go:794+).
func (h *Home) sidebarListHeight() int {
	contentHeight := h.height - 1 - h.footerHeight()
	if contentHeight < 1 {
		contentHeight = 1
	}
	if h.layoutMode() == "stacked" {
		sh := (contentHeight * 55) / 100
		if sh < 3 {
			sh = 3
		}
		return sh
	}
	return contentHeight
}

// contextMenuAnchor returns where the context-menu dropdown should hang: its
// left edge, the screen row of the sidebar cursor, and the first row it must stay
// clear of (the top of the footer).
//
// The sidebar's first content row is always y=2: renderBody spends row 0 on the
// header and row 1 on the panel's top border, in every layout mode. RenderSidebar
// then spends one more row on a "… N more above" indicator whenever it's scrolled.
func (h *Home) contextMenuAnchor() (int, int, int) {
	const (
		sidebarContentTop = 2 // header row + panel top border
		indent            = 3 // nudge the box into the sidebar, off the border
	)
	above := 0
	if h.viewOffset > 0 {
		above = 1
	}
	rowY := sidebarContentTop + above + (h.cursor - h.viewOffset)
	return indent, rowY, h.height - h.footerHeight()
}

// footerHeight returns the number of rows reserved at the bottom of the screen
// below the panel area. The default help bar takes 1 row, but focus mode and
// the filter overlay both render a border-rule plus a content line — 2 rows.
// Sizing the panel area against the wrong footer height is what makes the
// body overflow `h.height` by one row in those modes.
func (h *Home) footerHeight() int {
	if h.focusMode || h.filterActive || h.filterText != "" {
		return focusFilterFooterRows
	}
	return helpBarHeight
}

// sidebarMinVisibleRows is the conservative lower bound on visible session rows
// in the sidebar — it assumes both scroll indicators are drawn, so the value
// holds even mid-scroll. Used by syncViewport to anchor the cursor before
// RenderSidebar (sidebar.go) decides which indicators to draw.
// Subtracts panel chrome (title + underline = 2) and reserves 2 rows for the
// indicators RenderSidebar may add at top/bottom.
func (h *Home) sidebarMinVisibleRows() int {
	v := h.sidebarListHeight() - 4
	if v < 1 {
		v = 1
	}
	return v
}

// sidebarPanelRows is the actual sidebar panel height in rows, before any
// scroll indicators are drawn. Used as the PgUp/PgDn page step so a single
// page-jump moves a full panel; syncViewport handles anchoring if the target
// would otherwise sit on an indicator row.
func (h *Home) sidebarPanelRows() int {
	v := h.sidebarListHeight() - 2
	if v < 1 {
		v = 1
	}
	return v
}

func (h *Home) syncViewport() {
	if len(h.flatItems) == 0 {
		return
	}
	// Ensure cursor is within bounds.
	if h.cursor < 0 {
		h.cursor = 0
	}
	if h.cursor >= len(h.flatItems) {
		h.cursor = len(h.flatItems) - 1
	}
	contentHeight := h.sidebarMinVisibleRows()
	prevOffset := h.viewOffset
	// Scroll to keep cursor visible.
	if h.cursor < h.viewOffset {
		h.viewOffset = h.cursor
	}
	if h.cursor >= h.viewOffset+contentHeight {
		h.viewOffset = h.cursor - contentHeight + 1
	}
	if h.viewOffset != prevOffset {
		h.renderStats.RecordViewportDrift()
	}
}

func (h *Home) loadSessions() tea.Msg {
	rows, err := h.storage.LoadSessions()
	if err != nil {
		debuglog.Logger.Error("failed to load sessions from database", "err", err)
		return loadSessionsMsg{err: err}
	}

	sessions := make([]*session.Session, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, session.FromRow(row))
	}

	// Demo mode: only show sessions under the specified path prefix.
	if prefix := os.Getenv("FLEET_DEMO_PREFIX"); prefix != "" {
		filtered := make([]*session.Session, 0, len(sessions))
		for _, s := range sessions {
			if strings.HasPrefix(s.ProjectPath, prefix) {
				filtered = append(filtered, s)
			}
		}
		sessions = filtered
	}

	slotBindings, err := h.storage.LoadSlotBindings()
	if err != nil {
		debuglog.Logger.Error("failed to load slot bindings", "err", err)
		slotBindings = map[int]string{}
	}

	// Load + reconnect shells (drawer terminals). No liveness check — the
	// worker derives status from tmux; a dead shell renders as exited.
	shellRows, err := h.storage.LoadShells()
	if err != nil {
		debuglog.Logger.Error("failed to load shells", "err", err)
	}
	shells := make([]*shell.Shell, 0, len(shellRows))
	for _, row := range shellRows {
		shells = append(shells, shell.FromRow(row))
	}

	// These block but run in the tea.Cmd goroutine, not Update().
	configDir := hooks.GetClaudeConfigDir()
	hooks.InjectClaudeHooks(configDir)
	// Install Codex hooks too, but only if Codex is present — never create
	// ~/.codex for users who don't have it.
	if _, err := exec.LookPath("codex"); err == nil {
		hooks.InjectCodexHooks(hooks.GetCodexConfigDir())
	}
	// Install the OpenCode status plugin, but only if OpenCode is present —
	// never create ~/.config/opencode for users who don't have it.
	if _, err := exec.LookPath("opencode"); err == nil {
		if _, err := hooks.InjectOpenCodePlugin(hooks.GetOpenCodeConfigDir()); err != nil {
			debuglog.Logger.Error("opencode plugin inject failed", "err", err)
		}
	}
	// Install Cursor CLI hooks too, but only if cursor-agent is present — never
	// create ~/.cursor for users who don't have it.
	var cursorHookErr error
	if _, err := exec.LookPath("cursor-agent"); err == nil {
		if _, err := hooks.InjectCursorHooks(hooks.GetCursorConfigDir()); err != nil {
			debuglog.Logger.Error("cursor hooks inject failed", "err", err)
			cursorHookErr = err
		}
	}
	// Route tmux copy-mode selections to the system clipboard (pbcopy on
	// macOS; wl-copy/xclip/xsel on Linux), so drag/click-to-copy works on
	// terminals that block OSC 52 (iTerm2 default) or don't support it (Apple
	// Terminal). Runs here for users with existing sessions; Start covers
	// fresh installs once a server exists.
	tmux.EnsureCopyCommand()
	chrome.InstallNativeMessagingHost()
	ghAvailable := github.IsGHAvailable()

	// Check for claude CLI availability.
	var warning string
	if _, err := exec.LookPath("claude"); err != nil {
		warning = "claude CLI not found — install Claude Code to create sessions"
	}
	// A failed Cursor hook install is otherwise invisible: Cursor rides the
	// pure-hook status path, so with no hooks.json every one of its sessions
	// resets to idle on each tick — never running, never waiting — with nothing
	// on screen explaining why. Surface it like the missing-claude warning.
	if cursorHookErr != nil && warning == "" {
		warning = "cursor hooks install failed — Cursor sessions will show as idle"
	}

	// Load persisted PR cache. A failure here is non-fatal — the bootstrap
	// will just re-fetch from gh as usual.
	prCache, err := h.storage.LoadPRCache()
	if err != nil {
		debuglog.Logger.Error("failed to load PR cache", "err", err)
		prCache = nil
	}

	return loadSessionsMsg{
		sessions:     sessions,
		shells:       shells,
		slotBindings: slotBindings,
		ghAvailable:  ghAvailable,
		warning:      warning,
		prCache:      prCache,
	}
}

func (h *Home) setError(err error) {
	h.err = err
	h.errTime = time.Now()
	if err != nil {
		h.errorHistory.Add(err.Error())
		h.toasts.Add(ToastError, err.Error())
		analytics.Track(analytics.EventErrorOccurred, map[string]interface{}{
			"category": strings.SplitN(err.Error(), ":", 2)[0],
		})
	}
}

// openBugReport shows the report dialog and, when a session is under the
// cursor, freezes its status evidence in the same breath.
//
// The capture fires at the keypress rather than at submit because status is a
// moving target: by the time someone has typed a description, the session may
// have self-corrected, and re-reading it then would capture a state nobody is
// complaining about. The Cmd keeps the blocking tmux I/O off the Update loop,
// the same way the `D` key's capture does.
//
// It captures in memory only (persist=false). This fires on every `!` press,
// before a report kind is even chosen, so persisting would litter
// ~/.config/fleet/snapshots/ with cancelled dialogs and feature requests — and
// would put a full-process goroutine dump on a hot key. The filed issue is
// unaffected: its body is built from the returned values, not read off disk.
func (h *Home) openBugReport() (tea.Model, tea.Cmd) {
	h.actionLog.Add("open bug report", "", true)
	s := h.selectedSession()
	h.bugReport.Show(h.version, len(h.sessions), h.errorHistory, h.actionLog,
		h.width, h.height, &h.renderStats, time.Since(h.startTime), s)
	analytics.Track(analytics.EventBugReportOpened, nil)

	if s == nil {
		return h, nil
	}
	hb := h.workerHeartbeat()
	return h, func() tea.Msg {
		return reportSnapshotMsg{snap: captureStatusSnapshot(s, s.ID, hb, false, false)}
	}
}

func (h *Home) setInfo(msg string) {
	h.infoMsg = msg
	h.infoTime = time.Now()
	h.toasts.Add(ToastInfo, msg)
}

// contextMenuTarget identifies the sidebar row a context menu was opened on.
//
// The menu's entries all resolve their subject from h.cursor at dispatch time, so
// the cursor must still be on that row when the pick lands. Keys can't move it
// (routeToModal feeds them to the menu), but *messages* can: handleSessionCreate
// and the workspace-create handler both auto-select the row they just made, and
// rebuildFlatItems runs on the tick. Since workspace creation is non-blocking by
// design, a session created while the menu is open would slide the cursor onto a
// different row — and `d` would then confirm deleting a session the menu never
// named. Capturing the row's identity lets dispatch re-find it (§ focusContextMenuTarget).
type contextMenuTarget struct {
	sessionID string // session rows
	repoPath  string // checkout headers
	originKey string // origin headers
}

// find returns the index of the target's row in items, or -1 if it's gone.
func (t contextMenuTarget) find(items []SidebarItem) int {
	for i, item := range items {
		switch {
		case t.sessionID != "":
			if item.Session != nil && item.Session.ID == t.sessionID {
				return i
			}
		case t.repoPath != "":
			if item.IsCheckoutHeader && item.RepoPath == t.repoPath {
				return i
			}
		case t.originKey != "":
			if item.IsOriginHeader && item.OriginKey == t.originKey {
				return i
			}
		}
	}
	return -1
}

// targetForCursor captures the identity of the row under the cursor.
func (h *Home) targetForCursor() contextMenuTarget {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return contextMenuTarget{}
	}
	item := h.flatItems[h.cursor]
	switch {
	case item.IsOriginHeader:
		return contextMenuTarget{originKey: item.OriginKey}
	case item.IsCheckoutHeader:
		return contextMenuTarget{repoPath: item.RepoPath}
	case item.Session != nil:
		return contextMenuTarget{sessionID: item.Session.ID}
	}
	return contextMenuTarget{}
}

// focusContextMenuTarget re-points the cursor at the row the menu was opened on,
// so a pick always acts on the row the menu's title named — even if an async
// rebuild moved the cursor while the menu was up. Reports false when the row is
// gone (deleted from under the menu), in which case the caller must not dispatch.
func (h *Home) focusContextMenuTarget() bool {
	idx := h.contextMenuTarget.find(h.flatItems)
	if idx < 0 {
		return false
	}
	if idx != h.cursor {
		h.cursor = idx
		h.syncViewport()
	}
	return true
}

// buildContextMenuItems returns the title and action rows for the row under the
// cursor. Every ID is a dispatchCommand id, so the menu adds no behavior of its
// own — it only decides what applies here and what's currently reachable.
//
// Disabled rows still ship: they carry a Note saying why, so the menu teaches the
// action exists (e.g. fork-to-worktree is Claude-only) instead of hiding it.
// Returns no items for BarContextEmpty, which makes `.` a no-op on spacers,
// pending rows, and an empty fleet.
func (h *Home) buildContextMenuItems() (string, []ContextMenuItem) {
	switch h.cursorBarContext() {
	case BarContextSession:
		return h.sessionContextMenu()
	case BarContextCheckout:
		return h.checkoutContextMenu()
	case BarContextOrigin:
		return h.originContextMenu()
	default:
		return "", nil
	}
}

// hasPRForCursor reports whether the cursor's repo has a PR URL cached, which is
// the same condition openPRInBrowser enforces.
func (h *Home) hasPRForCursor() bool {
	repo := h.resolveCurrentRepo()
	if repo == "" {
		return false
	}
	info := h.gitInfo()[repo]
	return info != nil && info.PR != nil && info.PR.URL != ""
}

func (h *Home) sessionContextMenu() (string, []ContextMenuItem) {
	s := h.selectedSession()
	if s == nil {
		return "", nil
	}
	status := s.GetStatus()
	resumable := s.GetClaudeSessionID() != ""

	// In split mode handleKey swaps Enter↔Tab, so the menu has to follow or it
	// would print the wrong key next to the action.
	enterKey, tabKey := "⏎", "⇥"
	if h.cfg.GetEnterMode() == "split" {
		enterKey, tabKey = "⇥", "⏎"
	}

	// A suspended session has no tmux to attach to — it has to be restarted with
	// --resume first, so the primary action changes shape.
	//
	// Its shortcut is a literal Enter, NOT enterKey: handleKey's suspended check
	// sits inside `case "enter"` *above* the split-mode branch (it "overrides split
	// mode"), so Enter resumes in both modes. Advertising ⇥ in split mode would
	// point at `attachSelected()`, which has no live tmux to attach to.
	open := ContextMenuItem{ID: "attach", Label: "Attach", Shortcut: enterKey, Enabled: true}
	if status == session.StatusSuspended {
		open = ContextMenuItem{ID: "resume", Label: "Resume Session", Shortcut: "⏎", Enabled: true}
	}

	// Every guard below is a conjunction, so the dim note has to switch on which
	// clause actually failed. A note that names the wrong reason is worse than
	// none — it contradicts the status the sidebar is showing right next to it.
	approve := ContextMenuItem{ID: "approve", Label: "Quick Approve", Shortcut: "Y", Key: "Y"}
	switch {
	case status != session.StatusWaiting:
		approve.Note = "not waiting"
	case !s.IsAlive():
		approve.Note = "session not running"
	default:
		approve.Enabled = true
	}

	unread := ContextMenuItem{ID: "mark_unread", Label: "Mark as Unread", Shortcut: "m", Key: "m"}
	switch {
	case status != session.StatusIdle:
		unread.Note = "idle only"
	case s.GetHookStatus() == "":
		unread.Note = "hasn't run yet"
	default:
		unread.Enabled = true
	}

	forkSession := ContextMenuItem{ID: "fork", Label: "Fork Session", Shortcut: "f", Key: "f"}
	switch {
	case !s.Agent.SupportsFork():
		forkSession.Note = s.Agent.DisplayName() + " has no fork"
	case !resumable:
		forkSession.Note = "no session id yet"
	default:
		forkSession.Enabled = true
	}

	forkWorktree := ContextMenuItem{ID: "fork_worktree", Label: "Fork to Worktree", Shortcut: "F", Key: "F"}
	switch {
	case s.Agent != agent.Claude:
		forkWorktree.Note = "Claude only"
	case !resumable:
		forkWorktree.Note = "no session id yet" // same reason the `fork` row gives
	default:
		forkWorktree.Enabled = true
	}

	suspend := ContextMenuItem{ID: "suspend_session", Label: "Suspend Session"}
	switch {
	case status == session.StatusSuspended:
		suspend.Note = "already suspended"
	case status != session.StatusIdle && status != session.StatusFinished:
		suspend.Note = "idle or finished only"
	case !resumable:
		suspend.Note = "no session id yet"
	default:
		suspend.Enabled = true
	}

	items := []ContextMenuItem{
		open,
		{ID: "focus", Label: "Focus Preview", Shortcut: tabKey, Key: "tab", Enabled: true},
		approve,
		{ID: "restart", Label: "Restart", Shortcut: "r", Key: "r", Enabled: true},
		{ID: "rename", Label: "Rename", Shortcut: "R", Key: "R", Enabled: true},
		unread,
		{ID: "editor", Label: "Open in Editor", Shortcut: "e", Key: "e", Enabled: true},
		{
			ID: "open_pr", Label: "Open PR", Shortcut: "p", Key: "p",
			Enabled: h.hasPRForCursor(),
			Note:    "no PR",
		},
		forkSession,
		forkWorktree,
		suspend,
		h.snoozeMenuItem(),
		{ID: "new_worktree", Label: "New Worktree Session", Shortcut: "w", Key: "w", Enabled: true},
		{ID: "delete_at_cursor", Label: "Delete Session", Shortcut: "d", Key: "d", Enabled: true},
	}
	return "session: " + s.Title, items
}

func (h *Home) checkoutContextMenu() (string, []ContextMenuItem) {
	item := h.flatItems[h.cursor]
	repo := item.RepoPath

	expand := "Expand"
	if item.Expanded {
		expand = "Collapse"
	}

	// The delete label has to name what actually happens, which is the same
	// three-way branch confirmDeleteHeader takes. The title names the same kind,
	// so the menu can't call a row a repo while offering to remove a worktree.
	kind, deleteLabel := "repo", "Forget Repo"
	switch {
	case h.repoIsWorktree(repo) || h.failedWorktreeRemovals[repo]:
		kind, deleteLabel = "worktree", "Remove Worktree"
	case h.countSessionsForRepo(repo) == 0:
		deleteLabel = "Unpin Repo"
	}

	items := []ContextMenuItem{
		{ID: "toggle_group", Label: expand, Shortcut: "⏎", Enabled: true},
		{ID: "new_session", Label: "New Session", Shortcut: "a", Key: "a", Enabled: true},
		{ID: "new_session_pick", Label: "New Session (Pick Agent)", Shortcut: "A", Key: "A", Enabled: true},
		{ID: "new_worktree", Label: "New Worktree Session", Shortcut: "w", Key: "w", Enabled: true},
		{ID: "branch", Label: "Switch Branch", Shortcut: "b", Key: "b", Enabled: true},
		{
			ID: "open_pr", Label: "Open PR", Shortcut: "p", Key: "p",
			Enabled: h.hasPRForCursor(),
			Note:    "no PR",
		},
		h.snoozeMenuItem(),
		{ID: "delete_at_cursor", Label: deleteLabel, Shortcut: "d", Key: "d", Enabled: true},
	}
	return kind + ": " + filepath.Base(repo), items
}

func (h *Home) originContextMenu() (string, []ContextMenuItem) {
	item := h.flatItems[h.cursor]

	expand := "Expand"
	if item.Expanded {
		expand = "Collapse"
	}

	items := []ContextMenuItem{
		{ID: "toggle_group", Label: expand, Shortcut: "⏎", Enabled: true},
		{ID: "new_worktree", Label: "New Worktree Session", Shortcut: "w", Key: "w", Enabled: true},
		h.snoozeMenuItem(),
		{ID: "delete_at_cursor", Label: "Forget Origin Group", Shortcut: "d", Key: "d", Enabled: true},
	}
	return "origin: " + item.OriginLabel, items
}

// buildPaletteItems returns all palette rows: built-in commands plus every
// repo/worktree currently in the sidebar (matched by name, branch, or path).
func (h *Home) buildPaletteItems() []PaletteItem {
	commands := []PaletteItem{
		{Kind: PaletteKindCommand, ID: "attach", Name: "Attach Session", Shortcut: "Enter"},
		{Kind: PaletteKindCommand, ID: "focus", Name: "Focus Preview", Shortcut: "Tab"},
		{Kind: PaletteKindCommand, ID: "jump_next", Name: "Jump to Next Waiting", Shortcut: "Space"},
		{Kind: PaletteKindCommand, ID: "new_session", Name: "New Session", Shortcut: "a"},
		{Kind: PaletteKindCommand, ID: "new_session_pick", Name: "New Session (Pick Agent)", Shortcut: "A"},
		{Kind: PaletteKindCommand, ID: "new_repo", Name: "New Session (Any Repo)", Shortcut: "n"},
		{Kind: PaletteKindCommand, ID: "new_worktree", Name: "New Worktree Session", Shortcut: "w"},
		{Kind: PaletteKindCommand, ID: "fork", Name: "Fork Session", Shortcut: "f"},
		{Kind: PaletteKindCommand, ID: "fork_worktree", Name: "Fork to Worktree", Shortcut: "F"},
		{Kind: PaletteKindCommand, ID: "delete", Name: "Delete (session / repo / worktree)", Shortcut: "d"},
		{Kind: PaletteKindCommand, ID: "restart", Name: "Restart Session", Shortcut: "r"},
		{Kind: PaletteKindCommand, ID: "rename", Name: "Rename Session", Shortcut: "R"},
		{Kind: PaletteKindCommand, ID: "editor", Name: "Open in Editor", Shortcut: "e"},
		{Kind: PaletteKindCommand, ID: "open_pr", Name: "Open PR", Shortcut: "p"},
		{Kind: PaletteKindCommand, ID: "approve", Name: "Quick Approve", Shortcut: "Y"},
		{Kind: PaletteKindCommand, ID: "branch", Name: "Switch Branch", Shortcut: "b"},
		{Kind: PaletteKindCommand, ID: "filter", Name: "Filter Sessions", Shortcut: "/"},
		{Kind: PaletteKindCommand, ID: "settings", Name: "Settings", Shortcut: "S"},
		{Kind: PaletteKindCommand, ID: "bug_report", Name: "Bug Report", Shortcut: "!"},
		{Kind: PaletteKindCommand, ID: "help", Name: "Help", Shortcut: "?"},
		{Kind: PaletteKindCommand, ID: "whats_new", Name: "What's New", Shortcut: "Shift+W"},
		{Kind: PaletteKindCommand, ID: "release_notes", Name: "Release Notes"},
		{Kind: PaletteKindCommand, ID: "reload_all", Name: "Reload All Sessions"},
		{Kind: PaletteKindCommand, ID: "suspend_session", Name: "Suspend This Session"},
		{Kind: PaletteKindCommand, ID: "suspend_now", Name: "Suspend Idle Sessions Now"},
		{Kind: PaletteKindCommand, ID: "mark_all_read", Name: "Mark All as Read"},
		{Kind: PaletteKindCommand, ID: "mark_unread", Name: "Mark as Unread", Shortcut: "m"},
		{Kind: PaletteKindCommand, ID: "snooze", Name: "Snooze (Session / Repo / Worktree)", Shortcut: "z"},
		{Kind: PaletteKindCommand, ID: "unsnooze", Name: "Wake Now (Clear Snooze)", Shortcut: "z"},
		{Kind: PaletteKindCommand, ID: "expand_all", Name: "Expand All Repos"},
		{Kind: PaletteKindCommand, ID: "collapse_all", Name: "Collapse All Repos"},
		{Kind: PaletteKindCommand, ID: "quit", Name: "Quit", Shortcut: "⌃C"},
	}
	// Surfaced only while a macOS-protected folder is blocking tmux, so it's the
	// fix the tcc-blocked tip points to without cluttering the palette otherwise.
	if h.anyTCCBlocked() {
		commands = append(commands, PaletteItem{Kind: PaletteKindCommand, ID: "open_fda", Name: "Open Full Disk Access Settings"})
	}
	for i := range commands {
		commands[i].Haystack = commands[i].Name
	}

	// Lock-free read of the immutable git/PR snapshot.
	gitSnap := h.gitInfo()

	// Build the place list from the unfiltered repo universe — the sidebar
	// filter shouldn't strip repos from the palette (it's a global navigator).
	repoSet := make(map[string]bool)
	for repo := range session.GroupByRepo(h.sessions) {
		repoSet[repo] = true
	}
	for repo := range h.pinnedRepos {
		repoSet[repo] = true
	}
	for _, pw := range h.pendingWorkspaces {
		if pw != nil && pw.RepoPath != "" {
			repoSet[pw.RepoPath] = true
		}
	}
	repos := make([]string, 0, len(repoSet))
	for repo := range repoSet {
		if repo == "" {
			continue
		}
		repos = append(repos, repo)
	}
	sort.Strings(repos)

	places := make([]PaletteItem, 0, len(repos))
	for _, repo := range repos {
		kind := PaletteKindRepo
		branch := ""
		if info := gitSnap[repo]; info != nil {
			if info.IsWorktreeRepo {
				kind = PaletteKindWorktree
			}
			branch = info.Branch
		}

		name := filepath.Base(repo)
		places = append(places, PaletteItem{
			Kind:     kind,
			ID:       repo,
			Name:     name,
			Detail:   branch,
			Haystack: name + " " + branch,
		})
	}

	return append(commands, places...)
}

// dispatchPaletteSelection routes a palette pick to either a command or a
// place (repo/worktree).
func (h *Home) dispatchPaletteSelection(msg commandPaletteMsg) (tea.Model, tea.Cmd) {
	h.pushRecentPaletteID(msg.id)
	switch msg.kind {
	case PaletteKindRepo, PaletteKindWorktree:
		h.actionLog.Add("palette jump", msg.id, true)
		return h.jumpToRepoHeader(msg.id)
	default:
		h.actionLog.Add("command: "+msg.id, "", true)
		return h.dispatchCommand(msg.id)
	}
}

// pushRecentPaletteID prepends id to the recent list, dedupes, and trims to a cap.
func (h *Home) pushRecentPaletteID(id string) {
	const cap = 8
	out := make([]string, 0, cap)
	out = append(out, id)
	for _, prev := range h.recentPaletteIDs {
		if prev == id {
			continue
		}
		out = append(out, prev)
		if len(out) == cap {
			break
		}
	}
	h.recentPaletteIDs = out
}

// jumpToRepoHeader moves the cursor to the given repo header in the sidebar,
// expanding the group if collapsed. Mirrors jumpToSlot's pattern.
func (h *Home) jumpToRepoHeader(repoPath string) (tea.Model, tea.Cmd) {
	// Expand both header levels (origin group + checkout) so the header is visible
	// even when its origin group is collapsed.
	h.revealCheckout(repoPath)
	h.rebuildFlatItems()
	idx := -1
	for i, item := range h.flatItems {
		if item.IsRepoHeader && item.RepoPath == repoPath {
			idx = i
			break
		}
	}
	if idx < 0 {
		h.setInfo("Repo hidden by filter")
		return h, nil
	}
	h.cursor = idx
	h.syncViewport()
	return h, h.fetchPreviewForSelected()
}

// dispatchCommand executes a command selected from the palette.
func (h *Home) dispatchCommand(id string) (tea.Model, tea.Cmd) {
	switch id {
	case "attach":
		if s := h.selectedSession(); s != nil {
			h.actionLog.Add("attach session", s.Title, true)
			analytics.Track(analytics.EventSessionAttached, map[string]interface{}{"agent": string(s.Agent)})
			if analytics.MarkOnboardingMilestone(analytics.MilestoneFirstAttach) {
				analytics.Track(analytics.EventOnboardingFirstAttach, map[string]interface{}{
					"seconds_since_install": int(analytics.SecondsSinceInstall()),
				})
			}
		}
		return h, h.attachSelected()
	case "focus":
		return h, h.enterFocusMode()
	case "snooze":
		sc, ok := h.snoozeScopeAtCursor()
		if !ok {
			return h, nil
		}
		// Remember which row the picker speaks for, and re-anchor from the
		// cursor: reached via `z` or the palette no menu was ever opened, so
		// the stored target and anchor would be stale.
		h.contextMenuTarget = h.targetForCursor()
		h.snoozeDialog.SetAnchor(h.contextMenuAnchor())
		h.snoozeDialog.Show("Snooze " + sc.label)
		return h, nil
	case "unsnooze":
		sc, ok := h.snoozeScopeAtCursor()
		if !ok || !h.snoozed(sc) {
			return h, nil
		}
		h.clearSnooze(sc)
		return h, nil
	case "jump_next":
		h.jumpToNextAttentionSession()
		analytics.Track(analytics.EventSpaceJump, nil)
		return h, h.fetchPreviewForSelected()
	case "new_session":
		repoPath := h.resolveCurrentRepo()
		if repoPath == "" {
			h.newDialog.Show()
			return h, nil
		}
		h.actionLog.Add("create session", repoPath, true)
		return h.handleSessionCreate(sessionCreateMsg{
			path:  repoPath,
			title: filepath.Base(repoPath),
		})
	case "new_session_pick":
		repoPath := h.resolveCurrentRepo()
		if repoPath == "" {
			h.newDialog.Show()
			return h, nil
		}
		h.sessionCreateDialog.Show(repoPath, filepath.Base(repoPath), agent.Parse(h.cfg.GetDefaultAgent()))
		return h, nil
	case "new_repo":
		h.newDialog.Show()
		return h, nil
	case "new_worktree":
		repoPath := h.resolveWorktreeBaseRepo()
		if repoPath == "" {
			h.setError(fmt.Errorf("no repo selected"))
			return h, nil
		}
		h.worktreeDialog.ShowLoading()
		return h, tea.Batch(h.fetchWorkspaceListForRepo(repoPath), spinnerTickCmd)
	case "fork":
		return h, h.forkSelected()
	case "fork_worktree":
		return h, h.forkToWorktreeSelected()
	case "delete", "delete_at_cursor":
		// One delete, scope follows the cursor — the same code the `d` key runs.
		// Keeping a second, header-blind delete here would silently miss any future
		// guard added to deleteAtCursor (and no-op outright on a header).
		return h, h.deleteAtCursor()
	case "toggle_group":
		h.toggleRepoGroup()
		return h, nil
	case "resume":
		// A suspended session's tmux is gone, so "attach" would fail — it has to be
		// restarted with --resume first.
		s := h.selectedSession()
		if s == nil {
			return h, nil
		}
		return h, h.resumeSelected(s)
	case "restart":
		return h, h.confirmRestartSelected()
	case "suspend_session":
		return h, h.suspendSelected()
	case "suspend_now":
		return h, h.suspendIdleNow()
	case "rename":
		return h, h.renameSelected()
	case "editor":
		if s := h.selectedSession(); s != nil {
			h.actionLog.Add("open editor", fmt.Sprintf("%q at %s", h.cfg.GetEditor(), s.ProjectPath), true)
			analytics.Track(analytics.EventEditorOpened, map[string]interface{}{"editor": h.cfg.GetEditor()})
		}
		return h, h.openEditorSelected()
	case "open_pr":
		h.actionLog.Add("open PR", "", true)
		analytics.Track(analytics.EventPROpened, nil)
		return h, h.openPRInBrowser()
	case "approve":
		if s := h.selectedSession(); s != nil {
			h.actionLog.Add("quick approve", s.Title, true)
			analytics.Track(analytics.EventQuickApprove, nil)
		}
		return h, h.quickApproveSelected()
	case "branch":
		repoPath := h.resolveCurrentRepo()
		if repoPath == "" {
			return h, nil
		}
		h.branchDialog.ShowLoading()
		return h, tea.Batch(h.fetchBranchList(repoPath), spinnerTickCmd)
	case "filter":
		h.filterActive = true
		h.filterInput.Focus()
		analytics.Track(analytics.EventFilterUsed, nil)
		return h, nil
	case "settings":
		h.settingsDialog.Show()
		analytics.Track(analytics.EventSettingsOpened, nil)
		return h, nil
	case "bug_report":
		return h.openBugReport()
	case "help":
		h.helpOverlay.Show()
		return h, nil
	case "whats_new":
		return h.openReleaseNotes(true)
	case "release_notes":
		return h.openReleaseNotes(false)
	case "reload_all":
		analytics.Track(analytics.EventReloadAll, nil)
		return h, h.reloadAll()
	case "open_fda":
		h.actionLog.Add("open full disk access", "", true)
		return h, openFullDiskAccessSettings()
	case "mark_all_read":
		analytics.Track(analytics.EventMarkAllRead, nil)
		h.markAllAsRead()
		return h, nil
	case "mark_unread":
		h.markUnreadSelected()
		return h, nil
	case "expand_all":
		for _, key := range h.allExpandKeys() {
			h.setExpanded(key, true)
		}
		h.rebuildFlatItems()
		h.syncViewport()
		return h, nil
	case "collapse_all":
		// Snapshot the repo under the cursor so we can land on its
		// header after the rebuild hides everything else.
		var snapRepo string
		if h.cursor >= 0 && h.cursor < len(h.flatItems) {
			snapRepo = h.flatItems[h.cursor].RepoPath
		}
		// Force-write every origin + checkout key to false. We can't
		// rely on iterating h.repoExpanded — IsExpanded defaults missing
		// keys to true, so untouched groups would stay open.
		for _, key := range h.allExpandKeys() {
			h.setExpanded(key, false)
		}
		h.rebuildFlatItems()
		h.cursor = 0
		for i, item := range h.flatItems {
			if item.IsRepoHeader && item.RepoPath == snapRepo {
				h.cursor = i
				break
			}
		}
		h.syncViewport()
		return h, nil
	case "quit":
		return h, h.beginQuit("command-palette")
	}
	return h, nil
}

// releaseNotesLoadedMsg carries the async result of loading the changelog.
type releaseNotesLoadedMsg struct {
	releases []releasenotes.Release
	err      error
}

// loadReleaseNotes fetches (or reads the cached) GitHub releases off the Update
// goroutine and delivers them via releaseNotesLoadedMsg.
func (h *Home) loadReleaseNotes() tea.Cmd {
	return func() tea.Msg {
		rs, err := releasenotes.Load()
		return releaseNotesLoadedMsg{releases: rs, err: err}
	}
}

// openReleaseNotes opens the changelog dialog (full or the What's New reel) and
// marks everything through the newest release as seen so the badge clears. Wired
// to both the `W` key and the palette commands.
func (h *Home) openReleaseNotes(whatsNew bool) (tea.Model, tea.Cmd) {
	h.actionLog.Add("open release notes", "", true)
	if whatsNew {
		h.releaseNotes.ShowWhatsNew(h.version)
	} else {
		h.releaseNotes.Show(h.version)
	}
	if v := h.newestKnownVersion(); v != "" {
		h.cfg.MarkReleaseNotesSeen(v)
	}
	h.hasUnseenWhatsNew = false
	h.whatsNewShimmering = false
	return h, h.loadReleaseNotes()
}

// newestKnownVersion returns the version of the newest cached release ("" if
// none loaded). cachedReleases is sorted newest-first.
func (h *Home) newestKnownVersion() string {
	if len(h.cachedReleases) > 0 {
		return h.cachedReleases[0].Version
	}
	return ""
}

// recomputeWhatsNew refreshes hasUnseenWhatsNew from the cached releases and the
// stored seen version, using isUnseenHighlight — the seen-aware, badge-only
// predicate. The reel filters by isRecentHighlight (window only) instead, so the
// badge clears once seen while the reel stays re-viewable.
func (h *Home) recomputeWhatsNew() {
	seen := releasenotes.NormalizeVersion(h.cfg.GetReleaseNotesSeenVersion())
	h.hasUnseenWhatsNew = false
	for _, r := range h.cachedReleases {
		if isUnseenHighlight(r, seen) {
			h.hasUnseenWhatsNew = true
			return
		}
	}
}

// ensureWhatsNewShimmer starts the badge shimmer loop if the badge is visible
// and the loop isn't already running; returns nil otherwise. Safe to call every
// tick — the whatsNewShimmering latch keeps only one loop alive.
func (h *Home) ensureWhatsNewShimmer() tea.Cmd {
	if h.hasUnseenWhatsNew && !h.whatsNewShimmering {
		h.whatsNewShimmering = true
		return whatsNewTickCmd()
	}
	return nil
}

// reloadAll restarts all dead/error sessions concurrently.
func (h *Home) reloadAll() tea.Cmd {
	type target struct {
		session *session.Session
		title   string
	}
	var targets []target
	for _, s := range h.sessions {
		status := s.GetStatus()
		// Skip active/healthy sessions — never kill running Claude work or idle
		// sessions. Suspended sessions are intentionally parked: reviving them
		// here would defeat the memory relief, so leave them for lazy resume.
		if status == session.StatusRunning || status == session.StatusWaiting ||
			status == session.StatusStarting || status == session.StatusFinished ||
			status == session.StatusIdle || status == session.StatusSuspended {
			continue
		}
		targets = append(targets, target{session: s, title: s.Title})
	}

	if len(targets) == 0 {
		h.setInfo("All sessions healthy — nothing to reload")
		return nil
	}

	skipped := len(h.sessions) - len(targets)
	debuglog.Logger.Info("reload all", "eligible", len(targets), "skipped", skipped)

	return func() tea.Msg {
		var (
			mu     sync.Mutex
			errors []string
			wg     sync.WaitGroup
		)

		for _, t := range targets {
			wg.Add(1)
			go func(s *session.Session, title string) {
				defer wg.Done()
				var err error
				if s.IsAlive() && !s.GetTmuxSession().IsPaneDead() {
					err = s.RespawnClaude()
				} else {
					err = s.Restart()
				}
				if err != nil {
					mu.Lock()
					errors = append(errors, title)
					mu.Unlock()
					debuglog.Logger.Error("reload all: restart failed", "title", title, "err", err)
				}
			}(t.session, t.title)
		}

		wg.Wait()

		return reloadAllResultMsg{
			restarted: len(targets) - len(errors),
			skipped:   skipped,
			errors:    errors,
		}
	}
}

// markAllAsRead acknowledges all finished sessions, transitioning them to idle.
func (h *Home) markAllAsRead() {
	var count int
	for _, s := range h.sessions {
		if s.GetStatus() != session.StatusFinished {
			continue
		}
		s.Acknowledge()
		if err := h.storage.SetAcknowledged(s.ID, true); err != nil {
			debuglog.Logger.Error("storage: SetAcknowledged", "id", s.ID, "err", err)
		}
		count++
	}
	if count == 0 {
		h.setInfo("No finished sessions")
		return
	}
	h.rebuildFlatItems()
	h.setInfo(fmt.Sprintf("Marked %d sessions as read", count))
}

// markUnreadSelected flags the selected idle session as unread (Idle → Finished),
// the single-session inverse of markAllAsRead.
func (h *Home) markUnreadSelected() {
	s := h.selectedSession()
	if s == nil {
		return
	}
	if s.GetStatus() != session.StatusIdle {
		h.setInfo("Only idle sessions can be marked unread")
		return
	}
	// A session that never fired a hook (e.g. a freshly created Codex/OpenCode
	// session sitting at its prompt) would be flipped straight back to idle by the
	// worker's no-hook path, so the mark wouldn't stick. Only sessions with hook
	// state settle to Finished from Acknowledged=false.
	if s.GetHookStatus() == "" {
		h.setInfo("Session hasn't run yet — nothing to mark unread")
		return
	}
	analytics.Track(analytics.EventMarkUnread, nil)
	s.MarkUnread()
	if err := h.storage.UpdateStatus(s.ID, string(session.StatusFinished)); err != nil {
		debuglog.Logger.Error("storage: UpdateStatus", "id", s.ID, "err", err)
	}
	if err := h.storage.SetAcknowledged(s.ID, false); err != nil {
		debuglog.Logger.Error("storage: SetAcknowledged", "id", s.ID, "err", err)
	}
	h.rebuildFlatItems()
	h.setInfo("Marked as unread")
}

// ensureExactHeight pads or truncates content to exactly n lines.
func ensureExactHeight(content string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// ensureExactWidth pads or truncates each line to exactly the given visual width.
func ensureExactWidth(content string, width int) string {
	if width <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	result := make([]string, len(lines))
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w == width {
			result[i] = line
		} else if w < width {
			result[i] = line + strings.Repeat(" ", width-w)
		} else {
			truncated := lipgloss.NewStyle().MaxWidth(width).Render(line)
			tw := lipgloss.Width(truncated)
			if tw < width {
				truncated += strings.Repeat(" ", width-tw)
			}
			result[i] = truncated
		}
	}
	return strings.Join(result, "\n")
}
