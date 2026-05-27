// Package analytics ships anonymous usage events to Mixpanel. The public
// surface (Init, Track, Gauge, Distribution, SetUserProperties, Shutdown) is
// intentionally backend-agnostic so call sites in app.go / settings.go /
// hooks / chrome don't need to change when the backend does.
//
// Backend: github.com/mixpanel/mixpanel-go's ApiClient. Mixpanel's Track is
// a blocking HTTP call, which is unacceptable inside the Bubble Tea Update()
// loop — so this package buffers events on a channel and ships them from a
// single worker goroutine. Track/Gauge/Distribution return immediately.
package analytics

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/mixpanel/mixpanel-go"
)

const (
	mixpanelToken = "89fa427751dcb749b67d524d1dbc9f98"

	// queueSize is how many events we buffer before dropping. The TUI emits
	// at most a few events per second; this gives ~minutes of slack if
	// Mixpanel is slow before we start losing events.
	queueSize = 256

	// shutdownTimeout caps how long Shutdown blocks waiting for the worker
	// to drain the queue. 2s matches what the previous Sentry impl used.
	shutdownTimeout = 2 * time.Second
)

var (
	global   *Client
	globalMu sync.Mutex
)

// Client wraps the Mixpanel ApiClient with a queued worker.
//
// deviceID and distinctID are kept separate on purpose: deviceID is always the
// anonymous SHA256 of the hardware UUID (what DeviceID() exposes externally),
// while distinctID is the value sent to Mixpanel as the per-event identifier
// — usually the git user.email, with the device hash as a fallback. Logging
// distinctID would leak email; logging deviceID is safe.
type Client struct {
	mp         *mixpanel.ApiClient
	deviceID   string
	distinctID string
	queue      chan job
	wg         sync.WaitGroup
	disabled   bool
}

// Identity is the resolved per-install identity that callers pass into Init.
// Discovered via DiscoverIdentity() outside the Bubble Tea Update() loop so
// the consent-flow Init call is pure in-memory work.
type Identity struct {
	DeviceID  string // anonymous SHA256 of macOS hardware UUID
	GitName   string // git config --global user.name (may be empty)
	GitEmail  string // git config --global user.email (may be empty)
	OSVersion string // sw_vers -productVersion (may be "unknown")
}

// DiscoverIdentity runs the shell-outs needed to populate an Identity:
// ioreg (or hostname fallback) for the device ID, git config for name/email,
// and sw_vers for the OS version. CLAUDE.md mandates that blocking I/O like
// this never run inside the Bubble Tea Update() loop, so callers should
// invoke this from main.go before the TUI starts. Safe to call concurrently;
// no shared state.
func DiscoverIdentity() Identity {
	name, email := readGitIdentity()
	return Identity{
		DeviceID:  getOrCreateDeviceID(),
		GitName:   name,
		GitEmail:  email,
		OSVersion: osVersion(),
	}
}

// job represents one piece of work for the worker — either a Track event or a
// PeopleSet update. Exactly one of `event` and `people` is non-nil.
type job struct {
	event  *mixpanel.Event
	people *mixpanel.PeopleProperties
}

// Init wires up the Mixpanel client and starts the worker goroutine using a
// pre-resolved Identity. Pass the result of DiscoverIdentity() (called before
// the TUI starts) so no blocking I/O happens here. Safe to call once;
// subsequent calls are no-ops. When telemetry is disabled the client is
// created in a "disabled" state and all helper calls become no-ops.
func Init(telemetryEnabled bool, version string, identity Identity) {
	globalMu.Lock()
	defer globalMu.Unlock()

	if global != nil {
		return
	}

	if !telemetryEnabled || isOptedOut() {
		global = &Client{disabled: true}
		debuglog.Logger.Info("analytics disabled")
		return
	}

	// Mixpanel distinct_id: prefer git email (one Mixpanel user across all
	// of the person's machines), fall back to the anonymous device hash.
	distinctID := identity.DeviceID
	if identity.GitEmail != "" {
		distinctID = identity.GitEmail
	}

	mp := mixpanel.NewApiClient(mixpanelToken)

	c := &Client{
		mp:         mp,
		deviceID:   identity.DeviceID,
		distinctID: distinctID,
		queue:      make(chan job, queueSize),
	}
	c.wg.Add(1)
	go c.worker()

	global = c

	// Set baseline people properties so Mixpanel sees this device/install
	// even before the first explicit SetUserProperties call.
	people := map[string]any{
		"app_version":  version,
		"os_version":   identity.OSVersion,
		"arch":         runtime.GOARCH,
		"machine_hash": identity.DeviceID,
	}
	if identity.GitName != "" {
		people["$name"] = identity.GitName
	}
	if identity.GitEmail != "" {
		people["$email"] = identity.GitEmail
	}
	enqueuePeople(c, people)

	// Log only whether git identity was present, not the value or any prefix
	// of it (which for a git email is plenty to identify a person).
	debuglog.Logger.Info("analytics initialized", "git_identity_present", identity.GitEmail != "")
}

// readGitIdentity returns the user's globally-configured git name and email.
// Either string may be empty if git isn't installed or the value isn't set.
// Errors are intentionally swallowed — analytics never blocks startup.
func readGitIdentity() (name, email string) {
	if out, err := exec.Command("git", "config", "--global", "user.name").Output(); err == nil {
		name = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "config", "--global", "user.email").Output(); err == nil {
		email = strings.TrimSpace(string(out))
	}
	return name, email
}

// worker drains the job queue and ships events to Mixpanel one at a time.
// Each call gets a 5s timeout so a hung Mixpanel endpoint can't permanently
// stall the worker.
func (c *Client) worker() {
	defer c.wg.Done()
	for j := range c.queue {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		switch {
		case j.event != nil:
			if err := c.mp.Track(ctx, []*mixpanel.Event{j.event}); err != nil {
				debuglog.Logger.Debug("mixpanel track failed", "err", err)
			}
		case j.people != nil:
			if err := c.mp.PeopleSet(ctx, []*mixpanel.PeopleProperties{j.people}); err != nil {
				debuglog.Logger.Debug("mixpanel people_set failed", "err", err)
			}
		}
		cancel()
	}
}

// Track enqueues a Mixpanel event with the given properties.
func Track(eventType string, properties map[string]interface{}) {
	c := current()
	if c == nil || c.disabled {
		return
	}
	event := c.mp.NewEvent(eventType, c.distinctID, sanitizeProperties(properties))
	enqueueEvent(c, event)
}

// Gauge records a point-in-time value as an event with a numeric `value`
// property. Mixpanel doesn't have native gauges; this convention lets you
// chart averages or maxes by event name.
func Gauge(name string, value float64, properties map[string]interface{}) {
	c := current()
	if c == nil || c.disabled {
		return
	}
	props := mergeValue(properties, value)
	event := c.mp.NewEvent(name, c.distinctID, props)
	enqueueEvent(c, event)
}

// Distribution records a sampled value as an event with a numeric `value`
// property. Same Mixpanel-side semantics as Gauge — the distinction is for
// the caller's intent only.
func Distribution(name string, sample float64, properties map[string]interface{}) {
	c := current()
	if c == nil || c.disabled {
		return
	}
	props := mergeValue(properties, sample)
	event := c.mp.NewEvent(name, c.distinctID, props)
	enqueueEvent(c, event)
}

// SetUserProperties enqueues a Mixpanel /engage update on this device's
// people profile. Mixpanel automatically retains the latest value per
// property, so repeated calls during a session are fine.
func SetUserProperties(props map[string]interface{}) {
	c := current()
	if c == nil || c.disabled {
		return
	}
	enqueuePeople(c, sanitizeProperties(props))
}

// Shutdown closes the queue, drains in-flight events with a timeout, and
// clears the global. Subsequent Track calls are no-ops.
func Shutdown() {
	globalMu.Lock()
	c := global
	global = nil
	globalMu.Unlock()

	if c == nil || c.disabled {
		return
	}

	close(c.queue)
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownTimeout):
		debuglog.Logger.Info("analytics shutdown timed out, dropping queued events")
	}
	debuglog.Logger.Info("analytics shutdown")
}

// DeviceID returns the anonymous device ID, or "" if analytics is disabled.
func DeviceID() string {
	c := current()
	if c == nil {
		return ""
	}
	return c.deviceID
}

func current() *Client {
	globalMu.Lock()
	defer globalMu.Unlock()
	return global
}

// enqueueEvent pushes an event onto the worker queue without blocking. If the
// queue is full, the event is dropped — fleet emits few enough events that
// this should never happen in practice, but the non-blocking guarantee is
// what keeps Track safe to call from the Bubble Tea Update() loop.
//
// The recover handles a narrow Shutdown race: between current() returning a
// non-nil *Client and reaching the channel send below, Shutdown can close
// c.queue from another goroutine (hook watcher, chrome client, etc.). A
// closed channel makes `case c.queue <- …` selectable and panics. Analytics
// is best-effort, so we'd rather drop the event than crash the TUI.
func enqueueEvent(c *Client, event *mixpanel.Event) {
	defer func() { _ = recover() }()
	select {
	case c.queue <- job{event: event}:
	default:
		debuglog.Logger.Debug("analytics queue full, dropping event", "event", event.Name)
	}
}

func enqueuePeople(c *Client, props map[string]any) {
	defer func() { _ = recover() }()
	people := mixpanel.NewPeopleProperties(c.distinctID, props)
	select {
	case c.queue <- job{people: people}:
	default:
		debuglog.Logger.Debug("analytics queue full, dropping people_set")
	}
}

// mergeValue returns a copy of `properties` with a "value" key added. We
// don't mutate the caller's map.
func mergeValue(properties map[string]interface{}, value float64) map[string]any {
	out := make(map[string]any, len(properties)+1)
	for k, v := range properties {
		if !sanitizeKey(k) {
			continue
		}
		out[k] = v
	}
	out["value"] = value
	return out
}

// sanitizeProperties returns a copy of `properties` with PII-blocked keys
// dropped. Returns nil for nil/empty input so the Mixpanel SDK sees a clean
// payload. We don't mutate the caller's map.
func sanitizeProperties(properties map[string]interface{}) map[string]any {
	if len(properties) == 0 {
		return nil
	}
	out := make(map[string]any, len(properties))
	for k, v := range properties {
		if !sanitizeKey(k) {
			continue
		}
		out[k] = v
	}
	return out
}

// sanitizeKey returns false for property keys that could carry PII. This is
// defense-in-depth — callers are expected not to pass these in the first
// place. Compared on lowercased key for case-insensitive blocklisting.
func sanitizeKey(k string) bool {
	switch strings.ToLower(k) {
	case "path", "project_path", "file_path",
		"repo", "repo_name",
		"branch", "branch_name",
		"title", "session_title",
		"url",
		"host", "hostname",
		"prompt", "message":
		return false
	}
	return true
}

// IsOptedOutByEnv reports whether either env var (FLEET_TELEMETRY_DISABLED or
// DO_NOT_TRACK) is set to a truthy value. Useful for callers that want to
// skip a consent prompt when the user has already opted out at the shell
// level — there's nothing to ask about.
func IsOptedOutByEnv() bool {
	return isOptedOut()
}

// isOptedOut checks env vars for telemetry opt-out.
func isOptedOut() bool {
	if isTruthyEnv(os.Getenv("FLEET_TELEMETRY_DISABLED")) {
		return true
	}
	if isTruthyEnv(os.Getenv("DO_NOT_TRACK")) {
		return true
	}
	return false
}

func isTruthyEnv(v string) bool {
	switch strings.TrimSpace(strings.ToLower(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	}
	return false
}

// getOrCreateDeviceID returns a stable anonymous device ID.
// Cached in ~/.config/fleet/device_id after first generation.
func getOrCreateDeviceID() string {
	home, _ := os.UserHomeDir()
	idPath := filepath.Join(home, ".config", "fleet", "device_id")

	if data, err := os.ReadFile(idPath); err == nil {
		id := strings.TrimSpace(string(data))
		if len(id) >= 8 {
			return id
		}
	}

	id := generateDeviceID()

	_ = os.MkdirAll(filepath.Dir(idPath), 0700)
	_ = os.WriteFile(idPath, []byte(id), 0600)

	return id
}

// generateDeviceID creates a SHA256 hash of the macOS hardware UUID.
func generateDeviceID() string {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		hostname, _ := os.Hostname()
		seed := hostname + runtime.GOARCH
		h := sha256.Sum256([]byte(seed))
		return fmt.Sprintf("%x", h)
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "IOPlatformUUID") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				uuid := strings.TrimSpace(parts[1])
				uuid = strings.Trim(uuid, "\"")
				h := sha256.Sum256([]byte(uuid))
				return fmt.Sprintf("%x", h)
			}
		}
	}

	hostname, _ := os.Hostname()
	h := sha256.Sum256([]byte(hostname))
	return fmt.Sprintf("%x", h)
}

// osVersion returns the macOS version string.
func osVersion() string {
	out, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// TrackAppStarted records app launch with user properties merged into the
// people profile, then emits an EventAppStarted event.
func TrackAppStarted(version string, sessionCount, repoCount int, theme, enterMode string, autoName, copyClaudeSettings bool) {
	SetUserProperties(map[string]interface{}{
		"theme":                theme,
		"enter_mode":           enterMode,
		"auto_name_sessions":   autoName,
		"copy_claude_settings": copyClaudeSettings,
	})

	Track(EventAppStarted, map[string]interface{}{
		"version":       version,
		"session_count": sessionCount,
		"repo_count":    repoCount,
	})
}
