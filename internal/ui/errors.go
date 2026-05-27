package ui

import (
	"errors"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/analytics"
)

// ErrorEntry records a single error that was displayed to the user.
type ErrorEntry struct {
	Timestamp time.Time
	Message   string
}

// ErrorHistory is a thread-safe ring buffer of recent errors.
type ErrorHistory struct {
	mu      sync.Mutex
	entries []ErrorEntry
	maxSize int
}

// NewErrorHistory creates an ErrorHistory with the given capacity.
func NewErrorHistory(maxSize int) *ErrorHistory {
	return &ErrorHistory{
		entries: make([]ErrorEntry, 0, maxSize),
		maxSize: maxSize,
	}
}

// Add records a new error and reports it to Sentry (no-op when telemetry is
// disabled). Reporting is fire-and-forget; we don't block the UI on the
// network call — analytics.CaptureError hands off to the Sentry SDK which
// batches and sends in its own goroutine.
func (h *ErrorHistory) Add(msg string) {
	h.mu.Lock()
	entry := ErrorEntry{
		Timestamp: time.Now(),
		Message:   msg,
	}
	if len(h.entries) >= h.maxSize {
		copy(h.entries, h.entries[1:])
		h.entries[len(h.entries)-1] = entry
	} else {
		h.entries = append(h.entries, entry)
	}
	h.mu.Unlock()

	analytics.CaptureError(errors.New(msg), nil)
}

// Entries returns all entries, newest first.
func (h *ErrorHistory) Entries() []ErrorEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := make([]ErrorEntry, len(h.entries))
	for i, e := range h.entries {
		result[len(h.entries)-1-i] = e
	}
	return result
}
