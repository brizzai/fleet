package reviewjob

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/selfreview"
	"github.com/brizzai/fleet/internal/session"
)

// MaxConcurrent is how many reviewer calls one fleet process runs at once.
// Each is a `claude -p` that reads the worktree for a minute or two; more
// would contend with the sessions being reviewed for the same quota.
const MaxConcurrent = 2

var jobSlots = make(chan struct{}, MaxConcurrent)

// flight is one in-progress Prepare that later callers for the same checkout
// and cache key wait on instead of starting their own.
type flight struct {
	done  chan struct{}
	runID string // set once the preparing row exists
	rec   *Record
	err   error
}

var flights = struct {
	sync.Mutex
	m map[string]*flight
}{m: map[string]*flight{}}

func flightKey(checkout, cacheKey string) string { return checkout + "\x00" + cacheKey }

// InFlight reports whether this process is preparing a review of checkout.
func InFlight(checkout string) bool {
	flights.Lock()
	defer flights.Unlock()
	for k := range flights.m {
		if len(k) > len(checkout) && k[:len(checkout)] == checkout && k[len(checkout)] == 0 {
			return true
		}
	}
	return false
}

// inFlightRun reports whether this process is preparing the run with this id.
func inFlightRun(runID string) bool {
	flights.Lock()
	defer flights.Unlock()
	for _, f := range flights.m {
		if f.runID == runID {
			return true
		}
	}
	return false
}

// PrepareOptions tune one Prepare.
type PrepareOptions struct {
	// Force runs a new review even when this snapshot already has one — the
	// `r` regenerate key.
	Force bool
}

// Prepare returns a review of the checkout as it stands now, running the
// reviewer only when it has to: a snapshot that already has a ready, sent or
// approved run returns that run. Blocks for as long as the reviewer takes
// (up to selfreview.DefaultTimeout, plus a wait for a free slot).
//
// A run the reviewer fails is stored as failed and returned WITH the error,
// so the caller can show both the reason and the row.
func Prepare(ctx context.Context, d Deps, c Checkout) (*Record, error) {
	return PrepareWith(ctx, d, c, PrepareOptions{})
}

// PrepareWith is Prepare with options.
func PrepareWith(ctx context.Context, d Deps, c Checkout, o PrepareOptions) (*Record, error) {
	g, err := gather(ctx, d, c)
	if err != nil {
		return nil, err
	}
	model := d.model()

	if !o.Force {
		if rec, err := reuse(d, c.Root, g.snap); rec != nil || err != nil {
			return rec, err
		}
	}

	key := selfreview.CacheKey(selfreview.CacheKeyInput{
		Model: model, TreeSHA: g.snap.TreeSHA, BaseSHA: g.snap.BaseSHA,
		Prompts: g.in.Prompts, Ticket: g.in.Ticket, ReviewRules: g.rawRules,
		Round: g.in.Round, PrevRunID: g.prevRunID,
	})
	if o.Force {
		// A forced run must not be answered by the run it is replacing.
		key += "\x00force:" + newID()
	}

	fk := flightKey(c.Root, key)
	flights.Lock()
	if f, ok := flights.m[fk]; ok {
		flights.Unlock()
		select {
		case <-f.done:
			return f.rec, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	flights.m[fk] = f
	flights.Unlock()
	defer func() {
		flights.Lock()
		delete(flights.m, fk)
		flights.Unlock()
		close(f.done)
	}()

	// Another fleet process (the TUI while `fleet review` runs, or the other
	// way round) may already be preparing this exact run. Wait for its row
	// rather than paying for the same review twice.
	if !o.Force {
		if rec, ok, err := waitForOther(ctx, d, c.Root, key); ok {
			f.rec, f.err = rec, err
			return rec, err
		}
	}

	f.rec, f.err = run(ctx, d, c, g, model, key, f)
	return f.rec, f.err
}

// otherPoll is how often waitForOther re-reads another process's row.
var otherPoll = 2 * time.Second

// waitForOther waits on a fresh preparing row with this cache key that no
// flight in this process owns. ok is false when there is none (or it ended
// without a usable result), and the caller runs the review itself.
func waitForOther(ctx context.Context, d Deps, checkout, key string) (*Record, bool, error) {
	runs, err := d.Store.ListReviewRuns(checkout)
	if err != nil {
		return nil, false, nil
	}
	var id string
	for _, r := range runs {
		if r.CacheKey == key && r.Status == session.ReviewPreparing && !inFlightRun(r.ID) &&
			d.now().Sub(r.CreatedAt) < staleAfter {
			id = r.ID
			break
		}
	}
	if id == "" {
		return nil, false, nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil, true, ctx.Err()
		case <-time.After(otherPoll):
		}
		row, err := d.Store.LoadReviewRun(id)
		if err != nil || row == nil {
			return nil, false, nil
		}
		switch {
		case row.Status == session.ReviewPreparing && d.now().Sub(row.CreatedAt) < staleAfter:
			continue
		case row.Status == session.ReviewPreparing, row.Status == session.ReviewFailed:
			return nil, false, nil // it died or failed: try ourselves
		}
		rec, err := record(d.Store, row, d.now())
		return rec, true, err
	}
}

// reuse returns the run that already covers this snapshot, if one does.
func reuse(d Deps, checkout string, snap Snapshot) (*Record, error) {
	runs, err := d.Store.ListReviewRuns(checkout)
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		if r.TreeSHA != snap.TreeSHA || r.BaseSHA != snap.BaseSHA {
			continue
		}
		switch r.Status {
		case session.ReviewReady, session.ReviewSent, session.ReviewApproved:
			rec, err := record(d.Store, r, d.now())
			if err != nil {
				return nil, err
			}
			rec.Snapshot = snap
			return rec, nil
		}
	}
	return nil, nil
}

// run inserts the preparing row, waits for a slot, runs the reviewer and
// stores what it said.
func run(ctx context.Context, d Deps, c Checkout, g gathered, model, key string, f *flight) (*Record, error) {
	start := d.now()
	row := &session.ReviewRunRow{
		ID: newID(), Checkout: c.Root, Worktree: c.Root, Round: g.in.Round,
		BaseRef: g.snap.BaseRef, BaseSHA: g.snap.BaseSHA, TreeSHA: g.snap.TreeSHA, HeadSHA: g.snap.HeadSHA,
		CacheKey: key, Status: session.ReviewPreparing, Model: model, CreatedAt: start,
	}
	if err := d.Store.InsertReviewRun(row); err != nil {
		return nil, err
	}
	flights.Lock()
	f.runID = row.ID
	flights.Unlock()

	fail := func(err error) (*Record, error) {
		row.Status, row.Error = session.ReviewFailed, failureText(err)
		row.ElapsedMS = d.now().Sub(start).Milliseconds()
		if uerr := d.Store.UpdateReviewRun(row); uerr != nil {
			debuglog.Logger.Error("review: failed to record failure", "run", row.ID, "err", uerr)
		}
		rec := &Record{Row: row, Status: row.Status, Snapshot: g.snap}
		return rec, err
	}

	select {
	case jobSlots <- struct{}{}:
	case <-ctx.Done():
		return fail(ctx.Err())
	}
	defer func() { <-jobSlots }()

	email, configDir := d.account(ctx, c)
	row.Account = email
	debuglog.Logger.Info("review: running", "checkout", c.Root, "run", row.ID, "round", row.Round,
		"files", len(g.in.Files), "model", model, "account", email)

	result, meta, err := d.runner(model, configDir).Review(ctx, c.Root, g.in)
	if err != nil {
		return fail(err)
	}
	if meta.Model != "" {
		row.Model = meta.Model
	}
	data, err := json.Marshal(result)
	if err != nil {
		return fail(err)
	}
	row.Status, row.JSON, row.Error = session.ReviewReady, string(data), ""
	row.ElapsedMS = d.now().Sub(start).Milliseconds()
	if err := d.Store.UpdateReviewRun(row); err != nil {
		return nil, err
	}
	for _, w := range meta.Warnings {
		debuglog.Logger.Debug("review: enforce", "run", row.ID, "note", w)
	}
	return &Record{Row: row, Run: result, Snapshot: g.snap, Status: row.Status, Warnings: meta.Warnings}, nil
}

// failureText is the one line a failed run shows.
func failureText(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, selfreview.ErrNoJSON):
		return "the reviewer's answer couldn't be read"
	}
	msg := err.Error()
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:299]) + "…"
	}
	return msg
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:]) + "-" + time.Now().UTC().Format("20060102T150405")
}
