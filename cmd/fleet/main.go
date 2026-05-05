package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/migration"
	"github.com/brizzai/fleet/internal/perfwatch"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/tmux"
	"github.com/brizzai/fleet/internal/ui"
	"github.com/brizzai/fleet/internal/update"
	tea "github.com/charmbracelet/bubbletea"
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

	if err := tmux.IsTmuxAvailable(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cfg := config.Load()

	// Auto-update: check for newer version on launch.
	if cfg.IsAutoUpdateEnabled() && version != "dev" && update.ShouldCheck() {
		debuglog.Logger.Info("checking for updates", "current", version)
		newVer, err := update.Update(version)
		if err != nil {
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

	model := ui.NewHome(storage, cfg, version)
	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
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
