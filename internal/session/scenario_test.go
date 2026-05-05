package session

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

// --- Mock pane capturer ---

type mockPane struct {
	content string
	dead    bool
	alive   bool // controls IsAlive via !IsPaneDead
}

func (m *mockPane) CapturePane() (string, error) { return m.content, nil }
func (m *mockPane) IsPaneDead() bool             { return m.dead }

// --- Scenario types ---

// ScenarioEvent describes something that happens at a point in time.
type ScenarioEvent struct {
	At          time.Duration // relative to scenario start
	Hook        string        // hook status: "running", "waiting", "finished", "dead", or "" (no change)
	Pane        string        // pane content to set (raw string or "@fixture:filename.txt" for golden file)
	PaneDead    bool          // simulate pane death
	Acknowledge bool          // simulate user acknowledging the session
}

// ScenarioCheck asserts the session status at a point in time.
type ScenarioCheck struct {
	At       time.Duration
	Expected Status
}

// Scenario is a named sequence of events and status checks.
type Scenario struct {
	Name   string
	Events []ScenarioEvent
	Checks []ScenarioCheck
}

// --- Replay engine ---

// timelineEntry merges events and checks into a single sorted timeline.
type timelineEntry struct {
	at    time.Duration
	event *ScenarioEvent
	check *ScenarioCheck
}

func runScenario(t *testing.T, sc Scenario) {
	t.Helper()
	debuglog.Init()

	mock := &mockPane{alive: true}
	s := &Session{
		ID:           "test-scenario",
		Title:        sc.Name,
		Status:       StatusStarting,
		paneCapturer: mock,
	}

	// Build sorted timeline.
	var timeline []timelineEntry
	for i := range sc.Events {
		e := sc.Events[i]
		timeline = append(timeline, timelineEntry{at: e.At, event: &e})
	}
	for i := range sc.Checks {
		c := sc.Checks[i]
		timeline = append(timeline, timelineEntry{at: c.At, check: &c})
	}
	sort.SliceStable(timeline, func(i, j int) bool {
		if timeline[i].at == timeline[j].at {
			// Events before checks at the same timestamp.
			return timeline[i].event != nil && timeline[j].check != nil
		}
		return timeline[i].at < timeline[j].at
	})

	currentHook := ""
	var hookUpdatedAt time.Time

	for _, entry := range timeline {
		if entry.event != nil {
			e := entry.event

			// Apply hook status change.
			if e.Hook != "" {
				currentHook = e.Hook
				hookUpdatedAt = time.Now()
				s.UpdateHookStatus(&HookStatus{
					Status:    e.Hook,
					UpdatedAt: hookUpdatedAt,
				})
			}

			// Set pane content.
			if e.Pane != "" {
				mock.content = loadPaneContent(t, e.Pane)
			}

			// Set pane dead state.
			mock.dead = e.PaneDead

			// Acknowledge.
			if e.Acknowledge {
				s.mu.Lock()
				s.Acknowledged = true
				s.mu.Unlock()
			}

			// Adjust content timing for stability checks.
			// If this event sets the same pane content as before, we need to
			// simulate time passing. We do this by adjusting lastContentChangeAt
			// backwards by the delta between this event and the previous one.
			if e.At > 0 {
				s.mu.Lock()
				if s.lastContentChangeAt.IsZero() {
					s.lastContentChangeAt = time.Now().Add(-e.At)
				}
				s.mu.Unlock()
			}
		}

		if entry.check != nil {
			c := entry.check

			// For time-based checks (content stability), we need to fake the
			// elapsed time. Set lastContentChangeAt to simulate the right age.
			if c.At > 0 {
				s.mu.Lock()
				if !s.lastContentChangeAt.IsZero() {
					// Content has been stable since it was set. Adjust so
					// time.Since(lastContentChangeAt) reflects the scenario time.
					s.lastContentChangeAt = time.Now().Add(-c.At)
				}
				s.mu.Unlock()
			}

			// Run UpdateStatus to get the current classification.
			s.UpdateStatus()

			got := s.GetStatus()
			if got != c.Expected {
				t.Errorf("at t=%v: expected %q, got %q (hook=%q, paneDead=%v)",
					c.At, c.Expected, got, currentHook, mock.dead)
			}
		}
	}
}

// loadPaneContent loads pane content from a string or golden fixture.
// If content starts with "@fixture:", loads from testdata/.
func loadPaneContent(t *testing.T, content string) string {
	t.Helper()
	const prefix = "@fixture:"
	if len(content) > len(prefix) && content[:len(prefix)] == prefix {
		filename := content[len(prefix):]
		data, err := os.ReadFile(filepath.Join("testdata", filename))
		if err != nil {
			t.Fatalf("failed to load fixture %s: %v", filename, err)
		}
		return string(data)
	}
	return content
}

// --- Scenarios ---

func TestScenarioHappyPath(t *testing.T) {
	runScenario(t, Scenario{
		Name: "running → waiting → approved → running → finished",
		Events: []ScenarioEvent{
			{At: 0, Hook: "running", Pane: "⠋ Working on your request...\nctrl+c to interrupt\n"},
			{At: 3 * time.Second, Hook: "waiting", Pane: "output\n❯ 1. Yes\n  2. No\nEsc to cancel\n"},
			{At: 5 * time.Second, Hook: "running", Pane: "⠋ Applying changes...\nctrl+c to interrupt\n"},
			{At: 8 * time.Second, Hook: "finished", Pane: "Done!\n\n❯ \n"},
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusRunning},
			{At: 3 * time.Second, Expected: StatusWaiting},
			{At: 5 * time.Second, Expected: StatusRunning},
			{At: 8 * time.Second, Expected: StatusFinished},
		},
	})
}

func TestScenarioUserEscapesPermission(t *testing.T) {
	runScenario(t, Scenario{
		Name: "waiting → user escapes → pane shows idle → finished",
		Events: []ScenarioEvent{
			{At: 0, Hook: "waiting", Pane: "output\n❯ 1. Yes\n  2. No\nEsc to cancel\n"},
			{At: 3 * time.Second, Pane: "❯ \n"}, // user pressed Escape, no hook fires
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusWaiting},
			{At: 3 * time.Second, Expected: StatusFinished},
		},
	})
}

func TestScenarioContentStabilityTimeout(t *testing.T) {
	stableContent := "Some output text\nMore output\n"
	runScenario(t, Scenario{
		Name: "running → content stable >10s → finished",
		Events: []ScenarioEvent{
			{At: 0, Hook: "running", Pane: stableContent},
		},
		Checks: []ScenarioCheck{
			{At: 5 * time.Second, Expected: StatusRunning},
			{At: 11 * time.Second, Expected: StatusFinished},
		},
	})
}

func TestScenarioSubAgentPermission(t *testing.T) {
	runScenario(t, Scenario{
		Name: "hook=finished but pane shows waiting → override to waiting",
		Events: []ScenarioEvent{
			{At: 0, Hook: "finished", Pane: "output\n❯ 1. Yes\n  2. No\nEsc to cancel\n"},
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusWaiting},
		},
	})
}

// TestScenarioStaleRunningHookMemoizesOverride reproduces the "merge-master"
// oscillation bug: a running hook that's hours stale (no Stop ever fired
// because the user only ran slash commands), combined with periodic pane
// content changes (survey popup, cursor blink, scrollback redraws) would
// flip Status back to Running every time the hash changed, then 10s+ later
// flip back to Finished. The fix memoizes the stability override via
// hookOverriddenAt, so subsequent pane changes don't re-oscillate until a
// fresh hook lands.
func TestScenarioStaleRunningHookMemoizesOverride(t *testing.T) {
	runScenario(t, Scenario{
		Name: "stale running hook: hash change after override does not flip back to running",
		Events: []ScenarioEvent{
			{At: 0, Hook: "running", Pane: "line1\n❯ \n"},
			// Simulate pane content change after the override has fired
			// (e.g. session-rating popup appears, or scrollback redraws).
			// The hook is still the stale "running" from t=0.
			{At: 13 * time.Second, Pane: "line1\nDifferent content appeared\n❯ \n"},
		},
		Checks: []ScenarioCheck{
			{At: 2 * time.Second, Expected: StatusRunning},   // baseline: first tick sets lastContentHash
			{At: 11 * time.Second, Expected: StatusFinished}, // stability override fires, memoizes via hookOverriddenAt
			{At: 13 * time.Second, Expected: StatusFinished}, // without fix: oscillates back to Running on hash change
		},
	})
}

// TestScenarioStaleRunningResumesThenReIdles covers the escape path in
// applyHookRunning's override guard: after an override has memoized
// Status=Finished on a stale running hook, if the pane shows running (genuine
// resume — e.g. sub-agent burst without a new hook), the guard must clear the
// override so subsequent ticks can re-engage the stability heuristic when
// Claude stops again. Otherwise the session is stuck at Running forever.
func TestScenarioStaleRunningResumesThenReIdles(t *testing.T) {
	runScenario(t, Scenario{
		Name: "stale running hook: override → resume (pane running) → re-idle must re-override",
		Events: []ScenarioEvent{
			{At: 0, Hook: "running", Pane: "line1\n❯ \n"},
			// Pane flips to active running (sub-agent burst before any new hook lands).
			{At: 12 * time.Second, Pane: "⠋ Working on your request...\nctrl+c to interrupt\n"},
			// Pane flips back to idle — Claude stopped again, still no new hook.
			{At: 14 * time.Second, Pane: "line1\n❯ \n"},
		},
		Checks: []ScenarioCheck{
			{At: 2 * time.Second, Expected: StatusRunning},   // baseline
			{At: 11 * time.Second, Expected: StatusFinished}, // override memoizes
			{At: 12 * time.Second, Expected: StatusRunning},  // escape path: resume detected, override cleared
			// At t=14 pane flipped back to idle. Run an UpdateStatus at t=15 to lock
			// in the new hash baseline, then verify stability re-engages by t=26.
			{At: 15 * time.Second, Expected: StatusRunning},  // new idle pane, hash baseline being set
			{At: 26 * time.Second, Expected: StatusFinished}, // >10s stable on new idle pane → re-override
		},
	})
}

func TestScenarioAcknowledgedPreventsOscillation(t *testing.T) {
	runScenario(t, Scenario{
		Name: "acknowledged idle stays idle when hook says waiting + pane idle",
		Events: []ScenarioEvent{
			{At: 0, Hook: "finished", Pane: "❯ \n", Acknowledge: true},
			{At: 2 * time.Second, Hook: "waiting", Pane: "❯ \n"}, // stale hook
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusIdle},
			{At: 2 * time.Second, Expected: StatusIdle},
		},
	})
}

func TestScenarioPaneDeath(t *testing.T) {
	runScenario(t, Scenario{
		Name: "session crash → error",
		Events: []ScenarioEvent{
			{At: 0, Hook: "running", Pane: "⠋ Working...\nctrl+c to interrupt\n"},
			{At: 2 * time.Second, PaneDead: true},
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusRunning},
			{At: 2 * time.Second, Expected: StatusError},
		},
	})
}

func TestScenarioNoHooksFallback(t *testing.T) {
	runScenario(t, Scenario{
		Name: "no hooks, pane-only detection",
		Events: []ScenarioEvent{
			{At: 0, Pane: "⠋ Working...\nctrl+c to interrupt\n"},
			{At: 2 * time.Second, Pane: "output\n❯ 1. Yes\n  2. No\nEsc to cancel\n"},
			{At: 4 * time.Second, Pane: "Done!\n\n❯ \n"},
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusRunning},
			{At: 2 * time.Second, Expected: StatusWaiting},
			{At: 4 * time.Second, Expected: StatusFinished},
		},
	})
}

func TestScenarioRealANSIPermission(t *testing.T) {
	// Uses the golden fixture from the real bug we found.
	fixture := "pane_waiting_permission_3opt.txt"
	if _, err := os.Stat(filepath.Join("testdata", fixture)); err != nil {
		t.Skipf("fixture %s not available", fixture)
	}

	runScenario(t, Scenario{
		Name: "real ANSI permission prompt: hook=waiting → should stay waiting",
		Events: []ScenarioEvent{
			{At: 0, Hook: "waiting", Pane: "@fixture:" + fixture},
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusWaiting},
		},
	})
}

func TestScenarioStaleWaitingHookStaysFinished(t *testing.T) {
	// Regression: hook says "waiting" but user already rejected the permission.
	// The pane override correctly detects idle and sets finished, but on subsequent
	// cycles the stale hook used to flip the status back to waiting/running.
	// The fix records hookOverriddenAt to prevent re-evaluation of the same stale hook.
	idlePane := "Done!\n\n❯ \n"
	slightlyDifferentPane := "Done!\n\n❯ \nstatus bar updated\n" // simulates tmux status bar change

	runScenario(t, Scenario{
		Name: "stale waiting hook: override stays sticky across cycles",
		Events: []ScenarioEvent{
			{At: 0, Hook: "waiting", Pane: idlePane},                                            // first cycle: override to finished
			{At: 2 * time.Second, Pane: slightlyDifferentPane},                                  // pane changes slightly (status bar)
			{At: 4 * time.Second, Pane: idlePane},                                               // back to original
			{At: 6 * time.Second, Hook: "running", Pane: "⠋ Working...\nctrl+c to interrupt\n"}, // NEW hook resets flag
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusFinished},               // override fires
			{At: 2 * time.Second, Expected: StatusFinished}, // stays finished (stale hook, skip)
			{At: 4 * time.Second, Expected: StatusFinished}, // still finished
			{At: 6 * time.Second, Expected: StatusRunning},  // new hook, fresh evaluation
		},
	})
}

func TestScenarioWaitingRunningCooldown(t *testing.T) {
	// Regression: when hook says "waiting" but Claude is actively working
	// (e.g. AskUserQuestion response doesn't fire UserPromptSubmit), content
	// changes in bursts. Between bursts the hash is the same for a tick,
	// causing oscillation back to waiting. The 15s cooldown prevents this.
	runScenario(t, Scenario{
		Name: "waiting → content changes → stays running during cooldown",
		Events: []ScenarioEvent{
			{At: 0, Hook: "waiting", Pane: "permission prompt\n❯ 1. Yes\n  2. No\nEsc to cancel\n"},
			{At: 3 * time.Second, Pane: "Claude is working now\nsome output\n"}, // content changed → running
			{At: 7 * time.Second},  // same content, within 15s cooldown
			{At: 18 * time.Second}, // same content, 15s after the content change
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusWaiting},
			{At: 3 * time.Second, Expected: StatusRunning},
			{At: 7 * time.Second, Expected: StatusRunning},  // cooldown keeps it running
			{At: 18 * time.Second, Expected: StatusWaiting}, // cooldown expired (3s + 15s), falls back
		},
	})
}

func TestScenarioOverriddenWaitingResumesOnSpinner(t *testing.T) {
	// Regression: hook says "waiting" but was already overridden to finished (stale).
	// User approved the permission, Claude starts working (spinner visible).
	// No UserPromptSubmit fires for permission grants. The overridden hook's
	// early return must still check pane for running and resume.
	runScenario(t, Scenario{
		Name: "overridden waiting hook + pane spinner → running",
		Events: []ScenarioEvent{
			{At: 0, Hook: "waiting", Pane: "❯ \n"},                                              // override to finished (idle prompt)
			{At: 3 * time.Second, Pane: "⠋ Working on approved task...\nctrl+c to interrupt\n"}, // user approved, Claude running
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusFinished},              // override fires
			{At: 3 * time.Second, Expected: StatusRunning}, // pane spinner detected despite overridden hook
		},
	})
}

func TestScenarioRealANSIPermissionCursorOnOption2(t *testing.T) {
	// Regression: when the user moves the menu cursor off option 1, detectWaiting
	// used to require "❯ 1." and failed. The pipeline then fell through to
	// detectFinished which false-matched "❯ 2. …" as an idle prompt, flipping
	// the acknowledged session to idle. Captured from snapshot
	// 2026-04-13T12-36-25_brainstorm-conversation-cache-.
	fixture := "pane_waiting_permission_cursor_opt2.txt"
	if _, err := os.Stat(filepath.Join("testdata", fixture)); err != nil {
		t.Skipf("fixture %s not available", fixture)
	}

	runScenario(t, Scenario{
		Name: "permission menu, cursor on option 2: stays waiting across ticks",
		Events: []ScenarioEvent{
			{At: 0, Hook: "waiting", Pane: "@fixture:" + fixture},
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusWaiting},
			{At: 4 * time.Second, Expected: StatusWaiting},
			{At: 8 * time.Second, Expected: StatusWaiting},
		},
	})
}

func TestScenarioPermissionCursorOnOption3(t *testing.T) {
	// Plain-text counterpart for cursor-on-option-3 — no real ANSI fixture
	// needed because detection runs on StripANSI'd content.
	pane := "some output\nDo you want to proceed?\n  1. Yes\n  2. No, tell Claude\n❯ 3. Ask every time\nEsc to cancel\n"
	runScenario(t, Scenario{
		Name: "permission menu, cursor on option 3: detected as waiting",
		Events: []ScenarioEvent{
			{At: 0, Hook: "waiting", Pane: pane},
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusWaiting},
		},
	})
}

func TestScenarioStaleWaitingWithSubagentSpelunking(t *testing.T) {
	// Regression: when a user approves a permission and Claude launches an
	// Explore sub-agent, hooks stop firing. The pane shows active sub-agent
	// work ("✳ Spelunking… (3m 14s · ↑ 1.8k tokens)"). Before the fix,
	// detectRunning's whimsical check only matched `· ↓ tokens`, so the
	// sub-agent activity line was ignored. Detection periodically returned
	// Finished, applyHookWaiting's pane override fired, and the session
	// (acknowledged) collapsed to idle.
	//
	// With `· ↑` matching, detection reliably returns Running on the
	// spelunking fixture, so the override to finished never fires even
	// when Acknowledged=true.
	// Captured from snapshot 2026-04-13T18-03-40_align-button-figma-design-syst.
	fixture := "pane_running_subagent_spelunking_up_arrow.txt"
	if _, err := os.Stat(filepath.Join("testdata", fixture)); err != nil {
		t.Skipf("fixture %s not available", fixture)
	}

	runScenario(t, Scenario{
		Name: "stale waiting hook + acknowledged + sub-agent spelunking: transitions waiting→running, never idle",
		Events: []ScenarioEvent{
			// Waiting on permission prompt, user previously acknowledged the session.
			{At: 0, Hook: "waiting", Pane: "permission prompt\n❯ 1. Yes\n  2. No\nEsc to cancel\n", Acknowledge: true},
			// User approves; Claude launches Explore sub-agent. No new hook fires.
			{At: 3 * time.Second, Pane: "@fixture:" + fixture},
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusWaiting},
			// Content changed while waiting AND pane shows whimsical running signal:
			// must end up Running. Before the fix, pane detection would intermittently
			// return Finished → Acknowledged collapsed it to Idle.
			{At: 3 * time.Second, Expected: StatusRunning},
		},
	})
}

func TestScenarioFinishedAutoResumeRunning(t *testing.T) {
	runScenario(t, Scenario{
		Name: "hook=finished but pane shows spinner → override to running",
		Events: []ScenarioEvent{
			{At: 0, Hook: "finished", Pane: "⠋ Thinking...\nctrl+c to interrupt\n"},
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusRunning},
		},
	})
}

func TestScenarioExtendedThinkingNoOscillation(t *testing.T) {
	// Regression: during extended thinking, Claude shows a whimsical activity line
	// with "· ↓ tokens · thinking with high effort)" format. The timer/token count
	// updates infrequently (every 10-30s), causing normalizeForHash to see the same
	// content hash for >10s. The "content stable >10s" heuristic then overrides to
	// finished, but when the timer updates, the hash changes and it flips back to
	// running — oscillating 3+ times over a ~3 minute thinking session.
	//
	// Fix: isWhimsicalActivity matches the extended thinking format in both
	// detectRunning (pane says running) and normalizeForHash (strips the line
	// so timer changes don't affect the hash).
	// Captured from snapshot 2026-04-15T12-00-04_dashboard-cohort-implementatio.
	fixture := "pane_running_extended_thinking.txt"
	if _, err := os.Stat(filepath.Join("testdata", fixture)); err != nil {
		t.Skipf("fixture %s not available", fixture)
	}

	runScenario(t, Scenario{
		Name: "extended thinking: hook=running + whimsical activity → stays running, no oscillation",
		Events: []ScenarioEvent{
			{At: 0, Hook: "running", Pane: "@fixture:" + fixture},
		},
		Checks: []ScenarioCheck{
			// Pane shows whimsical "· Gesticulating… (5m 42s · ↓ 4.2k tokens · thinking with high effort)"
			// which should be detected as running. The content-stable check should NOT
			// override to finished because normalizeForHash strips the whimsical line.
			{At: 5 * time.Second, Expected: StatusRunning},
			{At: 11 * time.Second, Expected: StatusRunning}, // would have oscillated to finished before fix
		},
	})
}

func TestScenarioWaitingFirstTickPaneRunning(t *testing.T) {
	// Regression: when a waiting hook arrives but the pane already shows an
	// active spinner (user approved a prior prompt and Claude is working on
	// the next task), the first-tick baseline branch would only set the hash
	// and return — leaving status=waiting for at least one tick. With slow
	// spinners (e.g. "✽ Blanching…"), the hash stays stable across ticks
	// because normalizeForHash strips spinner lines, so it could persist.
	//
	// Fix: first-tick branch also trusts paneStatus=running.
	// Captured from snapshot 2026-04-16T16-45-03_merge-master.
	runScenario(t, Scenario{
		Name: "first tick in waiting + pane shows running → running immediately",
		Events: []ScenarioEvent{
			{At: 0, Hook: "waiting", Pane: "output\n✽ Blanching…\n  ⎿  Tip: some tip\n\n❯ \n"},
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusRunning},
		},
	})
}

func TestScenarioAskUserQuestionNavigationStaysWaiting(t *testing.T) {
	// Regression: Claude's AskUserQuestion tool keeps the prompt open while the
	// user navigates with Tab/arrow keys. Each keystroke mutates pane content
	// (cursor moves, checkbox toggles), so the hash drifts on every tick.
	// Before the fix, applyHookWaiting's "content changed → assume running"
	// override fired on each drift, flipping status to running for ≥15s. The
	// user only saw "waiting" again once they paused for the full cooldown.
	//
	// Fix: detectWaiting matches the AskUserQuestion footer ("Tab to switch
	// questions" + "Esc to cancel"), so paneStatus=Waiting through navigation.
	// applyHookWaiting suppresses the running override when paneStatus=Waiting
	// because the prompt is clearly still on screen.
	// Captured from snapshot 2026-05-05T15-38-57_lets-start-brainstorming-ideas.
	fixture := "pane_waiting_askuserquestion_checkbox_focus.txt"
	if _, err := os.Stat(filepath.Join("testdata", fixture)); err != nil {
		t.Skipf("fixture %s not available", fixture)
	}

	// Plain-text variants that share the AskUserQuestion footer but differ in
	// the checkbox row above it — simulates the user pressing Tab to toggle
	// which question is selected. Both must be detected as waiting; only the
	// hash changes.
	stackFocus := "Round 3: Architecture\n☒ Stack  ☐ Process model  ☐ V1 scope\nEnter to select · ↑/↓ to navigate · n to add notes · Tab to switch questions · Esc to cancel\n"
	processFocus := "Round 3: Architecture\n☐ Stack  ☒ Process model  ☐ V1 scope\nEnter to select · ↑/↓ to navigate · n to add notes · Tab to switch questions · Esc to cancel\n"

	runScenario(t, Scenario{
		Name: "AskUserQuestion navigation: hook=waiting + drifting hash → stays waiting",
		Events: []ScenarioEvent{
			{At: 0, Hook: "waiting", Pane: "@fixture:" + fixture}, // real ANSI capture
			{At: 3 * time.Second, Pane: stackFocus},               // first plain-text view
			{At: 6 * time.Second, Pane: processFocus},             // user Tab'd to next question, hash drifts
			{At: 9 * time.Second, Pane: stackFocus},               // Tab'd back, hash drifts again
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusWaiting},
			{At: 3 * time.Second, Expected: StatusWaiting},  // before fix: flipped to running on hash drift
			{At: 6 * time.Second, Expected: StatusWaiting},  // before fix: still running (15s cooldown)
			{At: 9 * time.Second, Expected: StatusWaiting},  // before fix: still running
			{At: 25 * time.Second, Expected: StatusWaiting}, // long after any cooldown
		},
	})
}

func TestScenarioStaleWaitingWithActiveSpinner(t *testing.T) {
	// Regression: user approves a PermissionRequest and Claude starts working.
	// No UserPromptSubmit fires for permission grants, so the hook stays "waiting".
	// The pane shows an active spinner (✳ Newspapering…) but normalizeForHash
	// strips it, making the content hash stable. After the 15s cooldown expired,
	// status reverted to waiting even though Claude was clearly running.
	//
	// Fix: applyHookWaiting checks paneStatus as a fallback — if pane detection
	// sees running indicators after cooldown, trust the pane and stay running.
	// Captured from snapshot 2026-04-15T14-07-00_align-button-figma-design-syst.
	fixture := "pane_running_stale_waiting_spinner.txt"
	if _, err := os.Stat(filepath.Join("testdata", fixture)); err != nil {
		t.Skipf("fixture %s not available", fixture)
	}

	runScenario(t, Scenario{
		Name: "stale waiting hook + active spinner: stays running after cooldown",
		Events: []ScenarioEvent{
			{At: 0, Hook: "waiting", Pane: "permission prompt\n❯ 1. Yes\n  2. No\nEsc to cancel\n"},
			{At: 3 * time.Second, Pane: "@fixture:" + fixture}, // user approved, Claude running
		},
		Checks: []ScenarioCheck{
			{At: 0, Expected: StatusWaiting},
			{At: 3 * time.Second, Expected: StatusRunning},  // content changed → running
			{At: 20 * time.Second, Expected: StatusRunning}, // 15s cooldown expired, but pane says running → stays running
		},
	})
}
