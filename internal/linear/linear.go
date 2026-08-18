// Package linear reads Linear issues through Linear's own GraphQL API.
//
// Design rules, in the order they matter:
//
//   - fleet holds exactly one credential and nothing else. It is stored in the
//     OS keychain where there is one, read at request time, and never written
//     into a session's tmux environment, a log line, or a bug report.
//   - The feature is per-repo and opt-out-by-absence: a repo that names no
//     Linear team (via .fleet.json or .linear.toml) behaves exactly as it did
//     before this package existed, even for a connected user.
//   - Nothing here runs on the Bubble Tea Update goroutine or in the status/git
//     workers. Every call is event-driven and one-shot, which is what keeps it
//     clear of workerStallThreshold's budget. The two functions the UI does call
//     synchronously — Available and TeamKeys — touch no network and no keychain.
//
// There is deliberately no `linear` CLI anywhere in here. An earlier version
// shelled out to one, which cost three version-skew bugs in a single session and
// would not have transferred to Jira.
package linear

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/brizzai/fleet/internal/workspace"
)

// Timeouts. Each is sized against a measured cost.
const (
	// metaTimeout bounds the small queries — a lite issue fetch, a search, the
	// workspace read. One GraphQL round trip, measured at ~260ms. 10s is ~40x
	// headroom for a slow link while staying well inside an inference deadline.
	metaTimeout = 10 * time.Second

	// fullTimeout bounds the full issue document (description, comments,
	// labels, workflow states). Same single round trip as metaTimeout, but a
	// much larger payload, so it gets its own budget rather than borrowing one
	// sized for a five-field reply.
	fullTimeout = 20 * time.Second

	// stateTimeout bounds the one mutation fleet ever makes. The workflow states
	// came along with the full fetch, so this really is a single round trip.
	stateTimeout = 15 * time.Second

	// imageFetchTimeout bounds one uploads.linear.app download, sized for
	// maxImageBytes on a poor link.
	imageFetchTimeout = 20 * time.Second
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

	// maxResponseBytes caps a GraphQL reply. An issue with fifty long comments
	// is well under a megabyte; this exists so a wedged or hostile endpoint
	// can't stream unboundedly into memory.
	maxResponseBytes = 8 << 20
)

var (
	// ErrNotConnected means fleet has no Linear credential. This is the resting
	// state for everyone who has not connected, so it is never surfaced as an
	// error — the ticket surfaces simply stay inert.
	ErrNotConnected = errors.New("linear: not connected")

	// ErrNotAuthenticated means the credential was rejected: a revoked key, a
	// typo, or an OAuth token whose refresh failed.
	ErrNotAuthenticated = errors.New("linear: credential rejected")

	// ErrNotFound means there is no such issue. This is an ordinary answer for a
	// branch-inferred identifier, not a failure.
	ErrNotFound = errors.New("linear: issue not found")
)

// Ticket is the projection of an issue that fleet's UI needs.
type Ticket struct {
	Identifier string // "BRZ-3182"; empty means the payload was unusable
	Title      string
	URL        string
	StateName  string
	// StateType is Linear's own category for the state — "started",
	// "unstarted", "backlog", "triage". Carried alongside the name because the
	// name is whatever a team called it ("In Dev", "Doing") and cannot be
	// ordered or compared, while the type can.
	StateType string
}

// Ok reports whether the payload carried enough to be worth acting on.
func (t Ticket) Ok() bool { return t.Identifier != "" }

// linearConfigFile is the `linear` CLI's own per-repo config. fleet does not
// require, write, or depend on that CLI, but reading the team key out of a file
// someone already has costs nothing and makes this zero-touch for them.
//
// Only team_id is read. api_key lives in the same file and is deliberately never
// touched: fleet resolves its own credential and has no business adopting one
// left there for another tool.
const linearConfigFile = ".linear.toml"

var teamIDRe = regexp.MustCompile(`(?m)^\s*team_id\s*=\s*["']([A-Za-z][A-Za-z0-9]*)["']`)

// TeamKeys returns the Linear team keys this repo tracks, or nil if it tracks
// none.
//
// Nil is the answer that keeps an unrelated repo silent for a connected user, so
// there is deliberately NO fallback to "every team in the workspace": that would
// put Linear suggestions under the branch field of every repo on the machine.
//
// Free and non-blocking — two small file reads, no network — because the branch
// inference path calls it from the Update goroutine.
func TeamKeys(repoPath string) []string {
	if repoPath == "" {
		return nil
	}
	if keys := workspace.LinearTeamKeys(repoPath); len(keys) > 0 {
		return keys
	}
	data, err := readFileLimited(filepath.Join(repoPath, linearConfigFile), 64<<10)
	if err != nil {
		return nil
	}
	m := teamIDRe.FindSubmatch(data)
	if m == nil {
		return nil
	}
	return []string{strings.ToUpper(string(m[1]))}
}

// readFileLimited reads at most limit bytes, so a pathological config file
// can't be pulled into memory whole.
func readFileLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}
