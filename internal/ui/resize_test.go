package ui

import (
	"testing"

	"github.com/brizzai/fleet/internal/config"
)

func TestSidebarWidth(t *testing.T) {
	tests := []struct {
		name       string
		termWidth  int
		sidebarPct *int
		wantWidth  int
	}{
		{
			name:      "default 35% at 120 cols",
			termWidth: 120,
			wantWidth: 42, // 120 * 35 / 100
		},
		{
			name:      "default 35% at 100 cols",
			termWidth: 100,
			wantWidth: 35,
		},
		{
			name:       "40% at 120 cols",
			termWidth:  120,
			sidebarPct: intP(40),
			wantWidth:  48,
		},
		{
			name:       "60% at 120 cols",
			termWidth:  120,
			sidebarPct: intP(60),
			wantWidth:  72,
		},
		{
			name:       "20% at 120 cols",
			termWidth:  120,
			sidebarPct: intP(20),
			wantWidth:  24,
		},
		{
			name:       "min sidebar 20 enforced",
			termWidth:  80,
			sidebarPct: intP(20),
			wantWidth:  20, // 80 * 20 / 100 = 16, clamped to 20
		},
		{
			name:       "preview min 30 enforced",
			termWidth:  60,
			sidebarPct: intP(60),
			wantWidth:  27, // 60 - 33 = 27 (preview gets 30)
		},
		{
			name:       "very narrow terminal prefers sidebar min",
			termWidth:  53,
			sidebarPct: intP(60),
			wantWidth:  20, // 53 - 33 = 20
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			if tt.sidebarPct != nil {
				cfg.SetSidebarPct(*tt.sidebarPct)
			}
			h := &Home{
				width: tt.termWidth,
				cfg:   cfg,
			}
			got := h.sidebarWidth()
			if got != tt.wantWidth {
				t.Errorf("sidebarWidth() = %d, want %d", got, tt.wantWidth)
			}
		})
	}
}

func intP(v int) *int { return &v }
