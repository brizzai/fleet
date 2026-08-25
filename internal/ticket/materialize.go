package ticket

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/git"
)

const (
	fleetDir   = ".fleet"
	ticketDir  = "ticket"
	imagesDir  = "images"
	ticketFile = "ticket.md"
	promptFile = "prompt.txt"
	metaFile   = "meta.json"
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

	// Provider is the Kind of the tracker that produced this identifier, when
	// the caller already knows it — which it does whenever the ticket came from
	// a Fetch or a Search rather than from a bare string the user typed.
	//
	// Carrying it is what stops the provider being resolved a SECOND time, by a
	// different rule, at a point where the decision was already made. See
	// ticketing.Materialize.
	Provider string

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
	Provider   string    `json:"provider,omitempty"`
	FetchedAt  time.Time `json:"fetched_at"`
	Images     int       `json:"images"`
	StateWrite string    `json:"state_write"` // "done" | "skipped" | "failed"
	MovedTo    string    `json:"moved_to,omitempty"`
}

// TicketDir returns where a ticket materializes inside a worktree.
//
// Deliberately not namespaced by provider. A Linear PROJ-1 and a Jira PROJ-1 in
// one repo would share a directory, but that needs a repo that tracks both
// trackers AND a key collision between them; namespacing would change the path
// under every worktree that already exists and lengthen the one line of the
// seed prompt an agent has to act on. The provider is recorded in meta.json
// instead, and a mismatch there re-materializes.
func TicketDir(worktreePath, id string) string {
	return filepath.Join(worktreePath, fleetDir, ticketDir, strings.ToUpper(id))
}

// Materialize fetches a ticket and writes it, with its screenshots, into the
// worktree.
//
// Failure posture matches its neighbours in the creation path
// (copyClaudeSettingsFile, workspace.CopyConfiguredFiles): once the worktree
// exists, nothing here may fail the caller. The returned error is advisory —
// log it, surface one line, and start the session anyway. The one hard rule is
// that Result.Prompt stays empty unless ticket.md verifiably exists: a pointer
// to files that were never written is worse than no prompt at all.
func Materialize(ctx context.Context, p Provider, o Opts) (Result, error) {
	var res Result

	if p == nil {
		return res, ErrNotConnected
	}
	if o.WorktreePath == "" || o.Identifier == "" {
		return res, fmt.Errorf("materialize needs a worktree and an identifier")
	}
	id := strings.ToUpper(o.Identifier)

	if _, busy := inFlight.LoadOrStore(o.WorktreePath, struct{}{}); busy {
		return res, fmt.Errorf("already materializing %s", o.WorktreePath)
	}
	defer inFlight.Delete(o.WorktreePath)

	// One round trip for everything: description, comments, labels, and
	// whatever the optional state write needs, so the whole flow costs one
	// query plus the image GETs.
	doc, err := p.Document(ctx, id)
	if err != nil {
		return res, err
	}

	res.Ticket = doc.Ticket
	res.Provider = p.Kind()
	dir := TicketDir(o.WorktreePath, res.Identifier)
	res.Dir = dir
	res.RelDir = filepath.Join(fleetDir, ticketDir, res.Identifier)

	// Exclude BEFORE writing a single byte. A window where the files exist and
	// the exclude does not is a window where `git add -A` sweeps a customer
	// screenshot into a commit.
	if err := git.AddFleetExclude(o.WorktreePath); err != nil {
		debuglog.Logger.Warn("ticket: could not exclude .fleet from git — ticket files are stageable",
			"worktree", o.WorktreePath, "error", err)
	}

	imgDir := filepath.Join(dir, imagesDir)
	if err := os.MkdirAll(imgDir, 0755); err != nil {
		return res, fmt.Errorf("create %s: %w", imgDir, err)
	}

	body, images, dropped := collectImages(ctx, doc, imgDir)
	res.Images, res.ImagesDropped = images, dropped

	if images == 0 {
		_ = os.Remove(imgDir) // only succeeds when empty, which is what we want
	}

	if err := os.WriteFile(filepath.Join(dir, ticketFile), renderTicketFile(res, doc, body), 0644); err != nil {
		return res, fmt.Errorf("write %s: %w", ticketFile, err)
	}

	res.Prompt = SeedPrompt(p.Name(), res)
	if err := os.WriteFile(filepath.Join(dir, promptFile), []byte(res.Prompt), 0644); err != nil {
		// Non-fatal: the prompt is still returned in-memory for this session,
		// it just won't be reused by the next one.
		debuglog.Logger.Debug("ticket: could not persist prompt.txt", "error", err)
	}

	m := meta{
		Identifier: res.Identifier,
		Provider:   p.Kind(),
		FetchedAt:  time.Now(),
		Images:     images,
		StateWrite: "skipped",
	}

	// meta.json is the durable record that keeps the one mutation exactly-once.
	// inFlight above cannot do this job: it is an in-process concurrency guard,
	// so it says nothing about a retry, a second fleet process, or a rerun after
	// a crash between the mutation and the write below.
	//
	// Its reach is bounded, and honestly so: the record lives inside the ticket
	// directory, so deleting that directory — the documented way to refresh —
	// deliberately re-arms the write. That is the intended behaviour, not a gap.
	// What this closes is every path where the directory survives.
	//
	// A record left by a DIFFERENT provider does not count as prior: the two
	// trackers keep separate boards, so a Linear write says nothing about
	// whether the Jira issue of the same key has been started.
	prior, hadPrior := readMeta(dir)
	if hadPrior && prior.Provider != "" && prior.Provider != p.Kind() {
		hadPrior = false
	}
	switch {
	case hadPrior && prior.StateWrite == "done":
		// Already moved. Carry the record forward rather than re-asserting it:
		// by now a human may have moved the issue on, and dragging it back to
		// "started" is the worst thing this feature could do.
		//
		// The record travels; the REPORT does not. res.StateMoved is what the
		// caller prints as "Moved %s to its started state", so setting it here
		// would claim a write to someone's board that this run did not make.
		// Only the mutation below may set it.
		m.StateWrite, m.MovedTo = "done", prior.MovedTo
	case o.MoveState && doc.Start != nil:
		if name, err := doc.Start(ctx); err != nil {
			m.StateWrite = "failed"
			debuglog.Logger.Warn("ticket: could not move issue to started",
				"provider", p.Kind(), "id", res.Identifier, "error", err)
		} else if name != "" {
			m.StateWrite, m.MovedTo = "done", name
			res.StateMoved = name
		}
	}
	writeMeta(dir, m)

	analytics.Track(analytics.EventTicketMaterialized, map[string]any{
		"provider": p.Kind(),
		"images":   images,
		"dropped":  dropped,
	})
	return res, nil
}

// collectImages downloads every image the document references into imgDir and
// rewrites the placeholders to the relative paths the agent will read.
//
// Both halves are required and neither is sufficient alone: an attachment URL
// is 401 to an agent with no credential, and an extensionless filename defeats
// extension dispatch in its file-read tool. Fix one and the agent still sees
// nothing.
//
// A placeholder whose download failed or hit a cap is replaced by its alt text
// alone, so ticket.md never links a file that isn't on disk.
func collectImages(ctx context.Context, doc *Document, imgDir string) (body string, kept, dropped int) {
	body = doc.Body

	var total int64
	for i, img := range doc.Images {
		// The whole link target, closing paren included. Matching the bare
		// token instead would make fleet-image:1 a prefix of fleet-image:10 —
		// so image 1 succeeding would rewrite half of image 10's link and leave
		// the rest as literal text.
		ref := "](" + PlaceholderFor(i) + ")"
		if !strings.Contains(body, ref) {
			// The provider listed a file the body never referenced. Nothing to
			// rewrite, so nothing to fetch.
			continue
		}
		if kept >= maxImages || total >= maxTotalBytes {
			dropped++
			body = dropImageLink(body, ref)
			continue
		}
		name, size, err := fetchImage(ctx, doc, img.URL, imgDir, img.Alt, i+1)
		if err != nil {
			debuglog.Logger.Debug("ticket: image unavailable", "error", err)
			dropped++
			body = dropImageLink(body, ref)
			continue
		}
		kept++
		total += size
		body = strings.ReplaceAll(body, ref, "]("+filepath.Join(imagesDir, name)+")")
	}
	return body, kept, dropped
}

// PlaceholderFor renders the link target a Document must use for Images[i].
func PlaceholderFor(i int) string { return ImagePlaceholder + strconv.Itoa(i) }

// dropImageLink turns ![alt](fleet-image:3) into a plain "(image: alt)" note.
//
// A failed download has to degrade to a sentence rather than to a dangling
// link: an agent told to open every file in images/ will try, and a 404 on the
// first thing it reads is a worse start than being told the screenshot is
// missing.
func dropImageLink(body, ref string) string {
	for {
		end := strings.Index(body, ref)
		if end < 0 {
			return body
		}
		note := "(image — not downloaded)"
		start := strings.LastIndex(body[:end], "![")
		if start < 0 {
			// Defensive: a placeholder outside an image link. Drop the target
			// so no fleet-image: token can survive into ticket.md.
			body = body[:end] + "]()" + body[end+len(ref):]
			continue
		}
		if alt := strings.TrimSpace(body[start+2 : end]); alt != "" {
			note = "(image: " + alt + " — not downloaded)"
		}
		body = body[:start] + note + body[end+len(ref):]
	}
}

// renderTicketFile writes front matter carrying the fields the body does not,
// plus honest provenance, so a reader (human or agent) knows this is a snapshot
// rather than live state.
func renderTicketFile(r Result, doc *Document, body string) []byte {
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
	if doc.Assignee != "" {
		fmt.Fprintf(&b, "assignee: %s\n", doc.Assignee)
	}
	if p := r.Priority.Name(); p != "" {
		fmt.Fprintf(&b, "priority: %s\n", p)
	}
	if len(doc.Labels) > 0 {
		fmt.Fprintf(&b, "labels: %s\n", strings.Join(doc.Labels, ", "))
	}
	if doc.Parent != "" {
		fmt.Fprintf(&b, "parent: %s\n", doc.Parent)
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
