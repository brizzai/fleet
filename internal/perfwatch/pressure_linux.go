package perfwatch

import "os"

// MemoryPressure reports the Linux system memory-pressure level and free swap
// in MB — the same OOM signal the darwin probe derives from Jetsam. Level
// comes from /proc/pressure/memory (PSI, kernel ≥ 4.20 with CONFIG_PSI;
// PressureUnknown when absent), free swap from /proc/meminfo (-1 when no swap
// is configured, so the caller's low-swap-is-critical gate can't fire on
// swapless boxes). Pure file reads — cheap, but call off the Update() loop
// like the darwin probe.
func MemoryPressure() (level int, swapFreeMB int64) {
	level = PressureUnknown
	swapFreeMB = -1
	if data, err := os.ReadFile("/proc/pressure/memory"); err == nil {
		level = parsePSIMemoryLevel(string(data))
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		swapFreeMB = parseMeminfoSwapFreeMB(string(data))
	}
	return level, swapFreeMB
}
