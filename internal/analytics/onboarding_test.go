package analytics

import (
	"os"
	"path/filepath"
	"testing"
)

// withTempHome sets HOME to a temp dir so install_state.json lands somewhere
// disposable. Returns a cleanup func.
func withTempHome(t *testing.T) func() {
	t.Helper()
	dir := t.TempDir()
	prev := os.Getenv("HOME")
	if err := os.Setenv("HOME", dir); err != nil {
		t.Fatalf("setenv HOME: %v", err)
	}
	return func() { _ = os.Setenv("HOME", prev) }
}

func TestMarkOnboardingMilestoneIdempotent(t *testing.T) {
	cleanup := withTempHome(t)
	defer cleanup()

	// First call on a fresh install should fire each milestone.
	for _, m := range []string{
		MilestoneFirstLaunch,
		MilestoneFirstSession,
		MilestoneFirstAttach,
		MilestoneFirstClaudeResponse,
		MilestoneFirstQuit,
	} {
		if !MarkOnboardingMilestone(m) {
			t.Errorf("MarkOnboardingMilestone(%q) first call = false, want true", m)
		}
	}

	// Second call on each milestone should return false.
	for _, m := range []string{
		MilestoneFirstLaunch,
		MilestoneFirstSession,
		MilestoneFirstAttach,
		MilestoneFirstClaudeResponse,
		MilestoneFirstQuit,
	} {
		if MarkOnboardingMilestone(m) {
			t.Errorf("MarkOnboardingMilestone(%q) second call = true, want false", m)
		}
	}

	// Unknown milestone returns false.
	if MarkOnboardingMilestone("not_a_milestone") {
		t.Errorf("MarkOnboardingMilestone with unknown name = true, want false")
	}

	// install_state.json should exist with non-zero size.
	statePath := filepath.Join(os.Getenv("HOME"), ".config", "fleet", "install_state.json")
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("install_state.json missing: %v", err)
	}
	if info.Size() == 0 {
		t.Errorf("install_state.json is empty")
	}
}

func TestSecondsSinceInstallOnFreshInstall(t *testing.T) {
	cleanup := withTempHome(t)
	defer cleanup()

	// With no state file the call should still return a finite value
	// (close to zero — install_state is synthesized with InstalledAt=now).
	v := SecondsSinceInstall()
	if v < 0 || v > 60 {
		t.Errorf("SecondsSinceInstall() on fresh install = %v, want ~0", v)
	}
}
