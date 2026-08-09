package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"text/tabwriter"

	tea "charm.land/bubbletea/v2"
	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/migration"
	"github.com/brizzai/fleet/internal/perfwatch"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/termkeys"
	"github.com/brizzai/fleet/internal/tmux"
	"github.com/brizzai/fleet/internal/ui"
	"github.com/brizzai/fleet/internal/update"
)

// version is set via -ldflags at build time. GoReleaser populates this automatically.
var version = "dev"

func init() {
	// Aliasing must happen before any subcommand runs: hook-handler subprocesses
	// inherited BRIZZCODE_INSTANCE_ID from the legacy TUI and need it visible
	// under FLEET_INSTANCE_ID. Cheap, env-only.
	migration.AliasLegacyEnv()
}

func main() {
	args := os.Args[1:]

	if len(args) == 0 {
		runTUI()
		return
	}

	// Chrome launches native messaging hosts with chrome-extension://... as the sole argument.
	// Detect this and route to chrome-host handler.
	if strings.HasPrefix(args[0], "chrome-extension://") {
		handleChromeHost()
		return
	}

	switch args[0] {
	case "add":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: fleet add <path>")
			os.Exit(1)
		}
		runAdd(args[1])
	case "list", "ls":
		runList()
	case "remove", "rm":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: fleet remove <id>")
			os.Exit(1)
		}
		runRemove(args[1])
	case "worktree", "wt":
		runWorktree(args[1:])
	case "hook-handler":
		handleHookHandler()
	case "chrome-host":
		handleChromeHost()
	case "hooks":
		handleHooksCmd(args[1:])
	case "update":
		runUpdate()
	case "version", "--version", "-v":
		fmt.Printf("fleet %s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

func runTUI() {
	// Run filesystem/tmux/hook migration before debuglog.Init creates ~/.config/fleet/.
	// migration.Run is a no-op after the first successful invocation.
	migration.Run()

	debuglog.Init()
	defer debuglog.Close()
	perfwatch.Init()
	debuglog.Logger.Info("fleet TUI starting", "version", version)

	// Bubble Tea prints loop/cmd panics to the terminal only, and a panic in
	// this goroutine outside p.Run() would never reach debug.log at all. Record
	// it before unwinding, then re-panic so the exit code and stack-to-terminal
	// behavior are unchanged.
	defer func() {
		if r := recover(); r != nil {
			debuglog.Logger.Error("fatal panic", "panic", r, "stack", string(debug.Stack()))
			panic(r)
		}
	}()

	if err := tmux.IsTmuxAvailable(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cfg := config.Load()

	// Analytics is initialized inside the TUI, after the first-launch
	// consent prompt is answered. We still defer Shutdown unconditionally
	// because Shutdown is a no-op when Init was never called.
	defer analytics.Shutdown()

	// Auto-update: check for newer version on launch.
	if cfg.IsAutoUpdateEnabled() && version != "dev" && update.ShouldCheck() {
		debuglog.Logger.Info("checking for updates", "current", version)
		newVer, err := update.Update(version)
		// A package-managed install can never swap its own binary. That's a
		// permanent property of how fleet was installed, not a failure, so it
		// is not counted as an update error — otherwise every packaged Linux
		// install reports a broken updater once an hour, forever.
		skipped := errors.Is(err, update.ErrNotReplaceable)
		// Record update health for analytics. The updater runs before
		// analytics.Init (which waits on the consent prompt inside the TUI) and
		// re-execs on a successful update, so these events can't be sent now —
		// QueuePending parks them for FlushPending to emit after Init. Full mode
		// only: update health is a full-telemetry signal, like the launch snapshot.
		if cfg.GetTelemetryMode() == config.TelemetryFull && !analytics.IsOptedOutByEnv() {
			analytics.QueuePending(analytics.EventUpdateCheck, map[string]any{
				"updated": err == nil && newVer != "",
				"error":   err != nil && !skipped,
			})
			if err == nil && newVer != "" {
				analytics.QueuePending(analytics.EventUpdateApplied, map[string]any{
					"from": version,
					"to":   newVer,
				})
			}
		}
		if skipped {
			debuglog.Logger.Info("auto-update skipped", "reason", err)
		} else if err != nil {
			debuglog.Logger.Error("auto-update failed", "err", err)
		} else if newVer != "" {
			debuglog.Logger.Info("auto-updated", "from", version, "to", newVer)
			fmt.Printf("Updated fleet to %s, restarting...\n", newVer)
			exe, _ := os.Executable()
			syscall.Exec(exe, os.Args, os.Environ())
		} else {
			debuglog.Logger.Info("already up to date", "version", version)
		}
	}

	storage, err := session.Open(session.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer storage.Close()

	// Resolve per-install identity (device hash, git name/email, OS version)
	// here so the Bubble Tea Update() loop never has to wait on git/ioreg/
	// sw_vers. analytics.Init then becomes pure in-memory plumbing.
	identity := analytics.DiscoverIdentity()

	model := ui.NewHome(storage, cfg, version, identity)
	// v2: alt-screen and mouse mode are declared on the View each frame
	// (see Home.chrome), not as program options.
	p := tea.NewProgram(model)
	// Wire the program back into the model so worker goroutines can push
	// state updates to the Update loop via h.send. Must happen before Run.
	model.SetProgram(p)

	// Force legacy key encoding so Ctrl+K (and other modified combos) reach the
	// TUI as control bytes rather than CSI-u sequences Bubble Tea v1 can't parse.
	// Terminals/tmux configs (e.g. gpakosz/.tmux) that enable modifyOtherKeys
	// otherwise break the Ctrl+K command palette.
	restoreKeys := func() {} // no-op unless Disable below succeeds
	if err := termkeys.Disable(os.Stdout); err != nil {
		debuglog.Logger.Warn("failed to set legacy key reporting", "err", err)
	} else {
		// Restore only when Disable succeeded, so a failed Disable leaves the
		// terminal untouched (fail-safe). Idempotent so the deferred call and the
		// explicit pre-os.Exit call below never double-restore.
		restored := false
		restoreKeys = func() {
			if restored {
				return
			}
			restored = true
			if err := termkeys.Restore(os.Stdout); err != nil {
				debuglog.Logger.Warn("failed to restore key reporting", "err", err)
			}
		}
	}
	defer restoreKeys()

	if _, err := p.Run(); err != nil {
		// os.Exit below skips deferred funcs, so restore key reporting first —
		// otherwise extended-key mode stays off for other apps in this terminal.
		restoreKeys()
		// A Bubble Tea loop/cmd panic surfaces here as ErrProgramKilled rather
		// than a Go panic, so log it — otherwise the exit is invisible in debug.log.
		debuglog.Logger.Error("TUI exited with error", "err", err)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runAdd(path string) {
	if err := tmux.IsTmuxAvailable(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Expand and validate path.
	path = expandPath(path)
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "Invalid directory: %s\n", path)
		os.Exit(1)
	}

	storage, err := session.Open(session.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer storage.Close()

	title := session.TitleFromPath(path)
	s := session.NewSession(title, path)

	// `fleet add` leaves Agent empty, which resolves to Claude at launch — so
	// the session is account-eligible and needs one picked, or it silently
	// ignores a configured multi-account setup.
	accounts := claudeaccount.Load()
	session.SetAccountConfigDirFunc(accounts.ConfigDirFor)
	cfg := config.Load()
	if acct, ok := claudeaccount.Select(claudeaccount.SelectOpts{
		Accounts: accounts.List(),
		Strategy: cfg.GetAccountStrategy(),
		Manual:   cfg.DefaultAccount,
	}); ok {
		if conflict := claudeaccount.GuardConflictingAuth(); conflict != "" {
			fmt.Fprintf(os.Stderr, "%s is set and overrides fleet's account selection — unset it to use %s\n", conflict, acct.Email)
			os.Exit(1)
		}
		s.Account = acct.Email
	}

	if err := s.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start session: %v\n", err)
		os.Exit(1)
	}

	if err := storage.SaveSession(s.ToRow()); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save session: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created session '%s' (%s)\n", title, s.ID)
}

func runList() {
	storage, err := session.Open(session.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer storage.Close()

	rows, err := storage.LoadSessions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load sessions: %v\n", err)
		os.Exit(1)
	}

	if len(rows) == 0 {
		fmt.Println("No sessions.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTITLE\tSTATUS\tPATH")
	for _, r := range rows {
		// Show short ID.
		shortID := r.ID
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", shortID, r.Title, r.Status, r.ProjectPath)
	}
	w.Flush()
}

func runRemove(idPrefix string) {
	storage, err := session.Open(session.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer storage.Close()

	rows, err := storage.LoadSessions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load sessions: %v\n", err)
		os.Exit(1)
	}

	// Find session by ID prefix.
	var match *session.SessionRow
	for _, r := range rows {
		if strings.HasPrefix(r.ID, idPrefix) {
			if match != nil {
				fmt.Fprintln(os.Stderr, "Ambiguous ID prefix, be more specific")
				os.Exit(1)
			}
			match = r
		}
	}

	if match == nil {
		fmt.Fprintf(os.Stderr, "No session found with ID starting with '%s'\n", idPrefix)
		os.Exit(1)
	}

	// Kill tmux session if alive.
	ts := tmux.ReconnectSession(match.TmuxSession, match.Title, match.ProjectPath)
	if ts.Exists() {
		_ = ts.Kill()
	}

	if err := storage.DeleteSession(match.ID); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to delete session: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Removed session '%s' (%s)\n", match.Title, match.ID)
}

func runUpdate() {
	fmt.Printf("fleet %s\n", version)
	fmt.Println("Checking for updates...")
	newVer, err := update.Update(version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Update failed: %v\n", err)
		os.Exit(1)
	}
	if newVer == "" {
		fmt.Println("Already up to date.")
	} else {
		fmt.Printf("Updated to %s\n", newVer)
	}
}

func printUsage() {
	fmt.Printf("fleet %s - manage Claude Code sessions\n", version)
	fmt.Println(`
Usage:
  fleet              Launch TUI
  fleet add <path>   Add a new session
  fleet list         List all sessions
  fleet remove <id>  Remove a session
  fleet worktree <branch>  Create a git worktree and start a session in it
                           (--base, --path, --agent, --no-session)
  fleet hooks <install|uninstall|status>  Manage Claude Code hooks
  fleet update       Update to latest version
  fleet version      Show version
  fleet help         Show this help`)
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}
