package ui

import (
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/perfwatch"
)

// suspendIdleThreshold encodes the aggressiveness-mode table. Lock it down so a
// tweak to one mode can't silently change another.
func TestSuspendIdleThreshold(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		level   int
		wantAct bool
		wantMin time.Duration
	}{
		{"off never acts (even critical)", config.SuspendOff, perfwatch.PressureCritical, false, 0},
		{"light ignores normal", config.SuspendLight, perfwatch.PressureNormal, false, 0},
		{"light ignores warning", config.SuspendLight, perfwatch.PressureWarning, false, 0},
		{"light acts on critical", config.SuspendLight, perfwatch.PressureCritical, true, 24 * time.Hour},
		{"balanced ignores normal", config.SuspendBalanced, perfwatch.PressureNormal, false, 0},
		{"balanced acts at warning", config.SuspendBalanced, perfwatch.PressureWarning, true, 4 * time.Hour},
		{"balanced acts at critical", config.SuspendBalanced, perfwatch.PressureCritical, true, 4 * time.Hour},
		{"aggressive ignores normal", config.SuspendAggressive, perfwatch.PressureNormal, false, 0},
		{"aggressive acts at warning", config.SuspendAggressive, perfwatch.PressureWarning, true, time.Hour},
		{"aggressive acts at critical", config.SuspendAggressive, perfwatch.PressureCritical, true, time.Hour},
		{"unknown pressure: light stays off", config.SuspendLight, perfwatch.PressureUnknown, false, 0},
		{"unknown pressure: aggressive stays off", config.SuspendAggressive, perfwatch.PressureUnknown, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMin, gotAct := suspendIdleThreshold(tt.mode, tt.level)
			if gotAct != tt.wantAct {
				t.Fatalf("act = %v, want %v", gotAct, tt.wantAct)
			}
			if tt.wantAct && gotMin != tt.wantMin {
				t.Fatalf("minIdle = %v, want %v", gotMin, tt.wantMin)
			}
		})
	}
}
