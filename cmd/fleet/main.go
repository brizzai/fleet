package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/daemonclient"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/migration"
	"github.com/brizzai/fleet/internal/perfwatch"
	"github.com/brizzai/fleet/internal/service"
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

	// TUI flags (anything prefixed with `--` other than the help/version
	// flags handled in the switch below) get routed through to runTUI,
	// which parses os.Args itself. Keeps `fleet --standalone` working
	// without requiring users to type a redundant `fleet tui --standalone`.
	if strings.HasPrefix(args[0], "--") && args[0] != "--help" && args[0] != "--version" {
		runTUI()
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
	case "daemon":
		runDaemon()
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

	// Stage 0 PR 5: TUI defaults to driving sessions through the daemon
	// (autospawned via `fleet daemon --detach` if not already up).
	// `--standalone` keeps the in-process path as an escape hatch.
	standalone := false
	for _, a := range os.Args[1:] {
		if a == "--standalone" {
			standalone = true
		}
	}

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

	svc, warning, err := startService(standalone, storage, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start session service: %v\n", err)
		os.Exit(1)
	}
	defer svc.Stop()

	model := ui.NewHome(svc, storage, cfg, version)
	if warning != "" {
		model.SetStartupWarning(warning)
	}
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
  fleet daemon [--detach]    Run the gRPC daemon (foreground; --detach forks into background and logs to ~/.config/fleet/daemon.log)
  fleet update       Update to latest version
  fleet version      Show version
  fleet help         Show this help`)
}

// startService picks between the daemon-client and in-process service
// implementations and starts the chosen one. In daemon mode it autospawns a
// detached `fleet daemon` if no socket is listening; if either spawn or
// connect fails, it logs the reason and falls back to standalone so the
// TUI still launches. Returns the service plus any non-fatal warning to
// surface in the UI.
func startService(standalone bool, storage *session.StateDB, cfg *config.Config) (service.Service, string, error) {
	if standalone {
		debuglog.Logger.Info("TUI: standalone mode (in-process service)")
		svc := service.NewSessionService(storage, cfg)
		warn, err := svc.Start()
		return svc, warn, err
	}

	debuglog.Logger.Info("TUI: daemon mode (autospawn if needed)")
	ctx := context.Background()
	sock, err := daemonclient.EnsureRunning(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fleet: daemon unreachable, falling back to --standalone (%v)\n", err)
		debuglog.Logger.Warn("daemon unreachable; falling back to standalone", "err", err)
		svc := service.NewSessionService(storage, cfg)
		warn, sErr := svc.Start()
		return svc, prependWarning(warn, "daemon unreachable, running standalone"), sErr
	}

	client := daemonclient.New(sock)
	warn, err := client.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fleet: daemon connect failed, falling back to --standalone (%v)\n", err)
		debuglog.Logger.Warn("daemon connect failed; falling back to standalone", "err", err)
		svc := service.NewSessionService(storage, cfg)
		sWarn, sErr := svc.Start()
		return svc, prependWarning(sWarn, "daemon connect failed, running standalone"), sErr
	}
	return client, warn, nil
}

// prependWarning combines an existing warning string with a fallback note,
// keeping the existing message intact if present.
func prependWarning(existing, prefix string) string {
	if existing == "" {
		return prefix
	}
	return prefix + "; " + existing
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
