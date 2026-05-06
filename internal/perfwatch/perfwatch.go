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
	"math"
	"os"
	"os/exec"
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
	// updateStormRate triggers a snapshot when sustained Update() throughput
	// over one heartbeat interval exceeds this (msgs/sec). Catches tea.Cmd
	// loops that flood the loop without any single Update going slow — the
	// watchdog can't see those because elapsed never crosses stallThreshold.
	updateStormRate = 200
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
	// 0700: snapshots include goroutine stacks + block/mutex profiles.
	if err := os.MkdirAll(stallDir, 0700); err != nil {
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

	// 0600: snapshot contents are user-private (goroutine stacks etc.).
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
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
		if !claimDump() {
			continue
		}

		msg := "unknown"
		if p := updateMsgType.Load(); p != nil {
			msg = *p
		}
		Snapshot(fmt.Sprintf("update_stall_%s_%dms", msg, elapsed.Milliseconds()))
	}
}

// claimDump returns true if no other dump fired within the last minDumpGap.
// Shared by the stall watchdog and the heartbeat storm detector so concurrent
// failure modes don't pile up dumps on disk.
func claimDump() bool {
	dumpMu.Lock()
	defer dumpMu.Unlock()
	if time.Since(lastDumpAt) < minDumpGap {
		return false
	}
	lastDumpAt = time.Now()
	return true
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
	lastUpdateCount := totalUpdates.Load()

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

		updateCount := totalUpdates.Load()
		updateRate := 0.0
		if wall > 0 {
			updateRate = float64(updateCount-lastUpdateCount) / wall
		}
		lastUpdateCount = updateCount

		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)

		debuglog.Logger.Debug("perfwatch heartbeat",
			"goroutines", runtime.NumGoroutine(),
			"cpu_pct", math.Round(pct*10)/10, // numeric float64 (sentinel -1.0 = unavailable) so structured-log filtering can compare against thresholds
			"heap_kb", ms.HeapAlloc/1024,
			"sys_free_mb", systemFreeMB(), // -1 if vm_stat unavailable; near-zero = imminent Jetsam OOM kill
			"updates_total", updateCount,
			"updates_per_sec", math.Round(updateRate*10)/10,
			"updates_slow", slowUpdates.Load(),
			"max_update_ms", maxUpdateMs.Load(),
		)

		// Storm detector: a tea.Cmd that re-arms without throttling can flood
		// the loop without any single Update going slow, so the watchdog never
		// fires. Snapshot once so the recent-message ring captures the culprit.
		if updateRate > updateStormRate && claimDump() {
			Snapshot(fmt.Sprintf("update_storm_%dpersec", int(updateRate)))
		}
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

// systemFreeMB returns macOS-wide free memory in MB. Returns -1 on any error.
// Implementation shells out to vm_stat once per heartbeat (~5ms) and parses
// the page size from its header line + the "Pages free" field. Fleet is
// macOS-only; on other platforms this still works if vm_stat is shimmed,
// otherwise returns -1.
//
// Why this matters: when this drops toward 0, macOS Jetsam will start killing
// user processes — Claude Code child processes are prime targets due to their
// large in-memory KV cache. Crash dumps cross-reference this trail to confirm
// OOM-kill diagnoses.
func systemFreeMB() int64 {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return -1
	}
	pageSize := int64(4096) // x86 default
	var freePages int64 = -1
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, "page size of "); i >= 0 {
			rest := line[i+len("page size of "):]
			var n int64
			if _, err := fmt.Sscanf(rest, "%d", &n); err == nil && n > 0 {
				pageSize = n
			}
			continue
		}
		if strings.HasPrefix(line, "Pages free:") {
			rest := strings.TrimSpace(strings.TrimPrefix(line, "Pages free:"))
			rest = strings.TrimSuffix(rest, ".")
			var n int64
			if _, err := fmt.Sscanf(rest, "%d", &n); err == nil {
				freePages = n
			}
		}
	}
	if freePages < 0 {
		return -1
	}
	return freePages * pageSize / (1024 * 1024)
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
