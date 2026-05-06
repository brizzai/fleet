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

// mapEventToStatus maps a Claude Code hook event to a fleet status string.
func mapEventToStatus(event string) string {
	switch event {
	case "UserPromptSubmit":
		return "running"
	case "Stop":
		return "finished"
	case "PermissionRequest":
		return "waiting"
	case "Notification":
		return "" // handled separately based on matcher
	case "SessionStart":
		return "finished"
	case "SessionEnd":
		return "dead"
	default:
		return ""
	}
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
	var userPrompt string
	var promptCount int
	if payload.HookEventName == "UserPromptSubmit" && payload.Prompt != "" {
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
	if payload.HookEventName == "UserPromptSubmit" {
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

// cleanStaleHookFiles removes hook status files older than 24 hours.
func cleanStaleHookFiles(hooksDir string) {
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
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
