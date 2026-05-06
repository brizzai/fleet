package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/brizzai/fleet/internal/debuglog"
)

// Config holds user-configurable settings.
type Config struct {
	TickIntervalSec    int    `json:"tick_interval_sec,omitempty"`
	DefaultProjectPath string `json:"default_project_path,omitempty"`
	Editor             string `json:"editor,omitempty"`
	Theme              string `json:"theme,omitempty"`
	AutoNameSessions   *bool  `json:"auto_name_sessions,omitempty"`
	AutoUpdate         *bool  `json:"auto_update,omitempty"`
	CopyClaudeSettings *bool  `json:"copy_claude_settings,omitempty"`
	EnterMode          string `json:"enter_mode,omitempty"` // "attach" or "split"
	Telemetry          *bool  `json:"telemetry,omitempty"`
}

// IsAutoNameEnabled returns whether auto-naming is enabled (default: true).
func (c *Config) IsAutoNameEnabled() bool {
	if c.AutoNameSessions == nil {
		return true
	}
	return *c.AutoNameSessions
}

// IsAutoUpdateEnabled returns whether auto-update is enabled (default: true).
// FLEET_AUTO_UPDATE_DISABLED takes precedence over the config file when truthy
// (1/true/yes/y/on) — useful for running a local dev build without the
// auto-updater overwriting it with the latest release.
func (c *Config) IsAutoUpdateEnabled() bool {
	if isTruthyEnv(os.Getenv("FLEET_AUTO_UPDATE_DISABLED")) {
		return false
	}
	if c.AutoUpdate == nil {
		return true
	}
	return *c.AutoUpdate
}

// isTruthyEnv returns true for common truthy values (1, true, yes, y, on).
func isTruthyEnv(v string) bool {
	switch strings.TrimSpace(strings.ToLower(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

// DefaultConfigPath returns the default config file path.
func DefaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fleet", "config.json")
}

// Load reads config from disk, returning defaults if missing.
func Load() *Config {
	cfg := &Config{
		TickIntervalSec: 2,
	}

	path := DefaultConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		debuglog.Logger.Info("config file not found, using defaults", "path", path)
		return cfg
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		debuglog.Logger.Error("failed to parse config file", "path", path, "error", err)
	} else {
		debuglog.Logger.Info("config loaded", "path", path)
	}

	// Enforce minimums.
	if cfg.TickIntervalSec < 1 {
		cfg.TickIntervalSec = 2
	}

	return cfg
}

// Save writes config to disk.
func (c *Config) Save() error {
	path := DefaultConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		debuglog.Logger.Error("failed to create config directory", "path", path, "error", err)
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		debuglog.Logger.Error("failed to marshal config", "error", err)
		return err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		debuglog.Logger.Error("failed to write config file", "path", path, "error", err)
		return err
	}
	return nil
}

// IsCopyClaudeSettingsEnabled returns whether to copy .claude/settings.local.json to new worktrees (default: true).
func (c *Config) IsCopyClaudeSettingsEnabled() bool {
	if c.CopyClaudeSettings == nil {
		return true
	}
	return *c.CopyClaudeSettings
}

// GetEnterMode returns the configured Enter key mode ("attach" or "split").
func (c *Config) GetEnterMode() string {
	if c.EnterMode == "split" {
		return "split"
	}
	return "attach"
}

// IsTelemetryEnabled returns whether telemetry is enabled (default: true).
func (c *Config) IsTelemetryEnabled() bool {
	if c.Telemetry == nil {
		return true
	}
	return *c.Telemetry
}

// GetEditor returns the configured editor, falling back to $EDITOR then "code".
func (c *Config) GetEditor() string {
	if c.Editor != "" {
		return c.Editor
	}
	if editor := os.Getenv("EDITOR"); editor != "" {
		return editor
	}
	return "code"
}
