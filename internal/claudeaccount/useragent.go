package claudeaccount

import (
	"strings"
	"sync"
)

// Fleet identifies itself to Anthropic as fleet, not as claude-code.
//
// Measured 2026-08-04: the messages endpoint serves `fleet/<version>` exactly
// as it serves `claude-code/<version>`, and serves a request carrying no
// User-Agent at all. There was never anything to buy by misrepresenting the
// client, so fleet doesn't.
//
// (Community tools split on this. claude-swap, the most widely used, also
// sends its own name.)
var (
	uaMu      sync.RWMutex
	userAgent = "fleet/dev"
)

// SetUserAgent records fleet's version for outbound requests. Called once at
// startup; tests can leave it at the default.
func SetUserAgent(version string) {
	uaMu.Lock()
	defer uaMu.Unlock()
	userAgent = "fleet/" + strings.TrimPrefix(version, "v")
}

func fleetUserAgent() string {
	uaMu.RLock()
	defer uaMu.RUnlock()
	return userAgent
}
