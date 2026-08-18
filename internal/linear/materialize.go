package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/git"
)

// Layout inside a worktree. Kept narrow (.fleet/ticket/, not .fleet/) because
// JetBrains Fleet owns .fleet/ in project roots and a repo may legitimately
// commit .fleet/settings.json.
const (
	fleetDir    = ".fleet"
	ticketDir   = "ticket"
	imagesDir   = "images"
	ticketFile  = "ticket.md"
	promptFile  = "prompt.txt"
	metaFile    = "meta.json"
	noTicketPin = ".no-ticket"
)

// Result describes what a Materialize call put on disk.
type Result struct {
	Ticket

	Dir    string // absolute: <worktree>/.fleet/ticket/BRZ-3182
	RelDir string // ".fleet/ticket/BRZ-3182" — what the prompt shows the agent
	Prompt string // the seeded first message; "" when nothing usable was written

	Images        int
	ImagesDropped int
	UsedFallback  bool   // fleet downloaded images the CLI failed to fetch
	StateMoved    string // resulting state name; "" if not attempted or failed
}

// Opts configures one materialization.
type Opts struct {
	// RepoDir is any directory inside the repo — the CLI locates .linear.toml
	// by shelling `git rev-parse --show-toplevel` in its own cwd, so this must
	// not be empty and must not point outside the checkout.
	RepoDir      string
	WorktreePath string
	Identifier   string

	// Ticket, when already fetched (the worktree dialog pays that ~0.5s round
	// trip while the user is still looking at the form), skips the metadata call.
	Ticket Ticket

	// MoveState requests the one mutation fleet ever makes.
	MoveState bool
}

// inFlight stops two rapid session creations in the same worktree from both
// materializing — and, worse, both moving the issue's state.
var inFlight sync.Map // worktreePath -> struct{}

// meta is the on-disk ledger. Its job is to make the state write exactly-once
// across fleet restarts: re-running it after a human moved the issue to In
// Review would silently drag it backwards, which is the single worst thing this
// feature could do.
type meta struct {
	Identifier string    `json:"identifier"`
	FetchedAt  time.Time `json:"fetched_at"`
	Images     int       `json:"images"`
	StateWrite string    `json:"state_write"` // "done" | "skipped" | "failed"
}

// TicketDir returns where a ticket materializes inside a worktree.
func TicketDir(worktreePath, id string) string {
	return filepath.Join(worktreePath, fleetDir, ticketDir, strings.ToUpper(id))
}

// ExistingPrompt returns a previously materialized prompt for this worktree.
//
// This is the fast path and the steady state: every session after the first in
// a ticket worktree hits it, at the cost of one ReadDir and one ReadFile, with
// no subprocess and no network. The filesystem is the ledger — it survives
// restarts, survives losing state.db, and a user who deletes the directory gets
// a re-fetch, which is the natural "refresh this ticket" gesture.
func ExistingPrompt(worktreePath string) (string, bool) {
	base := filepath.Join(worktreePath, fleetDir, ticketDir)
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(base, e.Name(), promptFile))
		if err == nil && len(data) > 0 {
			return string(data), true
		}
	}
	return "", false
}

// NegativelyPinned reports whether this worktree's branch was already resolved
// to "no such issue", so inference does not re-ask on every session start.
func NegativelyPinned(worktreePath, id string) bool {
	data, err := os.ReadFile(filepath.Join(worktreePath, fleetDir, ticketDir, noTicketPin))
	return err == nil && strings.EqualFold(strings.TrimSpace(string(data)), id)
}

func pinNoTicket(worktreePath, id string) {
	dir := filepath.Join(worktreePath, fleetDir, ticketDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, noTicketPin), []byte(id), 0644)
}

// Materialize fetches a ticket and writes it, with its screenshots, into the
// worktree.
//
// Failure posture matches its neighbours in the creation path
// (copyClaudeSettingsFile, workspace.CopyConfiguredFiles): once the worktree
// exists, nothing here may fail the caller. The returned error is advisory —
// log it, surface one line, and start the session anyway. The one hard rule is
// that Result.Prompt stays empty unless ticket.md verifiably exists: a pointer
// at a file that isn't there is worse than no pointer.
func Materialize(ctx context.Context, o Opts) (Result, error) {
	var res Result

	if !Available() {
		return res, ErrNotInstalled
	}
	if o.WorktreePath == "" || o.Identifier == "" {
		return res, fmt.Errorf("linear: materialize needs a worktree and an identifier")
	}
	id := strings.ToUpper(o.Identifier)

	if _, busy := inFlight.LoadOrStore(o.WorktreePath, struct{}{}); busy {
		return res, fmt.Errorf("linear: already materializing %s", o.WorktreePath)
	}
	defer inFlight.Delete(o.WorktreePath)

	repoDir := o.RepoDir
	if repoDir == "" {
		repoDir = o.WorktreePath
	}

	// Metadata first: it is the cheap call, and it is what tells us the ticket
	// exists at all before we create directories for it.
	t := o.Ticket
	if !t.Ok() {
		fetched, err := Fetch(ctx, repoDir, id)
		if err != nil {
			if err == ErrNotFound {
				pinNoTicket(o.WorktreePath, id)
			}
			return res, err
		}
		t = fetched
	}
	res.Ticket = t
	res.Identifier = t.Identifier

	dir := TicketDir(o.WorktreePath, res.Identifier)
	res.Dir = dir
	res.RelDir = filepath.Join(fleetDir, ticketDir, res.Identifier)

	// Exclude BEFORE writing a single byte. A window where the files exist and
	// the exclude does not is a window where `git add -A` sweeps a customer
	// screenshot into a commit.
	if err := git.AddFleetExclude(o.WorktreePath); err != nil {
		debuglog.Logger.Warn("linear: could not exclude .fleet from git — ticket files are stageable",
			"worktree", o.WorktreePath, "error", err)
	}

	imgDir := filepath.Join(dir, imagesDir)
	if err := os.MkdirAll(imgDir, 0755); err != nil {
		return res, fmt.Errorf("create %s: %w", imgDir, err)
	}

	// The markdown pass, never --json: the JSON branch returns before the CLI's
	// image downloader runs.
	markdown, err := fetchMarkdown(ctx, repoDir, res.Identifier)
	if err != nil {
		return res, err
	}

	body, images, dropped, fallback := o.collectImages(ctx, repoDir, markdown, imgDir)
	res.Images, res.ImagesDropped, res.UsedFallback = images, dropped, fallback

	if images == 0 {
		_ = os.Remove(imgDir) // only succeeds when empty, which is what we want
	}

	if err := os.WriteFile(filepath.Join(dir, ticketFile), renderTicketFile(res, body), 0644); err != nil {
		return res, fmt.Errorf("write %s: %w", ticketFile, err)
	}

	res.Prompt = SeedPrompt(res)
	if err := os.WriteFile(filepath.Join(dir, promptFile), []byte(res.Prompt), 0644); err != nil {
		// Non-fatal: the prompt is still returned in-memory for this session,
		// it just won't be reused by the next one.
		debuglog.Logger.Debug("linear: could not persist prompt.txt", "error", err)
	}

	m := meta{Identifier: res.Identifier, FetchedAt: time.Now(), Images: images, StateWrite: "skipped"}
	if o.MoveState {
		if name, err := MoveToStarted(ctx, repoDir, res.Identifier); err != nil {
			m.StateWrite = "failed"
			debuglog.Logger.Warn("linear: could not move issue to started", "id", res.Identifier, "error", err)
		} else {
			m.StateWrite = "done"
			res.StateMoved = name
		}
	}
	writeMeta(dir, m)

	analytics.Track(analytics.EventLinearTicketMaterialized, map[string]any{
		"images":   images,
		"dropped":  dropped,
		"fallback": fallback,
	})
	return res, nil
}

// collectImages copies every image the markdown references into imgDir and
// rewrites the links to the relative paths the agent will read.
//
// Both halves are required and neither is sufficient alone: an absolute
// $TMPDIR path is outside the project root (so an agent's read prompts for
// permission, or the file has been purged), and an extensionless filename
// defeats extension dispatch. Fix one and the agent still sees nothing.
func (o Opts) collectImages(ctx context.Context, repoDir string, markdown []byte, imgDir string) (body string, kept, dropped int, usedFallback bool) {
	refs := findImages(markdown)
	body = string(markdown)

	var token string
	var tokenTried bool
	var total int64

	for i, ref := range refs {
		if kept >= maxImages || total >= maxTotalBytes {
			dropped++
			continue
		}

		var name string
		var size int64
		var err error

		if ref.remote {
			// The CLI's downloader failed and swallowed the error — the
			// signature of the v1.7.0 build compiled without
			// --allow-net=uploads.linear.app. Fetch it ourselves.
			if !tokenTried {
				tokenTried = true
				token, _ = authToken(ctx, repoDir)
			}
			if token == "" {
				dropped++
				continue
			}
			name, size, err = fetchRemoteImage(ctx, ref.target, token, imgDir, ref.alt, i+1)
			if err == nil {
				usedFallback = true
			}
		} else {
			name, size, err = copyLocalImage(ref.target, imgDir, i+1)
		}

		if err != nil {
			debuglog.Logger.Debug("linear: image unavailable", "target", ref.target, "error", err)
			dropped++
			continue
		}

		kept++
		total += size
		body = strings.ReplaceAll(body, ref.target, filepath.Join(imagesDir, name))
	}
	return body, kept, dropped, usedFallback
}

// renderTicketFile writes front matter carrying the two things the markdown
// pass does not emit — the URL and the state — plus honest provenance, so a
// reader (human or agent) knows this is a snapshot rather than live state.
func renderTicketFile(r Result, body string) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "ticket: %s\n", r.Identifier)
	if r.URL != "" {
		fmt.Fprintf(&b, "url: %s\n", r.URL)
	}
	if r.StateName != "" {
		fmt.Fprintf(&b, "state_when_fetched: %s\n", r.StateName)
	}
	fmt.Fprintf(&b, "fetched_at: %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "fetched_by: fleet, via `linear issue view %s`\n", r.Identifier)
	fmt.Fprintf(&b, "images: %d\n", r.Images)
	b.WriteString("---\n\n")
	b.WriteString("<!-- Read-only snapshot. The live ticket may have moved on since fetched_at. -->\n\n")
	b.WriteString(strings.TrimSpace(body))
	b.WriteString("\n")
	if r.ImagesDropped > 0 {
		fmt.Fprintf(&b, "\n> %d further image(s) were not downloaded: over fleet's per-ticket cap, "+
			"or not a readable image.\n", r.ImagesDropped)
	}
	if r.Images == 0 {
		b.WriteString("\n> No images were downloaded. If this ticket has screenshots, your `linear` " +
			"CLI may be too old to fetch them — `brew upgrade schpet/tap/linear`.\n")
	}
	return []byte(b.String())
}

func writeMeta(dir string, m meta) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, metaFile), data, 0644)
}
