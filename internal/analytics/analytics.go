// Package analytics ships anonymous usage metrics to Sentry. The public surface
// (Init, Track, SetUserProperties, Shutdown) is intentionally unchanged from
// the previous Amplitude implementation so existing call sites keep working.
//
// Backend: sentry.NewMeter records Count/Gauge/Distribution metrics. Errors and
// panics also flow through this package via CaptureError so a single opt-out
// switch covers both usage analytics and crash reports.
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
	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
)

const sentryDSN = "https://ed169a419b5e672e4bb2b392155e26b4@o4510872302911488.ingest.us.sentry.io/4511460792598528"

var (
	global   *Client
	globalMu sync.Mutex
)

// Client holds the Sentry meter plus the anonymous device id.
type Client struct {
	meter    sentry.Meter
	deviceID string
	disabled bool
}

// Init wires up Sentry and a global meter. Safe to call once; subsequent calls
// are no-ops. When telemetry is disabled the client is created in a "disabled"
// state and all Track/Gauge/Distribution/CaptureError calls become no-ops.
func Init(telemetryEnabled bool, version string) {
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

	deviceID := getOrCreateDeviceID()

	if err := sentry.Init(sentry.ClientOptions{
		Dsn:              sentryDSN,
		Release:          "fleet@" + version,
		Environment:      sentryEnvironment(version),
		AttachStacktrace: true,
		EnableTracing:    false,
	}); err != nil {
		debuglog.Logger.Error("sentry init failed", "err", err)
		global = &Client{disabled: true}
		return
	}

	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetUser(sentry.User{ID: deviceID})
	})

	meter := sentry.NewMeter(context.Background())
	meter.SetAttributes(
		attribute.String("app_version", version),
		attribute.String("os_version", osVersion()),
		attribute.String("arch", runtime.GOARCH),
		attribute.String("device_id", deviceID),
	)

	global = &Client{
		meter:    meter,
		deviceID: deviceID,
	}

	preview := deviceID
	if len(preview) >= 8 {
		preview = preview[:8]
	}
	debuglog.Logger.Info("analytics initialized", "device_id", preview+"...")
}

// Track records a Count(name, 1) metric with optional attributes.
func Track(eventType string, properties map[string]interface{}) {
	c := current()
	if c == nil || c.disabled {
		return
	}
	c.meter.Count(eventType, 1, sentry.WithAttributes(propertiesToAttributes(properties)...))
}

// Gauge records a point-in-time value (e.g., session count snapshot).
func Gauge(name string, value float64, properties map[string]interface{}) {
	c := current()
	if c == nil || c.disabled {
		return
	}
	c.meter.Gauge(name, value, sentry.WithAttributes(propertiesToAttributes(properties)...))
}

// Distribution records a sampled value (e.g., session lifetime in seconds).
func Distribution(name string, sample float64, properties map[string]interface{}) {
	c := current()
	if c == nil || c.disabled {
		return
	}
	c.meter.Distribution(name, sample, sentry.WithAttributes(propertiesToAttributes(properties)...))
}

// CapturePanic reports a recovered panic value to Sentry. Use this from a
// deferred recover() so Sentry's panic API preserves the original panic
// value type and Go's full stack trace from the runtime — wrapping the
// panic in fmt.Errorf would flatten both.
func CapturePanic(r any) {
	c := current()
	if c == nil || c.disabled || r == nil {
		return
	}
	sentry.CurrentHub().Recover(r)
}

// CaptureError reports a Go error to Sentry with optional tags.
func CaptureError(err error, properties map[string]interface{}) {
	c := current()
	if c == nil || c.disabled || err == nil {
		return
	}
	if len(properties) == 0 {
		sentry.CaptureException(err)
		return
	}
	sentry.WithScope(func(scope *sentry.Scope) {
		for k, v := range properties {
			if !sanitizeKey(k) {
				continue
			}
			scope.SetTag(k, fmt.Sprintf("%v", v))
		}
		sentry.CaptureException(err)
	})
}

// SetUserProperties merges attributes into the meter's default attribute set.
// Per Sentry docs these are included in all subsequent metrics.
func SetUserProperties(props map[string]interface{}) {
	c := current()
	if c == nil || c.disabled {
		return
	}
	c.meter.SetAttributes(propertiesToAttributes(props)...)
}

// Shutdown flushes pending Sentry traffic. Idempotent.
func Shutdown() {
	globalMu.Lock()
	c := global
	global = nil
	globalMu.Unlock()

	if c == nil || c.disabled {
		return
	}

	sentry.Flush(2 * time.Second)
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

// propertiesToAttributes converts a property map to typed Sentry attributes.
// Drops keys that match the privacy blocklist via sanitizeKey.
func propertiesToAttributes(props map[string]interface{}) []attribute.Builder {
	if len(props) == 0 {
		return nil
	}
	attrs := make([]attribute.Builder, 0, len(props))
	for k, v := range props {
		if !sanitizeKey(k) {
			continue
		}
		switch val := v.(type) {
		case nil:
			continue
		case string:
			attrs = append(attrs, attribute.String(k, val))
		case bool:
			attrs = append(attrs, attribute.Bool(k, val))
		case int:
			attrs = append(attrs, attribute.Int(k, val))
		case int32:
			attrs = append(attrs, attribute.Int64(k, int64(val)))
		case int64:
			attrs = append(attrs, attribute.Int64(k, val))
		case float32:
			attrs = append(attrs, attribute.Float64(k, float64(val)))
		case float64:
			attrs = append(attrs, attribute.Float64(k, val))
		default:
			attrs = append(attrs, attribute.String(k, fmt.Sprintf("%v", val)))
		}
	}
	return attrs
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

// sentryEnvironment maps a build version to a Sentry environment tag.
func sentryEnvironment(version string) string {
	if version == "" || version == "dev" {
		return "development"
	}
	return "production"
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
// meter's default attributes, then emits an EventAppStarted counter.
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
