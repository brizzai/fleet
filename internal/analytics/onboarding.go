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
	FirstRunTraceAt       time.Time `json:"first_run_trace_at,omitempty"`
}

// Milestone names. Used as both event names (prefixed onboarding_) and as
// keys into installState — kept aligned via the milestoneField switch below.
const (
	MilestoneFirstLaunch         = "first_launch"
	MilestoneFirstSession        = "first_session"
	MilestoneFirstAttach         = "first_attach"
	MilestoneFirstClaudeResponse = "first_claude_response"
	MilestoneFirstQuit           = "first_quit"
	// MilestoneFirstRunTrace gates the one-shot first-run action trace. Unlike
	// the milestones above it is a stop signal as much as a send-once flag:
	// recording stops the moment it is marked (see internal/ui/firstrun_trace.go).
	MilestoneFirstRunTrace = "first_run_trace"
)

var onboardingMu sync.Mutex

func installStatePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fleet", "install_state.json")
}

// loadInstallState reads ~/.config/fleet/install_state.json. The second return
// value (freshInstall) is true ONLY when the file genuinely doesn't exist —
// permission errors and other I/O failures return false so we don't keep
// firing one-shot onboarding events forever on a system where state can be
// read but not written (or vice versa).
func loadInstallState() (*installState, bool) {
	path := installStatePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &installState{InstalledAt: time.Now()}, true
		}
		// Permission / transient I/O: synthesize state in-memory but treat
		// as a previously-seen install so milestones don't re-fire.
		return &installState{InstalledAt: time.Now()}, false
	}
	var s installState
	if err := json.Unmarshal(data, &s); err != nil {
		// Corrupt file — same conservative stance as I/O failure: don't
		// re-fire onboarding events for someone who has the app installed.
		return &installState{InstalledAt: time.Now()}, false
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

	if milestone == MilestoneFirstLaunch {
		// Synthetic — fresh install IS the first launch. No timestamp field;
		// the InstalledAt timestamp doubles as first-launch time.
		if !freshInstall {
			return false
		}
		if err := state.save(); err != nil {
			// Couldn't persist — refuse to fire so the event isn't lost-and-resent.
			return false
		}
		return true
	}

	field := milestoneField(state, milestone)
	if field == nil || !field.IsZero() {
		return false
	}
	*field = time.Now()
	if err := state.save(); err != nil {
		return false
	}
	return true
}

// MilestoneReached reports whether a milestone has already been marked, without
// marking it. Callers that need to know *before* the milestone's moment — the
// first-run trace recorder decides at startup whether to record at all — can't
// use MarkOnboardingMilestone for the question, since asking it would answer it.
//
// MilestoneFirstLaunch has no timestamp field of its own (the install being
// known at all is the mark), so it reports reached for any install already on
// disk.
func MilestoneReached(milestone string) bool {
	onboardingMu.Lock()
	defer onboardingMu.Unlock()

	state, freshInstall := loadInstallState()
	if milestone == MilestoneFirstLaunch {
		return !freshInstall
	}
	field := milestoneField(state, milestone)
	return field != nil && !field.IsZero()
}

// milestoneField maps a milestone name onto its timestamp in installState, so
// the marker and the reader can't drift into disagreeing about which field a
// name means. MilestoneFirstLaunch has no field (see MarkOnboardingMilestone)
// and an unknown name has none either; both return nil.
func milestoneField(s *installState, milestone string) *time.Time {
	switch milestone {
	case MilestoneFirstSession:
		return &s.FirstSessionAt
	case MilestoneFirstAttach:
		return &s.FirstAttachAt
	case MilestoneFirstClaudeResponse:
		return &s.FirstClaudeResponseAt
	case MilestoneFirstQuit:
		return &s.FirstQuitAt
	case MilestoneFirstRunTrace:
		return &s.FirstRunTraceAt
	}
	return nil
}

// SecondsSinceInstall returns the seconds elapsed since the install was first
// seen. Returns a near-zero value when the install_state.json file doesn't
// exist or is corrupt — loadInstallState synthesizes InstalledAt = time.Now()
// in those cases, so the duration is microseconds rather than exactly 0.
func SecondsSinceInstall() float64 {
	state, _ := loadInstallState()
	return time.Since(state.InstalledAt).Seconds()
}
