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

// Add records a new error in the ring buffer. We deliberately do NOT
// forward the raw message to the analytics backend — fleet error strings
// frequently contain file paths, repo names, and other user-specific text.
// `setError` emits a category-tagged EventErrorOccurred counter for the
// same frequency signal without that PII risk.
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
