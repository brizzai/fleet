package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/claudeaccount"
)

func TestIsAutoNameEnabled(t *testing.T) {
	t.Run("nil defaults to true", func(t *testing.T) {
		cfg := &Config{}
		if !cfg.IsAutoNameEnabled() {
			t.Error("expected true when AutoNameSessions is nil")
		}
	})

	t.Run("true", func(t *testing.T) {
		v := true
		cfg := &Config{AutoNameSessions: &v}
		if !cfg.IsAutoNameEnabled() {
			t.Error("expected true")
		}
	})

	t.Run("false", func(t *testing.T) {
		v := false
		cfg := &Config{AutoNameSessions: &v}
		if cfg.IsAutoNameEnabled() {
			t.Error("expected false")
		}
	})
}

func TestIsAutoUpdateEnabled(t *testing.T) {
	t.Run("nil defaults to true", func(t *testing.T) {
		cfg := &Config{}
		if !cfg.IsAutoUpdateEnabled() {
			t.Error("expected true when AutoUpdate is nil")
		}
	})

	t.Run("true", func(t *testing.T) {
		v := true
		cfg := &Config{AutoUpdate: &v}
		if !cfg.IsAutoUpdateEnabled() {
			t.Error("expected true")
		}
	})

	t.Run("false", func(t *testing.T) {
		v := false
		cfg := &Config{AutoUpdate: &v}
		if cfg.IsAutoUpdateEnabled() {
			t.Error("expected false")
		}
	})

	t.Run("FLEET_AUTO_UPDATE_DISABLED overrides config=true", func(t *testing.T) {
		t.Setenv("FLEET_AUTO_UPDATE_DISABLED", "1")
		v := true
		cfg := &Config{AutoUpdate: &v}
		if cfg.IsAutoUpdateEnabled() {
			t.Error("env var should override config=true")
		}
	})

	t.Run("FLEET_AUTO_UPDATE_DISABLED accepts truthy values", func(t *testing.T) {
		for _, val := range []string{"1", "true", "TRUE", "yes", "y", "on"} {
			t.Setenv("FLEET_AUTO_UPDATE_DISABLED", val)
			cfg := &Config{}
			if cfg.IsAutoUpdateEnabled() {
				t.Errorf("value %q should disable auto-update", val)
			}
		}
	})

	t.Run("FLEET_AUTO_UPDATE_DISABLED ignores non-truthy values", func(t *testing.T) {
		for _, val := range []string{"", "0", "false", "no", "off", "garbage"} {
			t.Setenv("FLEET_AUTO_UPDATE_DISABLED", val)
			cfg := &Config{} // default true
			if !cfg.IsAutoUpdateEnabled() {
				t.Errorf("value %q should NOT disable auto-update (default true)", val)
			}
		}
	})
}

func TestGetEditor(t *testing.T) {
	t.Run("configured", func(t *testing.T) {
		cfg := &Config{Editor: "vim"}
		if got := cfg.GetEditor(); got != "vim" {
			t.Errorf("got %q, want vim", got)
		}
	})

	t.Run("env fallback", func(t *testing.T) {
		cfg := &Config{}
		old := os.Getenv("EDITOR")
		os.Setenv("EDITOR", "nano")
		defer os.Setenv("EDITOR", old)

		if got := cfg.GetEditor(); got != "nano" {
			t.Errorf("got %q, want nano", got)
		}
	})

	t.Run("default code", func(t *testing.T) {
		cfg := &Config{}
		old := os.Getenv("EDITOR")
		os.Unsetenv("EDITOR")
		defer os.Setenv("EDITOR", old)

		if got := cfg.GetEditor(); got != "code" {
			t.Errorf("got %q, want code", got)
		}
	})
}

func TestIsCopyClaudeSettingsEnabled(t *testing.T) {
	t.Run("nil defaults to true", func(t *testing.T) {
		cfg := &Config{}
		if !cfg.IsCopyClaudeSettingsEnabled() {
			t.Error("expected true when CopyClaudeSettings is nil")
		}
	})

	t.Run("true", func(t *testing.T) {
		v := true
		cfg := &Config{CopyClaudeSettings: &v}
		if !cfg.IsCopyClaudeSettingsEnabled() {
			t.Error("expected true")
		}
	})

	t.Run("false", func(t *testing.T) {
		v := false
		cfg := &Config{CopyClaudeSettings: &v}
		if cfg.IsCopyClaudeSettingsEnabled() {
			t.Error("expected false")
		}
	})
}

func TestGetTelemetryMode(t *testing.T) {
	ptr := func(b bool) *bool { return &b }

	cases := []struct {
		name string
		cfg  *Config
		want string
	}{
		{"unset defaults to full", &Config{}, TelemetryFull},
		{"explicit full", &Config{TelemetryMode: TelemetryFull}, TelemetryFull},
		{"explicit minimal", &Config{TelemetryMode: TelemetryMinimal}, TelemetryMinimal},
		{"explicit off", &Config{TelemetryMode: TelemetryOff}, TelemetryOff},
		{"unrecognized mode falls back to minimal (conservative)", &Config{TelemetryMode: "bogus"}, TelemetryMinimal},
		{"legacy true migrates to full", &Config{Telemetry: ptr(true)}, TelemetryFull},
		{"legacy false (old decline) migrates to minimal", &Config{Telemetry: ptr(false)}, TelemetryMinimal},
		{"explicit mode overrides legacy bool", &Config{TelemetryMode: TelemetryOff, Telemetry: ptr(true)}, TelemetryOff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.GetTelemetryMode(); got != tc.want {
				t.Errorf("GetTelemetryMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTelemetryConfigured(t *testing.T) {
	ptr := func(b bool) *bool { return &b }

	if (&Config{}).TelemetryConfigured() {
		t.Error("empty config should report telemetry not configured")
	}
	if !(&Config{TelemetryMode: TelemetryMinimal}).TelemetryConfigured() {
		t.Error("explicit mode should report configured")
	}
	if !(&Config{Telemetry: ptr(false)}).TelemetryConfigured() {
		t.Error("legacy bool should report configured")
	}
	if (&Config{TelemetryMode: "bogus"}).TelemetryConfigured() {
		t.Error("invalid mode should not report configured (consent must still apply)")
	}
	if !(&Config{TelemetryMode: "bogus", Telemetry: ptr(true)}).TelemetryConfigured() {
		t.Error("invalid mode with a legacy bool should still report configured")
	}
}

func TestGetEnterMode(t *testing.T) {
	tests := []struct {
		name      string
		enterMode string
		want      string
	}{
		{"empty defaults to attach", "", "attach"},
		{"attach", "attach", "attach"},
		{"split", "split", "split"},
		{"invalid defaults to attach", "unknown", "attach"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{EnterMode: tt.enterMode}
			if got := cfg.GetEnterMode(); got != tt.want {
				t.Errorf("GetEnterMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfigJSONRoundTrip(t *testing.T) {
	autoName := true
	autoUpdate := false
	copySettings := true
	original := &Config{
		TickIntervalSec:    5,
		DefaultProjectPath: "/home/user/projects",
		Editor:             "nvim",
		Theme:              "catppuccin-mocha",
		AutoNameSessions:   &autoName,
		AutoUpdate:         &autoUpdate,
		CopyClaudeSettings: &copySettings,
		EnterMode:          "split",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if loaded.TickIntervalSec != original.TickIntervalSec {
		t.Errorf("TickIntervalSec: got %d, want %d", loaded.TickIntervalSec, original.TickIntervalSec)
	}
	if loaded.DefaultProjectPath != original.DefaultProjectPath {
		t.Errorf("DefaultProjectPath: got %q, want %q", loaded.DefaultProjectPath, original.DefaultProjectPath)
	}
	if loaded.Editor != original.Editor {
		t.Errorf("Editor: got %q, want %q", loaded.Editor, original.Editor)
	}
	if loaded.Theme != original.Theme {
		t.Errorf("Theme: got %q, want %q", loaded.Theme, original.Theme)
	}
	if loaded.AutoNameSessions == nil || *loaded.AutoNameSessions != *original.AutoNameSessions {
		t.Errorf("AutoNameSessions mismatch")
	}
	if loaded.AutoUpdate == nil || *loaded.AutoUpdate != *original.AutoUpdate {
		t.Errorf("AutoUpdate mismatch")
	}
	if loaded.CopyClaudeSettings == nil || *loaded.CopyClaudeSettings != *original.CopyClaudeSettings {
		t.Errorf("CopyClaudeSettings mismatch")
	}
	if loaded.EnterMode != original.EnterMode {
		t.Errorf("EnterMode: got %q, want %q", loaded.EnterMode, original.EnterMode)
	}
}

func TestConfigUnmarshalPartialJSON(t *testing.T) {
	// Only some fields set — rest should be zero values.
	data := []byte(`{"editor":"vim","theme":"nord"}`)

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if cfg.Editor != "vim" {
		t.Errorf("Editor: got %q, want %q", cfg.Editor, "vim")
	}
	if cfg.Theme != "nord" {
		t.Errorf("Theme: got %q, want %q", cfg.Theme, "nord")
	}
	if cfg.TickIntervalSec != 0 {
		t.Errorf("TickIntervalSec: got %d, want 0 (unset)", cfg.TickIntervalSec)
	}
	if cfg.AutoNameSessions != nil {
		t.Error("expected AutoNameSessions to be nil for unset field")
	}
	if cfg.AutoUpdate != nil {
		t.Error("expected AutoUpdate to be nil for unset field")
	}
}

func TestConfigUnmarshalInvalidJSON(t *testing.T) {
	data := []byte(`{invalid json}`)
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestConfigOmitEmptyFields(t *testing.T) {
	// Empty config should produce minimal JSON (omitempty).
	cfg := &Config{}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal raw failed: %v", err)
	}

	// With omitempty, zero-value fields should not be present.
	for _, key := range []string{"editor", "theme", "default_project_path", "enter_mode"} {
		if _, ok := raw[key]; ok {
			t.Errorf("expected %q to be omitted for zero value", key)
		}
	}
}

func TestIsFirstRun(t *testing.T) {
	// Isolate the config path to a fresh temp HOME so no real config leaks in.
	t.Setenv("HOME", t.TempDir())

	// No config file yet → first run.
	cfg := Load()
	if !cfg.IsFirstRun() {
		t.Error("Load with no config file should report IsFirstRun() == true")
	}

	// After a Save, the file exists, so a subsequent Load is not a first run.
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if cfg2 := Load(); cfg2.IsFirstRun() {
		t.Error("Load with an existing config file should report IsFirstRun() == false")
	}

	// loadedFromDisk is unexported, so a hand-built Config (e.g. in other
	// tests) is treated as a first run — the safe default.
	if !(&Config{}).IsFirstRun() {
		t.Error("zero-value Config should report IsFirstRun() == true")
	}
}

func TestSavePreservesUnknownKeys(t *testing.T) {
	// Regression: an older binary whose struct lacks a field a newer fleet wrote
	// must not silently drop it on save (the theme-reset / consent-prompt-
	// reappears bug). Load captures unknown keys; Save re-merges them.
	t.Setenv("HOME", t.TempDir())

	// Simulate a config written by a newer fleet: a key this struct knows
	// (theme) plus one it doesn't (future_feature).
	onDisk := `{"theme":"nord","future_feature":{"nested":true},"future_flag":42}`
	if err := os.MkdirAll(filepath.Dir(DefaultConfigPath()), 0700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(DefaultConfigPath(), []byte(onDisk), 0600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	cfg := Load()
	if cfg.Theme != "nord" {
		t.Errorf("Theme: got %q, want %q", cfg.Theme, "nord")
	}

	// A normal mutation + save (as the settings dialog would do).
	cfg.Editor = "vim"
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(DefaultConfigPath())
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal saved: %v", err)
	}
	for _, k := range []string{"future_feature", "future_flag"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("unknown key %q was dropped on save; got %s", k, data)
		}
	}
	if string(raw["theme"]) != `"nord"` {
		t.Errorf("theme not preserved: got %s", raw["theme"])
	}
	if string(raw["editor"]) != `"vim"` {
		t.Errorf("mutation not persisted: got %s", raw["editor"])
	}
}

func TestSaveNoUnknownKeysIsByteIdenticalToStructMarshal(t *testing.T) {
	// The common case (no unknown keys on disk) must stay exactly the struct's
	// own ordered marshal — the merge path only engages when there's something
	// to preserve.
	t.Setenv("HOME", t.TempDir())

	cfg := &Config{Theme: "nord", Editor: "vim"}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := os.ReadFile(DefaultConfigPath())
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	want, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("output diverged from struct marshal:\n got: %s\nwant: %s", got, want)
	}
}

// SetAllowedAccounts deletes rather than stores when the list empties, so an
// origin returns to genuinely unrestricted instead of carrying a list that
// happens to name everyone today. Storing the full set would read the same now
// and silently lock out the next account added.
func TestSetAllowedAccountsDeletesOnEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := &Config{}

	if err := c.SetAllowedAccounts("origin:github.com/acme/api", []string{"a@x.com"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := c.GetAllowedAccounts("origin:github.com/acme/api"); len(got) != 1 {
		t.Fatalf("GetAllowedAccounts = %v, want one entry", got)
	}

	if err := c.SetAllowedAccounts("origin:github.com/acme/api", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := c.GetAllowedAccounts("origin:github.com/acme/api"); len(got) != 0 {
		t.Errorf("GetAllowedAccounts = %v, want unrestricted", got)
	}
	// Nil, not an empty map: omitempty has to drop the key from the file once the
	// last restriction goes, or every config carries a dead "allowed_accounts".
	if c.AllowedAccounts != nil {
		t.Errorf("AllowedAccounts = %v, want nil once the last origin clears", c.AllowedAccounts)
	}

	data, err := os.ReadFile(DefaultConfigPath())
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(data), "allowed_accounts") {
		t.Errorf("saved config still carries allowed_accounts:\n%s", data)
	}
}

// The bug this closes: this getter switched on its own copy of the strategy
// list, so when the least-used mode split in two it flattened both back to the
// legacy alias. One whole mode became unreachable — it never survived a
// round-trip, so the picker could not leave it and Select never saw it.
//
// Every strategy must come back exactly as stored.
func TestGetAccountStrategyRoundTripsEveryStrategy(t *testing.T) {
	for _, s := range claudeaccount.Strategies {
		c := &Config{AccountStrategy: s}
		if got := c.GetAccountStrategy(); got != s {
			t.Errorf("stored %q, GetAccountStrategy() = %q — the value did not survive", s, got)
		}
	}
}

// The two inputs that are not members: an unset config and the pre-split alias
// both resolve to the default, and hand-edited casing still resolves.
func TestGetAccountStrategyResolvesAliasAndCasing(t *testing.T) {
	for in, want := range map[string]string{
		"":                              claudeaccount.StrategyLeastUsedWeekly,
		claudeaccount.StrategyLeastUsed: claudeaccount.StrategyLeastUsedWeekly,
		"nonsense":                      claudeaccount.StrategyLeastUsedWeekly,
		" Least_Used_5H ":               claudeaccount.StrategyLeastUsed5H,
		"MANUAL":                        claudeaccount.StrategyManual,
	} {
		c := &Config{AccountStrategy: in}
		if got := c.GetAccountStrategy(); got != want {
			t.Errorf("GetAccountStrategy(%q) = %q, want %q", in, got, want)
		}
	}
}
