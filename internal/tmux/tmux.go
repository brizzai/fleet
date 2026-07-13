package tmux

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
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

// Package-level session cache: a single `tmux list-panes -a` call per tick
// populates both maps, so per-session Exists/GetActivity/IsPaneDead lookups are
// served from memory instead of one shell-out each.
var (
	sessionCacheMu   sync.RWMutex
	sessionCacheData map[string]int64  // session_name -> window_activity timestamp
	sessionDeadData  map[string]bool   // session_name -> pane_dead
	sessionCmdData   map[string]string // session_name -> pane_current_command (foreground proc)
	sessionPidData   map[string]int    // session_name -> pane_pid (the pane's shell process)
	sessionCacheTime time.Time
)

// IsTmuxAvailable checks that tmux is installed and reachable.
func IsTmuxAvailable() error {
	cmd := exec.Command("tmux", "-V")
	if err := cmd.Run(); err != nil {
		hint := "brew install tmux"
		if runtime.GOOS == "linux" {
			hint = "apt install tmux (or your distro's equivalent)"
		}
		return fmt.Errorf("tmux not found: install with '%s'", hint)
	}
	return nil
}

// serverVersion returns tmux's version as (major, minor), parsed from
// `tmux -V` output like "tmux 3.4" or "tmux 3.3a". Development builds
// ("tmux next-3.5", "tmux master") and parse failures report ok=false;
// callers should treat those as new-enough rather than degrade a build
// that is almost certainly ahead of any release.
func serverVersion() (major, minor int, ok bool) {
	out, err := exec.Command("tmux", "-V").Output()
	if err != nil {
		return 0, 0, false
	}
	v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "tmux"))
	numMajor, rest, found := strings.Cut(v, ".")
	if !found {
		return 0, 0, false
	}
	major, err = strconv.Atoi(strings.TrimSpace(numMajor))
	if err != nil {
		return 0, 0, false
	}
	// Minor may carry a patch-letter suffix: "3a" -> 3.
	digits := rest
	for i, r := range rest {
		if r < '0' || r > '9' {
			digits = rest[:i]
			break
		}
	}
	minor, err = strconv.Atoi(digits)
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// supportsAllowPassthrough reports whether the installed tmux understands the
// allow-passthrough option (added in 3.3). On tmux 3.2 and older the option is
// unknown, and because Start batches its set-option calls into one command an
// unknown option would abort the entire batch — mouse, history-limit and
// remain-on-exit included — so it must be dropped, not just tolerated.
// Common on Linux: Ubuntu 22.04 LTS ships tmux 3.2a.
func supportsAllowPassthrough() bool {
	major, minor, ok := serverVersion()
	if !ok {
		return true // dev build or unparseable: assume modern
	}
	return major > 3 || (major == 3 && minor >= 3)
}

// EnsureCopyCommand points tmux's copy-command at the platform clipboard tool
// so a copy-mode selection (mouse drag, double/triple-click, keyboard copy)
// reaches the system clipboard. tmux's default copy bindings run
// `copy-pipe-and-cancel` with no argument, which pipes to copy-command; when
// that's unset they fall back to OSC 52 — which iTerm2 blocks by default and
// Apple Terminal doesn't support — so the selection never reaches the
// clipboard. A local pipe works on every terminal regardless of OSC 52.
//
// copy-command is a server option (global to the tmux server fleet shares, as
// it runs no dedicated socket); we set it only when unset, leaving a user's own
// copy-command untouched. Set FLEET_NO_COPY_COMMAND to opt out entirely — e.g.
// when you deliberately rely on OSC 52 (a remote tmux copying to a local
// clipboard over SSH).
//
// Re-checks the live server on every call rather than caching: cheap (one
// show-options), and a tmux server created fresh after a restart still gets
// the option. Called from Start (server guaranteed up) + the startup
// bootstrap. Best-effort; a no-server attempt just returns.
func EnsureCopyCommand() {
	if envIsTruthy("FLEET_NO_COPY_COMMAND") {
		return
	}
	out, err := exec.Command("tmux", "show-options", "-sv", "copy-command").Output()
	if err != nil {
		return // No server yet (or tmux error) — retry on a later call.
	}
	if strings.TrimSpace(string(out)) != "" {
		return // User already has a copy-command; leave it alone.
	}
	if copyCmd := clipboardCopyCommand(); copyCmd != "" {
		_ = exec.Command("tmux", "set-option", "-s", "copy-command", copyCmd).Run()
		return
	}
	// No local clipboard tool (headless Linux, no wl-copy/xclip/xsel): enable
	// set-clipboard so copy-mode falls back to OSC 52 and terminals that
	// support it (most modern Linux terminals, and anything over SSH) still
	// get the selection.
	_ = exec.Command("tmux", "set-option", "-s", "set-clipboard", "on").Run()
}

// clipboardCopyCommand returns the command line tmux should pipe copy-mode
// selections into, or "" when no local clipboard tool exists. macOS always has
// pbcopy. Linux picks the first tool present, preferring Wayland's wl-copy
// (wl-clipboard) over the X11 tools; under XWayland either works, and both
// xclip and xsel are widespread on X11 desktops.
func clipboardCopyCommand() string {
	if runtime.GOOS != "linux" {
		return "pbcopy"
	}
	for _, c := range [...]struct{ bin, cmdline string }{
		{"wl-copy", "wl-copy"},
		{"xclip", "xclip -selection clipboard -in"},
		{"xsel", "xsel --clipboard --input"},
	} {
		if _, err := exec.LookPath(c.bin); err == nil {
			return c.cmdline
		}
	}
	return ""
}

// envIsTruthy reports whether the named env var is set to a common truthy value.
func envIsTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

// DirAccessBlocked reports whether the tmux server is blocked by macOS from
// reading dir — the TCC "Operation not permitted" wall that hits ~/Documents,
// ~/Desktop and ~/Downloads. The check must run *inside* the tmux server: it
// daemonizes (double-fork + setsid) into its own TCC "responsible process", so a
// pane there can be denied a folder that fleet's own process (a child of the
// granted terminal) reads fine — probing from Go would give the wrong answer.
//
// Uses `tmux run-shell`, which executes the command from the server process, and
// captures `ls`'s stderr via a temp file (run-shell's output isn't returned to
// the CLI). Returns (blocked, determined): a folder counts as blocked only when
// stderr carries the specific EPERM "Operation not permitted" signature, so an
// empty stderr (accessible) or any other failure (deleted root, transient error)
// never raises a false alarm. determined is false when the probe couldn't run a
// conclusive check (no server yet, tmux error, unreadable result) — the caller
// treats that as unknown and re-probes later rather than caching a false answer.
// Set FLEET_NO_TCC_WARNING to skip the probe entirely (e.g. a remote tmux over
// SSH where the block is expected).
func DirAccessBlocked(dir string) (blocked, determined bool) {
	if dir == "" || envIsTruthy("FLEET_NO_TCC_WARNING") {
		return false, true
	}
	f, err := os.CreateTemp("", "fleet-tcc-*")
	if err != nil {
		return false, false
	}
	tmpPath := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	// run-shell runs in the daemonized server context; ls enumerates dir exactly
	// as the failing `ls` does, and its stderr (captured to the temp file) carries
	// the EPERM signature when TCC denies it.
	script := fmt.Sprintf("ls %s >/dev/null 2>%s", shellQuote(dir), shellQuote(tmpPath))
	ctx, cancel := context.WithTimeout(context.Background(), listPanesTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, "tmux", "run-shell", script).Run(); err != nil {
		return false, false // no server yet, or tmux error — inconclusive.
	}
	stderr, err := os.ReadFile(tmpPath)
	if err != nil {
		return false, false
	}
	return strings.Contains(string(stderr), "Operation not permitted"), true
}

// shellQuote wraps s in single quotes for safe interpolation into a /bin/sh
// command, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// NewSession creates a new Session with a unique tmux name.
func NewSession(displayName, workDir string) *Session {
	return NewSessionWithPrefix(SessionPrefix, displayName, workDir)
}

// NewSessionWithPrefix is like NewSession but lets callers pick the tmux name
// prefix. Used by the shells feature (prefix "fleetsh_") so shell sessions are
// distinguishable from agent sessions ("fleet_") in `list-panes -a`.
func NewSessionWithPrefix(prefix, displayName, workDir string) *Session {
	sanitized := sanitizeName(displayName)
	shortID := generateShortID()
	return &Session{
		Name:        prefix + sanitized + "_" + shortID,
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

	// Route copy-mode selections to the macOS clipboard (server exists now).
	EnsureCopyCommand()

	// Batch set options.
	// remain-on-exit keeps the dead pane around so the crash dump can read
	// `pane_dead_status` (exit code) and `pane_dead_signal` (terminating
	// signal). Without this we just see "tmux session gone" with no clue
	// what killed claude.
	optArgs := []string{
		"set-option", "-t", s.Name, "mouse", "on", ";",
		"set-option", "-t", s.Name, "history-limit", "10000", ";",
		"set-option", "-t", s.Name, "escape-time", "10", ";",
	}
	// allow-passthrough needs tmux ≥ 3.3; on older servers the unknown option
	// would abort this whole batched command, so it is skipped instead.
	if supportsAllowPassthrough() {
		optArgs = append(optArgs, "set-option", "-t", s.Name, "allow-passthrough", "on", ";")
	} else {
		debuglog.Logger.Warn("tmux < 3.3: allow-passthrough unavailable, skipping", "session", s.Name)
	}
	optArgs = append(optArgs, "set-option", "-t", s.Name, "remain-on-exit", "on")
	optCmd := exec.Command("tmux", optArgs...)
	_ = optCmd.Run() // Best effort.

	// Send command to the pane.
	if command != "" {
		sendCmd := exec.Command("tmux", "send-keys", "-t", s.Name, command, "Enter")
		if output, err := sendCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("tmux send-keys failed: %s: %w", string(output), err)
		}
	}

	// Lay down a baseline status bar with default colours so the pane has
	// readable chrome from the first frame. The UI worker re-applies with
	// the active fleet theme + live state on the next tick.
	s.ApplyStatusBar(StatusBarOpts{})

	// Immediately register in cache.
	sessionCacheMu.Lock()
	if sessionCacheData == nil {
		sessionCacheData = make(map[string]int64)
	}
	if sessionDeadData == nil {
		sessionDeadData = make(map[string]bool)
	}
	if sessionCmdData == nil {
		sessionCmdData = make(map[string]string)
	}
	sessionCacheData[s.Name] = time.Now().Unix()
	sessionDeadData[s.Name] = false
	sessionCmdData[s.Name] = "" // unknown until first refresh; "" reads as idle
	sessionCacheMu.Unlock()

	return nil
}

// StatusBarOpts holds the theme + content inputs for ApplyStatusBar. All
// colors are tmux-format hex strings (#RRGGBB); leaving any color blank
// falls back to a sane default.
type StatusBarOpts struct {
	// Theme.
	StripBg     string // status bar background — should be brighter than pane bg so the strip is visibly distinct
	StripFg     string // primary text on the strip
	Dim         string // secondary text (separators, detach hint)
	BorderColor string // tmux pane-border-style (inactive pane outline)
	AccentColor string // tmux pane-active-border-style + brand emphasis

	// Content shown on the bottom status bar.
	Origin    string // e.g. "brizzai/fleet" — left side, brand-coloured
	PRSummary string // e.g. "PR #100 (CI passing, 3 unresolved)" — middle, semantic colour
	PRColor   string // hex for the PR segment (green/yellow/red/purple). Defaults to Dim.

	// Content shown on the always-visible pane-border header (top of pane).
	DisplayName string // session title
	Branch      string
	Path        string // shortened to "~/..." by the caller

	// Detach hint, rendered dim on the bottom-right of the status bar.
	DetachHint string
}

// ApplyStatusBar repaints the tmux session chrome using the supplied opts.
// Idempotent and cheap (a single batched set-option call) so callers can
// re-apply on every status change without worry.
//
// Layout — three sides of chrome around the pane content:
//
//	───── 📁 <session-title> · <branch> ──────────── ~/code/<path> ─────
//	... session content ...
//	  <origin>  ·  PR #N (status)                         ctrl+q detach
//	  Branch lifecycle / additional context                   (line 2)
//
// Vertical sides are a tmux limitation — pane borders only draw *between*
// panes, so a single-pane session can't get left/right edges. We give it
// the most chrome tmux allows: a top horizontal rule (pane-border-status
// top, with the session identity inset into it) and a two-line bottom
// status bar with origin/PR/path/detach.
func (s *Session) ApplyStatusBar(o StatusBarOpts) {
	if o.StripBg == "" {
		o.StripBg = "#1a1b26"
	}
	if o.StripFg == "" {
		o.StripFg = "#a9b1d6"
	}
	if o.Dim == "" {
		o.Dim = "#565f89"
	}
	if o.BorderColor == "" {
		o.BorderColor = o.Dim
	}
	if o.AccentColor == "" {
		o.AccentColor = o.StripFg
	}
	if o.PRColor == "" {
		o.PRColor = o.Dim
	}
	if o.DetachHint == "" {
		o.DetachHint = "ctrl+q detach"
	}
	if o.DisplayName == "" {
		o.DisplayName = s.DisplayName
	}

	// Top border header carries ALL the useful info: session identity, origin,
	// PR status, branch (left to right by importance) and path right-aligned.
	// The bottom strip is reserved for the detach hint only.
	// User-derived fields are escaped so a literal `#` (e.g. in a branch
	// name like `fix/#123`) doesn't look like the start of a tmux format
	// sequence to the parser.
	topLeftParts := []string{}
	if o.DisplayName != "" {
		topLeftParts = append(topLeftParts, fmt.Sprintf("#[fg=%s,bold]📁 %s", o.StripFg, escapeTmuxFormat(o.DisplayName)))
	}
	if o.Origin != "" {
		topLeftParts = append(topLeftParts, fmt.Sprintf("#[fg=%s,bold]%s", o.AccentColor, escapeTmuxFormat(o.Origin)))
	}
	if o.Branch != "" {
		topLeftParts = append(topLeftParts, fmt.Sprintf("#[fg=%s,nobold]%s", o.StripFg, escapeTmuxFormat(o.Branch)))
	}
	if o.PRSummary != "" {
		topLeftParts = append(topLeftParts, fmt.Sprintf("#[fg=%s,nobold]%s", o.PRColor, escapeTmuxFormat(o.PRSummary)))
	}
	paneFmt := " " + strings.Join(topLeftParts, fmt.Sprintf(" #[fg=%s]·#[default] ", o.Dim)) + " "
	if o.Path != "" {
		paneFmt += fmt.Sprintf("#[align=right,fg=%s]%s ", o.Dim, escapeTmuxFormat(o.Path))
	}

	// Bottom: detach hint rendered in the regular fleet hotkey style —
	// accent-coloured key + plain-text description, left-aligned. Matches
	// the contextual footer in the main TUI so muscle memory carries over.
	key, desc, _ := strings.Cut(o.DetachHint, " ")
	if desc == "" {
		desc = "detach"
		key = o.DetachHint
	}
	bottomLeft := fmt.Sprintf(" #[fg=%s,bold]%s #[fg=%s,nobold]%s",
		o.AccentColor, escapeTmuxFormat(key), o.StripFg, escapeTmuxFormat(desc))

	// On Warp, warn (bottom-right) that drag-to-copy can't work — Warp doesn't
	// deliver mouse drag-selection to terminal apps, so it's unfixable from our
	// side. Amber so it stands out against any theme.
	bottomRight, bottomRightLen := "", "0"
	if isWarpTerminal() {
		bottomRight = "#[fg=#e0af68,bold]⚠ #[nobold]Warp doesn't support drag-to-copy (Warp bug) "
		bottomRightLen = "48"
	}

	// Bounded shell-out — a stalled tmux server can't hang session startup
	// or the periodic refresh path. Matches the pattern used by IsPaneDead /
	// PaneDeadInfo elsewhere in this file.
	ctx, cancel := context.WithTimeout(context.Background(), listPanesTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux",
		"set-option", "-t", s.Name, "status", "on", ";",
		"set-option", "-t", s.Name, "status-style", fmt.Sprintf("bg=%s,fg=%s", o.StripBg, o.StripFg), ";",
		"set-option", "-t", s.Name, "status-justify", "left", ";",
		"set-option", "-t", s.Name, "status-left", bottomLeft, ";",
		"set-option", "-t", s.Name, "status-left-length", "40", ";",
		"set-option", "-t", s.Name, "status-right", bottomRight, ";",
		"set-option", "-t", s.Name, "status-right-length", bottomRightLen, ";",
		"set-option", "-t", s.Name, "window-status-current-format", "", ";",
		"set-option", "-t", s.Name, "window-status-format", "", ";",
		// Top horizontal rule with all the orientation info.
		"set-option", "-t", s.Name, "pane-border-status", "top", ";",
		"set-option", "-t", s.Name, "pane-border-format", paneFmt, ";",
		"set-option", "-t", s.Name, "pane-border-style", fmt.Sprintf("fg=%s", o.BorderColor), ";",
		"set-option", "-t", s.Name, "pane-active-border-style", fmt.Sprintf("fg=%s", o.AccentColor),
	)
	if err := cmd.Run(); err != nil {
		debuglog.Logger.Error("tmux apply status bar failed", "session", s.Name, "err", err)
	}
}

// isWarpTerminal reports whether fleet is running inside Warp. Warp has a
// long-standing bug where it doesn't hand mouse drag-selection to terminal
// apps, so drag-to-copy can't work in an attached session there and no
// tmux-side fix exists (see the warning surfaced in the status bar).
//
// Detected from the fleet process's own env — the terminal fleet was launched
// from. fleet attaches via a PTY inside this same process, so the launch
// terminal IS the attach terminal for the normal flow, and the warning is
// accurate. The one case it can't see is a manual `tmux attach` from a
// different terminal (out of fleet's control); detecting the attach terminal
// from the status bar isn't feasible, so we accept that edge.
func isWarpTerminal() bool {
	return os.Getenv("TERM_PROGRAM") == "WarpTerminal"
}

// escapeTmuxFormat doubles literal `#` so user-derived strings embedded in
// `status-left` / `pane-border-format` don't get parsed as tmux format
// sequences (e.g. a branch like `fix/#123` would otherwise look like the
// start of `#{...}`). Tmux treats `##` as a literal `#`.
func escapeTmuxFormat(s string) string {
	return strings.ReplaceAll(s, "#", "##")
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

// IsPaneDead checks if the pane's process has exited. It reads the per-tick
// batch cache (populated by RefreshSessionCache via a single `list-panes -a`)
// when fresh, falling back to a live list-panes call for sessions the batch
// hasn't seen yet.
func (s *Session) IsPaneDead() bool {
	sessionCacheMu.RLock()
	if sessionDeadData != nil && time.Since(sessionCacheTime) < sessionCacheTTL {
		if dead, ok := sessionDeadData[s.Name]; ok {
			sessionCacheMu.RUnlock()
			return dead
		}
	}
	sessionCacheMu.RUnlock()

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
	delete(sessionDeadData, s.Name)
	delete(sessionCmdData, s.Name)
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

// SeedCapture pre-fills the capture cache with content obtained out-of-band
// (e.g. a batched capture), so a subsequent CapturePane() within captureCacheTTL
// returns it without shelling out. Empty content is ignored — CapturePane treats
// "" as a cache miss, so a missing/aborted batch entry falls back to a live
// per-session capture automatically.
func (s *Session) SeedCapture(content string) {
	if content == "" {
		return
	}
	s.cacheMu.Lock()
	s.cacheContent = content
	s.cacheTime = time.Now()
	s.cacheMu.Unlock()
}

// BatchCapturePanes captures several sessions' panes in a single tmux client
// invocation, chaining one capture-pane per name and delimiting each with a
// high-entropy sentinel emitted by display-message. It returns name->content for
// every session captured cleanly. A dead/missing pane aborts the tmux command
// chain (subsequent captures never run), so callers MUST treat any name absent
// from the result as "not captured" and fall back to a per-session capture —
// the cache-seeding path does this for free (a cold cache → live CapturePane).
func BatchCapturePanes(names []string) map[string]string {
	if len(names) == 0 {
		return nil
	}
	sentinel := captureSentinel()
	args := make([]string, 0, len(names)*10)
	for i, n := range names {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args,
			"capture-pane", "-p", "-e", "-t", n,
			";", "display-message", "-p", sentinel+"\t"+n)
	}

	ctx, cancel := context.WithTimeout(context.Background(), captureTimeout)
	defer cancel()
	// Output() returns whatever reached stdout before a non-zero exit, so a
	// mid-chain abort still yields the panes captured up to that point.
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	parsed := parseBatchCapture(string(out), sentinel)
	if err != nil && len(parsed) < len(names) {
		debuglog.Logger.Debug("tmux batch capture partial; missing sessions fall back to live capture",
			"want", len(names), "got", len(parsed))
	}
	return parsed
}

// parseBatchCapture splits BatchCapturePanes stdout into name->content. Each
// pane's raw capture bytes precede its "<sentinel>\t<name>\n" marker, so slicing
// on the sentinel preserves content exactly (including trailing newlines and
// ANSI escapes). Trailing bytes after the last marker — produced when the chain
// aborted before emitting that session's marker — are dropped.
func parseBatchCapture(stdout, sentinel string) map[string]string {
	out := make(map[string]string)
	token := sentinel + "\t"
	rest := stdout
	for {
		idx := strings.Index(rest, token)
		if idx < 0 {
			break
		}
		content := rest[:idx]
		rest = rest[idx+len(token):]
		name := rest
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			name = rest[:nl]
			rest = rest[nl+1:]
		} else {
			rest = ""
		}
		out[name] = content
	}
	return out
}

// captureSentinel returns a 32-hex-char nonce used to delimit batched captures.
// The entropy makes accidental collision with real pane content negligible.
func captureSentinel() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// RefreshSessionCache makes a single `tmux list-panes -a` call and updates the
// global cache with each session's window activity and pane-dead state. One
// call feeds Exists, GetActivity and IsPaneDead for every session, so the
// status worker never shells out per-session for these.
func RefreshSessionCache() {
	// pane_current_command stays LAST because a dead pane reports it empty, so its
	// line ends in a trailing tab; keeping the always-present pane_pid ahead of it
	// preserves the "possibly-empty trailing field" parsing below.
	cmd := exec.Command("tmux", "list-panes", "-a", "-F", "#{session_name}\t#{window_activity}\t#{pane_dead}\t#{pane_pid}\t#{pane_current_command}")
	output, err := cmd.Output()
	if err != nil {
		return // tmux server may not be running.
	}

	activityCache := make(map[string]int64)
	deadCache := make(map[string]bool)
	pidCache := make(map[string]int)
	cmdCache := make(map[string]string)
	// Do NOT TrimSpace the whole output: a dead pane has an empty
	// pane_current_command, so its line ends in a trailing tab ("name\t<act>\t1\t<pid>\t").
	// Trimming the final line's tab would make SplitN yield 4 fields, dropping a
	// dead pane that happens to be last. The line == "" guard handles blanks.
	for _, line := range strings.Split(string(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) < 5 {
			continue
		}
		name := parts[0]
		var activity int64
		fmt.Sscanf(parts[1], "%d", &activity)
		activityCache[name] = activity
		deadCache[name] = parts[2] == "1"
		var pid int
		fmt.Sscanf(parts[3], "%d", &pid)
		pidCache[name] = pid
		cmdCache[name] = parts[4]
	}

	sessionCacheMu.Lock()
	sessionCacheData = activityCache
	sessionDeadData = deadCache
	sessionPidData = pidCache
	sessionCmdData = cmdCache
	sessionCacheTime = time.Now()
	sessionCacheMu.Unlock()
}

// PaneCurrentCommand returns the cached foreground command for a session's pane
// (e.g. "zsh", "node", "vite"), or "" when unknown (not in the last refresh).
// Cache-only — no shell-out; RefreshSessionCache populates it once per tick.
// Used by the shells feature to tell idle (shell prompt) from running (a
// process executing in the pane).
func PaneCurrentCommand(name string) string {
	sessionCacheMu.RLock()
	defer sessionCacheMu.RUnlock()
	if sessionCmdData == nil {
		return ""
	}
	return sessionCmdData[name]
}

// PanePID returns the cached pane process id (the pane's shell) for a session,
// or 0 when unknown. Cache-only; RefreshSessionCache populates it once per tick.
// Used by the shells feature to find each shell's foreground child process (the
// command it's running) via proc.ForegroundCommands.
func PanePID(name string) int {
	sessionCacheMu.RLock()
	defer sessionCacheMu.RUnlock()
	if sessionPidData == nil {
		return 0
	}
	return sessionPidData[name]
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
