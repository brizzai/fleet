// Package envflag reads fleet's boolean environment switches (FLEET_NO_*,
// FLEET_TELEMETRY_DISABLED, FLEET_ALLOW_MULTIPLE, DO_NOT_TRACK, …) with one
// rule, so a switch means the same thing wherever it is read.
//
// internal/ui's FLEET_FREEZE_ANIM reader deliberately does not use this: it
// treats anything but an explicit "off" value as on.
package envflag

import (
	"os"
	"strings"
)

// IsTruthy reports whether v is a common truthy value: 1, true, yes, y or on,
// case- and whitespace-insensitive. Anything else, including empty, is false.
func IsTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	}
	return false
}

// Set reports whether the named environment variable holds a truthy value.
func Set(name string) bool {
	return IsTruthy(os.Getenv(name))
}
