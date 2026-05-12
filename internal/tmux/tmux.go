package tmux

import (
	"context"
	"crypto/rand"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"golang.org/x/sync/singleflight"
)

const (
	SessionPrefix   = "fleet_"
	captureCacheTTL = 400 * time.Millisecond
	captureTimeout  = 3 * time.Second
	sessionCacheTTL = 2 * time.Second
	// listPanesTimeout caps tmux list-panes shell-outs from IsPaneDead /
	// PaneDeadInfo. list-panes is much cheaper than capture-pane, so 2s is
	// generous; the cap exists so an unresponsive tmux server can't hang the
	// status worker (called once per session per tick).
	listPanesTimeout = 2 * time.Second
)

// Session represents a tmux session managed by fleet.
type Session struct {
	Name        string
	DisplayName string
	WorkDir     string

	cacheMu      sync.RWMutex
	cacheContent string
	cacheTime    time.Time
	captureSf    singleflight.Group
}

// Package-level session cache: single tmux list-windows call per tick.
var (
	sessionCacheMu   sync.RWMutex
	sessionCacheData map[string]int64 // session_name -> window_activity timestamp
	sessionCacheTime time.Time
)

// IsTmuxAvailable checks that tmux is installed and reachable.
func IsTmuxAvailable() error {
	cmd := exec.Command("tmux", "-V")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tmux not found: install with 'brew install tmux'")
	}
	return nil
}

// NewSession creates a new Session with a unique tmux name.
func NewSession(displayName, workDir string) *Session {
	sanitized := sanitizeName(displayName)
	shortID := generateShortID()
	return &Session{
		Name:        SessionPrefix + sanitized + "_" + shortID,
		DisplayName: displayName,
		WorkDir:     workDir,
	}
}

// ReconnectSession recreates a Session handle for an existing tmux session.
func ReconnectSession(tmuxName, displayName, workDir string) *Session {
	return &Session{
		Name:        tmuxName,
		DisplayName: displayName,
		WorkDir:     workDir,
	}
}

// Start creates a detached tmux session and runs the given command.
// Optional env vars are set at the tmux session level via -e flags,
// inherited by the shell and all child processes (avoids race with shell plugins).
func (s *Session) Start(command string, env ...string) error {
	// Create detached session.
	args := []string{"new-session", "-d", "-s", s.Name, "-c", s.WorkDir}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	cmd := exec.Command("tmux", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		debuglog.Logger.Error("tmux start failed", "session", s.Name, "workdir", s.WorkDir, "err", err)
		return fmt.Errorf("tmux new-session failed: %s: %w", string(output), err)
	}
	debuglog.Logger.Info("tmux session started", "session", s.Name, "workdir", s.WorkDir)

	// Batch set options.
	// remain-on-exit keeps the dead pane around so the crash dump can read
	// `pane_dead_status` (exit code) and `pane_dead_signal` (terminating
	// signal). Without this we just see "tmux session gone" with no clue
	// what killed claude.
	optArgs := []string{
		"set-option", "-t", s.Name, "mouse", "on", ";",
		"set-option", "-t", s.Name, "history-limit", "10000", ";",
		"set-option", "-t", s.Name, "escape-time", "10", ";",
		"set-option", "-t", s.Name, "allow-passthrough", "on", ";",
		"set-option", "-t", s.Name, "remain-on-exit", "on",
	}
	optCmd := exec.Command("tmux", optArgs...)
	_ = optCmd.Run() // Best effort.

	// Send command to the pane.
	if command != "" {
		sendCmd := exec.Command("tmux", "send-keys", "-t", s.Name, command, "Enter")
		if output, err := sendCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("tmux send-keys failed: %s: %w", string(output), err)
		}
	}

	// Configure status bar.
	s.ConfigureStatusBar()

	// Immediately register in cache.
	sessionCacheMu.Lock()
	if sessionCacheData == nil {
		sessionCacheData = make(map[string]int64)
	}
	sessionCacheData[s.Name] = time.Now().Unix()
	sessionCacheMu.Unlock()

	return nil
}

// ConfigureStatusBar sets up the tmux status bar with detach hint and session info.
func (s *Session) ConfigureStatusBar() {
	folderName := filepath.Base(s.WorkDir)
	rightStatus := fmt.Sprintf("#[fg=#565f89]ctrl+q detach#[default] │ 📁 %s | %s ", s.DisplayName, folderName)
	cmd := exec.Command("tmux",
		"set-option", "-t", s.Name, "status", "on", ";",
		"set-option", "-t", s.Name, "status-style", "bg=#1a1b26,fg=#a9b1d6", ";",
		"set-option", "-t", s.Name, "status-left", " ", ";",
		"set-option", "-t", s.Name, "status-right", rightStatus, ";",
		"set-option", "-t", s.Name, "status-right-length", "80",
	)
	if err := cmd.Run(); err != nil {
		debuglog.Logger.Error("tmux configure status bar failed", "session", s.Name, "err", err)
	}
}

// RespawnPane kills the current pane process and restarts with the given command.
// Optional env vars are set via -e flags on the respawned pane.
func (s *Session) RespawnPane(command string, env ...string) error {
	debuglog.Logger.Info("tmux respawning pane", "session", s.Name, "command", command)
	args := []string{"respawn-pane", "-k", "-t", s.Name + ":"}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, command)
	cmd := exec.Command("tmux", args...)
	if err := cmd.Run(); err != nil {
		debuglog.Logger.Error("tmux respawn failed", "session", s.Name, "err", err)
		return err
	}
	return nil
}

// IsPaneDead checks if the pane's process has exited.
func (s *Session) IsPaneDead() bool {
	ctx, cancel := context.WithTimeout(context.Background(), listPanesTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "list-panes", "-t", s.Name+":0.0", "-F", "#{pane_dead}").Output()
	if err != nil {
		debuglog.Logger.Error("tmux IsPaneDead check failed", "session", s.Name, "err", err)
		return false
	}
	return strings.TrimSpace(string(out)) == "1"
}

// CachedPane returns the most recent pane content captured by CapturePane,
// regardless of cache TTL. Useful for crash dumps where the live tmux session
// may already be gone. Returns "" if nothing has been captured yet.
func (s *Session) CachedPane() string {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	return s.cacheContent
}

// PaneDeadInfo returns whether the pane is dead, plus the exit status and
// signal that killed it (only meaningful with remain-on-exit set when the
// pane terminated). Returns ok=false if the tmux session no longer exists.
//
// Exit status is the integer returned by the process (typical: 0 clean, 1
// generic error). Signal is the POSIX number that terminated the process
// when non-zero (137-128=9 SIGKILL → OOM/manual kill, 134-128=6 SIGABRT →
// panic, 139-128=11 SIGSEGV).
func (s *Session) PaneDeadInfo() (dead bool, exitStatus, exitSignal string, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), listPanesTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "list-panes", "-t", s.Name+":0.0",
		"-F", "#{pane_dead}|#{pane_dead_status}|#{pane_dead_signal}").Output()
	if err != nil {
		return false, "", "", false
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 3)
	if len(parts) != 3 {
		return false, "", "", false
	}
	return parts[0] == "1", parts[1], parts[2], true
}

// Exists checks if the tmux session is alive.
func (s *Session) Exists() bool {
	// Try cache first.
	sessionCacheMu.RLock()
	if sessionCacheData != nil && time.Since(sessionCacheTime) < sessionCacheTTL {
		_, exists := sessionCacheData[s.Name]
		sessionCacheMu.RUnlock()
		return exists
	}
	sessionCacheMu.RUnlock()

	// Fallback to tmux has-session.
	cmd := exec.Command("tmux", "has-session", "-t", s.Name)
	return cmd.Run() == nil
}

// SendKeys sends keystrokes to the tmux pane.
func (s *Session) SendKeys(keys ...string) error {
	debuglog.Logger.Debug("tmux sending keys", "session", s.Name, "keys", keys)
	args := append([]string{"send-keys", "-t", s.Name}, keys...)
	cmd := exec.Command("tmux", args...)
	if err := cmd.Run(); err != nil {
		debuglog.Logger.Error("tmux send-keys failed", "session", s.Name, "keys", keys, "err", err)
		return err
	}
	return nil
}

// SendLiteralKeys sends literal text to the tmux pane (uses -l flag, no key-name interpretation).
func (s *Session) SendLiteralKeys(text string) error {
	debuglog.Logger.Debug("tmux sending literal keys", "session", s.Name, "text", text)
	cmd := exec.Command("tmux", "send-keys", "-t", s.Name, "-l", text)
	if err := cmd.Run(); err != nil {
		debuglog.Logger.Error("tmux send-literal-keys failed", "session", s.Name, "text", text, "err", err)
		return err
	}
	return nil
}

// CapturePaneFresh invalidates the cache before capturing, ensuring fresh output.
func (s *Session) CapturePaneFresh() (string, error) {
	s.cacheMu.Lock()
	s.cacheContent = ""
	s.cacheTime = time.Time{}
	s.cacheMu.Unlock()
	return s.CapturePane()
}

// Kill terminates the tmux session.
func (s *Session) Kill() error {
	debuglog.Logger.Info("tmux killing session", "session", s.Name)
	cmd := exec.Command("tmux", "kill-session", "-t", s.Name)
	if err := cmd.Run(); err != nil {
		debuglog.Logger.Error("tmux kill failed", "session", s.Name, "err", err)
		return fmt.Errorf("tmux kill-session failed: %w", err)
	}

	// Remove from cache.
	sessionCacheMu.Lock()
	delete(sessionCacheData, s.Name)
	sessionCacheMu.Unlock()

	return nil
}

// CapturePane reads the terminal output with caching and singleflight dedup.
func (s *Session) CapturePane() (string, error) {
	// Check cache.
	s.cacheMu.RLock()
	if time.Since(s.cacheTime) < captureCacheTTL && s.cacheContent != "" {
		content := s.cacheContent
		s.cacheMu.RUnlock()
		return content, nil
	}
	s.cacheMu.RUnlock()

	// Singleflight: deduplicate concurrent captures.
	result, err, _ := s.captureSf.Do(s.Name, func() (interface{}, error) {
		ctx, cancel := context.WithTimeout(context.Background(), captureTimeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", s.Name, "-p", "-e")
		output, err := cmd.Output()
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				debuglog.Logger.Error("tmux capture-pane timeout", "session", s.Name)
				// On timeout, return cached content if available.
				s.cacheMu.RLock()
				cached := s.cacheContent
				s.cacheMu.RUnlock()
				if cached != "" {
					return cached, nil
				}
			}
			debuglog.Logger.Error("tmux capture-pane failed", "session", s.Name, "err", err)
			return "", fmt.Errorf("capture-pane failed: %w", err)
		}

		content := string(output)

		// Update cache.
		s.cacheMu.Lock()
		s.cacheContent = content
		s.cacheTime = time.Now()
		s.cacheMu.Unlock()

		return content, nil
	})

	if err != nil {
		return "", err
	}
	return result.(string), nil
}

// RefreshSessionCache makes a single tmux list-windows call and updates the global cache.
func RefreshSessionCache() {
	cmd := exec.Command("tmux", "list-windows", "-a", "-F", "#{session_name}\t#{window_activity}")
	output, err := cmd.Output()
	if err != nil {
		return // tmux server may not be running.
	}

	newCache := make(map[string]int64)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		name := parts[0]
		var activity int64
		fmt.Sscanf(parts[1], "%d", &activity)
		newCache[name] = activity
	}

	sessionCacheMu.Lock()
	sessionCacheData = newCache
	sessionCacheTime = time.Now()
	sessionCacheMu.Unlock()
}

// ListSessions returns all fleet managed tmux session names.
func ListSessions() []string {
	sessionCacheMu.RLock()
	defer sessionCacheMu.RUnlock()

	var sessions []string
	for name := range sessionCacheData {
		if strings.HasPrefix(name, SessionPrefix) {
			sessions = append(sessions, name)
		}
	}
	return sessions
}

// GetActivity returns the cached window activity timestamp for a session.
func (s *Session) GetActivity() (int64, bool) {
	sessionCacheMu.RLock()
	defer sessionCacheMu.RUnlock()
	activity, ok := sessionCacheData[s.Name]
	return activity, ok
}

func generateShortID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	s := b.String()
	// Trim leading/trailing hyphens and collapse multiples.
	s = strings.Trim(s, "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	if s == "" {
		s = "session"
	}
	// Limit length.
	if len(s) > 30 {
		s = s[:30]
	}
	return s
}
