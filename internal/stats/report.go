// Package stats is fleet's local, never-sent personal analytics: it records
// what happens inside fleet (status changes, attaches, fleet actions) into
// ~/.config/fleet/stats.db, backfills agent turns from the transcripts of
// sessions fleet launched, and computes the Report the Stats screen renders.
//
// Nothing in this package may reach internal/analytics. The recorder writes
// enums and numbers only — never a prompt, title, path or message body.
package stats

import "time"

// Range is the window a Report covers.
type Range int

const (
	Range7d Range = iota
	Range30d
	RangeAll
)

// Ranges is the cycle order of the range chip.
var Ranges = []Range{Range7d, Range30d, RangeAll}

func (r Range) Label() string {
	switch r {
	case Range7d:
		return "7d"
	case Range30d:
		return "30d"
	default:
		return "all"
	}
}

// Report is everything the Stats screen shows for one Range. Computed off the
// Update goroutine; the UI only renders it. Zero values mean "no data", and the
// UI must render them honestly (a dash, not a 0 that reads as a measurement).
type Report struct {
	Range       Range
	From, To    time.Time // the window actually covered
	GeneratedAt time.Time
	// Since is the earliest moment any data exists (first recorded event or
	// earliest backfilled turn). Lets the UI say "since Sep 3".
	Since time.Time

	Hero Hero

	// Concurrency is the number of fleet sessions with an agent turn in flight,
	// sampled into equal-width buckets across [From,To). Each value is the
	// time-weighted MEAN concurrency in that bucket; ConcurrencyPeak is the
	// max within it. Sub-agents are not sessions and never count.
	Concurrency     []float64
	ConcurrencyPeak []int
	// ShareSolo/ShareTwo/ShareThreePlus: fraction of busy time (c>=1) spent at
	// c==1, c==2, c>=3. Sum to 1 when there was any busy time.
	ShareSolo, ShareTwo, ShareThreePlus float64

	// Days is one entry per local calendar day in the heatmap window (always
	// the last 53 weeks ending today, regardless of Range), oldest first.
	Days []Day
	// Streak / BestStreak: consecutive local days with any fleet activity.
	Streak, BestStreak int

	// Activity counts fleet actions in the window, most frequent first.
	Activity []ActionCount

	Records  []Record
	FunFacts []string // already rendered, plain text, <= ~70 cols each
	Repos    []RepoTime
	Agents   []AgentTime
	Coverage Coverage
}

// Hero is the top row of big numbers.
type Hero struct {
	// Parallel is agent-busy time divided by time with >=1 agent busy: "2.7×".
	Parallel float64
	// Peak is the most fleet sessions busy at once, and when.
	Peak   int
	PeakAt time.Time
	// AgentTime is the summed duration of all agent turns.
	AgentTime time.Duration
	// ReplyMedian is the median time from a session needing you (entering
	// waiting or finished) to you attaching to it or it running again. Zero
	// when fewer than MinSamples measurements exist.
	ReplyMedian time.Duration
	ReplyP90    time.Duration
	ReplyN      int
	// CostUSD is the API-list-price equivalent of the tokens used — money you
	// did not pay on a subscription. Zero when no priced tokens.
	CostUSD float64
	Tokens  int64 // input+output+cache, for the fun fact
	// Deltas vs the previous window of the same length; NaN-free, 0 = unknown.
	ParallelPrev  float64
	AgentTimePrev time.Duration
}

// MinSamples is the smallest n a percentile or rate is shown for.
const MinSamples = 5

type Day struct {
	Date      time.Time // local midnight
	Actions   int       // fleet actions + prompts recorded that day
	AgentTime time.Duration
}

type ActionCount struct {
	Action string // stats action enum, e.g. "space_jump"
	Label  string // human label, e.g. "Space jumps"
	N      int
}

type Record struct {
	Label  string // "longest turn"
	Value  string // "47m 12s"
	Detail string // "Sep 14" — never a prompt; a session title is allowed since it's on-screen already
	At     time.Time
}

type RepoTime struct {
	Name      string // repo base name as the sidebar shows it
	AgentTime time.Duration
	Sessions  int
}

type AgentTime struct {
	Agent     string // "claude" | "codex" | "opencode"
	AgentTime time.Duration
	Sessions  int
}

// Coverage tells the UI what the numbers are built from, so it can footnote
// honestly ("turns: Claude sessions only · waiting recorded since Sep 28").
type Coverage struct {
	RecordingSince time.Time // first stats.db event; zero = nothing recorded yet
	TurnsSince     time.Time // earliest backfilled turn
	ClaudeOnly     bool      // turn data comes only from Claude transcripts
	Sessions       int       // fleet sessions included
}

// Progress is reported while the transcript scan runs.
type Progress struct {
	Done, Total int
	Turns       int
	Tokens      int64
	Finished    bool
}

// Recap is the weekly "Your week" reel: the last full Mon–Sun week.
type Recap struct {
	WeekStart time.Time // local Monday 00:00
	Cards     []RecapCard
}

type RecapCard struct {
	Big     string   // "6"
	Caption string   // "agents at the same moment"
	Lines   []string // supporting lines
	Spark   []float64
	// RepoLines indexes the Lines that name repos. A copied card leaves them
	// out unless the user asks: repo names often carry client or ticket names.
	RepoLines []int
}
