package config

import (
	"encoding/json"
	"os"
	"testing"
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

func TestIsTelemetryEnabled(t *testing.T) {
	t.Run("nil defaults to true", func(t *testing.T) {
		cfg := &Config{}
		if !cfg.IsTelemetryEnabled() {
			t.Error("expected true when Telemetry is nil")
		}
	})

	t.Run("true", func(t *testing.T) {
		v := true
		cfg := &Config{Telemetry: &v}
		if !cfg.IsTelemetryEnabled() {
			t.Error("expected true")
		}
	})

	t.Run("false", func(t *testing.T) {
		v := false
		cfg := &Config{Telemetry: &v}
		if cfg.IsTelemetryEnabled() {
			t.Error("expected false")
		}
	})
}

func TestGetSidebarPct(t *testing.T) {
	t.Run("nil defaults to 35", func(t *testing.T) {
		cfg := &Config{}
		if got := cfg.GetSidebarPct(); got != 35 {
			t.Errorf("GetSidebarPct() = %d, want 35", got)
		}
	})

	t.Run("value within range", func(t *testing.T) {
		v := 40
		cfg := &Config{SidebarPct: &v}
		if got := cfg.GetSidebarPct(); got != 40 {
			t.Errorf("GetSidebarPct() = %d, want 40", got)
		}
	})

	t.Run("min boundary", func(t *testing.T) {
		v := 20
		cfg := &Config{SidebarPct: &v}
		if got := cfg.GetSidebarPct(); got != 20 {
			t.Errorf("GetSidebarPct() = %d, want 20", got)
		}
	})

	t.Run("max boundary", func(t *testing.T) {
		v := 60
		cfg := &Config{SidebarPct: &v}
		if got := cfg.GetSidebarPct(); got != 60 {
			t.Errorf("GetSidebarPct() = %d, want 60", got)
		}
	})

	t.Run("below min clamps to 20", func(t *testing.T) {
		v := 10
		cfg := &Config{SidebarPct: &v}
		if got := cfg.GetSidebarPct(); got != 20 {
			t.Errorf("GetSidebarPct() = %d, want 20", got)
		}
	})

	t.Run("above max clamps to 60", func(t *testing.T) {
		v := 80
		cfg := &Config{SidebarPct: &v}
		if got := cfg.GetSidebarPct(); got != 60 {
			t.Errorf("GetSidebarPct() = %d, want 60", got)
		}
	})

	t.Run("zero clamps to 20", func(t *testing.T) {
		v := 0
		cfg := &Config{SidebarPct: &v}
		if got := cfg.GetSidebarPct(); got != 20 {
			t.Errorf("GetSidebarPct() = %d, want 20", got)
		}
	})

	t.Run("negative clamps to 20", func(t *testing.T) {
		v := -5
		cfg := &Config{SidebarPct: &v}
		if got := cfg.GetSidebarPct(); got != 20 {
			t.Errorf("GetSidebarPct() = %d, want 20", got)
		}
	})
}

func TestSetSidebarPct(t *testing.T) {
	t.Run("normal value", func(t *testing.T) {
		cfg := &Config{}
		cfg.SetSidebarPct(40)
		if cfg.SidebarPct == nil || *cfg.SidebarPct != 40 {
			t.Errorf("expected 40, got %v", cfg.SidebarPct)
		}
	})

	t.Run("below min clamps", func(t *testing.T) {
		cfg := &Config{}
		cfg.SetSidebarPct(10)
		if cfg.SidebarPct == nil || *cfg.SidebarPct != 20 {
			t.Errorf("expected 20, got %v", cfg.SidebarPct)
		}
	})

	t.Run("above max clamps", func(t *testing.T) {
		cfg := &Config{}
		cfg.SetSidebarPct(75)
		if cfg.SidebarPct == nil || *cfg.SidebarPct != 60 {
			t.Errorf("expected 60, got %v", cfg.SidebarPct)
		}
	})
}

func TestStepSidebarPct(t *testing.T) {
	t.Run("full upward sequence from 20", func(t *testing.T) {
		cfg := &Config{}
		cfg.SetSidebarPct(20)
		// Expected grid: 20, 23, 25, 28, 30, 33, 35, 38, 40, 43, 45, 48, 50, 53, 55, 58, 60
		expected := []int{23, 25, 28, 30, 33, 35, 38, 40, 43, 45, 48, 50, 53, 55, 58, 60}
		for i, want := range expected {
			cfg.StepSidebarPct(1)
			got := cfg.GetSidebarPct()
			if got != want {
				t.Errorf("step %d: got %d, want %d", i+1, got, want)
			}
		}
	})

	t.Run("full downward sequence from 60", func(t *testing.T) {
		cfg := &Config{}
		cfg.SetSidebarPct(60)
		expected := []int{58, 55, 53, 50, 48, 45, 43, 40, 38, 35, 33, 30, 28, 25, 23, 20}
		for i, want := range expected {
			cfg.StepSidebarPct(-1)
			got := cfg.GetSidebarPct()
			if got != want {
				t.Errorf("step %d: got %d, want %d", i+1, got, want)
			}
		}
	})

	t.Run("clamps at min", func(t *testing.T) {
		cfg := &Config{}
		cfg.SetSidebarPct(20)
		cfg.StepSidebarPct(-1)
		if got := cfg.GetSidebarPct(); got != 20 {
			t.Errorf("got %d, want 20 (clamped at min)", got)
		}
	})

	t.Run("clamps at max", func(t *testing.T) {
		cfg := &Config{}
		cfg.SetSidebarPct(60)
		cfg.StepSidebarPct(1)
		if got := cfg.GetSidebarPct(); got != 60 {
			t.Errorf("got %d, want 60 (clamped at max)", got)
		}
	})
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
	sidebarPct := 45
	original := &Config{
		TickIntervalSec:    5,
		DefaultProjectPath: "/home/user/projects",
		Editor:             "nvim",
		Theme:              "catppuccin-mocha",
		AutoNameSessions:   &autoName,
		AutoUpdate:         &autoUpdate,
		CopyClaudeSettings: &copySettings,
		EnterMode:          "split",
		SidebarPct:         &sidebarPct,
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
	if loaded.SidebarPct == nil || *loaded.SidebarPct != *original.SidebarPct {
		t.Errorf("SidebarPct mismatch")
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
	for _, key := range []string{"editor", "theme", "default_project_path", "enter_mode", "sidebar_pct"} {
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
