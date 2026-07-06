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
		{"light acts on critical", config.SuspendLight, perfwatch.PressureCritical, true, 2 * time.Hour},
		{"balanced housekeeps at normal", config.SuspendBalanced, perfwatch.PressureNormal, true, 4 * time.Hour},
		{"balanced tightens at warning", config.SuspendBalanced, perfwatch.PressureWarning, true, 45 * time.Minute},
		{"balanced tightens at critical", config.SuspendBalanced, perfwatch.PressureCritical, true, 45 * time.Minute},
		{"aggressive acts at normal", config.SuspendAggressive, perfwatch.PressureNormal, true, 20 * time.Minute},
		{"aggressive tightens at warning", config.SuspendAggressive, perfwatch.PressureWarning, true, 10 * time.Minute},
		{"unknown pressure: light stays off", config.SuspendLight, perfwatch.PressureUnknown, false, 0},
		{"unknown pressure: aggressive still acts", config.SuspendAggressive, perfwatch.PressureUnknown, true, 20 * time.Minute},
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
