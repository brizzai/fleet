package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/fsnotify/fsnotify"
)

const hookDebounce = 100 * time.Millisecond

// HookStatus holds the decoded status from a hook status file.
type HookStatus struct {
	Status      string
	SessionID   string
	Event       string
	UpdatedAt   time.Time
	UserPrompt  string
	PromptCount int
	// AgentPID is the agent process that fired the hook, or 0 when the status file
	// predates the field. See StatusFile.AgentPID.
	AgentPID int
}

// HookWatcher watches ~/.config/fleet/hooks/ for status file changes
// and maintains a thread-safe in-memory status map.
type HookWatcher struct {
	hooksDir string
	watcher  *fsnotify.Watcher

	mu       sync.RWMutex
	statuses map[string]*HookStatus // fleet session ID -> latest status

	onChange chan struct{} // buffered(1), notifies when any status changes

	ctx    context.Context
	cancel context.CancelFunc
}

// GetHooksDir returns the path to the hooks status directory.
func GetHooksDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".config", "fleet", "hooks")
	}
	return filepath.Join(home, ".config", "fleet", "hooks")
}

// NewHookWatcher creates a new watcher for the hooks directory.
func NewHookWatcher() (*HookWatcher, error) {
	hooksDir := GetHooksDir()

	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		debuglog.Logger.Error("hook watcher: failed to create hooks dir", "dir", hooksDir, "err", err)
		return nil, err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		debuglog.Logger.Error("hook watcher: fsnotify watcher creation failed", "err", err)
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	debuglog.Logger.Info("hook watcher created", "dir", hooksDir)
	return &HookWatcher{
		hooksDir: hooksDir,
		watcher:  watcher,
		statuses: make(map[string]*HookStatus),
		onChange: make(chan struct{}, 1),
		ctx:      ctx,
		cancel:   cancel,
	}, nil
}

// Start begins watching the hooks directory. Blocks; run in a goroutine.
func (w *HookWatcher) Start() {
	if err := w.watcher.Add(w.hooksDir); err != nil {
		debuglog.Logger.Error("hook watcher: failed to watch hooks dir", "dir", w.hooksDir, "err", err)
		return
	}

	w.loadExisting()

	// Notify after loading existing files so TUI picks up pre-existing statuses quickly.
	select {
	case w.onChange <- struct{}{}:
	default:
	}

	var debounceTimer *time.Timer
	pendingFiles := make(map[string]bool)
	var pendingMu sync.Mutex

	for {
		select {
		case <-w.ctx.Done():
			return

		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}

			if filepath.Ext(event.Name) != ".json" {
				continue
			}
			// A removed/renamed status file means the session's hook state was
			// cleared (clearHookState runs on suspend/resume/restart). Drop the
			// cached entry immediately so a stale status — e.g. the "dead"
			// death-rattle a killed agent writes during suspend — can't be replayed
			// once the session's status leaves the protected Suspended state and
			// the worker reads the watcher again.
			if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				w.forget(event.Name)
				continue
			}
			if event.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}

			pendingMu.Lock()
			pendingFiles[event.Name] = true
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(hookDebounce, func() {
				pendingMu.Lock()
				files := make([]string, 0, len(pendingFiles))
				for f := range pendingFiles {
					files = append(files, f)
				}
				pendingFiles = make(map[string]bool)
				pendingMu.Unlock()

				for _, f := range files {
					w.processFile(f)
				}
			})
			pendingMu.Unlock()

		case watchErr, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			debuglog.Logger.Error("hook watcher: fsnotify error", "err", watchErr)
		}
	}
}

// Stop shuts down the watcher.
func (w *HookWatcher) Stop() {
	w.cancel()
	if w.watcher != nil {
		_ = w.watcher.Close()
	}
}

// GetStatus returns the hook status for a session, or nil if not available.
func (w *HookWatcher) GetStatus(sessionID string) *HookStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.statuses[sessionID]
}

// Changes returns a channel that receives a notification when any hook status changes.
// Buffered(1): callers may miss intermediate changes but will always see the latest state.
func (w *HookWatcher) Changes() <-chan struct{} {
	return w.onChange
}

// loadExisting reads all current status files on startup.
func (w *HookWatcher) loadExisting() {
	entries, err := os.ReadDir(w.hooksDir)
	if err != nil {
		debuglog.Logger.Error("hook watcher: loadExisting ReadDir failed", "dir", w.hooksDir, "err", err)
		return
	}

	count := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		w.processFile(filepath.Join(w.hooksDir, entry.Name()))
		count++
	}
	debuglog.Logger.Debug("hook watcher: loaded existing status files", "count", count)
}

// processFile reads a status file and updates the internal map.
func (w *HookWatcher) processFile(filePath string) {
	sf, err := ReadStatusFile(filePath)
	if err != nil {
		debuglog.Logger.Error("hook watcher: failed to parse status file", "file", filePath, "err", err)
		return
	}

	base := filepath.Base(filePath)
	instanceID := strings.TrimSuffix(base, ".json")

	hookStatus := &HookStatus{
		Status:      sf.Status,
		SessionID:   sf.SessionID,
		Event:       sf.Event,
		UpdatedAt:   time.Unix(sf.Timestamp, 0),
		UserPrompt:  sf.UserPrompt,
		PromptCount: sf.PromptCount,
		AgentPID:    sf.AgentPID,
	}

	w.mu.Lock()
	prev := w.statuses[instanceID]
	w.statuses[instanceID] = hookStatus
	w.mu.Unlock()

	emitHookMetrics(prev, hookStatus)

	// Notify listeners of the change (non-blocking).
	select {
	case w.onChange <- struct{}{}:
	default:
	}
}

// forget drops the cached status for a status file that was removed or renamed
// out of the way, so a stale entry (e.g. a suspended session's "dead"
// death-rattle) isn't served to the worker after the file is gone. Notifies
// listeners only if something was actually purged.
func (w *HookWatcher) forget(filePath string) {
	instanceID := strings.TrimSuffix(filepath.Base(filePath), ".json")
	w.mu.Lock()
	_, existed := w.statuses[instanceID]
	delete(w.statuses, instanceID)
	w.mu.Unlock()
	if !existed {
		return
	}
	select {
	case w.onChange <- struct{}{}:
	default:
	}
}

// emitHookMetrics fires analytics counters for new Claude prompt submissions
// and Stop events. Deduped against `prev` so repeated fsnotify writes for the
// same status don't double-count. loadExisting() also passes through this
// path on startup; nil-prev there means "first time we've seen this session
// in *this* fleet process" — that's expected, the counters are best-effort
// usage proxies, not exact tallies.
func emitHookMetrics(prev, curr *HookStatus) {
	if curr == nil {
		return
	}
	switch curr.Event {
	case "UserPromptSubmit":
		if prev == nil || curr.PromptCount > prev.PromptCount {
			analytics.Track(analytics.EventClaudePromptSubmitted, nil)
		}
	case "Stop":
		if prev == nil || prev.Event != "Stop" || !prev.UpdatedAt.Equal(curr.UpdatedAt) {
			analytics.Track(analytics.EventClaudeResponseReceived, nil)
			if analytics.MarkOnboardingMilestone(analytics.MilestoneFirstClaudeResponse) {
				analytics.Track(analytics.EventOnboardingFirstClaudeResponse, map[string]interface{}{
					"seconds_since_install": int(analytics.SecondsSinceInstall()),
				})
			}
		}
	}
}
