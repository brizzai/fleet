package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/hooks"
)

// hookPayload represents the JSON payload Claude Code sends to hooks via stdin.
type hookPayload struct {
	HookEventName string          `json:"hook_event_name"`
	SessionID     string          `json:"session_id"`
	Source        string          `json:"source"`
	Matcher       json.RawMessage `json:"matcher,omitempty"`
	Prompt        string          `json:"prompt,omitempty"`
	// Reason is set on SessionEnd: "clear", "logout", "prompt_input_exit", "other".
	Reason string `json:"reason,omitempty"`
}

// mapEventToStatus maps a hook event to a fleet status string. Claude and Codex
// send Claude-style event names; the OpenCode status plugin sends OpenCode-native
// names (session.busy/session.idle/permission.asked); Cursor CLI's hooks.json
// sends its own lowerCamelCase event names — these are all additive, no agent
// emits another's names, so the handler stays agent-neutral.
func mapEventToStatus(event string) string {
	switch event {
	case "UserPromptSubmit":
		return "running"
	case "Stop":
		return "finished"
	case "PreCompact":
		// /compact (and auto-compaction) is a multi-minute busy phase that fires no
		// other hook. Without this, the session reads finished/idle for the whole
		// compaction. The closing SessionStart(source="compact") is skipped in
		// handleHookHandler (not force-finished), so pane + stability detection —
		// or a queued prompt's UserPromptSubmit — settle the end of compaction.
		return "running"
	case "PermissionRequest":
		return "waiting"
	case "Notification":
		return "" // handled separately based on matcher
	case "SessionStart":
		return "finished"
	case "SessionEnd":
		return "dead"
	// OpenCode-native events (from the status plugin):
	case "session.busy":
		return "running"
	case "session.idle":
		return "finished"
	case "session.error":
		return "error"
	case "permission.asked":
		return "waiting"
	case "permission.replied":
		// A reply (approve/reject) resumes the turn; settle to finished/idle on
		// the next session.idle. Without this, waiting can stick if OpenCode
		// doesn't re-emit session.status{busy} after an in-flight approval.
		return "running"
	// Cursor CLI events (from hooks.json, see internal/hooks/cursor_hooks.go).
	// Cursor has no dedicated permission/approval hook, so beforeShellExecution/
	// afterShellExecution bracket the interactive approval prompt instead: the
	// hook fires and returns immediately, then (unless auto-approved) Cursor's
	// own UI blocks on a y/n prompt before the command actually runs and
	// afterShellExecution fires — so "waiting" is only wrong for auto-approved
	// commands, which resolve to "running" again almost immediately.
	//
	// No sessionStart case: unlike Claude, Cursor's initial status (see
	// initialRunStatus in session.go) starts idle, not running — so there's
	// nothing for sessionStart to correct, and mapping it to "finished" (as
	// Claude's SessionStart does) would immediately flip a freshly launched,
	// untouched session to finished before any turn ran. It falls through to
	// the default unmapped case below; fleet subscribes to no such hook (see
	// cursorHookEvents in internal/hooks/cursor_hooks.go).
	case "beforeSubmitPrompt":
		return "running"
	case "beforeShellExecution":
		return "waiting"
	case "afterShellExecution":
		return "running"
	case "stop":
		return "finished"
	case "sessionEnd":
		return "dead"
	default:
		return ""
	}
}

// isPromptSubmit reports whether event is a user-prompt-submission hook —
// Claude/Codex's UserPromptSubmit, or Cursor's beforeSubmitPrompt equivalent
// (see internal/hooks/cursor_hooks.go) — used to gate prompt-text capture and
// prompt-count increments in handleHookHandler.
func isPromptSubmit(event string) bool {
	return event == "UserPromptSubmit" || event == "beforeSubmitPrompt"
}

// isCompactSessionStart reports the SessionStart that Claude Code fires when a
// compaction completes. Its status must NOT be forced to "finished": on
// auto-compaction the turn is still running, so finishing here would flash a
// spurious "finished" mid-turn. Skipping it lets the prior "running" (from the
// PreCompact hook) stand; pane + stability detection settle a manual /compact
// that ends at an idle prompt.
func isCompactSessionStart(event, source string) bool {
	return event == "SessionStart" && source == "compact"
}

// handleHookHandler processes a Claude Code hook event.
// Reads JSON from stdin, maps the event to a status, and writes a status file.
// Always exits 0 to avoid blocking Claude Code.
func handleHookHandler() {
	debuglog.Init()
	defer debuglog.Close()
	log := debuglog.Logger

	defer func() {
		if r := recover(); r != nil {
			log.Error("hook-handler panic", "recover", r)
		}
	}()

	data, err := io.ReadAll(os.Stdin)
	if err != nil || len(data) == 0 {
		return
	}

	var payload hookPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		log.Warn("hook-handler: bad JSON", "err", err)
		return
	}

	instanceID := os.Getenv("FLEET_INSTANCE_ID")
	if instanceID == "" {
		log.Warn("hook-handler: no FLEET_INSTANCE_ID env var",
			"event", payload.HookEventName,
			"claudeSession", payload.SessionID,
			"source", payload.Source,
		)
		return
	}

	// A compaction's closing SessionStart must not force "finished" (see
	// isCompactSessionStart): keep the prior status file so PreCompact's "running"
	// stands and detection settles the end.
	if isCompactSessionStart(payload.HookEventName, payload.Source) {
		log.Debug("hook-handler: skipping compact SessionStart", "instance", instanceID)
		return
	}

	status := mapEventToStatus(payload.HookEventName)

	// Special handling for Notification events.
	if payload.HookEventName == "Notification" && payload.Matcher != nil {
		var matcher string
		if err := json.Unmarshal(payload.Matcher, &matcher); err == nil {
			switch matcher {
			case "permission_prompt", "elicitation_dialog":
				status = "waiting"
			case "idle_prompt":
				status = "finished"
			}
		}
	}

	if status == "" {
		log.Debug("hook-handler: unmapped event", "event", payload.HookEventName, "instance", instanceID)
		return
	}

	log.Info("hook-handler: writing status",
		"instance", instanceID,
		"event", payload.HookEventName,
		"status", status,
		"claudeSession", payload.SessionID,
	)

	// Extract user prompt and prompt count.
	promptSubmit := isPromptSubmit(payload.HookEventName)
	var userPrompt string
	var promptCount int
	if promptSubmit && payload.Prompt != "" {
		userPrompt = payload.Prompt
	}

	// Preserve user_prompt and prompt_count from previous status file.
	hooksDir := hooks.GetHooksDir()
	existingPath := filepath.Join(hooksDir, instanceID+".json")
	if existing, err := hooks.ReadStatusFile(existingPath); err == nil {
		promptCount = existing.PromptCount
		if userPrompt == "" && existing.UserPrompt != "" {
			userPrompt = existing.UserPrompt
		}
	}

	// Increment prompt count on new user prompt submissions.
	if promptSubmit {
		promptCount++
	}

	sf := &hooks.StatusFile{
		Status:      status,
		SessionID:   payload.SessionID,
		Event:       payload.HookEventName,
		Timestamp:   time.Now().Unix(),
		UserPrompt:  userPrompt,
		PromptCount: promptCount,
		Reason:      payload.Reason,
	}

	if err := hooks.WriteStatusFile(hooksDir, instanceID, sf); err != nil {
		log.Error("hook-handler: write failed", "err", err)
	}

	// Opportunistic cleanup of stale files.
	cleanStaleHookFiles(hooksDir)
}

// handleHooksCmd handles the "hooks" CLI subcommand for manual hook management.
func handleHooksCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: fleet hooks <install|uninstall|status>")
		os.Exit(1)
	}

	configDir := hooks.GetClaudeConfigDir()

	switch args[0] {
	case "install":
		installed, err := hooks.InjectClaudeHooks(configDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error installing hooks: %v\n", err)
			os.Exit(1)
		}
		if installed {
			fmt.Println("Claude Code hooks installed successfully.")
			fmt.Printf("Config: %s/settings.json\n", configDir)
		} else {
			fmt.Println("Claude Code hooks are already installed.")
		}
	case "uninstall":
		removed, err := hooks.RemoveClaudeHooks(configDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error removing hooks: %v\n", err)
			os.Exit(1)
		}
		if removed {
			fmt.Println("Claude Code hooks removed successfully.")
		} else {
			fmt.Println("No fleet hooks found to remove.")
		}
	case "status":
		installed := hooks.AreHooksInstalled(configDir)
		if installed {
			fmt.Println("Status: INSTALLED")
			fmt.Printf("Config: %s/settings.json\n", configDir)
		} else {
			fmt.Println("Status: NOT INSTALLED")
			fmt.Println("Run 'fleet hooks install' to install.")
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown hooks subcommand: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "Usage: fleet hooks <install|uninstall|status>")
		os.Exit(1)
	}
}

// cleanStaleHookFiles removes hook status files older than 24 hours. Temp files
// are swept too: they're uniquely named now (see hooks.WriteStatusFile), so a
// handler killed between creating one and renaming it leaves one behind rather
// than having it overwritten by the next write.
func cleanStaleHookFiles(hooksDir string) {
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if ext := filepath.Ext(entry.Name()); ext != ".json" && ext != ".tmp" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(hooksDir, entry.Name()))
		}
	}
}
