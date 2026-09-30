package stats

import "strings"

// price is Anthropic first-party API list price in USD per million tokens.
// Cache writes are derived from Input (1.25× for the 5-minute TTL, 2× for the
// 1-hour TTL Claude Code uses); cache reads are listed per model because they
// are not a fixed multiple on every model (Fable 5.1 and Opus 5.5 differ).
type price struct {
	Input, Output, CacheRead float64
}

// prices is keyed by model-id prefix; the longest matching prefix wins, so
// "claude-opus-4" (the 4.0/4.1 rate) never shadows "claude-opus-4-8".
// Source: Anthropic's published pricing as of 2026-09.
var prices = map[string]price{
	"claude-fable-5-1":  {10, 50, 0.25},
	"claude-mythos-5-1": {10, 50, 0.25},
	"claude-fable-5":    {10, 50, 1.00},
	"claude-mythos-5":   {10, 50, 1.00},
	"claude-opus-5-5":   {4, 20, 0.20},
	"claude-opus-5":     {5, 25, 0.50},
	"claude-opus-4-8":   {5, 25, 0.50},
	"claude-opus-4-7":   {5, 25, 0.50},
	"claude-opus-4-6":   {5, 25, 0.50},
	"claude-opus-4-5":   {5, 25, 0.50},
	"claude-opus-4":     {15, 75, 1.50}, // Opus 4 / 4.1
	"claude-sonnet-5":   {2, 10, 0.20},
	"claude-sonnet-4":   {3, 15, 0.30}, // Sonnet 4 / 4.5 / 4.6
	"claude-haiku-4-5":  {1, 5, 0.10},
	"claude-3-7-sonnet": {3, 15, 0.30},
	"claude-3-5-sonnet": {3, 15, 0.30},
	"claude-3-5-haiku":  {0.80, 4, 0.08},
}

// normalizeModel strips a context-window suffix ("claude-opus-5[1m]") and
// lowercases. It does not map aliases like "opus": those are not model ids
// and pricing them would be a guess.
func normalizeModel(m string) string {
	m = strings.ToLower(strings.TrimSpace(m))
	if i := strings.IndexByte(m, '['); i >= 0 {
		m = m[:i]
	}
	return m
}

func priceFor(model string) (price, bool) {
	m := normalizeModel(model)
	best, bestLen := price{}, 0
	for prefix, p := range prices {
		if len(prefix) > bestLen && strings.HasPrefix(m, prefix) {
			// Require a boundary so "claude-opus-5" doesn't claim a
			// hypothetical "claude-opus-50".
			if len(m) == len(prefix) || m[len(prefix)] == '-' || m[len(prefix)] == '@' {
				best, bestLen = p, len(prefix)
			}
		}
	}
	return best, bestLen > 0
}

// turnCost is the list-price USD of one turn's tokens; ok is false for a
// model the table doesn't know (its tokens still count, its dollars don't).
func turnCost(model string, in, out, cacheRead, cacheWrite5m, cacheWrite1h int64) (float64, bool) {
	p, ok := priceFor(model)
	if !ok {
		return 0, false
	}
	const m = 1e6
	return float64(in)*p.Input/m +
		float64(out)*p.Output/m +
		float64(cacheRead)*p.CacheRead/m +
		float64(cacheWrite5m)*p.Input*1.25/m +
		float64(cacheWrite1h)*p.Input*2/m, true
}
