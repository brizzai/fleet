package perfwatch

import "testing"

func TestParsePSIMemoryLevel(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{
			"healthy machine",
			"some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
			PressureNormal,
		},
		{
			"mild stall pressure is still normal",
			"some avg10=2.51 avg60=1.20 avg300=0.40 total=123456\nfull avg10=0.80 avg60=0.30 avg300=0.10 total=45678\n",
			PressureNormal,
		},
		{
			"sustained some pressure is warning",
			"some avg10=14.22 avg60=8.00 avg300=2.00 total=999999\nfull avg10=3.10 avg60=1.00 avg300=0.30 total=111111\n",
			PressureWarning,
		},
		{
			"heavy full pressure is critical",
			"some avg10=30.00 avg60=20.00 avg300=10.00 total=999999\nfull avg10=12.40 avg60=6.00 avg300=2.00 total=555555\n",
			PressureCritical,
		},
		{
			"extreme some pressure is critical even with low full",
			"some avg10=55.00 avg60=40.00 avg300=20.00 total=999999\nfull avg10=4.00 avg60=2.00 avg300=1.00 total=555555\n",
			PressureCritical,
		},
		{"empty file", "", PressureUnknown},
		{"garbage", "no psi here", PressureUnknown},
		{"some line only still parses", "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n", PressureNormal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsePSIMemoryLevel(tt.in); got != tt.want {
				t.Errorf("parsePSIMemoryLevel(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseMeminfoSwapFreeMB(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{
			"normal swap",
			"MemTotal:       16384000 kB\nSwapTotal:       8388604 kB\nSwapFree:        4194304 kB\n",
			4096,
		},
		{
			// A no-swap box (common on desktops and containers) must report
			// "unknown", not 0 — the caller treats low free swap as critical
			// pressure, which would otherwise fire permanently.
			"no swap configured reports unknown",
			"MemTotal:       16384000 kB\nSwapTotal:              0 kB\nSwapFree:               0 kB\n",
			-1,
		},
		{"missing swap lines", "MemTotal:       16384000 kB\n", -1},
		{"empty", "", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseMeminfoSwapFreeMB(tt.in); got != tt.want {
				t.Errorf("parseMeminfoSwapFreeMB(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseSwapUsageFreeMB(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{"typical output", "total = 11264.00M  used = 9955.44M  free = 1308.56M  (encrypted)", 1308},
		{"no free field", "total = 0.00M  used = 0.00M", -1},
		{"empty", "", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSwapUsageFreeMB(tt.in); got != tt.want {
				t.Errorf("parseSwapUsageFreeMB(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
