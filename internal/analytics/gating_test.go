package analytics

import (
	"sync"
	"testing"

	"github.com/posthog/posthog-go"
)

// fakeSink captures enqueued messages so mode-gating can be asserted without a
// network round-trip.
type fakeSink struct {
	mu     sync.Mutex
	msgs   []posthog.Message
	closed bool
}

func (f *fakeSink) Enqueue(m posthog.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, m)
	return nil
}

func (f *fakeSink) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeSink) captures() []posthog.Capture {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []posthog.Capture
	for _, m := range f.msgs {
		if c, ok := m.(posthog.Capture); ok {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeSink) identifies() []posthog.Identify {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []posthog.Identify
	for _, m := range f.msgs {
		if id, ok := m.(posthog.Identify); ok {
			out = append(out, id)
		}
	}
	return out
}

func captureByEvent(cs []posthog.Capture, event string) (posthog.Capture, bool) {
	for _, c := range cs {
		if c.Event == event {
			return c, true
		}
	}
	return posthog.Capture{}, false
}

// withFake installs a fresh fake sink, clears any prior client, and returns the
// fake plus a cleanup that restores global state.
func withFake(t *testing.T) *fakeSink {
	t.Helper()
	Shutdown() // clear any prior global
	t.Setenv("FLEET_TELEMETRY_DISABLED", "")
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("FLEET_POSTHOG_KEY", "phc_test")
	t.Setenv("FLEET_POSTHOG_HOST", "https://example.invalid")

	f := &fakeSink{}
	orig := newSink
	newSink = func(key, host string) (sink, error) { return f, nil }
	t.Cleanup(func() {
		Shutdown()
		newSink = orig
	})
	return f
}

var testIdentity = Identity{DeviceID: "devhash", GitName: "Ada", GitEmail: "ada@example.com", OSVersion: "14.5"}

func TestFullModeIdentifiesAndTracks(t *testing.T) {
	f := withFake(t)
	Init(ModeFull, "9.9.9", testIdentity)
	Track("thing_happened", map[string]interface{}{"count": 3})
	Shutdown() // drains the queue into the sink; assertions below are race-free

	// Init sets a baseline people profile.
	if len(f.identifies()) == 0 {
		t.Fatal("full mode should enqueue an Identify at Init")
	}

	c, ok := captureByEvent(f.captures(), "thing_happened")
	if !ok {
		t.Fatal("full mode should Track the event")
	}
	if c.DistinctId != "ada@example.com" {
		t.Errorf("full-mode distinct_id = %q, want the git email", c.DistinctId)
	}
	if c.Properties["mode"] != ModeFull {
		t.Errorf("event mode = %v, want %q", c.Properties["mode"], ModeFull)
	}
	if _, present := c.Properties["$process_person_profile"]; present {
		t.Error("full-mode events must not disable person profiles")
	}
}

func TestMinimalModeIsAnonymousDAUOnly(t *testing.T) {
	f := withFake(t)
	Init(ModeMinimal, "9.9.9", testIdentity)
	Track("thing_happened", map[string]interface{}{"count": 3}) // must no-op
	Heartbeat()                                                 // must still fire
	Shutdown()                                                  // drains the queue into the sink

	// No people profile in minimal mode.
	if got := len(f.identifies()); got != 0 {
		t.Errorf("minimal mode enqueued %d Identify messages, want 0", got)
	}

	if _, ok := captureByEvent(f.captures(), "thing_happened"); ok {
		t.Error("minimal mode must not send Track events")
	}

	// The heartbeat is the one thing minimal mode does send: anonymous, profile-less.
	c, ok := captureByEvent(f.captures(), EventAppActive)
	if !ok {
		t.Fatal("minimal mode should still send the app_active heartbeat")
	}
	if c.DistinctId != "devhash" {
		t.Errorf("minimal-mode distinct_id = %q, want the anonymous device hash", c.DistinctId)
	}
	if c.Properties["$process_person_profile"] != false {
		t.Errorf("minimal-mode events must set $process_person_profile=false, got %v", c.Properties["$process_person_profile"])
	}
	if c.Properties["mode"] != ModeMinimal {
		t.Errorf("heartbeat mode = %v, want %q (for the decline-rate breakdown)", c.Properties["mode"], ModeMinimal)
	}
}

func TestOffModeSendsNothing(t *testing.T) {
	f := withFake(t)
	Init(ModeOff, "9.9.9", testIdentity)

	Track("thing_happened", nil)
	Heartbeat()
	TrackAppStarted("9.9.9", 1, 1, "fleet-pink", "enter", "claude", true, true)
	Shutdown()

	if n := len(f.msgs); n != 0 {
		t.Errorf("off mode enqueued %d messages, want 0", n)
	}
}

func TestMissingKeyDisables(t *testing.T) {
	Shutdown()
	t.Setenv("FLEET_TELEMETRY_DISABLED", "")
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("FLEET_POSTHOG_KEY", "") // no env override
	origKey := projectAPIKey
	projectAPIKey = "" // simulate the pre-account state: no key configured
	f := &fakeSink{}
	orig := newSink
	newSink = func(key, host string) (sink, error) { return f, nil }
	t.Cleanup(func() {
		Shutdown()
		newSink = orig
		projectAPIKey = origKey
	})

	Init(ModeFull, "9.9.9", testIdentity)
	Track("thing_happened", nil)
	Shutdown()
	if len(f.msgs) != 0 {
		t.Errorf("with no project key, expected a disabled client sending nothing; got %d messages", len(f.msgs))
	}
}
