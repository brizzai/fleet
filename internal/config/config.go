package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/brizzai/fleet/internal/debuglog"
)

// Telemetry modes. Full sends usage events tagged with the user's git
// name/email; Minimal sends only an anonymous daily-active ping (enough to
// count daily active users, no identity); Off sends nothing.
const (
	TelemetryFull    = "full"
	TelemetryMinimal = "minimal"
	TelemetryOff     = "off"
)

// Config holds user-configurable settings.
type Config struct {
	TickIntervalSec      int    `json:"tick_interval_sec,omitempty"`
	DefaultProjectPath   string `json:"default_project_path,omitempty"`
	Editor               string `json:"editor,omitempty"`
	Theme                string `json:"theme,omitempty"`
	AutoNameSessions     *bool  `json:"auto_name_sessions,omitempty"`
	AutoUpdate           *bool  `json:"auto_update,omitempty"`
	CopyClaudeSettings   *bool  `json:"copy_claude_settings,omitempty"`
	ConfirmBeforeRestart *bool  `json:"confirm_before_restart,omitempty"`
	// OriginDeleteRemovesWorktrees controls what forgetting a whole origin row
	// does to worktree directories on disk. Default true: each checkout follows
	// its own rule (worktrees get `git worktree remove`, the main repo's folder
	// is kept). False: nothing is removed from disk — every checkout is just
	// un-tracked.
	OriginDeleteRemovesWorktrees *bool  `json:"origin_delete_removes_worktrees,omitempty"`
	EnterMode                    string `json:"enter_mode,omitempty"`       // "attach" or "split"
	StatusIndicator              string `json:"status_indicator,omitempty"` // "icon" (default) or "bar"
	// TelemetryMode is the three-way telemetry preference: "full" (usage +
	// git name/email), "minimal" (anonymous daily-active ping only, no
	// identity), or "off" (nothing). Read via GetTelemetryMode, which migrates
	// the legacy Telemetry bool below when this is unset.
	TelemetryMode string `json:"telemetry_mode,omitempty"`
	// Telemetry is the legacy on/off flag, kept only for migration — new writes
	// go to TelemetryMode. See GetTelemetryMode.
	Telemetry    *bool  `json:"telemetry,omitempty"`
	DefaultAgent string `json:"default_agent,omitempty"` // "claude" or "codex"
	DrawerHeight int    `json:"drawer_height,omitempty"` // terminal-drawer body rows (default 12)

	// Sidebar display toggles. All default to true (on) via the *bool nil
	// pattern, so an unconfigured fleet renders the full vocabulary. Each is
	// surfaced in the Appearance category of the Settings dialog and drives a
	// package-level render flag in the ui package (see ApplyDisplayConfig).
	ShowAgentGlyphs    *bool  `json:"show_agent_glyphs,omitempty"`    // per-session ✻/◇ agent sigil
	ShowStatusPills    *bool  `json:"show_status_pills,omitempty"`    // header "2● 1◐" status summary
	ShowPRBadges       *bool  `json:"show_pr_badges,omitempty"`       // "#123 ✓" PR badge on checkout headers
	ShowDirtyIndicator *bool  `json:"show_dirty_indicator,omitempty"` // "*" dirty-worktree marker
	ShowSlotBadges     *bool  `json:"show_slot_badges,omitempty"`     // "[N]" hotkey slot badge
	ShowHeaderCounts   *bool  `json:"show_header_counts,omitempty"`   // session count on origin/checkout headers
	ChevronStyle       string `json:"chevron_style,omitempty"`        // "triangle" (default ▾▸) or "plusminus" (−+)
	SidebarDensity     string `json:"sidebar_density,omitempty"`      // "normal" (default) or "compact" (no inter-group gap)

	// AnalyticsConsentSeen is true once the user has been shown the
	// first-launch consent prompt and answered it (either way). When false,
	// the TUI shows the prompt before initializing analytics.
	AnalyticsConsentSeen bool `json:"analytics_consent_seen,omitempty"`

	// DisplayOnboardingSeen is true once the user has been shown the
	// first-launch theme/onboarding screen (whether they picked a theme or
	// skipped). When false, the TUI shows it after the consent prompt.
	DisplayOnboardingSeen bool `json:"display_onboarding_seen,omitempty"`

	// SeenTips records IDs of one-time (tipOnce) contextual tips the user has
	// dismissed or that have timed out, so they never reappear. Recurring,
	// condition-driven tips are not stored here — they reset in-memory.
	SeenTips []string `json:"seen_tips,omitempty"`

	// loadedFromDisk records whether Load read an existing config file. It is
	// unexported (never serialized) and powers IsFirstRun: a brand-new install
	// has no config.json, so this is the one signal that doesn't depend on the
	// telemetry/consent path.
	loadedFromDisk bool

	// unknownKeys preserves any top-level JSON keys present on disk that this
	// binary's struct doesn't recognize — e.g. a field a newer fleet wrote that
	// an older binary would otherwise silently drop on the next Save. Captured
	// in Load, re-merged in Save. Unexported, so the struct path never
	// serializes it; it is re-injected manually by marshal.
	unknownKeys map[string]json.RawMessage
}

// IsFirstRun reports whether this is a genuinely fresh install — no config file
// existed when Load ran. Used to decide whether to show first-run experiences
// (e.g. theme onboarding) on launches that skip the consent prompt.
func (c *Config) IsFirstRun() bool { return !c.loadedFromDisk }

// GetDefaultAgent returns the default coding agent for new sessions ("claude",
// "codex", or "opencode"). The stored value is normalized (trimmed +
// lower-cased) so hand-edited configs like "Codex" or " codex " resolve
// correctly instead of silently falling back.
func (c *Config) GetDefaultAgent() string {
	switch strings.TrimSpace(strings.ToLower(c.DefaultAgent)) {
	case "codex":
		return "codex"
	case "opencode":
		return "opencode"
	default:
		return "claude"
	}
}

// IsTipSeen reports whether a one-time tip has been dismissed or has timed out.
func (c *Config) IsTipSeen(id string) bool {
	for _, s := range c.SeenTips {
		if s == id {
			return true
		}
	}
	return false
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
	cfg.loadedFromDisk = true

	if err := json.Unmarshal(data, cfg); err != nil {
		debuglog.Logger.Error("failed to parse config file", "path", path, "error", err)
	} else {
		cfg.unknownKeys = extractUnknownKeys(data)
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
	data, err := c.marshal()
	if err != nil {
		debuglog.Logger.Error("failed to marshal config", "error", err)
		return err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		debuglog.Logger.Error("failed to write config file", "path", path, "error", err)
		return err
	}
	// Stamp every save with the build that wrote it (VCS revision, present even
	// for `go run`/`make run`) and the exact keys persisted. If a stale binary
	// ever drops a newer field, this line pins which build did it and when.
	debuglog.Logger.Info("config saved", "path", path, "build", buildID(), "keys", topLevelKeys(data))
	return nil
}

// marshal renders the config to indented JSON, re-merging any unknown keys
// captured at load time so fields written by a newer fleet survive a save by
// this binary. With no unknown keys (the common case) the output is byte-for-
// byte the struct's own ordered marshal; the merge path alphabetizes keys.
func (c *Config) marshal() ([]byte, error) {
	if len(c.unknownKeys) == 0 {
		return json.MarshalIndent(c, "", "  ")
	}
	base, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	merged := map[string]json.RawMessage{}
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	for k, v := range c.unknownKeys {
		if _, known := merged[k]; !known {
			merged[k] = v
		}
	}
	return json.MarshalIndent(merged, "", "  ")
}

// extractUnknownKeys returns the top-level keys in on-disk config JSON that this
// binary's Config struct doesn't define. Preserving them across a Save round-
// trip stops an older binary from silently dropping fields a newer one wrote.
func extractUnknownKeys(data []byte) map[string]json.RawMessage {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	known := knownConfigKeys()
	for k := range raw {
		if known[k] {
			delete(raw, k)
		}
	}
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// knownConfigKeys is the set of JSON field names the Config struct defines,
// derived from its json tags so it never drifts as fields are added/removed.
func knownConfigKeys() map[string]bool {
	t := reflect.TypeFor[Config]()
	keys := make(map[string]bool, t.NumField())
	for f := range t.Fields() {
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		if name, _, _ := strings.Cut(tag, ","); name != "" {
			keys[name] = true
		}
	}
	return keys
}

// topLevelKeys returns the JSON object's top-level keys, sorted and comma-joined,
// for the save-log. Empty string if data isn't a JSON object.
func topLevelKeys(data []byte) string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// buildID returns a short identifier for the running binary: the embedded VCS
// revision (Go embeds it for `go build`/`go run`, so it works under `make run`),
// suffixed "-dirty" when the working tree had uncommitted changes at build time.
// Falls back to "unknown" when no VCS info is embedded.
func buildID() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	var rev, mod string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			mod = s.Value
		}
	}
	if rev == "" {
		return "unknown"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if mod == "true" {
		rev += "-dirty"
	}
	return rev
}

// IsCopyClaudeSettingsEnabled returns whether to copy .claude/settings.local.json to new worktrees (default: true).
func (c *Config) IsCopyClaudeSettingsEnabled() bool {
	if c.CopyClaudeSettings == nil {
		return true
	}
	return *c.CopyClaudeSettings
}

// IsConfirmBeforeRestartEnabled returns whether to confirm before restarting a session (default: true).
func (c *Config) IsConfirmBeforeRestartEnabled() bool {
	if c.ConfirmBeforeRestart == nil {
		return true
	}
	return *c.ConfirmBeforeRestart
}

// GetOriginDeleteRemovesWorktrees reports whether forgetting an origin row also
// removes its worktree directories from disk (default: true).
func (c *Config) GetOriginDeleteRemovesWorktrees() bool {
	if c.OriginDeleteRemovesWorktrees == nil {
		return true
	}
	return *c.OriginDeleteRemovesWorktrees
}

// GetEnterMode returns the configured Enter key mode ("attach" or "split").
func (c *Config) GetEnterMode() string {
	if c.EnterMode == "split" {
		return "split"
	}
	return "attach"
}

// GetStatusIndicator returns the configured sidebar status style — "icon"
// (default, semantic circles) or "bar" (single colored vertical bar).
func (c *Config) GetStatusIndicator() string {
	if c.StatusIndicator == "bar" {
		return "bar"
	}
	return "icon"
}

// DrawerHeightMin/Max bound the terminal-drawer body height. The max mirrors the
// UI's hard body cap (ui.drawerMaxBodyRows) — the panels above always keep a few
// rows — so a larger drawer_height can never actually render; clamp it here so the
// config, settings, and rendered height all agree.
const (
	DrawerHeightMin     = 4
	DrawerHeightMax     = 14
	DrawerHeightDefault = 12
)

// GetDrawerHeight returns the terminal-drawer body height in rows (default 12
// when unset), clamped to [DrawerHeightMin, DrawerHeightMax].
func (c *Config) GetDrawerHeight() int {
	if c.DrawerHeight <= 0 {
		return DrawerHeightDefault
	}
	if c.DrawerHeight < DrawerHeightMin {
		return DrawerHeightMin
	}
	if c.DrawerHeight > DrawerHeightMax {
		return DrawerHeightMax
	}
	return c.DrawerHeight
}

// boolDefaultTrue resolves an optional bool flag, defaulting to true when unset.
func boolDefaultTrue(p *bool) bool {
	if p == nil {
		return true
	}
	return *p
}

// IsShowAgentGlyphs reports whether the per-session ✻/◇ agent sigil is shown (default: true).
func (c *Config) IsShowAgentGlyphs() bool { return boolDefaultTrue(c.ShowAgentGlyphs) }

// IsShowStatusPills reports whether header status-summary pills are shown (default: true).
func (c *Config) IsShowStatusPills() bool { return boolDefaultTrue(c.ShowStatusPills) }

// IsShowPRBadges reports whether PR badges are shown on checkout headers (default: true).
func (c *Config) IsShowPRBadges() bool { return boolDefaultTrue(c.ShowPRBadges) }

// IsShowDirtyIndicator reports whether the dirty "*" marker is shown (default: true).
func (c *Config) IsShowDirtyIndicator() bool { return boolDefaultTrue(c.ShowDirtyIndicator) }

// IsShowSlotBadges reports whether "[N]" hotkey slot badges are shown (default: true).
func (c *Config) IsShowSlotBadges() bool { return boolDefaultTrue(c.ShowSlotBadges) }

// IsShowHeaderCounts reports whether session counts are shown on headers (default: true).
func (c *Config) IsShowHeaderCounts() bool { return boolDefaultTrue(c.ShowHeaderCounts) }

// GetChevronStyle returns the configured header chevron style — "triangle"
// (default ▾▸) or "plusminus" (−+).
func (c *Config) GetChevronStyle() string {
	if c.ChevronStyle == "plusminus" {
		return "plusminus"
	}
	return "triangle"
}

// GetSidebarDensity returns the configured sidebar density — "normal" (default,
// a blank gap between origin groups) or "compact" (no gap).
func (c *Config) GetSidebarDensity() string {
	if c.SidebarDensity == "compact" {
		return "compact"
	}
	return "normal"
}

// isValidTelemetryMode reports whether mode is one of the documented telemetry
// modes. An unrecognized value (typo, corruption, or a mode written by a newer
// build) must not be treated as explicit consent.
func isValidTelemetryMode(mode string) bool {
	switch mode {
	case TelemetryFull, TelemetryMinimal, TelemetryOff:
		return true
	default:
		return false
	}
}

// GetTelemetryMode returns the telemetry mode: "full", "minimal", or "off".
// Precedence: a valid explicit TelemetryMode wins; otherwise the legacy
// Telemetry bool is migrated (true→full, false→minimal — a previously-declined
// user keeps anonymous daily-active tracking); otherwise the default is "full".
// A non-empty but unrecognized mode falls back to "minimal", never "full", so a
// corrupt/typo value can't silently grant identity-tagged telemetry. A
// brand-new install is shown the consent prompt before anything is sent, so
// these defaults only govern pre-answer and edge paths.
func (c *Config) GetTelemetryMode() string {
	if isValidTelemetryMode(c.TelemetryMode) {
		return c.TelemetryMode
	}
	if c.TelemetryMode != "" && c.Telemetry == nil {
		return TelemetryMinimal
	}
	if c.Telemetry != nil {
		if *c.Telemetry {
			return TelemetryFull
		}
		return TelemetryMinimal
	}
	return TelemetryFull
}

// TelemetryConfigured reports whether the user already has an explicit
// telemetry preference on disk (a valid mode field or the legacy bool). Used
// to skip the first-launch consent prompt for upgrading users. An invalid mode
// value does not count as configured, so consent still applies.
func (c *Config) TelemetryConfigured() bool {
	return isValidTelemetryMode(c.TelemetryMode) || c.Telemetry != nil
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
