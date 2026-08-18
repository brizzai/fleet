package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/debuglog"
)

// linear failures are one-shot rather than polled, so this throttle isn't
// stopping a flood the way trackGHFailure's is — it stops a user who creates ten
// worktrees against a broken CLI from emitting ten identical events. reason is a
// low-cardinality label, never an issue identifier or a path.
const failTrackInterval = 10 * time.Minute

var (
	failMu   sync.Mutex
	failLast time.Time
)

func trackFailure(reason string) {
	failMu.Lock()
	if !failLast.IsZero() && time.Since(failLast) < failTrackInterval {
		failMu.Unlock()
		return
	}
	failLast = time.Now()
	failMu.Unlock()
	analytics.Track(analytics.EventLinearCommandFailure, map[string]any{
		"reason": reason,
	})
}

// classifyError maps the CLI's stderr onto a sentinel. Unknown stderr returns
// nil, so callers fall through to their own generic handling.
func classifyError(stderr string) error {
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "no api token configured"), strings.Contains(s, "not authenticated"):
		return ErrNotConfigured
	case strings.Contains(s, "401"), strings.Contains(s, "unauthorized"),
		strings.Contains(s, "authentication failed"), strings.Contains(s, "invalid api key"):
		return ErrNotAuthenticated
	case strings.Contains(s, "entity not found"), strings.Contains(s, "could not find issue"),
		strings.Contains(s, "does not contain a valid linear issue id"):
		return ErrNotFound
	}
	return nil
}

// run executes `linear <args>` in dir under timeout and returns stdout.
//
// dir must be inside the repo: the CLI locates .linear.toml by shelling
// `git rev-parse --show-toplevel` in its OWN working directory, so a wrong dir
// loses the team/workspace context with no error.
//
// The environment additions are load-bearing:
//   - LINEAR_DOWNLOAD_IMAGES=1 outranks a repo that set download_images = false
//     in .linear.toml (CLI precedence is flag > env > toml).
//   - PAGER=cat and NO_COLOR=1 guard against a pager or SGR codes if this ever
//     runs somewhere with a TTY attached.
func run(ctx context.Context, timeout time.Duration, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "linear", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LINEAR_DOWNLOAD_IMAGES=1", "PAGER=cat", "NO_COLOR=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}

	// A timeout must be distinguishable from a real CLI error: on the deadline
	// cmd.Output() errors with EMPTY stderr, which would otherwise classify as
	// "unknown" and be swallowed. Check the context, not the returned error —
	// cmd.Output() returns *exec.ExitError on kill, so errors.Is on err alone
	// would not match. Wrap with %w so errors.Is works for our callers.
	if ctx.Err() == context.DeadlineExceeded {
		debuglog.Logger.Debug("linear: timed out", "args", args, "dir", dir)
		trackFailure("timeout")
		return nil, fmt.Errorf("linear %s timed out: %w", args[0], ctx.Err())
	}

	msg := strings.TrimSpace(stderr.String())
	if classified := classifyError(msg); classified != nil {
		debuglog.Logger.Debug("linear: classified failure", "args", args, "err", classified, "stderr", msg)
		trackFailure(strings.TrimPrefix(classified.Error(), "linear: "))
		return nil, classified
	}
	debuglog.Logger.Debug("linear: command failed", "args", args, "dir", dir, "stderr", msg)
	return nil, fmt.Errorf("linear %s: %w (%s)", args[0], err, truncate(msg, 200))
}

// ticketJSON mirrors the fields fleet uses. Both the v1.7.0 (6-key) and v2.5.0
// (15-key) payloads decode into it; extra keys are ignored by encoding/json,
// and a missing or null `state` leaves StateName empty rather than failing.
type ticketJSON struct {
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	State      *struct {
		Name string `json:"name"`
	} `json:"state"`
}

// Fetch returns the issue's metadata.
//
// This is the ONLY place --json is used. It must never be used for images: the
// CLI returns from the JSON branch before its image downloader runs, so a JSON
// fetch emits raw uploads.linear.app URLs and writes nothing to disk.
func Fetch(ctx context.Context, dir, id string) (Ticket, error) {
	if !Available() {
		return Ticket{}, ErrNotInstalled
	}
	out, err := run(ctx, metaTimeout, dir, "issue", "view", id, "--json", "--no-pager")
	if err != nil {
		return Ticket{}, err
	}
	var raw ticketJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		debuglog.Logger.Debug("linear: JSON parse failed", "id", id, "error", err)
		return Ticket{}, ErrNotFound
	}
	if raw.Identifier == "" {
		return Ticket{}, ErrNotFound
	}
	t := Ticket{Identifier: raw.Identifier, Title: raw.Title, URL: raw.URL}
	if raw.State != nil {
		t.StateName = raw.State.Name
	}
	return t, nil
}

// Search returns issues matching a full-text term, for the worktree dialog's
// suggestion list.
//
// `issue query` (CLI v2+) is the right command: `issue list` is an alias of
// `issue mine` and only ever returns your own issues, which would hide a ticket
// someone just handed you. On an older CLI this errors, and the caller simply
// shows no suggestions — the identifier path keeps working either way.
func Search(ctx context.Context, dir, teamKey, term string, limit int) ([]Ticket, error) {
	if !Available() {
		return nil, ErrNotInstalled
	}
	args := []string{"issue", "query", "--search", term, "--json", "--no-pager"}
	if teamKey != "" {
		args = append(args, "--team", teamKey)
	}
	if limit > 0 {
		args = append(args, "--limit", strconv.Itoa(limit))
	}
	out, err := run(ctx, metaTimeout, dir, args...)
	if err != nil {
		return nil, err
	}
	return decodeTicketList(out), nil
}

// decodeTicketList is deliberately tolerant: the CLI's JSON shape changed in
// v2.0.0 to preserve GraphQL connection shapes, so accept both a bare array and
// an object wrapping one, and drop anything unusable rather than failing.
func decodeTicketList(out []byte) []Ticket {
	var flat []ticketJSON
	if err := json.Unmarshal(out, &flat); err != nil {
		var wrapped struct {
			Nodes  []ticketJSON `json:"nodes"`
			Issues []ticketJSON `json:"issues"`
		}
		if err := json.Unmarshal(out, &wrapped); err != nil {
			return nil
		}
		flat = wrapped.Nodes
		if len(flat) == 0 {
			flat = wrapped.Issues
		}
	}
	var tickets []Ticket
	for _, raw := range flat {
		if raw.Identifier == "" {
			continue
		}
		t := Ticket{Identifier: raw.Identifier, Title: raw.Title, URL: raw.URL}
		if raw.State != nil {
			t.StateName = raw.State.Name
		}
		tickets = append(tickets, t)
	}
	return tickets
}

// fetchMarkdown returns the issue rendered as markdown, with image links
// already rewritten to absolute local paths for every image the CLI managed to
// download.
//
// Deliberately NOT --json. Two reasons, and both are structural rather than
// bugs that might get fixed: the JSON branch returns before the downloader
// runs, and the link substitution happens after it. Under a pipe (which
// cmd.Output() gives us) the CLI skips its ANSI renderer and pager and prints
// raw markdown, which is exactly what we want to parse.
//
// A link left pointing at uploads.linear.app means the CLI's download failed
// and swallowed the error — that is the detector for the broken v1.7.0 build,
// and the caller fetches those itself.
func fetchMarkdown(ctx context.Context, dir, id string) ([]byte, error) {
	if !Available() {
		return nil, ErrNotInstalled
	}
	return run(ctx, markdownTimeout, dir, "issue", "view", id, "--no-pager")
}

// MoveToStarted moves the issue into the team's first started workflow state
// and returns the resulting state name.
//
// `-s started` matches on state TYPE against a position-sorted list, which is
// the same resolution `linear issue start` uses — so fleet never enumerates or
// caches workflow states, and this works with teams whose started state is
// called "In Dev" or anything else. (A state literally named "started" would
// win the name match first, which is arguably what its author intended.)
//
// `linear issue start` is deliberately NOT used: it also creates its own git
// branch, which would collide with the worktree fleet just made.
func MoveToStarted(ctx context.Context, dir, id string) (string, error) {
	if !Available() {
		return "", ErrNotInstalled
	}
	if _, err := run(ctx, stateTimeout, dir, "issue", "update", id, "-s", "started"); err != nil {
		trackFailure("state_write_failed")
		return "", err
	}
	return "started", nil
}

// authToken returns the CLI's API token, for the fallback image download only.
//
// Held in a local for the duration of one request and never logged, persisted,
// or placed into a session's tmux environment. fleet storing a credential is
// precisely what this package's design avoids; borrowing one for a single
// authenticated GET is not the same thing, but it is close enough to deserve
// saying out loud.
func authToken(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, authTimeout, dir, "auth", "token")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func readFileLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}
