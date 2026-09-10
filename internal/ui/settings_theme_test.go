package ui

import (
	"testing"

	"github.com/brizzai/fleet/internal/config"
)

// A user who never picked a theme has an empty Theme, while cycling stores the
// explicit name — so a full lap back to the default must not read as a change.
func TestSettingsThemeChangedComparesEffectiveTheme(t *testing.T) {
	var other string
	for _, p := range BuiltinPalettes {
		if p.Name != DefaultPaletteName {
			other = p.Name
			break
		}
	}

	d := NewSettingsDialog(&config.Config{})
	d.Show() // opens on the unset theme
	for theme, want := range map[string]bool{
		"":                 false,
		DefaultPaletteName: false,
		other:              true,
	} {
		d.cfg.Theme = theme
		if got := d.themeChanged(); got != want {
			t.Errorf("opened unset, closed on %q: themeChanged() = %v, want %v", theme, got, want)
		}
	}
}
