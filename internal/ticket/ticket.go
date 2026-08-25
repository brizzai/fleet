// Package ticket is fleet's tracker-agnostic ticket layer: the types every
// provider speaks, and the machinery none of them should own a copy of.
//
// Design rules, in the order they matter:
//
//   - fleet holds one credential per provider and nothing else. Each is stored
//     in the OS keychain where there is one, read at request time, and never
//     written into a session's tmux environment, a log line, or a bug report.
//   - The feature is per-repo and opt-out-by-absence: a repo that names no
//     team and no project behaves exactly as it did before any of this existed,
//     even for a connected user.
//   - Nothing here runs on the Bubble Tea Update goroutine or in the status/git
//     workers. Every call is event-driven and one-shot, which is what keeps it
//     clear of workerStallThreshold's budget. The handful of functions the UI
//     does call synchronously — Available, Resolved, Keys — touch no network
//     and no keychain.
//
// There is deliberately no tracker CLI anywhere in here, for Linear or for
// Jira. An earlier version shelled out to one, which cost three version-skew
// bugs in a single session; TestNoTicketSubprocess is an allowlist of the three
// OS helpers fleet may run, so a new subprocess has to be added deliberately.
package ticket

import (
	"context"
	"errors"
	"sort"
	"time"
)

// Timeouts. Each is sized against a measured cost.
const (
	// MetaTimeout bounds the small queries — a lite issue fetch, a search, the
	// account read. One round trip, measured at ~260ms against Linear. 10s is
	// ~40x headroom for a slow link while staying well inside an inference
	// deadline.
	MetaTimeout = 10 * time.Second

	// FullTimeout bounds the full issue document (description, comments,
	// labels, workflow states). Same single round trip as MetaTimeout, but a
	// much larger payload, so it gets its own budget rather than borrowing one
	// sized for a five-field reply.
	FullTimeout = 20 * time.Second

	// StateTimeout bounds the one mutation fleet ever makes.
	StateTimeout = 15 * time.Second

	// ImageFetchTimeout bounds one attachment download, sized for maxImageBytes
	// on a poor link.
	ImageFetchTimeout = 20 * time.Second
)

// Caps on what a single ticket may drag into a worktree.
//
// The binding constraint is the agent's context, not disk: a dozen screenshots
// is already a large vision payload before it has read a line of code, and a
// ticket with more than that is a design document that wants a human summary.
const (
	maxImages     = 12
	maxImageBytes = 8 << 20
	maxTotalBytes = 32 << 20

	// MaxResponseBytes caps an API reply. An issue with fifty long comments is
	// well under a megabyte; this exists so a wedged or hostile endpoint can't
	// stream unboundedly into memory.
	MaxResponseBytes = 8 << 20
)

var (
	// ErrNotConnected means fleet has no credential for this provider. This is
	// the resting state for everyone who has not connected, so it is never
	// surfaced as an error — the ticket surfaces simply stay inert.
	ErrNotConnected = errors.New("not connected")

	// ErrNotAuthenticated means the credential was rejected: a revoked key, a
	// typo, or an OAuth token whose refresh failed.
	ErrNotAuthenticated = errors.New("credential rejected")

	// ErrNotFound means there is no such issue.
	//
	// The sentinels carry no provider prefix on purpose. They used to read
	// "linear: issue not found", which the UI then rendered as
	// "Linear: linear: issue not found" — so every caller had to hand-word the
	// message instead of formatting the error. The provider name belongs to
	// whoever is drawing the line, not to the error.
	ErrNotFound = errors.New("issue not found")
)

// Priority is a ticket's urgency, normalized across providers.
//
// The numbering is Linear's, deliberately: 0 means "not set" rather than "most
// important", which is why Rank exists and why the raw value can never be
// sorted on. Keeping the numbers identical also means the palette's priority
// gauge, which switches on 1..4, needs no translation layer.
type Priority int

const (
	PriorityNone   Priority = 0
	PriorityUrgent Priority = 1
	PriorityHigh   Priority = 2
	PriorityMedium Priority = 3
	PriorityLow    Priority = 4
)

// Rank orders priorities most-urgent-first, with unset sorting last — which is
// what "not set" should mean in a queue.
func (p Priority) Rank() int {
	switch p {
	case PriorityUrgent:
		return 0
	case PriorityHigh:
		return 1
	case PriorityMedium:
		return 2
	case PriorityLow:
		return 3
	}
	return 4
}

// Name is the word that goes in ticket.md's front matter. Unset is "", which is
// not worth a line.
func (p Priority) Name() string {
	switch p {
	case PriorityUrgent:
		return "urgent"
	case PriorityHigh:
		return "high"
	case PriorityMedium:
		return "medium"
	case PriorityLow:
		return "low"
	}
	return ""
}

// StateType is a workflow state's category, normalized across providers.
//
// Carried alongside the state's name because the name is whatever a team called
// it ("In Dev", "Doing") and cannot be ordered or compared, while the category
// can.
type StateType int

const (
	StateStarted StateType = iota
	StateUnstarted
	StateTriage
	StateBacklog
	StateOther
)

// Rank orders state categories by how close the work is to your hands.
func (s StateType) Rank() int {
	switch s {
	case StateStarted, StateUnstarted, StateTriage, StateBacklog:
		return int(s)
	}
	return int(StateOther)
}

// Ticket is the projection of an issue that fleet's UI needs.
type Ticket struct {
	// Provider is the Kind of the provider that produced this ticket, so a
	// caller holding a merged list still knows who to route back to.
	Provider string

	Identifier string // "BRZ-3182"; empty means the payload was unusable
	Title      string
	URL        string
	StateName  string
	State      StateType
	Priority   Priority

	// StatePos orders two states of the same category — the difference between
	// "In Progress" and "In Review" leading the list. A provider with no such
	// notion supplies 0 and the tiebreak falls through to priority.
	StatePos float64
}

// Ok reports whether the payload carried enough to be worth acting on.
func (t Ticket) Ok() bool { return t.Identifier != "" }

// Account is the connected workspace or site, for display and for telling the
// user which keys they can put in .fleet.json.
type Account struct {
	Name string   // "Brizz" / "acme.atlassian.net"
	Keys []string // Linear team keys / Jira project keys, upper-cased and sorted
}

// SortAssigned applies the ordering the tickets tab depends on.
//
// State category, then position within it, then priority, with whatever order
// the caller was given surviving as the stable tiebreak — which for every
// provider here is "most recently updated". Recency has to remain underneath
// priority: most of a real backlog shares one priority, and priority alone
// would leave that block reordering itself between opens for no visible reason.
func SortAssigned(ts []Ticket) {
	sort.SliceStable(ts, func(a, b int) bool {
		if ra, rb := ts[a].State.Rank(), ts[b].State.Rank(); ra != rb {
			return ra < rb
		}
		if pa, pb := ts[a].StatePos, ts[b].StatePos; pa != pb {
			return pa < pb
		}
		return ts[a].Priority.Rank() < ts[b].Priority.Rank()
	})
}

// ContextWithTimeout is the package's one place for a detached deadline, so the
// helpers that must not inherit a caller's cancellation all read the same.
func ContextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
