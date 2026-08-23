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
	"sync/atomic"
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
	// hasSessionTimeout caps the `tmux has-session` fallback in Exists. It only
	// runs on a session-cache miss, but that is exactly when the server is
	// likely to be busy — and an unbounded fork here blocked the Bubble Tea
	// Update goroutine for 500ms+, which reads as the whole UI freezing.
	hasSessionTimeout = 2 * time.Second
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

// serverVersion returns the tmux *server's* version as (major, minor).
// Options are interpreted by the server, and on Linux the server and the
// client binary routinely disagree: a package upgrade swaps the binary but
// never restarts a user's running server, so `tmux -V` alone would let a
// 3.4 client wave allow-passthrough through to a still-running 3.2 server —
// aborting the very batch the gate exists to protect. So ask the live
// server first (`display-message -p '#{version}'`, which errors rather than
// spawning a server when none runs) and only fall back to the binary's
// `tmux -V` when there is no server yet — any server started later comes
// from this binary. Development builds ("next-3.5") and parse failures
// report ok=false; callers treat those as new-enough rather than degrade a
// build that is almost certainly ahead of any release.
//
// Memoized: Start calls this per session — a reloadAll of N dead sessions
// would otherwise fork N redundant probes. Memoizing is conservative: the
// binary can't change under a running fleet, so a server restarted mid-run
// can only be newer than the memoized answer, and a stale too-old answer
// merely skips passthrough instead of aborting the batch.
func serverVersion() (major, minor int, ok bool) {
	major, minor, _, ok = serverVersionParts()
	return major, minor, ok
}

// serverVersionParts is serverVersion plus the patch suffix ("a" in "3.5a",
// "-rc2" in "3.4-rc2"), which extendedKeysSafe needs: 3.5 and 3.5a differ only
// there, and one of them ships a broken extended-keys parser.
func serverVersionParts() (major, minor int, suffix string, ok bool) {
	versionOnce.Do(func() {
		out, err := exec.Command("tmux", "display-message", "-p", "#{version}").Output()
		if err != nil {
			out, err = exec.Command("tmux", "-V").Output()
			if err != nil {
				return // leave the zero values: ok=false
			}
		}
		versionMajor, versionMinor, versionSuffix, versionOK = parseTmuxVersionParts(string(out))
	})
	return versionMajor, versionMinor, versionSuffix, versionOK
}

var (
	versionOnce   sync.Once
	versionMajor  int
	versionMinor  int
	versionSuffix string
	versionOK     bool
)

// parseTmuxVersion parses a tmux version string — `tmux -V` output ("tmux
// 3.4") or the bare `#{version}` format variable ("3.4") — split from the
// exec so the spiky inputs (patch letters, dev builds) are testable.
func parseTmuxVersion(out string) (major, minor int, ok bool) {
	major, minor, _, ok = parseTmuxVersionParts(out)
	return major, minor, ok
}

// parseTmuxVersionParts is parseTmuxVersion plus whatever trailed the minor
// number — "a" for "3.5a", "-rc2" for "3.4-rc2", "" for a plain release.
func parseTmuxVersionParts(out string) (major, minor int, suffix string, ok bool) {
	v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out), "tmux"))
	numMajor, rest, found := strings.Cut(v, ".")
	if !found {
		return 0, 0, "", false
	}
	var err error
	major, err = strconv.Atoi(strings.TrimSpace(numMajor))
	if err != nil {
		return 0, 0, "", false
	}
	// Minor may carry a patch-letter or rc suffix: "3a" -> 3, "4-rc2" -> 4.
	digits := rest
	for i, r := range rest {
		if r < '0' || r > '9' {
			digits, suffix = rest[:i], rest[i:]
			break
		}
	}
	minor, err = strconv.Atoi(digits)
	if err != nil {
		return 0, 0, "", false
	}
	return major, minor, suffix, true
}

// supportsAllowPassthrough reports whether the installed tmux understands the
// allow-passthrough option (added in 3.3). On tmux 3.2 and older the option is
// unknown, and because Start batches its set-option calls into one command an
// unknown option would abort the entire batch — mouse, history-limit and
// remain-on-exit included — so it must be dropped, not just tolerated.
// Common on Linux: Ubuntu 22.04 LTS ships tmux 3.2a.
func supportsAllowPassthrough() bool {
	return allowPassthroughSupported(serverVersion())
}

func allowPassthroughSupported(major, minor int, ok bool) bool {
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
	// No local clipboard tool (headless Linux, no wl-copy/xclip/xsel): leave
	// everything alone. tmux's default set-clipboard (external) already sends
	// copy-mode selections to the outer terminal via OSC 52, so the fallback
	// needs no help from fleet — and a user who deliberately hardened with
	// `set-clipboard off` keeps that choice untouched.
	if copyCmd := clipboardCopyCommand(); copyCmd != "" {
		_ = exec.Command("tmux", "set-option", "-s", "copy-command", copyCmd).Run()
	}
}

// clipboardCopyCommand returns the command line tmux should pipe copy-mode
// selections into, or "" when no usable clipboard tool exists. macOS always
// has pbcopy. Linux requires both the tool on PATH *and* its display server
// reachable: wl-copy without WAYLAND_DISPLAY (or xclip/xsel without DISPLAY)
// exits non-zero, so the local pipe delivers nothing. That is not by itself
// data loss — copy-pipe-and-cancel passes set_clip=1, so tmux emits the OSC 52
// selection *in addition to* running copy-command, and the two are independent
// (verified on 3.5a: a copy-command exiting 1 still produced OSC 52). The copy
// is only lost when the user has also set `set-clipboard off`, which suppresses
// OSC 52 entirely. Still worth returning "" rather than a tool that cannot run:
// it keeps the setting honest, and it is the only path that works for the
// set-clipboard-off case. wl-clipboard is a common transitive dependency, so
// "wl-copy on PATH" says nothing about the session type.
//
// Display vars come from fleet's own process environment. tmux runs
// copy-command jobs with the *session* environment (environ_for_session),
// which update-environment refreshes from the most recently attached client
// — not the global table frozen at server start, and there is no one table
// to read a true answer from, since display reachability varies per client
// while copy-command is a single server-wide option. fleet's env describes
// the terminal the user is driving fleet from — the same client its
// sessions get attached from — which makes it the best available proxy.
func clipboardCopyCommand() string {
	hasTool := func(bin string) bool {
		_, err := exec.LookPath(bin)
		return err == nil
	}
	return clipboardCopyCommandFor(runtime.GOOS, os.Getenv, hasTool)
}

// clipboardCopyCommandFor is the pure core of clipboardCopyCommand, split out
// so the routing table is testable without a real PATH or display server.
// Wayland sessions normally export DISPLAY too (XWayland), so checking
// WAYLAND_DISPLAY first keeps the wl-copy preference while still letting a
// Wayland session without wl-clipboard fall through to the X11 tools.
func clipboardCopyCommandFor(goos string, getenv func(string) string, hasTool func(string) bool) string {
	if goos != "linux" {
		return "pbcopy"
	}
	if getenv("WAYLAND_DISPLAY") != "" && hasTool("wl-copy") {
		return "wl-copy"
	}
	if getenv("DISPLAY") != "" {
		if hasTool("xclip") {
			// xclip forks and keeps its inherited stdout open to serve the
			// selection to a future paste; tmux's copy-pipe blocks reading
			// that pipe until it closes, freezing copy-mode until something
			// else takes clipboard ownership. Redirecting stdout closes it
			// immediately (the standard fix — see the tmux FAQ on xclip).
			return "xclip -selection clipboard -in >/dev/null"
		}
		if hasTool("xsel") {
			return "xsel --clipboard --input"
		}
	}
	return ""
}

// EnsureExtendedKeys turns on tmux extended-keys reporting so modified keys —
// most visibly Shift+Enter — reach the agent inside the pane instead of
// collapsing to a bare Enter. With extended-keys off (tmux's default) Shift+Enter
// and plain Enter are the same byte (\r), so Claude Code can't tell them apart and
// submits the message instead of inserting a newline.
//
// Both halves matter: `extended-keys on` makes tmux forward the extended encoding
// to an app that asked for it (Claude asks), and advertising `extkeys` in
// terminal-features tells tmux the outer terminal can carry those sequences —
// which tmux otherwise only believes for the handful of terminals it can name off
// XTVERSION (iTerm2, XTerm, mintty; notably not Ghostty on any shipping release).
// The pane receives `\x1b[27;2;13~`, not CSI-u `\x1b[13;2u`: extended-keys-format
// defaults to `xterm` and is left alone deliberately — Claude accepts both, and
// it's one more shared-server option fleet has no business owning.
// extended-keys and terminal-features are server options (global to the tmux
// server fleet shares, as it runs no dedicated socket); we set extended-keys to
// on unless it's already on/always, and append the extkeys feature only when
// absent (no duplicate entries). Note this also overrides an explicit
// `extended-keys off`: tmux `show-options -sv` returns the effective value, not
// empty-when-unset, so we can't distinguish a deliberate off from the default.
// Set FLEET_NO_EXTENDED_KEYS to opt out entirely — e.g. a terminal whose
// Shift+Enter you've bound differently, a remote tmux you don't want
// reconfigured, or a server where you've deliberately set extended-keys off.
//
// Note: the outer terminal must itself honor xterm's modifyOtherKeys mode 2, so
// enabling extended-keys here is necessary but not sufficient. iTerm2 does it
// unconfigured. kitty never will — it declines modifyOtherKeys by design and tmux
// ignores kitty's own \x1b[>1u request — so kitty needs a manual
// `map shift+enter send_text all \x1b[13;2u`. Claude's /terminal-setup is not the
// answer for these: per Anthropic's docs it targets VS Code/Cursor/Alacritty/Zed.
//
// Skipped on the tmux versions whose extended-keys parser is known broken — see
// extendedKeysSafe. Re-checks the live server each call rather than caching,
// like EnsureCopyCommand: cheap (two show-options), and a server created fresh
// after a restart still gets configured. Called from Start (server guaranteed
// up) + the startup bootstrap. Best-effort; a no-server attempt just returns.
func EnsureExtendedKeys() {
	if envIsTruthy("FLEET_NO_EXTENDED_KEYS") {
		return
	}
	if !extendedKeysSafe(serverVersionParts()) {
		return
	}
	out, err := exec.Command("tmux", "show-options", "-sv", "extended-keys").Output()
	if err != nil {
		return // No server yet (or tmux error) — retry on a later call.
	}
	// Set on unless already on/always. This also overrides an explicit off (see
	// the doc comment); FLEET_NO_EXTENDED_KEYS is the opt-out.
	if v := strings.TrimSpace(string(out)); v != "on" && v != "always" {
		_ = exec.Command("tmux", "set-option", "-s", "extended-keys", "on").Run()
	}
	// Advertise extkeys for xterm-like outer terminals, once. Match the exact
	// `xterm*:extkeys` entry, not a bare `extkeys` substring: a user may already
	// carry extkeys for a different pattern (e.g. `screen*:extkeys`), and we still
	// need to add the xterm entry. terminal-features is additive, so this also
	// guards against piling up the same entry across calls.
	feat, err := exec.Command("tmux", "show-options", "-sv", "terminal-features").Output()
	if err == nil && !strings.Contains(string(feat), "xterm*:extkeys") {
		_ = exec.Command("tmux", "set-option", "-sa", "terminal-features", "xterm*:extkeys").Run()
	}
}

// extendedKeysSafe reports whether this tmux can be asked for extended keys
// without shipping a known upstream key-parsing bug along with the feature.
// Two releases can't:
//
//   - exactly 3.5: Alt+Backspace emits invalid bytes (\x1b\xf4\x8e\x86\x94) and
//     Shift-modified keys are mis-encoded — tmux#4146, tmux#4156, both fixed in
//     3.5a, which is the only thing separating the two versions.
//   - 3.6, 3.6a, 3.6b: a fast key burst trips the assume-paste path, which
//     writes the outer terminal's raw bytes through verbatim, so literal
//     ^[[27;5;102~ lands in the pane — tmux#5031, fixed in 3.7. Ubuntu 26.04
//     ships 3.6a.
//
// Both live in the code that parses extended keys *arriving from the outer
// terminal*, which is unreachable while extended-keys is off — tmux's default.
// So these are bugs fleet would be handing users, not ones they already have.
// Skipping rather than warning is deliberate: extended-keys is a server option,
// so the damage would land in the user's own tmux windows, not just fleet's
// panes, and nothing there would point back at fleet.
//
// An unparseable version (dev build) is treated as new enough, matching
// allowPassthroughSupported. tmux older than 3.2 has no extended-keys option at
// all; it needs no gate here because EnsureExtendedKeys's show-options probe
// already errors out and returns.
func extendedKeysSafe(major, minor int, suffix string, ok bool) bool {
	if !ok || major != 3 {
		return true
	}
	switch minor {
	case 5:
		// A patch letter means 3.5a or later. An rc suffix does not.
		return len(suffix) == 1 && suffix[0] >= 'a' && suffix[0] <= 'z'
	case 6:
		return false
	}
	return true
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
	// Forward extended keys (Shift+Enter et al.) into the pane instead of
	// collapsing them to plain Enter.
	EnsureExtendedKeys()

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

// ExistsCached answers Exists from the shared session cache alone, never
// shelling out. known is false when the cache is cold or has aged past
// sessionCacheTTL, leaving it to the caller to decide what an unknown means.
//
// Callers on the Bubble Tea Update goroutine must use this rather than Exists:
// RefreshSessionCache runs on the status worker, so anything that blocks that
// worker lets the cache go stale, and Exists would then fork tmux inline on
// every tick. That is the shape of the previewTick stalls in the perfwatch
// dumps — a busy worker turning a map lookup into a half-second freeze.
func (s *Session) ExistsCached() (exists, known bool) {
	sessionCacheMu.RLock()
	defer sessionCacheMu.RUnlock()
	if sessionCacheData == nil || time.Since(sessionCacheTime) >= sessionCacheTTL {
		return false, false
	}
	_, ok := sessionCacheData[s.Name]
	return ok, true
}

// existsStale answers from the session cache ignoring its TTL. Only for the
// timeout path in Exists, where a stale-but-real answer beats a fabricated one.
func (s *Session) existsStale() (exists, known bool) {
	sessionCacheMu.RLock()
	defer sessionCacheMu.RUnlock()
	if sessionCacheData == nil {
		return false, false
	}
	_, ok := sessionCacheData[s.Name]
	return ok, true
}

// Exists checks if the tmux session is alive, falling back to a bounded
// `tmux has-session` when the cache can't answer.
//
// Only a probe that actually completed may report absence. Every caller reads
// false as "the session is gone" and acts on it — restartSession rebuilds
// instead of respawning, `fleet send` refuses a live session, finalizeDelete
// skips its Kill and leaks the tmux session — and a timeout is not evidence of
// any of that. It is evidence of a busy server, which is precisely the state
// this timeout exists to survive, so a timed-out probe answers from the cache
// however stale it has become, and says so: a silent false negative here would
// be undiagnosable from the outside.
func (s *Session) Exists() bool {
	if exists, known := s.ExistsCached(); known {
		return exists
	}

	ctx, cancel := context.WithTimeout(context.Background(), hasSessionTimeout)
	defer cancel()
	err := exec.CommandContext(ctx, "tmux", "has-session", "-t", s.Name).Run()
	if ctx.Err() == nil {
		return err == nil
	}

	exists, known := s.existsStale()
	debuglog.Logger.Warn("tmux has-session timed out; not treating it as absence",
		"session", s.Name, "timeout", hasSessionTimeout,
		"answered_from_stale_cache", known, "exists", exists)
	return exists
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

// sendBufferSeq distinguishes concurrent PasteAndSubmit calls within one
// process; the pid in the name separates them across processes.
var sendBufferSeq atomic.Uint64

// PasteAndSubmit types text into the pane as a paste and then presses Enter.
//
// Deliberately not SendLiteralKeys: that types the text a byte at a time, so
// every newline in a multi-line message reaches the agent as its own Enter and
// submits a partial message (and, in a pane sitting at a shell, would run each
// line). load-buffer + `paste-buffer -p` hands the whole message over inside
// bracketed-paste markers, which every agent TUI treats as one block. The -p
// markers are only emitted when the pane's application asked for bracketed
// paste, so a pane that didn't is left with plain text rather than escape
// sequences on screen.
//
// Enter is a separate call on purpose — it must land after the paste's closing
// marker for the agent to read it as "submit" rather than as pasted content.
func (s *Session) PasteAndSubmit(text string) error {
	// The buffer name must be unique per *call*, not per target session: two
	// concurrent sends to one session are exactly the case that shares a name,
	// and they interleave as load(A) → load(B) → paste -d(A pastes B's text and
	// drops the buffer) → paste(B) fails. A's message is silently replaced by
	// B's while A reports success and B — whose text actually landed — reports
	// an error. pid + counter makes each call's buffer its own.
	buf := fmt.Sprintf("fleet-send-%d-%d", os.Getpid(), sendBufferSeq.Add(1))

	load := exec.Command("tmux", "load-buffer", "-b", buf, "-")
	load.Stdin = strings.NewReader(text)
	if out, err := load.CombinedOutput(); err != nil {
		debuglog.Logger.Error("tmux load-buffer failed", "session", s.Name, "err", err)
		return fmt.Errorf("tmux load-buffer failed: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// -d drops the buffer once pasted, so a message never lingers in the shared
	// tmux server's buffer stack where a later paste could resurrect it.
	paste := exec.Command("tmux", "paste-buffer", "-d", "-p", "-b", buf, "-t", s.Name)
	if out, err := paste.CombinedOutput(); err != nil {
		// -d never ran, so delete the buffer here instead of leaving the text behind.
		_ = exec.Command("tmux", "delete-buffer", "-b", buf).Run()
		debuglog.Logger.Error("tmux paste-buffer failed", "session", s.Name, "err", err)
		return fmt.Errorf("tmux paste-buffer failed: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return s.SendKeys("Enter")
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
// DetachClient detaches every client attached to this session, returning the
// user to whatever they were in before. The session itself keeps running.
func (s *Session) DetachClient() error {
	ctx, cancel := context.WithTimeout(context.Background(), captureTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "tmux", "detach-client", "-s", s.Name).Run()
}

// CapturePaneJoined captures the pane with wrapped lines rejoined and the full
// scrollback included, returning plain text.
//
// Separate from CapturePane because the two want opposite things. Status
// detection needs `-e` (ANSI preserved) and only the visible screen. Reading a
// *value* off a pane needs the opposite: `-J` so a string longer than the pane
// is one line instead of several, `-S -` so it still resolves after scrolling,
// and no `-e` so escape bytes can't land mid-value.
//
// Without -J this silently truncates at every pane width — the prompt consumes
// columns before the value even starts, so even a pane wider than the value
// cuts it. Uncached and unsingleflighted: callers read a pane once, deliberately.
func (s *Session) CapturePaneJoined() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), captureTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", s.Name, "-p", "-J", "-S", "-")
	output, err := cmd.Output()
	if err != nil {
		debuglog.Logger.Error("tmux capture-pane -J failed", "session", s.Name, "err", err)
		return "", fmt.Errorf("capture-pane failed: %w", err)
	}
	return string(output), nil
}

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
