package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/chrome"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/hooks"
	"github.com/brizzai/fleet/internal/naming"
	"github.com/brizzai/fleet/internal/perfwatch"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/tmux"
	"github.com/brizzai/fleet/internal/workspace"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	overlay "github.com/rmhubbert/bubbletea-overlay"
)

const (
	tickInterval           = 2 * time.Second
	previewTickInterval    = 500 * time.Millisecond
	previewCacheTTL        = 500 * time.Millisecond
	layoutBreakpointSingle = 50
	layoutBreakpointDual   = 80
	helpBarHeight          = 1 // single row of contextual hotkeys, no top rule
	statusRoundRobin       = 5 // sessions per tick
	undoDeleteTimeout      = 5 * time.Second
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
	pendingDeleteExpireMsg struct {
		nonce string
	}
	sessionRestartMsg struct {
		id  string
		err error
	}
	sessionCreateResultMsg struct {
		session *session.Session
		err     error
	}
	previewMsg struct {
		sessionID string
		content   string
	}
	loadSessionsMsg struct {
		sessions     []*session.Session
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
	// splashFrameMsg advances the boot-splash spinner. Self-rescheduled
	// while !booted; not emitted after bootstrapDoneMsg.
	splashFrameMsg time.Time
)

func spinnerTickCmd() tea.Msg {
	time.Sleep(100 * time.Millisecond)
	return spinnerTickMsg{}
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
	consentDialog         *ConsentDialog

	pendingWorkspaces []*PendingWorkspace // in-flight workspace creations
	pendingForkCtx    *forkContext        // set while Shift+F worktree picker is open; consumed on pick/cancel
	pendingDeletes    []PendingDelete     // undo stack for deferred deletions
	// finalizingDeletes holds entries whose undo window has expired but whose
	// background cleanup (tmux kill, hook removal, workspace destroy) is still
	// running. Quit drains both this list and pendingDeletes so an in-flight
	// kill isn't lost when fleet exits mid-cleanup.
	finalizingDeletes []PendingDelete
	pinnedRepos       map[string]bool // pinned repo paths (persist in SQLite)

	repoExpanded     map[string]bool // checkout-path / "origin:<key>" -> expanded state (default expanded when missing)
	idleFolded       map[string]bool // checkout path -> idle sessions folded ("z" key)
	previewCache     map[string]string
	previewCacheTime map[string]time.Time
	statusRRIndex    int // round-robin index for status updates

	gitInfoCache  map[string]*git.RepoInfo // repo root path -> git info
	repoLastHotAt map[string]time.Time     // repo root -> last time a session in it was Running (guarded by workerMu)
	ghAvailable   bool                     // cached gh CLI availability

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

	// Slot hotkeys (RTS-style quick access: digit=jump, double-digit=attach, alt+digit or =<digit>=bind).
	slotBindings      map[int]string // slot (0-9) -> session ID
	lastSlotTapSlot   int            // -1 when no pending tap
	lastSlotTapAt     time.Time
	slotAssignMode    int // 0=off, 1=bind pending (=<digit>), 2=unbind pending (==<digit>)
	slotAssignExpires time.Time

	// Floating toast overlay (bottom-right).
	toasts *ToastStack

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
	priorityStatusUpdates chan string   // buffered, session IDs with fresh hook changes — drained before round-robin
	workerMu              sync.Mutex    // protects sessions/gitInfoCache from concurrent worker access
	ctx                   context.Context
	cancel                context.CancelFunc
	workerStarted         bool

	startTime time.Time // app start time for uptime tracking

	// Throttles the "gh rate-limited" WARN log so it doesn't fire every
	// 2s tick. Reset to time.Time{} when a refresh comes back clean.
	lastRateLimitWarn time.Time

	// Boot splash. `booted` is false until the bootstrap fan-out resolves
	// every visible repo's OriginKey (or the 4s deadline expires); while
	// false, View() returns the splash and the steady-state worker has
	// not started.
	booted             bool
	splashFrame        int
	bootstrapRepos     int          // total repos the bootstrap is waiting on (for progress UI)
	bootstrapResolved  atomic.Int32 // goroutines that have finished within the current bootstrap fan-out

	// Rendering diagnostics (accumulated counters for bug reports).
	renderStats RenderStats
}

// NewHome creates the main TUI model.
func NewHome(storage *session.StateDB, cfg *config.Config, version string, identity analytics.Identity) *Home {
	ctx, cancel := context.WithCancel(context.Background())

	fi := textinput.New()
	fi.Placeholder = "filter..."
	fi.CharLimit = 64
	fi.Width = 20

	// Apply theme — PaletteByName falls back to the flagship default when
	// cfg.Theme is empty or unknown.
	ApplyPalette(PaletteByName(cfg.Theme))
	StatusIndicatorMode = cfg.GetStatusIndicator()

	return &Home{
		storage:               storage,
		sessionByID:           make(map[string]*session.Session),
		repoExpanded:          make(map[string]bool),
		idleFolded:            make(map[string]bool),
		slotBindings:          make(map[int]string),
		lastSlotTapSlot:       -1,
		toasts:                NewToastStack(),
		pinnedRepos:           make(map[string]bool),
		newDialog:             NewNewSessionDialog(),
		confirmDialog:         NewConfirmDialog(),
		renameDialog:          NewRenameDialog(),
		helpOverlay:           NewHelpOverlay(),
		settingsDialog:        NewSettingsDialog(cfg),
		worktreeDialog:        NewWorktreeDialog(),
		createWorkspaceDialog: NewCreateWorkspaceDialog(),
		branchDialog:          NewBranchCheckoutDialog(),
		commandPalette:        NewCommandPaletteDialog(),
		consentDialog:         NewConsentDialog(),
		bugReport:             NewBugReportDialog(),
		previewCache:          make(map[string]string),
		previewCacheTime:      make(map[string]time.Time),
		gitInfoCache:          make(map[string]*git.RepoInfo),
		repoLastHotAt:         make(map[string]time.Time),
		filterInput:           fi,
		cfg:                   cfg,
		version:               version,
		identity:              identity,
		errorHistory:          NewErrorHistory(50),
		actionLog:             NewActionLog(100),
		statusTrigger:         make(chan struct{}, 1),
		priorityStatusUpdates: make(chan string, 256),
		ctx:                   ctx,
		cancel:                cancel,
		startTime:             time.Now(),
	}
}

// Init implements tea.Model.
func (h *Home) Init() tea.Cmd {
	return tea.Batch(
		h.loadSessions,
		h.tick(),
		h.previewTick(),
	)
}

// Update implements tea.Model.
func (h *Home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if perfwatch.Enabled() {
		tok := perfwatch.MarkUpdateStart(fmt.Sprintf("%T", msg))
		defer perfwatch.MarkUpdateEnd(tok)
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
		h.consentDialog.SetSize(msg.Width, msg.Height)
		h.bugReport.SetSize(msg.Width, msg.Height)
		h.syncViewport()
		return h, nil

	case tea.KeyMsg:
		return h.handleKey(msg)

	case tickMsg:
		return h.handleTick()

	case hookChangedMsg:
		// HookWatcher detected a status file change. Do immediate hook-only sync,
		// then hand sessions whose hook changed to the worker via the priority queue
		// so they get a full UpdateStatus() within ~100ms instead of waiting for round-robin.
		h.workerMu.Lock()
		changed := h.syncHookStatuses(h.sessions)
		h.rebuildFlatItems()
		h.workerMu.Unlock()
		h.enqueuePriorityUpdates(changed)
		return h, h.listenForHookChanges

	case statusUpdateMsg:
		// Returned after detaching from session.
		h.isAttaching.Store(false)
		// Immediate hook sync (data already in HookWatcher from hooks that fired during attach).
		h.workerMu.Lock()
		changed := h.syncHookStatuses(h.sessions)
		h.rebuildFlatItems()
		h.workerMu.Unlock()
		h.enqueuePriorityUpdates(changed)
		// Also trigger full background refresh for pane captures, git, etc.
		select {
		case h.statusTrigger <- struct{}{}:
		default:
		}
		return h, nil

	case sessionCreateMsg:
		return h.handleSessionCreate(msg)

	case forkSessionMsg:
		s := session.NewSession(msg.title, msg.path)
		s.WorkspaceName = msg.workspaceName
		s.ForkFromID = msg.parentClaudeSessionID
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

	case commandPaletteMsg:
		return h.dispatchPaletteSelection(msg)

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
		// flipped) Telemetry toggle — otherwise the change only takes
		// effect on next launch.
		analytics.SyncEnabled(h.cfg.IsTelemetryEnabled(), h.version, h.identity)
		return h, nil

	case consentResultMsg:
		// Persist the answer so we don't ask again, then run startup
		// analytics if (and only if) the user accepted.
		enabled := msg.accepted
		h.cfg.Telemetry = &enabled
		h.cfg.AnalyticsConsentSeen = true
		if err := h.cfg.Save(); err != nil {
			debuglog.Logger.Error("config: save after consent", "err", err)
		}
		if enabled {
			// Worker is already running by this point — guard the read.
			h.workerMu.Lock()
			repoCount := len(session.GroupByRepo(h.sessions))
			h.workerMu.Unlock()
			h.fireStartupAnalytics(repoCount)
		}
		return h, nil

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
		// Refresh git info for the repo.
		h.workerMu.Lock()
		h.gitInfoCache[msg.repoPath] = git.RefreshGitInfo(msg.repoPath)
		h.workerMu.Unlock()
		h.rebuildFlatItems()
		// Trigger PR refresh for new branch.
		select {
		case h.statusTrigger <- struct{}{}:
		default:
		}
		return h, nil

	case statusSnapshotMsg:
		if msg.err != nil {
			h.setError(fmt.Errorf("snapshot: %w", msg.err))
		} else {
			h.setInfo("Snapshot saved: " + msg.path)
		}
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

		// Expand the repo group and rebuild sidebar.
		h.repoExpanded[msg.repoPath] = true
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
			h.clearPendingFork()
			h.rebuildFlatItems()
			// Clamp cursor if it was on the removed phantom.
			if h.cursor >= len(h.flatItems) && len(h.flatItems) > 0 {
				h.cursor = len(h.flatItems) - 1
			}
			return h, nil
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
		if msg.workspaceErr != nil {
			h.setError(fmt.Errorf("workspace destroy: %w", msg.workspaceErr))
		}
		return h, nil

	case previewTickMsg:
		// Fast preview-only tick — skips status/git work, just refreshes the preview pane.
		if h.focusMode {
			return h, h.previewTick() // focus mode has its own faster tick
		}
		var previewCmd tea.Cmd
		if sel := h.selectedSession(); sel != nil && sel.IsAlive() {
			previewCmd = h.fetchPreview(sel)
		}
		if previewCmd != nil {
			return h, tea.Batch(previewCmd, h.previewTick())
		}
		return h, h.previewTick()

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

	case bootstrapDoneMsg:
		h.booted = true
		h.workerMu.Lock()
		h.rebuildFlatItems()
		// Land the cursor on the first actionable row (a session) instead
		// of the first origin header — first keystroke does something
		// useful immediately.
		if idx := FirstSessionItem(h.flatItems); idx >= 0 {
			h.cursor = idx
			h.syncViewport()
		}
		h.workerMu.Unlock()
		go h.statusWorker()
		return h, nil

	case splashFrameMsg:
		if h.booted {
			return h, nil
		}
		h.splashFrame++
		return h, h.splashTick()

	case loadSessionsMsg:
		if msg.err != nil {
			h.setError(msg.err)
			return h, nil
		}
		if msg.warning != "" {
			h.setError(fmt.Errorf("%s", msg.warning))
		}
		h.sessions = msg.sessions
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
			for repo, row := range msg.prCache {
				if now.Sub(row.LastPRRefresh) > cutoff {
					continue
				}
				h.gitInfoCache[repo] = &git.RepoInfo{
					Branch:          row.Branch,
					OriginKey:       row.OriginKey,
					PR:              row.PR,
					LastPRRefresh:   row.LastPRRefresh,
					PRRateLimitedAt: row.PRRateLimitedAt,
				}
			}
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
				// Empty fleet: no probes to wait on. Skip the splash entirely.
				h.booted = true
				go h.statusWorker()
			} else {
				startupCmd = tea.Batch(h.bootstrapGitInfo(repos), h.splashTick())
			}

			switch {
			case h.cfg.AnalyticsConsentSeen:
				h.fireStartupAnalytics(len(groups))
			case analytics.IsOptedOutByEnv():
				h.fireStartupAnalytics(len(groups))
			case !h.cfg.IsTelemetryEnabled():
				// User already opted out via config (e.g. from a pre-consent-dialog
				// version). Don't re-prompt; treat as already answered.
				h.fireStartupAnalytics(len(groups))
			default:
				h.consentDialog.Show()
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
func (h *Home) View() string {
	if h.isAttaching.Load() {
		return ""
	}
	if h.width == 0 {
		return lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render("   fleet")
	}
	if !h.booted {
		return RenderSplash(h.width, h.height, h.bootProgress(), h.splashFrame)
	}
	base := h.renderBody()
	// Command palette is a true overlay: render it on top of the main UI so
	// the sidebar/preview stay visible behind the dialog box. The base is
	// dimmed first so the palette visually lifts above the content.
	if h.commandPalette.IsVisible() {
		base = dimBackdrop(base)
		base = overlay.Composite(h.commandPalette.View(), base, overlay.Center, overlay.Center, 0, 0)
	}
	toast := h.toasts.View(h.width)
	if toast == "" {
		return base
	}
	// Bottom-right, with a 1-cell right margin and a 1-row lift so the toast
	// clears the help-bar baseline.
	return overlay.Composite(toast, base, overlay.Right, overlay.Bottom, -1, -1)
}

func (h *Home) renderBody() string {
	// Modals take priority. Consent goes first — it gates analytics init
	// and must be the user's first interaction with the TUI.
	if h.consentDialog.IsVisible() {
		return h.consentDialog.View()
	}
	if h.helpOverlay.IsVisible() {
		return h.helpOverlay.View()
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
	if h.newDialog.IsVisible() {
		return h.newDialog.View()
	}
	if h.confirmDialog.IsVisible() {
		return h.confirmDialog.View()
	}
	if h.renameDialog.IsVisible() {
		return h.renameDialog.View()
	}

	var b strings.Builder

	// Snapshot gitInfoCache under lock — the worker goroutine writes to
	// it concurrently, and View() must not read the live map without a lock.
	h.workerMu.Lock()
	gitInfoSnap := make(map[string]*git.RepoInfo, len(h.gitInfoCache))
	for k, v := range h.gitInfoCache {
		gitInfoSnap[k] = v
	}
	h.workerMu.Unlock()

	// Header.
	header := h.renderHeader()
	b.WriteString(header)
	b.WriteString("\n") // line break that starts panel row 0 — NOT a blank padding row

	// Content area. Header is 1 row, help bar is helpBarHeight rows, leaving
	// the rest for the panels.
	contentHeight := h.height - 1 - helpBarHeight
	if contentHeight < 1 {
		contentHeight = 1
	}

	// Status counts now live in the top header (statusCountsLine) — the
	// Sessions panel bottom border stays unadorned.
	statusFooter := ""

	switch h.layoutMode() {
	case "single":
		// Inner content area = total - 2 for the border on each side.
		innerW := h.width - 2
		innerH := contentHeight - 2
		sidebar := RenderSidebar(h.flatItems, h.sessions, gitInfoSnap, h.slotBindings, h.cursor, h.viewOffset, innerW, innerH)
		sidebar = ensureExactHeight(sidebar, innerH)
		sidebar = ensureExactWidth(sidebar, innerW)
		b.WriteString(RenderBorderedPanelFooter(sidebar, "Sessions", statusFooter, h.width, contentHeight, h.focusMode))
	case "stacked":
		sidebarHeight := (contentHeight * 55) / 100
		if sidebarHeight < 3 {
			sidebarHeight = 3
		}
		previewHeight := contentHeight - sidebarHeight - 1 // 1 for gap row
		innerW := h.width - 2

		sidebarInner := RenderSidebar(h.flatItems, h.sessions, gitInfoSnap, h.slotBindings, h.cursor, h.viewOffset, innerW, sidebarHeight-2)
		sidebarInner = ensureExactHeight(sidebarInner, sidebarHeight-2)
		sidebarInner = ensureExactWidth(sidebarInner, innerW)
		b.WriteString(RenderBorderedPanelFooter(sidebarInner, "Sessions", statusFooter, h.width, sidebarHeight, h.focusMode))
		b.WriteString("\n\n")

		s, content := h.selectedPreview()
		previewRepoInfo := h.repoInfoFromSnap(gitInfoSnap)
		previewInner := RenderPreview(s, content, previewRepoInfo, innerW, previewHeight-2, h.focusMode)
		previewInner = ensureExactHeight(previewInner, previewHeight-2)
		previewInner = ensureExactWidth(previewInner, innerW)
		previewTitle := BuildPreviewTitle(s, previewRepoInfo, h.focusMode, h.width-6)
		previewFooter := BuildPreviewFooter(s, h.width-6)
		b.WriteString(RenderBorderedPanelFooter(previewInner, previewTitle, previewFooter, h.width, previewHeight, h.focusMode))
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
			inner := RenderSidebar(h.flatItems, h.sessions, gitInfoSnap, h.slotBindings, h.cursor, h.viewOffset, sidebarInnerW, innerH)
			inner = ensureExactHeight(inner, innerH)
			inner = ensureExactWidth(inner, sidebarInnerW)
			leftPanel = RenderBorderedPanelFooter(inner, "Sessions", statusFooter, sidebarWidth, contentHeight, h.focusMode)
			h.cachedSidebar = leftPanel
			h.sidebarDirty = false
		}

		s, content := h.selectedPreview()
		previewRepoInfo := h.repoInfoFromSnap(gitInfoSnap)
		previewInner := RenderPreview(s, content, previewRepoInfo, previewInnerW, innerH, h.focusMode)
		previewInner = ensureExactHeight(previewInner, innerH)
		previewInner = ensureExactWidth(previewInner, previewInnerW)
		previewTitle := BuildPreviewTitle(s, previewRepoInfo, h.focusMode, previewWidth-6)
		previewFooter := BuildPreviewFooter(s, previewWidth-6)
		rightPanel := RenderBorderedPanelFooter(previewInner, previewTitle, previewFooter, previewWidth, contentHeight, h.focusMode)

		// Build the single-column gap as transparent spaces.
		gapLines := make([]string, contentHeight)
		for i := range gapLines {
			gapLines[i] = " "
		}
		gapCol := strings.Join(gapLines, "\n")

		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, gapCol, rightPanel))
	}

	// Pad to fill content area.
	lineCount := strings.Count(b.String(), "\n") + 1
	for lineCount < h.height-helpBarHeight {
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

func (h *Home) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Route to active dialog/overlay.
	if h.helpOverlay.IsVisible() {
		overlay, cmd := h.helpOverlay.Update(msg)
		h.helpOverlay = overlay
		return h, cmd
	}
	if h.consentDialog.IsVisible() {
		dialog, cmd := h.consentDialog.Update(msg)
		h.consentDialog = dialog
		return h, cmd
	}
	if h.bugReport.IsVisible() {
		dialog, cmd := h.bugReport.Update(msg)
		h.bugReport = dialog
		return h, cmd
	}
	if h.settingsDialog.IsVisible() {
		dialog, cmd := h.settingsDialog.Update(msg)
		h.settingsDialog = dialog
		return h, cmd
	}
	if h.createWorkspaceDialog.IsVisible() {
		dialog, cmd := h.createWorkspaceDialog.Update(msg)
		h.createWorkspaceDialog = dialog
		// User cancelled with ESC — drop fork ctx. Submit (Enter) also hides
		// the dialog but emits a workspaceCreateMsg that consumes the ctx,
		// so we must NOT clear on every Hide.
		if isEscKey(msg) && !h.createWorkspaceDialog.IsVisible() && !h.worktreeDialog.IsVisible() {
			h.clearPendingFork()
		}
		return h, cmd
	}
	if h.worktreeDialog.IsVisible() {
		dialog, cmd := h.worktreeDialog.Update(msg)
		h.worktreeDialog = dialog
		// Same reasoning as createWorkspaceDialog above: only ESC clears.
		if isEscKey(msg) && !h.worktreeDialog.IsVisible() && !h.createWorkspaceDialog.IsVisible() {
			h.clearPendingFork()
		}
		return h, cmd
	}
	if h.branchDialog.IsVisible() {
		dialog, cmd := h.branchDialog.Update(msg)
		h.branchDialog = dialog
		return h, cmd
	}
	if h.commandPalette.IsVisible() {
		dialog, cmd := h.commandPalette.Update(msg)
		h.commandPalette = dialog
		return h, cmd
	}
	if h.newDialog.IsVisible() {
		dialog, cmd := h.newDialog.Update(msg)
		h.newDialog = dialog
		return h, cmd
	}
	if h.confirmDialog.IsVisible() {
		dialog, cmd := h.confirmDialog.Update(msg)
		h.confirmDialog = dialog
		return h, cmd
	}
	if h.renameDialog.IsVisible() {
		dialog, cmd := h.renameDialog.Update(msg)
		h.renameDialog = dialog
		return h, cmd
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

	// Snapshot and clear the double-tap window: only a consecutive digit press
	// should attach, so any other key falling through this switch invalidates
	// the window for free. The digit case restores the snapshot before jumping.
	prevSlotTapSlot := h.lastSlotTapSlot
	prevSlotTapAt := h.lastSlotTapAt
	h.lastSlotTapSlot = -1

	switch msg.String() {
	case "j", "down":
		h.cursor = NextSelectableItem(h.flatItems, h.cursor, 1)
		h.syncViewport()
		return h, h.fetchPreviewForSelected()
	case "k", "up":
		h.cursor = NextSelectableItem(h.flatItems, h.cursor, -1)
		h.syncViewport()
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
		if h.cfg.GetEnterMode() == "split" {
			return h, h.enterFocusMode()
		}
		if s := h.selectedSession(); s != nil {
			h.actionLog.Add("attach session", s.Title, true)
			analytics.Track(analytics.EventSessionAttached, nil)
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
	case " ":
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
	case "n":
		// New session at any repo path.
		h.newDialog.Show()
		return h, nil
	case "w":
		// New worktree session.
		repoPath := h.resolveCurrentRepo()
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
	case "d":
		// On a repo/worktree header, delete acts on the container (§ confirmDeleteHeader).
		if h.cursor >= 0 && h.cursor < len(h.flatItems) && h.flatItems[h.cursor].IsRepoHeader {
			return h, h.confirmDeleteHeader(h.flatItems[h.cursor])
		}
		if s := h.selectedSession(); s != nil {
			h.actionLog.Add("delete session", s.Title, true)
		}
		return h, h.confirmDeleteSelected()
	case "u":
		return h.undoDelete()
	case "z":
		h.toggleIdleFold()
		return h, nil
	case "r":
		if s := h.selectedSession(); s != nil {
			h.actionLog.Add("restart session", s.Title, true)
			analytics.Track(analytics.EventSessionRestarted, nil)
		}
		return h, h.restartSelected()
	case "R":
		return h, h.renameSelected()
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
		analytics.Track(analytics.EventCommandPalette, nil)
		return h, nil
	case "S":
		h.settingsDialog.Show()
		analytics.Track(analytics.EventSettingsOpened, nil)
		return h, nil
	case "!":
		h.actionLog.Add("open bug report", "", true)
		h.bugReport.Show(h.version, len(h.sessions), h.errorHistory, h.actionLog, h.width, h.height, &h.renderStats, time.Since(h.startTime))
		analytics.Track(analytics.EventBugReportOpened, nil)
		return h, nil
	case "D":
		s := h.selectedSession()
		if s == nil {
			return h, nil
		}
		h.actionLog.Add("status snapshot", s.Title, true)
		return h, func() tea.Msg {
			return captureStatusSnapshot(s, s.ID)
		}
	case "?":
		h.helpOverlay.Show()
		return h, nil
	case "q", "ctrl+c":
		h.cancel() // stops background worker
		if h.hookWatcher != nil {
			h.hookWatcher.Stop()
		}
		if h.controlClient != nil {
			h.controlClient.Close()
		}
		// Finalize all pending deletes before quitting.
		h.finalizeAllPendingDeletes()

		uptime := time.Since(h.startTime).Seconds()

		// h.cancel() above only signals the status worker — it can still be
		// mid-cycle, holding workerMu and mutating h.sessions. Take the lock
		// for the direct reads; collectSnapshot and anyAttached self-lock.
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
		return h, tea.Quit
	}

	return h, nil
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
	s := h.flatItems[h.cursor].Session
	if s == nil || !s.IsAlive() {
		return nil
	}

	h.markSessionAccessed(s)
	s.Acknowledge()
	if err := h.storage.SetAcknowledged(s.ID, true); err != nil {
		debuglog.Logger.Error("storage: SetAcknowledged", "id", s.ID, "err", err)
	}

	h.isAttaching.Store(true)
	attachStart := time.Now()

	return tea.Exec(attachCmd{session: s.GetTmuxSession()}, func(err error) tea.Msg {
		// CRITICAL: Clear isAttaching before returning the message.
		// Prevents race where View() returns empty string after detach.
		h.isAttaching.Store(false)
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

func (h *Home) handleSessionCreate(msg sessionCreateMsg) (tea.Model, tea.Cmd) {
	if _, err := exec.LookPath("claude"); err != nil {
		h.setError(fmt.Errorf("claude CLI not found — install Claude Code to create sessions"))
		return h, nil
	}
	debuglog.Logger.Info("creating session", "title", msg.title, "path", msg.path)
	s := session.NewSession(msg.title, msg.path)
	s.WorkspaceName = msg.workspaceName
	return h, func() tea.Msg {
		if err := s.Start(); err != nil {
			debuglog.Logger.Error("session Start() failed", "title", msg.title, "path", msg.path, "err", err)
			return sessionCreateResultMsg{err: err}
		}
		return sessionCreateResultMsg{session: s}
	}
}

func (h *Home) handleSessionCreateResult(msg sessionCreateResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		h.setError(fmt.Errorf("failed to start session: %w", msg.err))
		return h, nil
	}

	analytics.Track(analytics.EventSessionCreated, nil)
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
	h.repoExpanded[repo] = true
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

	return h, h.fetchPreviewForSelected()
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
		"Press z to undo within 5s",
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

// confirmDeleteHeader handles `d` pressed on a repo/worktree header. Scope is the
// container: a worktree header removes the worktree dir + its sessions; a real-repo
// header "forgets" the repo from fleet (deletes its sessions + unpins, folder kept).
func (h *Home) confirmDeleteHeader(item SidebarItem) tea.Cmd {
	repoPath := item.RepoPath
	count := h.countSessionsForRepo(repoPath)
	isWorktree := h.repoIsWorktree(repoPath)

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
		details = []string{
			fmt.Sprintf("Deletes %d session(s) + the worktree directory", count),
			"Press z to undo within 5s",
		}
	case isWorktree: // empty worktree
		title = "Remove Worktree?"
		details = []string{"Removes the worktree directory"}
	default: // real repo with sessions
		title = "Remove repo from fleet?"
		details = []string{
			fmt.Sprintf("Deletes %d session(s) — folder untouched", count),
			"Press z to undo within 5s",
		}
	}

	h.actionLog.Add("delete "+map[bool]string{true: "worktree", false: "repo"}[isWorktree], base, true)
	h.confirmDialog.ShowDanger(title, base, details, func() tea.Msg {
		return repoDeleteMsg{repoPath: repoPath, destroyWorkspace: isWorktree}
	})
	return nil
}

// repoIsWorktree reports whether a repo group is a git worktree. Uses the cached
// git info when available (populated by the worker for repos with sessions); for
// an empty worktree header the worker never refreshes it, so fall back to a direct
// git check — cheap, and only on the keypress that opens the delete dialog.
func (h *Home) repoIsWorktree(repoPath string) bool {
	// Snapshot under workerMu — the status worker writes gitInfoCache concurrently.
	h.workerMu.Lock()
	info := h.gitInfoCache[repoPath]
	h.workerMu.Unlock()
	if info != nil {
		return info.IsWorktreeRepo
	}
	return git.IsWorktree(repoPath)
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
// the per-session deferred-delete machinery so `z`-undo still works (LIFO). The last
// session carries the container-level side effects (unpin, optional worktree destroy).
// An empty worktree (no sessions) is removed directly in the background.
func (h *Home) deferDeleteRepo(msg repoDeleteMsg) (tea.Model, tea.Cmd) {
	var sess []*session.Session
	for _, s := range h.sessions {
		if session.GetRepoRoot(s.ProjectPath) == msg.repoPath {
			sess = append(sess, s)
		}
	}

	var cmds []tea.Cmd
	if len(sess) == 0 {
		// Empty worktree: unpin + background `git worktree remove` (not undoable;
		// the confirm dialog is the safety gate, nothing live to lose).
		h.unpinRepoHeader(msg.repoPath)
		if msg.destroyWorkspace {
			repoPath := msg.repoPath
			cmds = append(cmds, func() tea.Msg {
				p := workspace.ResolveProvider(repoPath)
				var err error
				if p != nil && p.CanDestroy() {
					err = p.Destroy(repoPath, filepath.Base(repoPath))
				}
				return deleteCleanupDoneMsg{workspaceErr: err}
			})
		}
		return h, tea.Batch(cmds...)
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

func (h *Home) restartSelected() tea.Cmd {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) || h.flatItems[h.cursor].IsRepoHeader {
		return nil
	}
	s := h.flatItems[h.cursor].Session
	if s == nil {
		return nil
	}

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

func (h *Home) forkSelected() tea.Cmd {
	s := h.selectedSession()
	if s == nil {
		h.setError(fmt.Errorf("cannot fork: no session selected"))
		return nil
	}
	if s.ClaudeSessionID == "" {
		h.setError(fmt.Errorf("cannot fork: session has no Claude conversation ID yet"))
		return nil
	}
	title := s.Title + " (fork)"
	claudeSessionID := s.ClaudeSessionID
	path := s.ProjectPath
	workspaceName := s.WorkspaceName
	return func() tea.Msg {
		return forkSessionMsg{
			parentClaudeSessionID: claudeSessionID,
			sourcePath:            path,
			path:                  path,
			title:                 title,
			workspaceName:         workspaceName,
		}
	}
}

// forkContext holds the parent-session fields captured when Shift+F opens the
// worktree picker, so the deferred result handlers can build a forkSessionMsg
// targeting the chosen destination.
type forkContext struct {
	parentClaudeSessionID string
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
	km, ok := msg.(tea.KeyMsg)
	return ok && km.Type == tea.KeyEsc
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
	sourcePath := ctx.parentProjectPath
	return func() tea.Msg {
		return forkSessionMsg{
			parentClaudeSessionID: parentClaudeSessionID,
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
		parentProjectPath:     s.ProjectPath,
		parentTitle:           s.Title,
	}
	h.worktreeDialog.ShowLoading()
	return tea.Batch(h.fetchWorkspaceListForRepo(repoPath), spinnerTickCmd)
}

// toggleIdleFold flips the idle-fold state for the checkout at cursor. Acts on
// the checkout under the cursor — whether the cursor sits on the checkout
// header itself, on a session inside it, or on the "+ N idle" placeholder.
func (h *Home) toggleIdleFold() {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return
	}
	item := h.flatItems[h.cursor]
	repo := ""
	switch {
	case item.IsCheckoutHeader || item.IsIdleFold:
		repo = item.RepoPath
	case item.Session != nil:
		repo = session.GetRepoRoot(item.Session.ProjectPath)
	case item.Pending != nil:
		repo = item.Pending.RepoPath
	default:
		return
	}
	if repo == "" {
		return
	}
	h.idleFolded[repo] = !h.idleFolded[repo]
	h.rebuildFlatItems()
	h.syncViewport()
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
	h.repoExpanded[key] = !IsExpanded(h.repoExpanded, key)
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
	h.repoExpanded[key] = true
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
	h.repoExpanded[key] = false
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

// jumpToNextAttentionSession cycles through sessions needing attention:
// waiting first, then finished. Wraps around, auto-expands collapsed groups.
func (h *Home) jumpToNextAttentionSession() {
	// Build ordered list of ALL sessions (same order as sidebar).
	groups := session.GroupByRepo(h.sessions)
	repos := make([]string, 0, len(groups))
	for repo := range groups {
		repos = append(repos, repo)
	}
	sort.Strings(repos)

	type candidate struct {
		s    *session.Session
		repo string
	}
	var allSessions []candidate
	for _, repo := range repos {
		for _, s := range groups[repo] {
			allSessions = append(allSessions, candidate{s: s, repo: repo})
		}
	}
	if len(allSessions) == 0 {
		return
	}

	// Find the current session's position in allSessions.
	var currentID string
	if h.cursor >= 0 && h.cursor < len(h.flatItems) && !h.flatItems[h.cursor].IsRepoHeader {
		if s := h.flatItems[h.cursor].Session; s != nil {
			currentID = s.ID
		}
	}
	currentIdx := -1
	for i, c := range allSessions {
		if c.s.ID == currentID {
			currentIdx = i
			break
		}
	}

	// findNext scans forward (wrapping) for a session with the given status.
	findNext := func(status session.Status) *candidate {
		n := len(allSessions)
		start := currentIdx + 1
		for i := 0; i < n; i++ {
			c := &allSessions[(start+i)%n]
			if c.s.GetStatus() == status {
				return c
			}
		}
		return nil
	}

	// Priority: waiting > finished.
	target := findNext(session.StatusWaiting)
	targetKind := "waiting"
	if target == nil {
		target = findNext(session.StatusFinished)
		targetKind = "finished"
	}

	// visIdx returns the index of a session ID in the visible flatItems, or -1.
	visIdx := func(id string) int {
		for i, item := range h.flatItems {
			if !item.IsRepoHeader && item.Session != nil && item.Session.ID == id {
				return i
			}
		}
		return -1
	}

	if target == nil {
		debuglog.Logger.Debug("spacejump: no waiting/finished target",
			"cursor", h.cursor, "currentID", currentID, "allSessions", len(allSessions))
		return // Silent no-op.
	}

	// DIAGNOSTIC: capture orderings + collapse/filter state before we touch
	// anything. allSessions is repo-path-sorted; flatItems is origin-tree
	// sorted — when these diverge, "next" in the jump list isn't "next" on
	// screen, which is the suspected "only works going down" bug.
	debuglog.Logger.Debug("spacejump: target found",
		"kind", targetKind,
		"targetID", target.s.ID,
		"targetRepo", target.repo,
		"targetOrigin", h.originOf(target.repo),
		"cursor", h.cursor,
		"currentID", currentID,
		"currentIdx_allSessions", currentIdx,
		"currentVisIdx", visIdx(currentID),
		"targetVisIdx_beforeExpand", visIdx(target.s.ID),
		"originCollapsed", !IsExpanded(h.repoExpanded, OriginExpandKey(h.originOf(target.repo))),
		"checkoutCollapsed", !IsExpanded(h.repoExpanded, target.repo),
		"filterText", h.filterText,
	)

	// Expand the repo group if collapsed.
	h.repoExpanded[target.repo] = true
	h.rebuildFlatItems()

	// Set cursor to the target session.
	if i := visIdx(target.s.ID); i >= 0 {
		dir := "down"
		if i < h.cursor {
			dir = "up"
		}
		debuglog.Logger.Debug("spacejump: landed",
			"targetID", target.s.ID, "oldCursor", h.cursor, "newCursor", i, "direction", dir)
		h.cursor = i
		h.syncViewport()
		return
	}

	// Target found by findNext but absent from flatItems after expanding only
	// the checkout key — it's hidden under a collapsed ORIGIN header (we never
	// expand the "origin:" key here) or filtered out. Cursor does NOT move:
	// this is the silent "nothing happens" the user reported.
	debuglog.Logger.Warn("spacejump: target HIDDEN, cursor NOT moved",
		"targetID", target.s.ID,
		"targetRepo", target.repo,
		"targetOrigin", h.originOf(target.repo),
		"originCollapsed", !IsExpanded(h.repoExpanded, OriginExpandKey(h.originOf(target.repo))),
		"filterText", h.filterText,
	)
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
	parts := strings.Fields(h.cfg.GetEditor())
	if len(parts) == 0 {
		return func() tea.Msg {
			return openEditorMsg{err: fmt.Errorf("no editor configured")}
		}
	}
	projectPath := s.ProjectPath
	return func() tea.Msg {
		args := append(parts[1:], projectPath)
		cmd := exec.Command(parts[0], args...)
		if err := cmd.Start(); err != nil {
			debuglog.Logger.Error("editor launch failed", "editor", parts[0], "args", args, "err", err)
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

func (h *Home) handleFocusKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := h.selectedSession()
	if s == nil || !s.IsAlive() {
		h.focusMode = false
		h.sidebarDirty = true
		return h, nil
	}

	if msg.Type == tea.KeyEsc {
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

	switch msg.Type {
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
	case tea.KeyCtrlC:
		cc.SendKeys(target, "C-c")
	case tea.KeyCtrlD:
		cc.SendKeys(target, "C-d")
	case tea.KeyCtrlA:
		cc.SendKeys(target, "C-a")
	case tea.KeyCtrlU:
		cc.SendKeys(target, "C-u")
	case tea.KeyCtrlL:
		cc.SendKeys(target, "C-l")
	case tea.KeyCtrlW:
		cc.SendKeys(target, "C-w")
	case tea.KeyCtrlK:
		cc.SendKeys(target, "C-k")
	case tea.KeyRunes:
		cc.SendLiteralKeys(target, string(msg.Runes))
	default:
		if str := msg.String(); str != "" {
			cc.SendLiteralKeys(target, str)
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

	h.workerMu.Lock()
	info := h.gitInfoCache[repo]
	h.workerMu.Unlock()
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
			// Fallback to macOS open command.
			debuglog.Logger.Debug("chrome extension unavailable, falling back to open", "err", err)
			if openErr := exec.Command("open", prURL).Start(); openErr != nil {
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
	}

	// Clear any slot binding pointing at this session. FK cascade drops the
	// DB row (triggered by the DeleteSession above), but the in-memory map
	// needs explicit cleanup so the [N] badge disappears from the sidebar.
	// Slot bindings do NOT survive undo: restoring the session via `z` leaves
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
		if pd.DestroyWS && pd.WorkspaceName != "" {
			provider := workspace.ResolveProvider(pd.RepoPath)
			if provider != nil && provider.CanDestroy() {
				workspaceErr = provider.Destroy(pd.RepoPath, pd.WorkspaceName)
			}
		}
		return deleteCleanupDoneMsg{sessionID: pd.Session.ID, workspaceErr: workspaceErr}
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
			provider := workspace.ResolveProvider(pd.RepoPath)
			if provider != nil && provider.CanDestroy() {
				if err := provider.Destroy(pd.RepoPath, pd.WorkspaceName); err != nil {
					debuglog.Logger.Error("failed to destroy workspace on quit", "id", pd.Session.ID, "workspace", pd.WorkspaceName, "err", err)
				}
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
		return fmt.Sprintf("Deleted %q. z to undo", title)
	}
	return fmt.Sprintf("Deleted %q. z to undo (%d pending)", title, n)
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

	// Read worker results under lock and rebuild.
	h.workerMu.Lock()
	h.rebuildFlatItems()
	h.workerMu.Unlock()

	// Preview is now handled by the faster previewTick, no need to fetch here.
	return h, h.tick()
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
		h.workerMu.Lock()
		info := h.gitInfoCache[repo]
		h.workerMu.Unlock()
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
// flips `booted=true` in the same Update tick, so the splash never appears
// for users with no repos either way.
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
	ticker := time.NewTicker(tickInterval)
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
func (h *Home) syncHookStatuses(sessions []*session.Session) []string {
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
			}) {
				changed = append(changed, s.ID)
			}
			// Persist new Claude session ID if it changed.
			if s.ClaudeSessionID != oldClaudeSessionID && s.ClaudeSessionID != "" {
				if err := h.storage.UpdateClaudeSessionID(s.ID, s.ClaudeSessionID); err != nil {
					debuglog.Logger.Error("storage: UpdateClaudeSessionID", "id", s.ID, "err", err)
				}
			}
			// Persist prompt changes and reset title on every new prompt
			// (for non-manually-renamed, non-Claude-named sessions).
			if s.PromptCount != oldPromptCount {
				h.markSessionAccessed(s)
				if err := h.storage.UpdatePromptCount(s.ID, s.PromptCount); err != nil {
					debuglog.Logger.Error("storage: UpdatePromptCount", "id", s.ID, "err", err)
				}
				if h.cfg.IsAutoNameEnabled() && s.TitleGenerated && !s.ManuallyRenamed && s.ClaudeSessionName == "" {
					s.TitleGenerated = false
					if err := h.storage.ResetTitleGenerated(s.ID); err != nil {
						debuglog.Logger.Error("storage: ResetTitleGenerated", "id", s.ID, "err", err)
					}
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
func (h *Home) updateAndPersistStatus(s *session.Session) {
	oldStatus := s.GetStatus()
	s.UpdateStatus()
	newStatus := s.GetStatus()
	if oldStatus != newStatus {
		if err := h.storage.UpdateStatus(s.ID, string(newStatus)); err != nil {
			debuglog.Logger.Error("storage: UpdateStatus", "id", s.ID, "status", newStatus, "err", err)
		}
	}
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

func (h *Home) statusWorkerCycle() {
	// Recover from panics to keep the worker alive.
	defer func() {
		if r := recover(); r != nil {
			debuglog.Logger.Error("statusWorkerCycle panic recovered", "panic", r)
		}
	}()

	// 1. Refresh tmux session cache (blocking but in background).
	tmux.RefreshSessionCache()

	// 2. Take a snapshot of sessions under lock.
	h.workerMu.Lock()
	sessions := make([]*session.Session, len(h.sessions))
	copy(sessions, h.sessions)
	h.workerMu.Unlock()

	if len(sessions) == 0 {
		return
	}

	// 3. Sync hook status (fast: in-memory map lookups).
	h.syncHookStatuses(sessions)

	// 3b. Auto-name: generate title for ONE session per cycle.
	// Priority: manual (R key) > Claude session name > last prompt heuristic.
	if h.cfg.IsAutoNameEnabled() {
		for _, s := range sessions {
			if s.ManuallyRenamed {
				continue
			}

			// Periodically re-read Claude's session name from JSONL (~every 30s per session).
			if s.ClaudeSessionID != "" && time.Since(s.ClaudeNameLastChecked) > 30*time.Second {
				s.ClaudeNameLastChecked = time.Now()
				name := session.ReadClaudeSessionName(s.ClaudeSessionID, s.ProjectPath)
				if name != "" && name != s.ClaudeSessionName {
					s.ClaudeSessionName = name
					s.Title = name
					if err := h.storage.UpdateTitle(s.ID, name); err != nil {
						debuglog.Logger.Error("storage: UpdateTitle (claude name)", "id", s.ID, "err", err)
					}
					s.TitleGenerated = true
					if err := h.storage.MarkTitleGenerated(s.ID); err != nil {
						debuglog.Logger.Error("storage: MarkTitleGenerated", "id", s.ID, "err", err)
					}
				}
			}
			if s.ClaudeSessionName != "" {
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

	// 4. Priority updates first — sessions whose hook file just changed.
	// These bypass round-robin so the UI reflects fresh hook status within
	// ~100ms of the hook firing (vs. up to (N/statusRoundRobin)*tickInterval seconds).
	priorityIDs := make(map[string]bool)
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
	for _, s := range sessions {
		if !priorityIDs[s.ID] {
			continue
		}
		h.updateAndPersistStatus(s)
		processed[s.ID] = true
	}

	// 5. Round-robin status updates (pane capture — blocking), skipping already-processed.
	count := statusRoundRobin
	if count > len(sessions) {
		count = len(sessions)
	}
	for i := 0; i < count; i++ {
		idx := (h.statusRRIndex + i) % len(sessions)
		s := sessions[idx]
		if processed[s.ID] {
			continue
		}
		h.updateAndPersistStatus(s)
	}
	h.statusRRIndex = (h.statusRRIndex + count) % len(sessions)

	// 5. Git+PR refresh: fan out across all session repos in parallel,
	// bounded to 4 concurrent goroutines so the subprocess load stays
	// flat. Branch/dirty lands within the 2s tick; PR refresh respects
	// the per-repo TTL gate (60s hot / 2 min cold) inside the goroutine.
	//
	// First: stamp `repoLastHotAt` so the TTL classifier (next call) can
	// see who's active right now. A repo is "hot" if any of its sessions
	// is currently Running — checked every cycle, cheap.
	now := time.Now()
	h.workerMu.Lock()
	for _, s := range sessions {
		if s.GetStatus() == session.StatusRunning {
			h.repoLastHotAt[session.GetRepoRoot(s.ProjectPath)] = now
		}
	}
	h.workerMu.Unlock()

	repos := h.uniqueRepoPathsFromSessions(sessions)
	h.refreshAllGitAndPR(repos, 4, 0, nil)
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
// Caller must hold workerMu.
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

	// Precompute per-repo TTL under the lock so the per-repo goroutines
	// don't need to grab workerMu just for classification. Bootstrap and
	// any cycle where `repoLastHotAt` is empty produce all-cold TTLs;
	// that's fine — the bootstrap honors carried-forward LastPRRefresh
	// values from the persisted cache the same way.
	h.workerMu.Lock()
	repoTTL := make(map[string]time.Duration, len(repos))
	for _, r := range repos {
		repoTTL[r] = h.repoTTLFor(r, cycleStart)
	}
	h.workerMu.Unlock()

	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for _, repo := range repos {
		wg.Add(1)
		sem <- struct{}{}
		go func(r string) {
			defer wg.Done()
			defer func() { <-sem }()
			if progress != nil {
				defer progress.Add(1)
			}
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
			h.workerMu.Lock()
			if old, ok := h.gitInfoCache[r]; ok && old.Branch == info.Branch {
				info.PR = old.PR
				info.LastPRRefresh = old.LastPRRefresh
				info.PRRateLimitedAt = old.PRRateLimitedAt
			}
			h.workerMu.Unlock()

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

			h.workerMu.Lock()
			h.gitInfoCache[r] = info
			h.workerMu.Unlock()

			// Persist to SQLite so the next launch can carry this forward
			// instead of re-firing gh. Storage method logs errors itself;
			// a failed write doesn't affect the in-memory cache.
			_ = h.storage.SavePRCacheRow(&session.PRCacheRow{
				RepoPath:        r,
				Branch:          info.Branch,
				OriginKey:       info.OriginKey,
				PR:              info.PR,
				LastPRRefresh:   info.LastPRRefresh,
				PRRateLimitedAt: info.PRRateLimitedAt,
			})
		}(repo)
	}

	if deadline <= 0 {
		wg.Wait()
	} else {
		done := make(chan struct{})
		go func() {
			wg.Wait()
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
	h.workerMu.Lock()
	last := h.lastRateLimitWarn
	now := time.Now()
	if !last.IsZero() && now.Sub(last) < cooldown {
		h.workerMu.Unlock()
		return
	}
	h.lastRateLimitWarn = now
	h.workerMu.Unlock()
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
		workspaces, err := provider.List(repoPath)
		defaultBranch := git.GetDefaultBranch(repoPath)
		return workspaceListMsg{workspaces: workspaces, provider: provider, repoPath: repoPath, defaultBranch: defaultBranch, err: err}
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
	bg := ColorSurface
	sp := lipgloss.NewStyle().Background(bg).Render
	logo := lipgloss.NewStyle().Foreground(ColorBrand).Background(bg).Bold(true).Render("❯_")
	title := logo + sp(" ") + TitleStyle.Background(bg).Render("fleet")

	breadcrumb := h.cursorBreadcrumb(bg)
	counts := h.statusCountsLine(bg)

	left := title
	if breadcrumb != "" {
		sep := lipgloss.NewStyle().Foreground(ColorTextDim).Background(bg).Render("  ›  ")
		left += sep + breadcrumb
	}

	if h.width <= 0 {
		return HeaderBarStyle.Render(left)
	}
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(counts)
	pad := h.width - leftW - rightW
	if pad < 1 {
		pad = 1
	}
	return HeaderBarStyle.Render(left + sp(strings.Repeat(" ", pad)) + counts)
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
func (h *Home) cursorBreadcrumb(bg lipgloss.Color) string {
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
		h.workerMu.Lock()
		if info := h.gitInfoCache[item.RepoPath]; info != nil {
			branch = info.Branch
		}
		h.workerMu.Unlock()
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

// statusCountsLine renders the global session-status pill ("N RUN · N WAIT ·
// N idle") for the right side of the header bar. Same content as the old
// sidebarStatusFooter; now lives in the top dashboard instead.
func (h *Home) statusCountsLine(bg lipgloss.Color) string {
	counts := make(map[session.Status]int)
	for _, s := range h.sessions {
		counts[s.GetStatus()]++
	}
	var parts []string
	if n := counts[session.StatusRunning] + counts[session.StatusStarting]; n > 0 {
		parts = append(parts, StatusRunningStyle.Background(bg).Render(fmt.Sprintf("%d RUN", n)))
	}
	if n := counts[session.StatusWaiting]; n > 0 {
		parts = append(parts, StatusWaitingStyle.Background(bg).Render(fmt.Sprintf("%d WAIT", n)))
	}
	if n := counts[session.StatusError]; n > 0 {
		parts = append(parts, StatusErrorStyle.Background(bg).Render(fmt.Sprintf("%d ERR", n)))
	}
	if n := counts[session.StatusFinished]; n > 0 {
		parts = append(parts, StatusFinishedStyle.Background(bg).Render(fmt.Sprintf("%d FIN", n)))
	}
	if n := counts[session.StatusIdle]; n > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(ColorTextDim).Background(bg).Render(fmt.Sprintf("%d idle", n)))
	}
	if len(parts) == 0 {
		return ""
	}
	sep := lipgloss.NewStyle().Foreground(ColorTextDim).Background(bg).Render(" · ")
	return strings.Join(parts, sep)
}

func (h *Home) renderHelpBar() string {
	contextKeys, globalKeys := HelpBarBindingsFor(h.cursorBarContext())

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

	// Expand the repo group if collapsed, so the session is visible and selectable.
	repo := session.GetRepoRoot(s.ProjectPath)
	if !h.repoExpanded[repo] {
		h.repoExpanded[repo] = true
		h.rebuildFlatItems()
	}

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

func (h *Home) rebuildFlatItems() {
	h.flatItems = BuildFlatItems(h.sessions, h.pendingWorkspaces, h.repoExpanded, h.filterText, h.pinnedRepos, h.originOf, h.idleFolded)
	h.sidebarDirty = true
}

// originOf maps a repo root to its stable origin key, falling back to
// "local:<basename>" for repos whose RepoInfo hasn't been refreshed yet.
// Reads the snapshot directly from the worker-managed cache; callers that
// hold workerMu can pass it as a function value safely.
func (h *Home) originOf(repoRoot string) string {
	if info := h.gitInfoCache[repoRoot]; info != nil && info.OriginKey != "" {
		return info.OriginKey
	}
	return "local:" + filepath.Base(repoRoot)
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
	contentHeight := h.height - 1 - helpBarHeight
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

	// These block but run in the tea.Cmd goroutine, not Update().
	configDir := hooks.GetClaudeConfigDir()
	hooks.InjectClaudeHooks(configDir)
	chrome.InstallNativeMessagingHost()
	ghAvailable := github.IsGHAvailable()

	// Check for claude CLI availability.
	var warning string
	if _, err := exec.LookPath("claude"); err != nil {
		warning = "claude CLI not found — install Claude Code to create sessions"
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

func (h *Home) setInfo(msg string) {
	h.infoMsg = msg
	h.infoTime = time.Now()
	h.toasts.Add(ToastInfo, msg)
}

// buildPaletteItems returns all palette rows: built-in commands plus every
// repo/worktree currently in the sidebar (matched by name, branch, or path).
func (h *Home) buildPaletteItems() []PaletteItem {
	commands := []PaletteItem{
		{Kind: PaletteKindCommand, ID: "attach", Name: "Attach Session", Shortcut: "Enter"},
		{Kind: PaletteKindCommand, ID: "focus", Name: "Focus Preview", Shortcut: "Tab"},
		{Kind: PaletteKindCommand, ID: "jump_next", Name: "Jump to Next Waiting", Shortcut: "Space"},
		{Kind: PaletteKindCommand, ID: "new_session", Name: "New Session", Shortcut: "a"},
		{Kind: PaletteKindCommand, ID: "new_repo", Name: "New Session (Any Repo)", Shortcut: "n"},
		{Kind: PaletteKindCommand, ID: "new_worktree", Name: "New Worktree Session", Shortcut: "w"},
		{Kind: PaletteKindCommand, ID: "fork", Name: "Fork Session", Shortcut: "f"},
		{Kind: PaletteKindCommand, ID: "fork_worktree", Name: "Fork to Worktree", Shortcut: "F"},
		{Kind: PaletteKindCommand, ID: "delete", Name: "Delete Session", Shortcut: "d"},
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
		{Kind: PaletteKindCommand, ID: "reload_all", Name: "Reload All Sessions"},
		{Kind: PaletteKindCommand, ID: "mark_all_read", Name: "Mark All as Read"},
		{Kind: PaletteKindCommand, ID: "expand_all", Name: "Expand All Repos"},
		{Kind: PaletteKindCommand, ID: "collapse_all", Name: "Collapse All Repos"},
		{Kind: PaletteKindCommand, ID: "quit", Name: "Quit", Shortcut: "q"},
	}
	for i := range commands {
		commands[i].Haystack = commands[i].Name
	}

	// Snapshot gitInfoCache so we can read it without holding workerMu during fuzzy match.
	h.workerMu.Lock()
	gitSnap := make(map[string]*git.RepoInfo, len(h.gitInfoCache))
	for k, v := range h.gitInfoCache {
		gitSnap[k] = v
	}
	h.workerMu.Unlock()

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
	if !h.repoExpanded[repoPath] {
		h.repoExpanded[repoPath] = true
		h.rebuildFlatItems()
	}
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
			analytics.Track(analytics.EventSessionAttached, nil)
			if analytics.MarkOnboardingMilestone(analytics.MilestoneFirstAttach) {
				analytics.Track(analytics.EventOnboardingFirstAttach, map[string]interface{}{
					"seconds_since_install": int(analytics.SecondsSinceInstall()),
				})
			}
		}
		return h, h.attachSelected()
	case "focus":
		return h, h.enterFocusMode()
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
	case "new_repo":
		h.newDialog.Show()
		return h, nil
	case "new_worktree":
		repoPath := h.resolveCurrentRepo()
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
	case "delete":
		if s := h.selectedSession(); s != nil {
			h.actionLog.Add("delete session", s.Title, true)
		}
		return h, h.confirmDeleteSelected()
	case "restart":
		if s := h.selectedSession(); s != nil {
			h.actionLog.Add("restart session", s.Title, true)
			analytics.Track(analytics.EventSessionRestarted, nil)
		}
		return h, h.restartSelected()
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
		h.actionLog.Add("open bug report", "", true)
		h.bugReport.Show(h.version, len(h.sessions), h.errorHistory, h.actionLog, h.width, h.height, &h.renderStats, time.Since(h.startTime))
		analytics.Track(analytics.EventBugReportOpened, nil)
		return h, nil
	case "help":
		h.helpOverlay.Show()
		return h, nil
	case "reload_all":
		analytics.Track(analytics.EventReloadAll, nil)
		return h, h.reloadAll()
	case "mark_all_read":
		analytics.Track(analytics.EventMarkAllRead, nil)
		h.markAllAsRead()
		return h, nil
	case "expand_all":
		for _, key := range h.allExpandKeys() {
			h.repoExpanded[key] = true
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
			h.repoExpanded[key] = false
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
		return h, tea.Quit
	}
	return h, nil
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
		// Skip active/healthy sessions — never kill running Claude work or idle sessions.
		if status == session.StatusRunning || status == session.StatusWaiting ||
			status == session.StatusStarting || status == session.StatusFinished ||
			status == session.StatusIdle {
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
