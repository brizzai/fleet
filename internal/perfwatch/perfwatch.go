// Package perfwatch instruments the Bubble Tea Update() loop to detect and
// post-mortem-debug stalls. When FLEET_DEBUG is set:
//
//   - Every Update() is wrapped via MarkUpdateStart/MarkUpdateEnd. The last 32
//     messages and their durations are kept in a ring buffer.
//   - A watchdog goroutine ticks every 100ms; if Update() has been running
//     longer than stallThreshold, it writes a dump to ~/.config/fleet/stalls/
//     containing goroutine stacks, block + mutex profiles, recent message ring,
//     and counters.
//   - A heartbeat logs goroutine count and process CPU% every 5s — keeps
//     running during attach so background-worker CPU use shows up in debug.log
//     even when the TUI loop is suspended.
//   - SIGUSR1 forces a snapshot on demand.
//
// When FLEET_DEBUG is unset, all entry points short-circuit and the package
// adds no measurable overhead.
package perfwatch

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

const (
	stallThreshold     = 500 * time.Millisecond
	slowLogThreshold   = 100 * time.Millisecond
	watchdogInterval   = 100 * time.Millisecond
	heartbeatInterval  = 5 * time.Second
	minDumpGap         = 1500 * time.Millisecond
	recentMsgRing      = 32
	blockProfileRateNs = int64(time.Millisecond) // record blocks >= 1ms
)

var (
	enabled atomic.Bool

	updateStartUnixNano atomic.Int64
	updateMsgType       atomic.Pointer[string]

	totalUpdates atomic.Int64
	slowUpdates  atomic.Int64
	maxUpdateMs  atomic.Int64

	recentMu   sync.Mutex
	recentBuf  [recentMsgRing]recentEntry
	recentNext int

	dumpMu     sync.Mutex
	lastDumpAt time.Time

	stallDir string
)

type recentEntry struct {
	when     time.Time
	msgType  string
	duration time.Duration
}

// UpdateToken carries the start state for a single Update() invocation.
// Returned by MarkUpdateStart and consumed by MarkUpdateEnd.
type UpdateToken struct {
	start   time.Time
	msgType string
}

// Enabled reports whether perfwatch instrumentation is active.
func Enabled() bool { return enabled.Load() }

// Init starts perfwatch if FLEET_DEBUG is set. Otherwise it is a no-op and
// all hot paths short-circuit.
func Init() {
	if os.Getenv("FLEET_DEBUG") == "" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		debuglog.Logger.Error("perfwatch: UserHomeDir failed", "err", err)
		return
	}
	stallDir = filepath.Join(home, ".config", "fleet", "stalls")
	if err := os.MkdirAll(stallDir, 0755); err != nil {
		debuglog.Logger.Error("perfwatch: mkdir stalls", "err", err)
		return
	}

	runtime.SetBlockProfileRate(int(blockProfileRateNs))
	runtime.SetMutexProfileFraction(1)

	enabled.Store(true)
	debuglog.Logger.Info("perfwatch: enabled",
		"stall_dir", stallDir,
		"stall_threshold_ms", stallThreshold.Milliseconds(),
	)

	go watchdogLoop()
	go heartbeatLoop()
	go signalLoop()
}

// MarkUpdateStart records the entry to a Bubble Tea Update() call. Call as the
// first statement of Update; the returned token must be passed to MarkUpdateEnd
// (typically via defer).
func MarkUpdateStart(msgType string) UpdateToken {
	if !enabled.Load() {
		return UpdateToken{}
	}
	now := time.Now()
	updateStartUnixNano.Store(now.UnixNano())
	mt := msgType
	updateMsgType.Store(&mt)
	return UpdateToken{start: now, msgType: msgType}
}

// MarkUpdateEnd records the exit from a Bubble Tea Update() call.
func MarkUpdateEnd(t UpdateToken) {
	if !enabled.Load() || t.start.IsZero() {
		return
	}
	dur := time.Since(t.start)
	updateStartUnixNano.Store(0)
	updateMsgType.Store(nil)
	totalUpdates.Add(1)

	if ms := dur.Milliseconds(); ms > maxUpdateMs.Load() {
		maxUpdateMs.Store(ms)
	}
	// Counter mirrors the WARN log threshold (≥100ms), not the stall-dump
	// threshold (≥500ms). The skill surfaces this as "Updates >100ms".
	if dur >= slowLogThreshold {
		slowUpdates.Add(1)
	}

	recentMu.Lock()
	recentBuf[recentNext] = recentEntry{when: t.start, msgType: t.msgType, duration: dur}
	recentNext = (recentNext + 1) % recentMsgRing
	recentMu.Unlock()

	if dur >= slowLogThreshold {
		debuglog.Logger.Warn("perfwatch: slow Update",
			"msg", t.msgType,
			"duration_ms", dur.Milliseconds(),
		)
	}
}

// Snapshot writes a stall dump to the stalls directory and returns its path.
// Safe to call from any goroutine. Returns "" if perfwatch is disabled.
func Snapshot(reason string) string {
	if !enabled.Load() {
		return ""
	}
	ts := time.Now().Format("20060102-150405.000")
	safe := sanitizeFilename(reason)
	path := filepath.Join(stallDir, ts+"_"+safe+".txt")

	f, err := os.Create(path)
	if err != nil {
		debuglog.Logger.Error("perfwatch: create snapshot", "err", err, "path", path)
		return ""
	}
	defer f.Close()

	writeSnapshot(f, reason)
	debuglog.Logger.Warn("perfwatch: snapshot written", "path", path, "reason", reason)
	return path
}

func writeSnapshot(f *os.File, reason string) {
	fmt.Fprintf(f, "=== perfwatch snapshot ===\n")
	fmt.Fprintf(f, "Reason:     %s\n", reason)
	fmt.Fprintf(f, "Time:       %s\n", time.Now().Format(time.RFC3339Nano))
	fmt.Fprintf(f, "Goroutines: %d\n", runtime.NumGoroutine())

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Fprintf(f, "HeapAlloc:  %d KB\n", ms.HeapAlloc/1024)
	fmt.Fprintf(f, "Sys:        %d KB\n", ms.Sys/1024)
	fmt.Fprintf(f, "NumGC:      %d\n", ms.NumGC)

	if startNs := updateStartUnixNano.Load(); startNs != 0 {
		elapsed := time.Since(time.Unix(0, startNs))
		msg := "(unknown)"
		if p := updateMsgType.Load(); p != nil {
			msg = *p
		}
		fmt.Fprintf(f, "\nUpdate() IN FLIGHT: msg=%s elapsed=%s\n", msg, elapsed)
	} else {
		fmt.Fprintf(f, "\nUpdate() not in flight at snapshot time\n")
	}

	fmt.Fprintf(f, "Counters: total=%d slow=%d max_ms=%d\n",
		totalUpdates.Load(), slowUpdates.Load(), maxUpdateMs.Load())

	fmt.Fprintf(f, "\n=== Recent Update() messages (oldest -> newest) ===\n")
	recentMu.Lock()
	for i := range recentMsgRing {
		idx := (recentNext + i) % recentMsgRing
		e := recentBuf[idx]
		if e.when.IsZero() {
			continue
		}
		fmt.Fprintf(f, "%s  %-40s  %s\n",
			e.when.Format("15:04:05.000"), e.msgType, e.duration)
	}
	recentMu.Unlock()

	fmt.Fprintf(f, "\n=== Goroutine stacks (debug=2) ===\n")
	if p := pprof.Lookup("goroutine"); p != nil {
		if err := p.WriteTo(f, 2); err != nil {
			fmt.Fprintf(f, "(goroutine profile error: %v)\n", err)
		}
	}

	fmt.Fprintf(f, "\n=== Block profile ===\n")
	if p := pprof.Lookup("block"); p != nil {
		if err := p.WriteTo(f, 1); err != nil {
			fmt.Fprintf(f, "(block profile error: %v)\n", err)
		}
	}

	fmt.Fprintf(f, "\n=== Mutex profile ===\n")
	if p := pprof.Lookup("mutex"); p != nil {
		if err := p.WriteTo(f, 1); err != nil {
			fmt.Fprintf(f, "(mutex profile error: %v)\n", err)
		}
	}
}

func watchdogLoop() {
	t := time.NewTicker(watchdogInterval)
	defer t.Stop()
	for range t.C {
		startNs := updateStartUnixNano.Load()
		if startNs == 0 {
			continue
		}
		elapsed := time.Since(time.Unix(0, startNs))
		if elapsed < stallThreshold {
			continue
		}

		dumpMu.Lock()
		if time.Since(lastDumpAt) < minDumpGap {
			dumpMu.Unlock()
			continue
		}
		lastDumpAt = time.Now()
		dumpMu.Unlock()

		msg := "unknown"
		if p := updateMsgType.Load(); p != nil {
			msg = *p
		}
		Snapshot(fmt.Sprintf("update_stall_%s_%dms", msg, elapsed.Milliseconds()))
	}
}

func heartbeatLoop() {
	// /cpu/classes/total reports CPU resources *available* (wall × NumCPU),
	// not consumed. Subtract idle to get actual fleet CPU usage. Reported as
	// multi-core %: 100% = one full core busy, 250% = 2.5 cores busy.
	samples := []metrics.Sample{
		{Name: "/cpu/classes/total:cpu-seconds"},
		{Name: "/cpu/classes/idle:cpu-seconds"},
	}
	metrics.Read(samples)
	lastTotal, lastIdle, cpuOK := readCPUSamples(samples)
	lastWall := time.Now()

	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for now := range t.C {
		metrics.Read(samples)
		total, idle, ok := readCPUSamples(samples)
		wall := now.Sub(lastWall).Seconds()
		pct := -1.0 // sentinel: CPU metric unavailable on this runtime
		if ok && cpuOK && wall > 0 {
			pct = ((total - lastTotal) - (idle - lastIdle)) / wall * 100
		}
		lastTotal, lastIdle, cpuOK = total, idle, ok
		lastWall = now

		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)

		debuglog.Logger.Debug("perfwatch heartbeat",
			"goroutines", runtime.NumGoroutine(),
			"cpu_pct", fmt.Sprintf("%.1f", pct),
			"heap_kb", ms.HeapAlloc/1024,
			"updates_total", totalUpdates.Load(),
			"updates_slow", slowUpdates.Load(),
			"max_update_ms", maxUpdateMs.Load(),
		)
	}
}

// readCPUSamples returns total/idle CPU seconds, or ok=false when the runtime
// doesn't expose these metrics as Float64 (e.g. metric removed or renamed in a
// future Go release). Calling Float64() on a non-Float64 sample panics.
func readCPUSamples(samples []metrics.Sample) (total, idle float64, ok bool) {
	if len(samples) < 2 {
		return 0, 0, false
	}
	if samples[0].Value.Kind() != metrics.KindFloat64 || samples[1].Value.Kind() != metrics.KindFloat64 {
		return 0, 0, false
	}
	return samples[0].Value.Float64(), samples[1].Value.Float64(), true
}

func signalLoop() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	for range ch {
		Snapshot("manual_sigusr1")
	}
}

func sanitizeFilename(s string) string {
	if len(s) > 80 {
		s = s[:80]
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, s)
}
