// Package analytics ships usage events to fleet's own ingestion endpoint (a
// small Next.js + Postgres service). Identity may include the user's git
// name/email when telemetry is enabled — see DiscoverIdentity and
// internal/ui/consent.go for exactly what gets sent. The public surface
// (Init, Track, Gauge, Distribution, SetUserProperties, Shutdown) is
// intentionally backend-agnostic so call sites in app.go / settings.go /
// hooks / chrome don't need to change when the backend does.
//
// Backend: an HTTP POST of a small JSON batch to ingestURL. The POST is a
// blocking network call, which is unacceptable inside the Bubble Tea Update()
// loop — so this package buffers events on a channel and ships them from a
// single worker goroutine. Track/Gauge/Distribution return immediately.
package analytics

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

// Telemetry modes, mirroring config.Telemetry{Full,Minimal,Off}. Kept as
// plain strings so this package stays decoupled from internal/config.
const (
	ModeFull    = "full"
	ModeMinimal = "minimal"
	ModeOff     = "off"
)

const (
	// defaultIngestURL is fleet's analytics ingestion endpoint. Overridable
	// via FLEET_ANALYTICS_URL for local testing against a dev deployment.
	defaultIngestURL = "https://fleet-analytics-beryl.vercel.app/api/events"

	// ingestToken is sent as the x-fleet-token header. Like the Mixpanel
	// project token it replaces, it's embedded in the (public) binary: it only
	// authorizes event ingestion — never data reads — so the worst it unlocks
	// is someone POSTing junk events.
	ingestToken = "ac5058d5201279372cff69cae5b09979b130623a6904fda46f6d21e24dce5d26"

	// queueSize is how many events we buffer before dropping. The TUI emits
	// at most a few events per second; this gives ~minutes of slack if the
	// endpoint is slow before we start losing events.
	queueSize = 256

	// shutdownTimeout caps how long Shutdown blocks waiting for the worker to
	// drain the queue. Kept short so quitting fleet exits promptly; on a slow
	// or unreachable network any events still queued past this are dropped.
	shutdownTimeout = 500 * time.Millisecond
)

var (
	global   *Client
	globalMu sync.Mutex
)

// Client posts events to the ingestion endpoint from a queued worker.
//
// deviceID and distinctID are kept separate on purpose: deviceID is always the
// anonymous SHA256 of the hardware UUID (what DeviceID() exposes externally),
// while distinctID is the per-event identifier sent to the endpoint — usually
// the git user.email, with the device hash as a fallback. Logging distinctID
// would leak email; logging deviceID is safe.
type Client struct {
	httpClient *http.Client
	ingestURL  string
	deviceID   string
	distinctID string
	version    string
	queue      chan ingestItem
	wg         sync.WaitGroup
	disabled   bool
	// mode is the resolved telemetry mode this client runs in
	// (ModeFull/ModeMinimal) — the single source of truth, also emitted as the
	// "mode" property on DAU events. Minimal is anonymous, DAU-only: no git
	// name/email, no people profile, and Track/Gauge/Distribution/
	// SetUserProperties all no-op; only app_started and the app_active heartbeat
	// are sent, keyed on the anonymous device hash. A disabled client leaves
	// this empty.
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

// ingestItem is one record for the worker to POST — either an event or a
// profile (people) update, distinguished by Kind. It serializes directly to
// the endpoint's batch contract.
type ingestItem struct {
	Kind       string         `json:"kind"` // "event" | "profile"
	Event      string         `json:"event,omitempty"`
	DistinctID string         `json:"distinct_id"`
	DeviceID   string         `json:"device_id,omitempty"`
	Mode       string         `json:"mode,omitempty"`
	Version    string         `json:"version,omitempty"`
	TS         string         `json:"ts,omitempty"`
	Props      map[string]any `json:"props,omitempty"`
}

// Init wires up the client and starts the worker goroutine using a pre-resolved
// Identity. Pass the result of DiscoverIdentity() (called before the TUI
// starts) so no blocking I/O happens here. Safe to call once; subsequent calls
// are no-ops. mode is one of ModeFull/ModeMinimal/ModeOff. ModeOff (or an env
// opt-out) creates a "disabled" client and all helper calls become no-ops.
// ModeMinimal creates an anonymous client that sends only the DAU signals
// (app_started + app_active) with no git name/email and no people profile.
func Init(mode string, version string, identity Identity) {
	globalMu.Lock()
	defer globalMu.Unlock()

	if global != nil {
		return
	}

	if mode == ModeOff || isOptedOut() {
		global = &Client{disabled: true}
		debuglog.Logger.Info("analytics disabled")
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

	ingestURL := defaultIngestURL
	if v := strings.TrimSpace(os.Getenv("FLEET_ANALYTICS_URL")); v != "" {
		ingestURL = v
	}

	c := &Client{
		httpClient: &http.Client{},
		ingestURL:  ingestURL,
		deviceID:   identity.DeviceID,
		distinctID: distinctID,
		version:    version,
		mode:       mode,
		queue:      make(chan ingestItem, queueSize),
	}
	c.wg.Add(1)
	go c.worker()

	global = c

	// Minimal mode ships no people profile at all — only anonymous events.
	if !minimal {
		// Set baseline profile properties so the endpoint sees this
		// device/install even before the first explicit SetUserProperties call.
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
		enqueueProfile(c, people)
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

// worker drains the queue and POSTs each item to the ingestion endpoint one at
// a time. Each request gets a 5s timeout so a hung endpoint can't permanently
// stall the worker.
func (c *Client) worker() {
	defer c.wg.Done()
	for item := range c.queue {
		if err := c.post(item); err != nil {
			debuglog.Logger.Debug("analytics post failed", "err", err)
		}
	}
}

// post sends a single-item batch to the ingestion endpoint. The endpoint's
// contract is {"batch":[item]}; sending one per request keeps the worker
// simple, and fleet emits few enough events that batching isn't worth it.
func (c *Client) post(item ingestItem) error {
	body, err := json.Marshal(map[string][]ingestItem{"batch": {item}})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ingestURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-fleet-token", ingestToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	// Drain the body before closing so net/http can reuse the keep-alive
	// connection instead of opening a fresh TLS handshake per event.
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ingest returned %d", resp.StatusCode)
	}
	return nil
}

// Track enqueues an event with the given properties. No-op in minimal mode,
// which only ships the anonymous DAU signals (see Heartbeat / trackRaw).
func Track(eventType string, properties map[string]interface{}) {
	c := current()
	if c == nil || c.disabled || c.mode == ModeMinimal {
		return
	}
	enqueue(c, newEvent(c, eventType, sanitizeProperties(properties)))
}

// trackRaw enqueues an event bypassing the minimal-mode gate. Reserved for the
// DAU signals (app_started, app_active) that must send even in minimal mode.
// Callers must pass already-clean, PII-free properties.
func trackRaw(c *Client, eventType string, properties map[string]any) {
	if c == nil || c.disabled {
		return
	}
	enqueue(c, newEvent(c, eventType, properties))
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

	trackRaw(c, EventAppActive, map[string]any{"version": c.version, "mode": c.mode})
}

// Gauge records a point-in-time value as an event with a numeric `value`
// property. There's no native gauge type; this convention lets you chart
// averages or maxes by event name.
func Gauge(name string, value float64, properties map[string]interface{}) {
	c := current()
	if c == nil || c.disabled || c.mode == ModeMinimal {
		return
	}
	props := mergeValue(properties, value)
	enqueue(c, newEvent(c, name, props))
}

// Distribution records a sampled value as an event with a numeric `value`
// property. Same semantics as Gauge — the distinction is for the caller's
// intent only.
func Distribution(name string, sample float64, properties map[string]interface{}) {
	c := current()
	if c == nil || c.disabled || c.mode == ModeMinimal {
		return
	}
	props := mergeValue(properties, sample)
	enqueue(c, newEvent(c, name, props))
}

// SetUserProperties enqueues a profile update for this device. The endpoint
// merges props into the existing profile (latest value per key wins), so
// repeated calls during a session are fine.
func SetUserProperties(props map[string]interface{}) {
	c := current()
	if c == nil || c.disabled || c.mode == ModeMinimal {
		return
	}
	enqueueProfile(c, sanitizeProperties(props))
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

// newEvent builds an event item stamped with the client's identity and the
// current time (the endpoint records it as client_ts).
func newEvent(c *Client, eventType string, props map[string]any) ingestItem {
	return ingestItem{
		Kind:       "event",
		Event:      eventType,
		DistinctID: c.distinctID,
		DeviceID:   c.deviceID,
		Mode:       c.mode,
		Version:    c.version,
		TS:         time.Now().Format(time.RFC3339),
		Props:      props,
	}
}

// enqueue pushes an item onto the worker queue without blocking. If the queue
// is full, the item is dropped — fleet emits few enough events that this
// should never happen in practice, but the non-blocking guarantee is what
// keeps Track safe to call from the Bubble Tea Update() loop.
//
// The recover handles a narrow Shutdown race: between current() returning a
// non-nil *Client and reaching the channel send below, Shutdown can close
// c.queue from another goroutine (hook watcher, chrome client, etc.). A
// closed channel makes `case c.queue <- …` selectable and panics. Analytics
// is best-effort, so we'd rather drop the item than crash the TUI.
func enqueue(c *Client, item ingestItem) {
	defer func() { _ = recover() }()
	select {
	case c.queue <- item:
	default:
		debuglog.Logger.Debug("analytics queue full, dropping item", "kind", item.Kind, "event", item.Event)
	}
}

// enqueueProfile queues a profile (people) update keyed on this device's
// distinct_id.
func enqueueProfile(c *Client, props map[string]any) {
	enqueue(c, ingestItem{
		Kind:       "profile",
		DistinctID: c.distinctID,
		DeviceID:   c.deviceID,
		Props:      props,
	})
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
// mode), with no people profile and no counts — the leanest launch signal that
// still marks the device active today.
func TrackAppStarted(version string, sessionCount, repoCount int, theme, enterMode, defaultAgent string, autoName, copyClaudeSettings bool) {
	c := current()
	if c == nil || c.disabled {
		return
	}

	if c.mode == ModeMinimal {
		trackRaw(c, EventAppStarted, map[string]any{"version": version, "mode": c.mode})
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
		"version":       version,
		"session_count": sessionCount,
		"repo_count":    repoCount,
		"mode":          c.mode,
	})
}
