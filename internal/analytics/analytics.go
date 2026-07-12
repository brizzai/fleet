// Package analytics ships usage events to PostHog. Identity may include the
// user's git name/email when telemetry is enabled — see DiscoverIdentity and
// internal/ui/consent.go for exactly what gets sent. The public surface
// (Init, Track, Gauge, Distribution, SetUserProperties, Heartbeat, Shutdown)
// is intentionally backend-agnostic so call sites in app.go / settings.go /
// hooks / chrome don't need to change when the backend does.
//
// Backend: the posthog-go SDK, which owns its own batching/flush worker —
// Enqueue is non-blocking and safe to call from the Bubble Tea Update() loop.
// A thin Client wraps it to enforce the telemetry modes (full/minimal/off),
// strip PII, and keep anonymous (minimal-mode) events profile-less.
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
	"github.com/posthog/posthog-go"
)

// Telemetry modes, mirroring config.Telemetry{Full,Minimal,Off}. Kept as
// plain strings so this package stays decoupled from internal/config.
const (
	ModeFull    = "full"
	ModeMinimal = "minimal"
	ModeOff     = "off"
)

const (
	// defaultPostHogHost is fleet's PostHog ingest host — the project lives in
	// the US region. Overridable via FLEET_POSTHOG_HOST for local testing.
	defaultPostHogHost = "https://us.i.posthog.com"

	// flushInterval is how often the SDK ships a batch. fleet emits few events;
	// a short interval keeps dashboards fresh and Close() quick on quit.
	flushInterval = 5 * time.Second

	// shutdownTimeout caps how long the SDK's Close blocks draining the queue,
	// so quitting fleet stays prompt. Any events still queued past this are
	// dropped on a slow/unreachable network.
	shutdownTimeout = 500 * time.Millisecond
)

// projectAPIKey is PostHog's project (capture) API key, embedded in the
// (public) binary. Like the ingest token it replaces, a project key is
// write-only — it authorizes event capture, never data reads — so the worst it
// unlocks is someone POSTing junk events. Overridable via FLEET_POSTHOG_KEY.
// (The secret *personal* API key, used only for reading data / scripting
// dashboards, is never embedded here.) A var rather than a const so the
// disabled-until-configured guard stays testable.
var projectAPIKey = "phc_zHpJfaeu7J42KWQZ9J5CDWuR76T7nK45mzWNghzXfsHx"

var (
	global   *Client
	globalMu sync.Mutex
)

// sink is the subset of posthog.Client that Client depends on. Abstracted so
// tests can inject a fake and assert mode-gating without a network round-trip;
// *posthog.client satisfies it in production via newSink.
type sink interface {
	Enqueue(posthog.Message) error
	Close() error
}

// newSink builds the production PostHog client. Overridden in tests.
var newSink = func(key, host string) (sink, error) {
	return posthog.NewWithConfig(key, posthog.Config{
		Endpoint:        host,
		Interval:        flushInterval,
		ShutdownTimeout: shutdownTimeout,
		Logger:          phLogger{},
	})
}

// Client wraps the PostHog SDK, enforcing the telemetry mode on every call.
//
// deviceID and distinctID are kept separate on purpose: deviceID is always the
// anonymous SHA256 of the hardware UUID (what DeviceID() exposes externally),
// while distinctID is the identifier sent to PostHog — the git user.email in
// full mode (so one human is one person across their machines), and the device
// hash in minimal mode (so no identity leaves the machine). Logging distinctID
// would leak email; logging deviceID is safe.
type Client struct {
	ph         sink
	deviceID   string
	distinctID string
	version    string
	disabled   bool
	// mode is the resolved telemetry mode this client runs in
	// (ModeFull/ModeMinimal) — the single source of truth, also emitted as the
	// "mode" property on every event. Minimal is anonymous, DAU-only: no git
	// name/email, no people profile (events carry $process_person_profile=false
	// so the device still counts as a unique user), and Track/Gauge/Distribution/
	// SetUserProperties all no-op; only app_started and the app_active heartbeat
	// are sent. A disabled client leaves this empty.
	mode string

	// mu guards lastActiveDay, which the daily-active Heartbeat reads and
	// updates. lastActiveDay is the calendar day (YYYY-MM-DD) of the last
	// app_active event, so a long-running instance emits at most one per day.
	mu            sync.Mutex
	lastActiveDay string
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

// Init wires up the client using a pre-resolved Identity. Pass the result of
// DiscoverIdentity() (called before the TUI starts) so no blocking I/O happens
// here. Safe to call once; subsequent calls are no-ops. mode is one of
// ModeFull/ModeMinimal/ModeOff. ModeOff (or an env opt-out, or a missing
// project key) creates a "disabled" client and all helper calls become no-ops.
// ModeMinimal creates an anonymous client that sends only the DAU signals
// (app_started + app_active) with no git name/email and no people profile.
func Init(mode string, version string, identity Identity) {
	globalMu.Lock()
	defer globalMu.Unlock()

	if global != nil {
		return
	}

	key := projectAPIKey
	if v := strings.TrimSpace(os.Getenv("FLEET_POSTHOG_KEY")); v != "" {
		key = v
	}

	// Disabled when the user opted out, or before the PostHog project key is
	// configured — the latter keeps this file safe to land ahead of the account.
	if mode == ModeOff || isOptedOut() || key == "" {
		global = &Client{disabled: true}
		if key == "" && mode != ModeOff && !isOptedOut() {
			debuglog.Logger.Warn("analytics disabled: no PostHog project key configured")
		} else {
			debuglog.Logger.Info("analytics disabled")
		}
		return
	}

	minimal := mode == ModeMinimal

	// distinct_id: in full mode prefer git email (one user across all of the
	// person's machines); in minimal mode always use the anonymous device hash
	// so no identity leaves the machine.
	distinctID := identity.DeviceID
	if !minimal && identity.GitEmail != "" {
		distinctID = identity.GitEmail
	}

	host := defaultPostHogHost
	if v := strings.TrimSpace(os.Getenv("FLEET_POSTHOG_HOST")); v != "" {
		host = v
	}

	ph, err := newSink(key, host)
	if err != nil {
		global = &Client{disabled: true}
		debuglog.Logger.Warn("analytics disabled: posthog init failed", "err", err)
		return
	}

	c := &Client{
		ph:         ph,
		deviceID:   identity.DeviceID,
		distinctID: distinctID,
		version:    version,
		mode:       mode,
	}
	global = c

	// Minimal mode ships no people profile at all — only anonymous events.
	if !minimal {
		// Set baseline profile properties so PostHog has this device/install
		// even before the first explicit SetUserProperties call.
		people := posthog.NewProperties().
			Set("app_version", version).
			Set("os_version", identity.OSVersion).
			Set("arch", runtime.GOARCH).
			Set("machine_hash", identity.DeviceID)
		if identity.GitName != "" {
			people.Set("$name", identity.GitName)
		}
		if identity.GitEmail != "" {
			people.Set("$email", identity.GitEmail)
		}
		c.identify(people)
	}

	// Log the mode and only whether git identity was present, never the value
	// or any prefix of it (which for a git email is plenty to identify a
	// person).
	debuglog.Logger.Info("analytics initialized", "mode", mode, "git_identity_present", !minimal && identity.GitEmail != "")
}

// readGitIdentity returns the user's globally-configured git name and email.
// Either string may be empty if git isn't installed or the value isn't set.
// Errors are intentionally swallowed — analytics never blocks startup.
func readGitIdentity() (name, email string) {
	// Bounded: this runs synchronously on the startup path (DiscoverIdentity →
	// before the TUI renders), so a hung `git config` (dead/NFS $HOME, held
	// global-config lock, wedged credential helper) must not freeze launch.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "git", "config", "--global", "user.name").Output(); err == nil {
		name = strings.TrimSpace(string(out))
	}
	if out, err := exec.CommandContext(ctx, "git", "config", "--global", "user.email").Output(); err == nil {
		email = strings.TrimSpace(string(out))
	}
	return name, email
}

// phLogger routes the SDK's internal logging into fleet's debug log. Without
// it the SDK would write to stdout/stderr and corrupt the Bubble Tea display.
// Everything is logged at debug level — analytics is best-effort, so its noise
// should never surface as a fleet error.
type phLogger struct{}

func (phLogger) Debugf(f string, a ...interface{}) {
	debuglog.Logger.Debug("posthog: " + fmt.Sprintf(f, a...))
}
func (phLogger) Logf(f string, a ...interface{}) {
	debuglog.Logger.Debug("posthog: " + fmt.Sprintf(f, a...))
}
func (phLogger) Warnf(f string, a ...interface{}) {
	debuglog.Logger.Debug("posthog: " + fmt.Sprintf(f, a...))
}
func (phLogger) Errorf(f string, a ...interface{}) {
	debuglog.Logger.Debug("posthog: " + fmt.Sprintf(f, a...))
}

// baseProps builds the property set stamped on every event: the client's
// version, resolved mode, and anonymous device hash, plus any caller extras.
func (c *Client) baseProps(extra map[string]any) posthog.Properties {
	p := posthog.NewProperties().
		Set("version", c.version).
		Set("mode", c.mode).
		Set("device_id", c.deviceID)
	for k, v := range extra {
		p.Set(k, v)
	}
	return p
}

// capture enqueues an event. In minimal mode it sets $process_person_profile
// to false, so the event still counts the device as a unique user (DAU) but
// creates no person profile and carries no identity.
func (c *Client) capture(event string, props posthog.Properties) {
	if c == nil || c.ph == nil {
		return
	}
	if props == nil {
		props = posthog.NewProperties()
	}
	if c.mode == ModeMinimal {
		props.Set("$process_person_profile", false)
	}
	if err := c.ph.Enqueue(posthog.Capture{
		DistinctId: c.distinctID,
		Event:      event,
		Properties: props,
		Timestamp:  time.Now(),
	}); err != nil {
		debuglog.Logger.Debug("analytics enqueue failed", "event", event, "err", err)
	}
}

// identify enqueues a people-profile update for this device's distinct_id.
// Only used in full mode; PostHog merges props (latest value per key wins).
func (c *Client) identify(props posthog.Properties) {
	if c == nil || c.ph == nil {
		return
	}
	if err := c.ph.Enqueue(posthog.Identify{
		DistinctId: c.distinctID,
		Properties: props,
	}); err != nil {
		debuglog.Logger.Debug("analytics identify failed", "err", err)
	}
}

// Track enqueues an event with the given properties. No-op in minimal mode,
// which only ships the anonymous DAU signals (see Heartbeat / trackRaw).
func Track(eventType string, properties map[string]interface{}) {
	c := current()
	if c == nil || c.disabled || c.mode == ModeMinimal {
		return
	}
	c.capture(eventType, c.baseProps(sanitizeProperties(properties)))
}

// trackRaw enqueues an event bypassing the minimal-mode gate. Reserved for the
// DAU signals (app_started, app_active) that must send even in minimal mode.
// Callers must pass already-clean, PII-free properties.
func trackRaw(c *Client, eventType string, properties map[string]any) {
	if c == nil || c.disabled {
		return
	}
	c.capture(eventType, c.baseProps(properties))
}

// Heartbeat records that this device is active today, at most once per calendar
// day per process. It is the DAU signal that survives long-running instances:
// app_started only fires at launch, but fleet is often left open for days, so a
// midnight rollover re-emits app_active on the next tick. Sends in both full
// and minimal mode; no-op when disabled.
func Heartbeat() {
	c := current()
	if c == nil || c.disabled {
		return
	}
	day := time.Now().Format("2006-01-02")
	c.mu.Lock()
	if c.lastActiveDay == day {
		c.mu.Unlock()
		return
	}
	c.lastActiveDay = day
	c.mu.Unlock()

	trackRaw(c, EventAppActive, nil)
}

// Gauge records a point-in-time value as an event with a numeric `value`
// property. There's no native gauge type; this convention lets you chart
// averages or maxes by event name in PostHog.
func Gauge(name string, value float64, properties map[string]interface{}) {
	c := current()
	if c == nil || c.disabled || c.mode == ModeMinimal {
		return
	}
	c.capture(name, c.baseProps(mergeValue(properties, value)))
}

// Distribution records a sampled value as an event with a numeric `value`
// property. Same semantics as Gauge — the distinction is for the caller's
// intent only.
func Distribution(name string, sample float64, properties map[string]interface{}) {
	c := current()
	if c == nil || c.disabled || c.mode == ModeMinimal {
		return
	}
	c.capture(name, c.baseProps(mergeValue(properties, sample)))
}

// SetUserProperties enqueues a people-profile update for this device. PostHog
// merges props into the existing profile (latest value per key wins), so
// repeated calls during a session are fine. No-op in minimal mode.
func SetUserProperties(props map[string]interface{}) {
	c := current()
	if c == nil || c.disabled || c.mode == ModeMinimal {
		return
	}
	c.identify(toProps(sanitizeProperties(props)))
}

// SyncEnabled reconciles the live client with the user's current telemetry
// mode — call this when the Settings dialog closes. Without it, a mode change
// would only take effect on the next launch: the helpers check c.disabled /
// c.mode (set once at Init), not the live config. Env opt-out always wins;
// this is a no-op when the effective state (disabled + minimal) is unchanged.
// Otherwise the live client is torn down and re-inited in the requested mode.
func SyncEnabled(mode string, version string, identity Identity) {
	globalMu.Lock()
	c := global
	globalMu.Unlock()

	wantDisabled := mode == ModeOff || isOptedOut()
	wantMinimal := !wantDisabled && mode == ModeMinimal

	if c == nil {
		if wantDisabled {
			return
		}
	} else if c.disabled == wantDisabled && (c.mode == ModeMinimal) == wantMinimal {
		return
	}

	Shutdown()
	Init(mode, version, identity)
}

// Shutdown flushes in-flight events (bounded by the SDK's ShutdownTimeout) and
// clears the global. Subsequent Track calls are no-ops.
func Shutdown() {
	globalMu.Lock()
	c := global
	global = nil
	globalMu.Unlock()

	if c == nil || c.disabled || c.ph == nil {
		return
	}

	// posthog Close respects Config.ShutdownTimeout, so this can't hang quit.
	if err := c.ph.Close(); err != nil {
		debuglog.Logger.Debug("analytics shutdown", "err", err)
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

// toProps converts a plain property map into a posthog.Properties. A nil map
// yields an empty (non-nil) Properties.
func toProps(m map[string]any) posthog.Properties {
	p := posthog.NewProperties()
	for k, v := range m {
		p.Set(k, v)
	}
	return p
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
// dropped. Returns nil for nil/empty input so the payload stays clean. We
// don't mutate the caller's map.
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

// TrackAppStarted records app launch. In full mode it merges usage properties
// into the people profile and emits an app_started event with session/repo
// counts. In minimal mode it emits only an anonymous app_started (version +
// mode via baseProps), with no people profile and no counts — the leanest
// launch signal that still marks the device active today.
func TrackAppStarted(version string, sessionCount, repoCount int, theme, enterMode, defaultAgent string, autoName, copyClaudeSettings bool) {
	c := current()
	if c == nil || c.disabled {
		return
	}

	if c.mode == ModeMinimal {
		trackRaw(c, EventAppStarted, nil)
		return
	}

	SetUserProperties(map[string]interface{}{
		"theme":                theme,
		"enter_mode":           enterMode,
		"default_agent":        defaultAgent,
		"auto_name_sessions":   autoName,
		"copy_claude_settings": copyClaudeSettings,
	})

	Track(EventAppStarted, map[string]interface{}{
		"session_count": sessionCount,
		"repo_count":    repoCount,
	})
}
