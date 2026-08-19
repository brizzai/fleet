package linear

import (
	"context"
	"encoding/json"
	"errors"
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
	StateMoved    string // resulting state name; "" if not attempted or failed
}

// Opts configures one materialization.
type Opts struct {
	WorktreePath string
	Identifier   string

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
	MovedTo    string    `json:"moved_to,omitempty"`
}

// TicketDir returns where a ticket materializes inside a worktree.
func TicketDir(worktreePath, id string) string {
	return filepath.Join(worktreePath, fleetDir, ticketDir, strings.ToUpper(id))
}

// ExistingPrompt returns a previously materialized prompt for this worktree.
//
// This is the fast path and the steady state: every session after the first in
// a ticket worktree hits it, at the cost of one ReadDir and one ReadFile, with
// no network. The filesystem is the ledger — it survives
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

	if o.WorktreePath == "" || o.Identifier == "" {
		return res, fmt.Errorf("linear: materialize needs a worktree and an identifier")
	}
	id := strings.ToUpper(o.Identifier)

	if _, busy := inFlight.LoadOrStore(o.WorktreePath, struct{}{}); busy {
		return res, fmt.Errorf("linear: already materializing %s", o.WorktreePath)
	}
	defer inFlight.Delete(o.WorktreePath)

	// One round trip for everything: description, comments, labels, and the
	// team's workflow states so the optional state write needs no second query.
	issue, err := fetchFull(ctx, id)
	if err != nil {
		// errors.Is, not ==: a wrapped sentinel would skip the negative pin and
		// make inference re-ask Linear on every session start — the exact cost
		// NegativelyPinned exists to avoid.
		if errors.Is(err, ErrNotFound) {
			pinNoTicket(o.WorktreePath, id)
		}
		return res, err
	}

	res.Ticket = issue.ticket()
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

	body, images, dropped := collectImages(ctx, renderBody(issue), imgDir)
	res.Images, res.ImagesDropped = images, dropped

	if images == 0 {
		_ = os.Remove(imgDir) // only succeeds when empty, which is what we want
	}

	if err := os.WriteFile(filepath.Join(dir, ticketFile), renderTicketFile(res, issue, body), 0644); err != nil {
		return res, fmt.Errorf("write %s: %w", ticketFile, err)
	}

	res.Prompt = SeedPrompt(res)
	if err := os.WriteFile(filepath.Join(dir, promptFile), []byte(res.Prompt), 0644); err != nil {
		// Non-fatal: the prompt is still returned in-memory for this session,
		// it just won't be reused by the next one.
		debuglog.Logger.Debug("linear: could not persist prompt.txt", "error", err)
	}

	m := meta{Identifier: res.Identifier, FetchedAt: time.Now(), Images: images, StateWrite: "skipped"}

	// meta.json is the durable record that keeps the one mutation exactly-once.
	// inFlight above cannot do this job: it is an in-process concurrency guard,
	// so it says nothing about a retry, a second fleet process, or a rerun after
	// a crash between the mutation and the write below.
	//
	// Its reach is bounded, and honestly so: the record lives inside the ticket
	// directory, so deleting that directory — the documented way to refresh —
	// deliberately re-arms the write. That is the intended behaviour, not a gap.
	// What this closes is every path where the directory survives.
	prior, hadPrior := readMeta(dir)
	switch {
	case hadPrior && prior.StateWrite == "done":
		// Already moved. Carry the record forward rather than re-asserting it:
		// by now a human may have moved the issue on, and dragging it back to
		// "started" is the worst thing this feature could do.
		//
		// The record travels; the REPORT does not. res.StateMoved is what the
		// caller prints as "Moved %s to its team's started state", so setting it
		// here would claim a write to someone's board that this run did not
		// make. Only the mutation below may set it.
		m.StateWrite, m.MovedTo = "done", prior.MovedTo
	case o.MoveState:
		if name, err := MoveToStarted(ctx, issue); err != nil {
			m.StateWrite = "failed"
			debuglog.Logger.Warn("linear: could not move issue to started", "id", res.Identifier, "error", err)
		} else if name != "" {
			m.StateWrite, m.MovedTo = "done", name
			res.StateMoved = name
		}
	}
	writeMeta(dir, m)

	analytics.Track(analytics.EventLinearTicketMaterialized, map[string]any{
		"images":  images,
		"dropped": dropped,
	})
	return res, nil
}

// fetchFull reads the whole issue in one query.
func fetchFull(ctx context.Context, id string) (*issueFull, error) {
	var out struct {
		Issue *issueFull `json:"issue"`
	}
	if err := execute(ctx, fullTimeout, issueFullQuery, map[string]any{"id": id}, &out); err != nil {
		return nil, err
	}
	if out.Issue == nil || out.Issue.Identifier == "" {
		return nil, ErrNotFound
	}
	return out.Issue, nil
}

// renderBody turns the issue into the markdown an agent will read.
//
// fleet composes this itself rather than asking the API for a rendered form,
// which is what lets comments carry their author and time. "Who asked for this
// and when" is usually the part that decides whether a ticket is still current.
func renderBody(i *issueFull) string {
	var b strings.Builder
	desc := strings.TrimSpace(i.Description)
	if desc == "" {
		desc = "_(no description)_"
	}
	b.WriteString("## Description\n\n")
	b.WriteString(desc)
	b.WriteString("\n")

	if n := len(i.Comments.Nodes); n > 0 {
		fmt.Fprintf(&b, "\n## Comments (%d)\n", n)
		for _, c := range i.Comments.Nodes {
			author := "someone"
			if c.User != nil && c.User.DisplayName != "" {
				author = c.User.DisplayName
			}
			fmt.Fprintf(&b, "\n### %s — %s\n\n", author, c.CreatedAt.Format("2006-01-02 15:04"))
			b.WriteString(strings.TrimSpace(c.Body))
			b.WriteString("\n")
		}
	}

	if n := len(i.Children.Nodes); n > 0 {
		b.WriteString("\n## Sub-issues\n\n")
		for _, c := range i.Children.Nodes {
			fmt.Fprintf(&b, "- %s — %s\n", c.Identifier, c.Title)
		}
	}

	// Attachments are links (PRs, Figma, Slack threads), not files. Listed
	// rather than downloaded: fleet fetches images because an agent cannot
	// follow a URL, and it deliberately does not go crawling anything else.
	if n := len(i.Attachments.Nodes); n > 0 {
		b.WriteString("\n## Links\n\n")
		for _, a := range i.Attachments.Nodes {
			title := a.Title
			if title == "" {
				title = a.URL
			}
			fmt.Fprintf(&b, "- [%s](%s)\n", title, a.URL)
		}
	}
	return b.String()
}

// collectImages downloads every image the body references into imgDir and
// rewrites the links to the relative paths the agent will read.
//
// Both halves are required and neither is sufficient alone: an uploads.linear.app
// URL is 401 to an agent with no credential, and an extensionless filename
// defeats extension dispatch in its file-read tool. Fix one and the agent still
// sees nothing.
func collectImages(ctx context.Context, markdown, imgDir string) (body string, kept, dropped int) {
	refs := findImages([]byte(markdown))
	body = markdown

	var total int64
	for i, ref := range refs {
		if kept >= maxImages || total >= maxTotalBytes {
			dropped++
			continue
		}
		name, size, err := fetchImage(ctx, ref.target, imgDir, ref.alt, i+1)
		if err != nil {
			debuglog.Logger.Debug("linear: image unavailable", "error", err)
			dropped++
			continue
		}
		kept++
		total += size
		body = strings.ReplaceAll(body, ref.target, filepath.Join(imagesDir, name))
	}
	return body, kept, dropped
}

// renderTicketFile writes front matter carrying the fields the body does not,
// plus honest provenance, so a reader (human or agent) knows this is a snapshot
// rather than live state.
func renderTicketFile(r Result, i *issueFull, body string) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "ticket: %s\n", r.Identifier)
	fmt.Fprintf(&b, "title: %s\n", r.Title)
	if r.URL != "" {
		fmt.Fprintf(&b, "url: %s\n", r.URL)
	}
	if r.StateName != "" {
		fmt.Fprintf(&b, "state_when_fetched: %s\n", r.StateName)
	}
	if i.Assignee != nil && i.Assignee.DisplayName != "" {
		fmt.Fprintf(&b, "assignee: %s\n", i.Assignee.DisplayName)
	}
	if p := priorityName(i.Priority); p != "" {
		fmt.Fprintf(&b, "priority: %s\n", p)
	}
	if n := len(i.Labels.Nodes); n > 0 {
		names := make([]string, 0, n)
		for _, l := range i.Labels.Nodes {
			names = append(names, l.Name)
		}
		fmt.Fprintf(&b, "labels: %s\n", strings.Join(names, ", "))
	}
	if i.Parent != nil && i.Parent.Identifier != "" {
		fmt.Fprintf(&b, "parent: %s — %s\n", i.Parent.Identifier, i.Parent.Title)
	}
	fmt.Fprintf(&b, "fetched_at: %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "images: %d\n", r.Images)
	b.WriteString("---\n\n")
	b.WriteString("<!-- Read-only snapshot fetched by fleet. The live ticket may have moved on since fetched_at. -->\n\n")
	b.WriteString(strings.TrimSpace(body))
	b.WriteString("\n")
	if r.ImagesDropped > 0 {
		fmt.Fprintf(&b, "\n> %d further image(s) were not downloaded: over fleet's per-ticket cap, "+
			"or not a readable image.\n", r.ImagesDropped)
	}
	return []byte(b.String())
}

// priorityName maps Linear's numeric priority. 0 means "not set", which is not
// worth a line in the front matter.
func priorityName(p int) string {
	switch p {
	case 1:
		return "urgent"
	case 2:
		return "high"
	case 3:
		return "medium"
	case 4:
		return "low"
	}
	return ""
}

// readMeta returns a previously written record for this ticket directory.
//
// A missing or unreadable file reports "no record", which re-arms the state
// write. That is the safe direction: the alternative — treating an unreadable
// file as "already done" — would silently disable the mutation for good the
// first time a disk hiccup truncated it.
func readMeta(dir string) (meta, bool) {
	data, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return meta{}, false
	}
	var m meta
	if err := json.Unmarshal(data, &m); err != nil {
		return meta{}, false
	}
	return m, true
}

func writeMeta(dir string, m meta) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, metaFile), data, 0644)
}
