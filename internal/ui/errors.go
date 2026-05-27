package ui

import (
	"sync"
	"time"
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

// Add records a new error in the ring buffer. Sentry reporting happens
// separately at the setError call site (which emits a category-tagged
// EventErrorOccurred counter); we deliberately do NOT forward the raw
// message here because fleet error strings frequently contain file paths,
// repo names, and other user-specific text that shouldn't ship to Sentry.
func (h *ErrorHistory) Add(msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
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
