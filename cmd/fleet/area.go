package main

import (
	"fmt"
	"strings"

	"github.com/brizzai/fleet/internal/area"
	"github.com/brizzai/fleet/internal/config"
)

// parseAreaFlag reads `--area <name>` / `--area=<name>` from the TUI's own
// arguments and validates it against the configured areas. "" means no flag
// (show everything). The error lists the available areas.
func parseAreaFlag(args []string, cfg *config.Config) (string, error) {
	var name string
	var set bool
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--area":
			if i+1 >= len(args) {
				return "", fmt.Errorf("--area needs a name")
			}
			name, set = args[i+1], true
			i++
		case strings.HasPrefix(a, "--area="):
			name, set = strings.TrimPrefix(a, "--area="), true
		default:
			return "", fmt.Errorf("unknown argument %q (usage: fleet [--area <name>])", a)
		}
	}
	if !set {
		return "", nil
	}
	m := area.New(cfg.Areas, "")
	if !m.Enabled() {
		return "", fmt.Errorf("no areas configured: add an \"areas\" list to ~/.config/fleet/config.json")
	}
	resolved, ok := m.Resolve(name)
	if !ok {
		return "", fmt.Errorf("unknown area %q (available: %s)", name, strings.Join(m.Names(), ", "))
	}
	return resolved, nil
}
