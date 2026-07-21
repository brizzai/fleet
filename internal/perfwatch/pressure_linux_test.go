package perfwatch

import "testing"

// TestSwapEscalatesPressure pins the Linux semantics: low free swap only
// corroborates pressure PSI already sees. A healthy box with a small or
// partially-used swap partition (the common Linux case) must never read as
// critical, and a PSI-less kernel (unknown level) must never act on swap
// alone — that's the "nothing is auto-suspended without PSI" guarantee.
func TestSwapEscalatesPressure(t *testing.T) {
	cases := []struct {
		name string
		lvl  int
		swap int64
		want bool
	}{
		{"warning + low swap escalates", PressureWarning, 100, true},
		{"critical + low swap escalates", PressureCritical, 100, true},
		{"normal + low swap does not", PressureNormal, 100, false},
		{"unknown + low swap does not (PSI-less kernel)", PressureUnknown, 100, false},
		{"warning + ample swap does not", PressureWarning, 4096, false},
		{"warning + swapless (-1) does not", PressureWarning, -1, false},
		{"warning + exactly at threshold does not", PressureWarning, swapCriticalMB, false},
	}
	for _, c := range cases {
		if got := SwapEscalatesPressure(c.lvl, c.swap); got != c.want {
			t.Errorf("%s: SwapEscalatesPressure(%d, %d) = %v, want %v",
				c.name, c.lvl, c.swap, got, c.want)
		}
	}
}
