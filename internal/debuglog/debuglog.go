package debuglog

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

const maxLogSize = 1 << 20  // 1 MB
const keepBytes = 512 << 10 // keep last 512 KB after truncation

var (
	mu       sync.Mutex
	initOnce sync.Once
	logFile  *os.File
	Logger   *slog.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil)) // fallback
)

// LogPath returns the debug log file path.
func LogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "fleet-debug.log")
	}
	return filepath.Join(home, ".config", "fleet", "debug.log")
}

// Init opens the debug log file and configures the global Logger. Idempotent:
// subsequent calls are no-ops. The sync.Once guard prevents a race where tests
// (or any code) re-init Logger while a long-lived goroutine (e.g. crash-dump
// writer) is still reading it — only the first call writes Logger.
// On startup, if the log file exceeds maxLogSize, it is truncated to the last keepBytes.
func Init() {
	initOnce.Do(func() {
		path := LogPath()
		_ = os.MkdirAll(filepath.Dir(path), 0755)
		truncateIfNeeded(path)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return
		}
		mu.Lock()
		logFile = f
		level := slog.LevelInfo
		if os.Getenv("FLEET_DEBUG") != "" {
			level = slog.LevelDebug
		}
		Logger = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level}))
		mu.Unlock()
	})
}

// Close closes the log file.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if logFile != nil {
		logFile.Close()
		logFile = nil
	}
}

// truncateIfNeeded keeps only the last keepBytes of the log file if it exceeds maxLogSize.
func truncateIfNeeded(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= maxLogSize {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	tail := data
	if len(data) > keepBytes {
		tail = data[len(data)-keepBytes:]
	}
	// Advance to the first newline so we don't start mid-line.
	for i, b := range tail {
		if b == '\n' {
			tail = tail[i+1:]
			break
		}
	}
	_ = os.WriteFile(path, tail, 0644)
}
