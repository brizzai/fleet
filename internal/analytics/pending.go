package analytics

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/brizzai/fleet/internal/debuglog"
)

// pendingEventsPath is where events that must be recorded but can't be sent yet
// are parked. The auto-updater runs in main() BEFORE analytics.Init (which
// happens inside the TUI, after the consent prompt), and on a successful update
// it re-execs the new binary — so an update_applied event Track'd at that point
// would be dropped (no client yet) and lost across the exec. QueuePending
// persists such events to disk; FlushPending drains them on the next launch,
// once the client is up.
func pendingEventsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fleet", "pending_events.jsonl")
}

type pendingEvent struct {
	Event string         `json:"event"`
	Props map[string]any `json:"props,omitempty"`
}

// QueuePending appends an event to the on-disk pending queue for a later
// FlushPending. Safe to call before Init and from any goroutine; best-effort
// (errors are logged, never fatal). Intended only for the rare pre-Init events
// (update_check / update_applied) — not for high-frequency events, since the
// queue is drained one line at a time on the next launch.
func QueuePending(event string, props map[string]any) {
	line, err := json.Marshal(pendingEvent{Event: event, Props: props})
	if err != nil {
		return
	}
	path := pendingEventsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		debuglog.Logger.Debug("pending events: mkdir failed", "err", err)
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		debuglog.Logger.Debug("pending events: open failed", "err", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		debuglog.Logger.Debug("pending events: write failed", "err", err)
	}
}

// FlushPending emits any queued pending events through Track, then removes the
// queue file. Call once right after Init. No-op when the file is absent (the
// common case). Track itself no-ops when analytics is disabled/minimal, so the
// file is cleared either way — queued events are never leaked or resent.
func FlushPending() {
	path := pendingEventsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return // no pending file: nothing to do
	}
	// Remove first, so a malformed line or a crash mid-flush can't cause the
	// same events to resend on every subsequent launch.
	_ = os.Remove(path)

	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev pendingEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			debuglog.Logger.Debug("pending events: skipping malformed line", "err", err)
			continue
		}
		Track(ev.Event, ev.Props)
	}
}
