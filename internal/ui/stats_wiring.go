package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/session"
	"github.com/brizzai/fleet/internal/stats"
	"github.com/brizzai/fleet/internal/tmux"
)

// Stats wiring: the recorder tees, the transcript scan, and the Report/Recap
// round trips behind the Stats screen (`i`) and the weekly "Your week" badge.
//
// Everything here stays on this machine. stats.db is fed enums, ids, numbers
// and repo base names only, and nothing in this file may reach
// internal/analytics — the tee runs one way, from fleet into the local file.

// statsOpenStore opens stats.db for NewHome. A seam so the package tests, which
// build a Home per test, never open a database each (TestMain swaps it out).
var statsOpenStore = func() (*stats.Store, error) { return stats.Open(stats.DefaultPath()) }

const (
	// statsScanStartupDelay lets the boot fan-out (git, gh, tmux) finish before
	// the first transcript scan starts competing with it for disk.
	statsScanStartupDelay = 10 * time.Second
	// statsProgressEvery throttles scan progress to ~10 repaints a second.
	statsProgressEvery = 100 * time.Millisecond
	// statsFlushTimeout bounds how long a Report waits for queued events.
	statsFlushTimeout = time.Second
)

type (
	// statsScanKickMsg starts the deferred startup scan.
	statsScanKickMsg struct{}
	// statsProgressMsg is one throttled scan progress report.
	statsProgressMsg struct{ p stats.Progress }
	// statsScanDoneMsg ends a scan; last is the final progress it reported.
	statsScanDoneMsg struct {
		last stats.Progress
		err  error
	}
	// statsReportMsg carries a computed Report. gen guards against an older
	// compute for the same range landing after a newer one.
	statsReportMsg struct {
		rng    stats.Range
		gen    int
		report stats.Report
		err    error
	}
	// statsRecapMsg carries last week's Recap. open is set when the user asked
	// to see it; otherwise it only decides whether the badge shows.
	statsRecapMsg struct {
		recap stats.Recap
		open  bool
		err   error
	}
	// statsCopiedMsg reports a recap card's clipboard copy.
	statsCopiedMsg struct {
		err      error
		redacted bool
	}
)

// openStatsStoreOrNil opens stats.db, logging and returning nil on failure:
// every recorder method is nil-safe, so fleet runs on without Stats.
func openStatsStoreOrNil() *stats.Store {
	st, err := statsOpenStore()
	if err != nil {
		debuglog.Logger.Warn("stats: could not open stats.db; running without it", "err", err)
		return nil
	}
	return st
}

// statsKickCmd schedules the startup scan.
func statsKickCmd() tea.Cmd {
	return tea.Tick(statsScanStartupDelay, func(time.Time) tea.Msg { return statsScanKickMsg{} })
}

// recordStats counts one fleet action. Safe from any goroutine; a no-op with no
// store. Callers pass a stats.Action* constant, never a built string.
func (h *Home) recordStats(action string) {
	h.statsStore.RecordAction(action)
}

// statsRepeatWindow is how close together two presses of the same jump key
// must be to read as one held key. Terminal auto-repeat fires every ~30–50ms,
// so holding Space for a second counted as ~25 jumps: one user's week read
// 1,292 Space jumps, 942 of them in a single day.
const statsRepeatWindow = 300 * time.Millisecond

// statsJumpGate decides which key-driven jumps count. Update goroutine only:
// a map lookup and a time compare per press.
type statsJumpGate struct {
	last map[string]time.Time // repeat key → its most recent press
}

// count reports whether a press of key at now counts as a jump. A press that
// didn't land anywhere new never counts, and a press within statsRepeatWindow
// of the previous press of the same key — counted or not — continues that
// burst rather than starting one. Measuring from the previous PRESS, not the
// previous counted one, is what makes a held key one jump however long it is
// held; measuring from the counted one still let a long hold count every 300ms.
func (g *statsJumpGate) count(key string, moved bool, now time.Time) bool {
	if g.last == nil {
		g.last = map[string]time.Time{}
	}
	prev, seen := g.last[key]
	g.last[key] = now
	return moved && (!seen || now.Sub(prev) >= statsRepeatWindow)
}

// recordStatsJump counts one Space / P / slot jump through statsJumpGate.
// key separates the repeat bursts (each slot digit is its own key); moved is
// whether the jump landed on a different row than the cursor started on.
func (h *Home) recordStatsJump(action, key string, moved bool) {
	if h.statsJumps.count(key, moved, time.Now()) {
		h.recordStats(action)
	}
}

// recordStatus tees a persisted status transition. from may be "" when the
// caller cannot know the previous status (a restart that ran off-loop).
func (h *Home) recordStatus(id string, from, to session.Status) {
	h.statsStore.RecordStatus(id, string(from), string(to), time.Now())
}

// statsActionFor maps an ActionLog action name onto a stats enum. The table is
// fixed on purpose: an unmapped action records nothing, and the detail — where
// titles and paths live — is never read. Attaches are counted from their own
// table (RecordAttach), a session start from handleSessionCreateResult and a
// delete from its confirmed message (the "delete …" actions log the prompt, not
// the yes), so none of those appear here.
func statsActionFor(action string) string {
	switch action {
	case "fork session", "fork to worktree":
		return stats.ActionFork
	case "undo delete":
		return stats.ActionUndo
	case "restart session":
		return stats.ActionRestart
	case "resume session":
		return stats.ActionResume
	case "quick approve":
		return stats.ActionQuickApprove
	case "open terminal drawer":
		return stats.ActionDrawerOpen
	case "snooze session", "snooze checkout", "snooze origin":
		return stats.ActionSnooze
	case "bind slot":
		return stats.ActionSlotBind
	case "mark unread":
		return stats.ActionMarkUnread
	case "open editor":
		return stats.ActionEditorOpen
	case "open PR":
		return stats.ActionPROpen
	case "copy PR link":
		return stats.ActionCopyPRLink
	}
	return ""
}

// noteStatsSessions tells stats.db which fleet sessions exist, so the scan
// knows whose transcripts to read. Fields are read here, on the caller's
// goroutine; the repo name is resolved on a goroutine of its own, since
// GetRepoRoot can shell out to git on a cold cache.
func (h *Home) noteStatsSessions(ss ...*session.Session) {
	if note := h.statsNotes(ss); note != nil {
		go note()
	}
}

// statsNotes snapshots ss and returns the (blocking) work of noting them, or
// nil with nothing to do.
func (h *Home) statsNotes(ss []*session.Session) func() {
	if h.statsStore == nil || len(ss) == 0 {
		return nil
	}
	type pending struct {
		meta stats.SessionMeta
		path string
	}
	ps := make([]pending, 0, len(ss))
	for _, s := range ss {
		if s == nil {
			continue
		}
		ag := string(s.Agent)
		if ag == "" {
			ag = "claude" // legacy rows: the sidebar falls back to Claude too
		}
		ps = append(ps, pending{
			meta: stats.SessionMeta{ID: s.ID, ClaudeSessionID: s.GetClaudeSessionID(), Agent: ag, CreatedAt: s.CreatedAt},
			path: s.ProjectPath,
		})
	}
	store := h.statsStore
	return func() {
		for _, p := range ps {
			p.meta.Repo = h.statsRepoName(p.path)
			store.NoteSession(p.meta)
		}
	}
}

// statsRepoName is the name a session's repo goes by in Stats: the origin's
// label as the sidebar shows it, so worktrees count toward their repo. Before
// the git cache has the origin it falls back to the checkout's base name, and a
// later note overwrites it. Off the Update goroutine only.
func (h *Home) statsRepoName(projectPath string) string {
	if projectPath == "" {
		return ""
	}
	return labelForOrigin(h.originOf(session.GetRepoRoot(projectPath)))
}

// startStatsScan runs one incremental transcript scan on its own goroutine —
// never the status worker, whose stall budget is spent on git and gh, and never
// Update. A scan already running is left to finish; its done handler refreshes
// whatever is on screen.
func (h *Home) startStatsScan() tea.Cmd {
	if h.statsStore == nil || h.statsScanning {
		return nil
	}
	h.statsScanning = true
	// Re-note every live session first: a /clear rotation can change a Claude id
	// on paths that never pass UpdateClaudeSessionID, and the git cache has
	// usually warmed since load, upgrading worktree names to their repo's.
	h.workerMu.Lock()
	snapshot := append([]*session.Session(nil), h.sessions...)
	h.workerMu.Unlock()
	note := h.statsNotes(snapshot)

	store, ctx := h.statsStore, h.ctx
	return func() tea.Msg {
		if note != nil {
			note()
		}
		store.Flush(statsFlushTimeout) // the notes must land before Scan reads them
		var last stats.Progress
		var lastSent time.Time
		err := store.Scan(ctx, func(p stats.Progress) {
			last = p
			if time.Since(lastSent) < statsProgressEvery && !p.Finished {
				return
			}
			// h.send is a rendezvous with the Update loop, which an attach
			// suspends; a dropped progress frame costs nothing, a wedged scan does.
			if h.isAttaching.Load() {
				return
			}
			lastSent = time.Now()
			h.send(statsProgressMsg{p: p})
		})
		return statsScanDoneMsg{last: last, err: err}
	}
}

// statsReportCmd computes the Report for rng off the Update goroutine.
func (h *Home) statsReportCmd(rng stats.Range) tea.Cmd {
	if h.statsStore == nil {
		return nil
	}
	if h.statsReportGen == nil {
		h.statsReportGen = map[stats.Range]int{}
	}
	h.statsReportGen[rng]++
	gen, store := h.statsReportGen[rng], h.statsStore
	return func() tea.Msg {
		store.Flush(statsFlushTimeout)
		r, err := store.Report(rng, time.Now())
		return statsReportMsg{rng: rng, gen: gen, report: r, err: err}
	}
}

// statsRecapCmd computes last full week's Recap off the Update goroutine.
func (h *Home) statsRecapCmd(open bool) tea.Cmd {
	if h.statsStore == nil {
		return nil
	}
	store := h.statsStore
	return func() tea.Msg {
		store.Flush(statsFlushTimeout)
		r, err := store.Recap(stats.LastFullWeekStart(time.Now()))
		return statsRecapMsg{recap: r, open: open, err: err}
	}
}

// statsWeekKey is how a recap week is stored in config: its local Monday.
func statsWeekKey(weekStart time.Time) string {
	if weekStart.IsZero() {
		return ""
	}
	return weekStart.In(time.Local).Format("2006-01-02")
}

// openStats opens the Stats screen: a Report for the chip's range right away
// (it includes whatever the recorder holds), plus an incremental scan whose
// completion refreshes it. With the weekly badge up, the recap opens on top —
// the badge advertises `i`, so `i` is what answers it.
func (h *Home) openStats() tea.Cmd {
	if h.statsStore == nil {
		h.setError("stats_unavailable", errors.New("stats unavailable — stats.db could not be opened (see debug log)"))
		return nil
	}
	// Opening Stats is not recorded as an action: pressing `i` must not inflate
	// the numbers it opens (a first open would otherwise read as a busiest day).
	h.statsView.Show()
	if h.statsRecapUnseen {
		h.showRecap(h.statsRecap)
	}
	return tea.Batch(h.statsReportCmd(h.statsView.Range()), h.startStatsScan())
}

// openRecap computes and opens last week's recap (Ctrl+K → "Your Week", or `w`
// inside Stats).
func (h *Home) openRecap() tea.Cmd {
	if h.statsStore == nil {
		h.setError("stats_unavailable", errors.New("stats unavailable — stats.db could not be opened (see debug log)"))
		return nil
	}
	return h.statsRecapCmd(true)
}

// showRecap opens the reel and marks its week seen, which clears the badge.
func (h *Home) showRecap(r stats.Recap) {
	h.recapView.Show(r)
	h.statsRecapUnseen = false
	h.cfg.SetStatsRecapSeenWeek(statsWeekKey(r.WeekStart))
}

// handleStatsMsg handles every Stats message. ok is false for anything else.
func (h *Home) handleStatsMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case statsScanKickMsg:
		return h.startStatsScan(), true

	case statsProgressMsg:
		h.statsView.SetProgress(msg.p)
		return nil, true

	case statsScanDoneMsg:
		h.statsScanning = false
		if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
			debuglog.Logger.Warn("stats: transcript scan failed", "err", msg.err)
		}
		final := msg.last
		final.Finished = true
		h.statsView.SetProgress(final)
		cmds := []tea.Cmd{h.statsRecapCmd(false)}
		if h.statsView.IsVisible() {
			cmds = append(cmds, h.statsReportCmd(h.statsView.Range()))
		}
		return tea.Batch(cmds...), true

	case statsReportMsg:
		if msg.gen != h.statsReportGen[msg.rng] {
			return nil, true // a newer compute for this range is on its way
		}
		if msg.err != nil {
			debuglog.Logger.Warn("stats: report failed", "range", msg.rng.Label(), "err", msg.err)
			return nil, true
		}
		h.statsView.SetReport(msg.report)
		return nil, true

	case statsRangeMsg:
		return h.statsReportCmd(msg.Range), true

	case statsOpenRecapMsg:
		return h.openRecap(), true

	case statsRecapMsg:
		if msg.err != nil {
			debuglog.Logger.Warn("stats: recap failed", "err", msg.err)
			if msg.open {
				h.setError("stats_recap_failed", fmt.Errorf("weekly recap: %w", msg.err))
			}
			return nil, true
		}
		h.statsRecap = msg.recap
		h.statsView.SetHasRecap(len(msg.recap.Cards) > 0)
		if msg.open {
			if len(msg.recap.Cards) == 0 {
				h.setInfo("Nothing recorded last week yet")
				return nil, true
			}
			h.showRecap(msg.recap)
			return nil, true
		}
		h.statsRecapUnseen = len(msg.recap.Cards) > 0 &&
			statsWeekKey(msg.recap.WeekStart) != h.cfg.GetStatsRecapSeenWeek()
		return nil, true

	case statsCopyMsg:
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			return nil, true
		}
		redacted := msg.Redacted
		return func() tea.Msg { return statsCopiedMsg{err: tmux.CopyToClipboard(text), redacted: redacted} }, true

	case statsCopiedMsg:
		if msg.err != nil {
			h.setError("stats_copy_failed", fmt.Errorf("copy card: %w", msg.err))
			return nil, true
		}
		if msg.redacted {
			h.setInfo("Copied card — repo names left out (r includes them)")
			return nil, true
		}
		h.setInfo("Copied card")
		return nil, true

	case statsRecapReposMsg:
		if msg.On {
			h.setInfo("Copied cards will include repo names")
		} else {
			h.setInfo("Copied cards leave repo names out")
		}
		return nil, true
	}
	return nil, false
}

// renderStatsRecapBadge is the header badge plus the key that answers it.
func renderStatsRecapBadge() string {
	return renderStatsBadge() + DimStyle.Render(" · press ") + HelpKeyStyle.Render("i")
}

// fitStatsRecapBadge is the widest form of the badge that fits in room
// columns: with its key hint, then without, then nothing.
func fitStatsRecapBadge(room int) string {
	for _, b := range []string{renderStatsRecapBadge(), renderStatsBadge()} {
		if lipgloss.Width(b) <= room {
			return b
		}
	}
	return ""
}

// maybeRecapNewWeek recomputes the recap once the last full week rolls over
// while fleet keeps running — instances run for days, and otherwise only the
// startup scan and a Stats open ever compute it, so the badge would never light
// for the week that just ended. Called on the ~2s tick; asks once per week.
func (h *Home) maybeRecapNewWeek(now time.Time) tea.Cmd {
	week := stats.LastFullWeekStart(now)
	if h.statsStore == nil || week.Equal(h.statsRecapWeek) {
		return nil
	}
	h.statsRecapWeek = week
	return h.statsRecapCmd(false)
}
