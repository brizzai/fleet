package analytics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// installState tracks first-time milestones per install so onboarding events
// fire exactly once. Persisted at ~/.config/fleet/install_state.json.
type installState struct {
	InstalledAt           time.Time `json:"installed_at"`
	FirstSessionAt        time.Time `json:"first_session_at,omitempty"`
	FirstAttachAt         time.Time `json:"first_attach_at,omitempty"`
	FirstClaudeResponseAt time.Time `json:"first_claude_response_at,omitempty"`
	FirstQuitAt           time.Time `json:"first_quit_at,omitempty"`
}

// Milestone names. Used as both event names (prefixed onboarding_) and as
// keys into installState — kept aligned via the milestoneField switch below.
const (
	MilestoneFirstLaunch         = "first_launch"
	MilestoneFirstSession        = "first_session"
	MilestoneFirstAttach         = "first_attach"
	MilestoneFirstClaudeResponse = "first_claude_response"
	MilestoneFirstQuit           = "first_quit"
)

var onboardingMu sync.Mutex

func installStatePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fleet", "install_state.json")
}

// loadInstallState reads ~/.config/fleet/install_state.json. Returns a fresh
// state with InstalledAt=now() if the file doesn't exist; the second return
// value is true when this is a brand-new install (first launch).
func loadInstallState() (*installState, bool) {
	path := installStatePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return &installState{InstalledAt: time.Now()}, true
	}
	var s installState
	if err := json.Unmarshal(data, &s); err != nil {
		// Corrupt — treat as fresh install so we don't silently miss the user.
		return &installState{InstalledAt: time.Now()}, true
	}
	if s.InstalledAt.IsZero() {
		s.InstalledAt = time.Now()
	}
	return &s, false
}

func (s *installState) save() error {
	path := installStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// MarkOnboardingMilestone returns true if `milestone` was reached for the
// first time on this install (and persists that fact). Returns false on every
// subsequent call. Callers should only emit the corresponding onboarding_*
// event when this returns true.
//
// The first call ever — when install_state.json doesn't exist — also acts as
// the "first launch" detection. SecondsSinceInstall (a useful Distribution
// attribute) can be derived from time.Since(InstalledAt).
func MarkOnboardingMilestone(milestone string) bool {
	onboardingMu.Lock()
	defer onboardingMu.Unlock()

	state, freshInstall := loadInstallState()

	var field *time.Time
	switch milestone {
	case MilestoneFirstLaunch:
		// Synthetic — fresh install IS the first launch. No timestamp field;
		// the InstalledAt timestamp doubles as first-launch time.
		if freshInstall {
			_ = state.save()
			return true
		}
		return false
	case MilestoneFirstSession:
		field = &state.FirstSessionAt
	case MilestoneFirstAttach:
		field = &state.FirstAttachAt
	case MilestoneFirstClaudeResponse:
		field = &state.FirstClaudeResponseAt
	case MilestoneFirstQuit:
		field = &state.FirstQuitAt
	default:
		return false
	}

	if !field.IsZero() {
		return false
	}
	*field = time.Now()
	_ = state.save()
	return true
}

// SecondsSinceInstall returns the seconds elapsed since the install was first
// seen. Returns 0 if the install_state.json file doesn't exist or is corrupt.
func SecondsSinceInstall() float64 {
	state, _ := loadInstallState()
	return time.Since(state.InstalledAt).Seconds()
}
