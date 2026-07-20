package perfwatch

import (
	"fmt"
	"os/exec"
	"strings"
)

// MemoryPressure reports the macOS system memory-pressure level and free swap
// in MB. level follows kern.memorystatus_vm_pressure_level (1 normal / 2
// warning / 4 critical) — a far better OOM predictor than "Pages free"
// (systemFreeMB), which macOS always keeps low by design. Returns
// (PressureUnknown, -1) on error. Shells out to sysctl (~1-2ms) — call off the
// Update() loop.
func MemoryPressure() (level int, swapFreeMB int64) {
	level = PressureUnknown
	swapFreeMB = -1
	if out, err := exec.Command("sysctl", "-n", "kern.memorystatus_vm_pressure_level").Output(); err == nil {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n); err == nil {
			level = n
		}
	}
	if out, err := exec.Command("sysctl", "-n", "vm.swapusage").Output(); err == nil {
		swapFreeMB = parseSwapUsageFreeMB(string(out))
	}
	return level, swapFreeMB
}

// SwapEscalatesPressure reports whether critically-low free swap should
// escalate the pressure level to critical. On macOS swap files are grown and
// shrunk on demand, so free swap only runs low when the system is genuinely
// out of room — and the Jetsam level lags swap thrash, so low free swap is
// trusted on its own, whatever the reported level says.
func SwapEscalatesPressure(_ int, swapFreeMB int64) bool {
	return swapFreeMB >= 0 && swapFreeMB < swapCriticalMB
}
