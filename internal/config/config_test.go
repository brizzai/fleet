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
