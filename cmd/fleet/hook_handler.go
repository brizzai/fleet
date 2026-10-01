package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
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

	// Copilot CLI's camelCase payload. Its event name arrives in argv instead
	// (see copilotHookArgs); notification is the one event that also carries it.
	CopilotSessionID string `json:"sessionId,omitempty"`
	NotificationType string `json:"notification_type,omitempty"`
}

// copilotHookArgs reads the `--agent copilot --event <name>` suffix fleet's
// Copilot hooks file appends to the hook command. ok is false for any other
// agent's invocation.
func copilotHookArgs(args []string) (event string, ok bool) {
	var ag string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--agent":
			ag = args[i+1]
		case "--event":
			event = args[i+1]
		}
	}
	return event, ag == "copilot" && event != ""
}

// normalizeCopilotEvent maps a Copilot hook onto the Claude event name the rest
// of the handler keys on, and the status it means. Copilot's SessionEnd also
// fires on /clear with the same reason as /exit, so it is never "dead" here:
// the TUI reads a pane back at a shell as the exit.
func normalizeCopilotEvent(event string, p *hookPayload) (claudeEvent, status string) {
	p.SessionID = p.CopilotSessionID
	// A reason marks a SessionEnd (it keeps a dead session's crash dump from
	// re-arming), so no other event may carry one through.
	reason := p.Reason
	p.Reason = ""
	switch event {
	case "userPromptSubmitted":
		return "UserPromptSubmit", "running"
	case "postToolUse", "postToolUseFailure":
		// Approving a permission fires no hook; the tool finishing is the first
		// sign the session left the prompt.
		return "PostToolUse", "running"
	case "notification":
		switch p.NotificationType {
		case "permission_prompt", "elicitation_dialog":
			return "Notification", "waiting"
		}
		return "Notification", ""
	case "agentStop":
		return "Stop", "finished"
	case "sessionEnd":
		p.Reason = sanitizeExitReason(reason)
		return "SessionEnd", "finished"
	}
	return event, ""
}

// isCopilotSubagent reports whether a Copilot hook came from a subagent. A
// subagent fires the same hooks as a conversation — userPromptSubmitted
// included — under its own session id, but logs into its parent's events.jsonl
// and gets no session-state/<id>/ of its own. A real conversation always has
// one by the time its first hook runs (session.start precedes it).
func isCopilotSubagent(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	_, err := os.Stat(hooks.CopilotSessionDir(sessionID))
	return os.IsNotExist(err)
}

// sanitizeExitReason keeps a reason only when it is a short snake_case enum: it
// reaches analytics as exit_reason, and must be non-empty, since an empty one
// would no longer mark the hook as a SessionEnd.
func sanitizeExitReason(r string) string {
	if r == "" || len(r) > 32 {
		return "other"
	}
	for _, c := range r {
		if (c < 'a' || c > 'z') && c != '_' {
			return "other"
		}
	}
	return r
}

// mapEventToStatus maps a hook event to a fleet status string. Claude and Codex
// send Claude-style event names; the OpenCode status plugin sends OpenCode-native
// names (session.busy/session.idle/permission.asked) — these are additive, the
// other agents never emit them, so the handler stays agent-neutral.
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
	default:
		return ""
	}
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
	copilotEvent, isCopilot := copilotHookArgs(os.Args[2:])
	// Copilot runs hooks synchronously and for every Copilot session on the
	// machine, fleet's or not. Leave before touching the log for the ones that
	// aren't fleet's, or each of their tool calls would add a line to debug.log.
	if isCopilot && os.Getenv("FLEET_INSTANCE_ID") == "" {
		return
	}
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

	var status string
	if isCopilot {
		payload.HookEventName, status = normalizeCopilotEvent(copilotEvent, &payload)
		if isCopilotSubagent(payload.SessionID) {
			log.Debug("hook-handler: copilot subagent hook dropped", "event", payload.HookEventName, "instance", instanceID)
			return
		}
	} else {
		status = mapEventToStatus(payload.HookEventName)
	}

	// Special handling for Notification events.
	if !isCopilot && payload.HookEventName == "Notification" && payload.Matcher != nil {
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

	// Extract user prompt and prompt count.
	var userPrompt string
	var promptCount int
	if payload.HookEventName == "UserPromptSubmit" && payload.Prompt != "" {
		userPrompt = payload.Prompt
	}

	// Preserve user_prompt and prompt_count from previous status file.
	hooksDir := hooks.GetHooksDir()
	existingPath := filepath.Join(hooksDir, instanceID+".json")
	var existingStatus, existingSessionID, promptSessionID string
	if existing, err := hooks.ReadStatusFile(existingPath); err == nil {
		existingStatus, existingSessionID = existing.Status, existing.SessionID
		promptSessionID = existing.PromptSessionID
		promptCount = existing.PromptCount
		if userPrompt == "" && existing.UserPrompt != "" {
			userPrompt = existing.UserPrompt
		}
	}

	// Increment prompt count on new user prompt submissions.
	if payload.HookEventName == "UserPromptSubmit" {
		promptCount++
		promptSessionID = payload.SessionID
	}

	// Copilot fires PostToolUse for every tool call; it only matters as the
	// waiting → running edge, so a repeat would just rewrite the file and log.
	if payload.HookEventName == "PostToolUse" && existingStatus == status && existingSessionID == payload.SessionID {
		return
	}

	log.Info("hook-handler: writing status",
		"instance", instanceID,
		"event", payload.HookEventName,
		"status", status,
		"claudeSession", payload.SessionID,
	)

	sf := &hooks.StatusFile{
		Status:          status,
		SessionID:       payload.SessionID,
		Event:           payload.HookEventName,
		Timestamp:       time.Now().Unix(),
		UserPrompt:      userPrompt,
		PromptCount:     promptCount,
		Reason:          payload.Reason,
		PromptSessionID: promptSessionID,
		// The agent runs the hook command directly, so our parent IS the agent
		// process whose conversation this status describes. Recording it lets the
		// TUI later ask whether that conversation is still alive.
		AgentPID: os.Getppid(),
	}

	if err := hooks.WriteStatusFile(hooksDir, instanceID, sf); err != nil {
		log.Error("hook-handler: write failed", "err", err)
	}

	// Opportunistic cleanup of stale files — but not per tool call: Copilot
	// blocks on its hooks, and PostToolUse fires for every one.
	if payload.HookEventName != "PostToolUse" {
		cleanStaleHookFiles(hooksDir)
	}
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
		if _, err := exec.LookPath("copilot"); err == nil {
			copilotDir := hooks.GetCopilotConfigDir()
			if installed, err := hooks.InjectCopilotHooks(copilotDir); err != nil {
				fmt.Fprintf(os.Stderr, "Error installing Copilot hooks: %v\n", err)
				os.Exit(1)
			} else if installed {
				fmt.Printf("Copilot hooks installed: %s/hooks/fleet.json\n", copilotDir)
			} else {
				fmt.Println("Copilot hooks are already installed.")
			}
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
		if removed, err := hooks.RemoveCopilotHooks(hooks.GetCopilotConfigDir()); err != nil {
			fmt.Fprintf(os.Stderr, "Error removing Copilot hooks: %v\n", err)
			os.Exit(1)
		} else if removed {
			fmt.Println("Copilot hooks removed successfully.")
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
		if _, err := exec.LookPath("copilot"); err == nil {
			if hooks.CopilotHooksInstalled(hooks.GetCopilotConfigDir()) {
				fmt.Println("Copilot: INSTALLED")
			} else {
				fmt.Println("Copilot: NOT INSTALLED (run 'fleet hooks install')")
			}
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
