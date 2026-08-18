// Package linear reads Linear issues by shelling out to the `linear` CLI
// (github.com/schpet/linear-cli), the same way internal/github shells out to
// `gh` for PR badges.
//
// Design rules, in the order they matter:
//
//   - fleet stores no credential. The CLI owns auth (LINEAR_API_KEY, or api_key
//     in .linear.toml). We never read that file's api_key, never persist a
//     token, and never forward one into a session's tmux environment.
//   - The feature is per-repo and opt-out-by-absence: no `linear` on PATH, or no
//     .linear.toml at the repo root, and every entry point here is inert.
//   - Nothing in this package runs on the Bubble Tea Update goroutine or in the
//     status/git workers. Every call is event-driven and one-shot, which is what
//     keeps it clear of workerStallThreshold's budget.
package linear

import (
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Timeouts. Each is sized against a measured cost, in the style of ghTimeout.
const (
	// metaTimeout bounds `linear issue view --json`: one GraphQL round trip,
	// measured at ~0.5s. 10s is 20x headroom for a slow link while staying small
	// enough that a wedged metadata call still leaves budget for the markdown
	// pass inside an inference deadline.
	metaTimeout = 10 * time.Second

	// markdownTimeout bounds the markdown pass, which is one GraphQL round trip
	// PLUS N image downloads the CLI performs sequentially. At the image cap and
	// a few seconds each that is tens of seconds; 45s clears it. This never runs
	// on the status worker, so it cannot interact with workerStallThreshold.
	markdownTimeout = 45 * time.Second

	// stateTimeout bounds `linear issue update -s started`: a workflow-states
	// query then an issueUpdate mutation — two round trips, the shape ghTimeout
	// was sized for.
	stateTimeout = 15 * time.Second

	// authTimeout bounds `linear auth token`, which only reads local config and
	// env. Past a few seconds it is wedged, not slow.
	authTimeout = 5 * time.Second

	// imageFetchTimeout bounds one fallback download of an uploads.linear.app
	// asset that the CLI failed to fetch. Sized for maxImageBytes on a poor link.
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
)

var (
	// ErrNotInstalled means the `linear` binary is not on PATH. Never surfaced
	// as an error to a user who did not ask for Linear.
	ErrNotInstalled = errors.New("linear: CLI not installed")

	// ErrNotConfigured means the CLI found no API token.
	ErrNotConfigured = errors.New("linear: no API token configured")

	// ErrNotAuthenticated means the token was rejected.
	ErrNotAuthenticated = errors.New("linear: API token rejected")

	// ErrNotFound means there is no such issue. This is an ordinary answer for a
	// branch-inferred identifier, not a failure.
	ErrNotFound = errors.New("linear: issue not found")
)

// Ticket is the version-defensive projection of `linear issue view --json`.
//
// Every field is optional on purpose: CLI v1.7.0 returns six keys and v2.5.0
// returns fifteen, and the JSON shape changed in v2.0.0 to preserve GraphQL
// field names. A field that moves or disappears must degrade to a zero value,
// never to an error.
type Ticket struct {
	Identifier string // "BRZ-3182"; empty means the payload was unusable
	Title      string
	URL        string
	StateName  string
}

// Ok reports whether the payload carried enough to be worth acting on.
func (t Ticket) Ok() bool { return t.Identifier != "" }

// Available reports whether the `linear` CLI is installed.
//
// Deliberately exec.LookPath and not `linear --version`: this is called on the
// path that opens the worktree dialog, so it must cost microseconds.
func Available() bool {
	_, err := exec.LookPath("linear")
	return err == nil
}

// configFile is the CLI's own per-repo config. Its presence is fleet's signal
// that a repo is Linear-connected — better than a global setting, because it is
// per-repo and already true for anyone using the CLI seriously.
const configFile = ".linear.toml"

var teamIDRe = regexp.MustCompile(`(?m)^\s*team_id\s*=\s*["']([A-Za-z][A-Za-z0-9]*)["']`)

// TeamKey returns the team identifier (e.g. "BRZ") from .linear.toml at
// repoPath, and whether the repo is Linear-connected at all.
//
// Only team_id is read. api_key lives in the same file and is deliberately not
// touched: fleet holding a credential is exactly what this design avoids.
func TeamKey(repoPath string) (string, bool) {
	data, err := readFileLimited(filepath.Join(repoPath, configFile), 64<<10)
	if err != nil {
		return "", false
	}
	m := teamIDRe.FindSubmatch(data)
	if m == nil {
		return "", false
	}
	return strings.ToUpper(string(m[1])), true
}
