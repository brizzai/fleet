package ui

import (
	"context"
	"encoding/json"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/github"
	"github.com/brizzai/fleet/internal/review"
)

const (
	// tourTimeout bounds the call. Measured: a 50-file, 245KB changeset took
	// 1m55s, which two minutes would have cut off at the finish line — and the
	// cost of waiting is only that the route arrives later into a reader you
	// are already using, while the cost of timing out is the whole feature.
	tourTimeout = 5 * time.Minute

	// tourInputBudget caps what fleet sends. A monorepo pull request can carry
	// megabytes of patch, and what drops off the end is what salience already
	// ranked lowest.
	tourInputBudget = 240 << 10
)

// maybeStartTour installs the cached tour, and generates one when there is none
// for the commit on screen.
//
// Fired when the reader opens, which is the moment the intent is unambiguous:
// you are about to read this pull request. Doing it when the cursor merely
// lands on the row would spend a call on every review you scroll past.
func (h *Home) maybeStartTour(k reviewKey, files github.PRFiles) tea.Cmd {
	if h.storage == nil || k.repo == "" {
		return nil
	}

	// A cached tour for ANY commit goes up straight away, even a stale one: a
	// route built against the previous head still leads somewhere, and a blank
	// panel while a fresh one is built leads nowhere at all.
	cachedSHA, raw, ok := h.storage.LoadReviewTour(k.repo, k.pr)
	if ok {
		if t, err := review.ParseTour(raw); err == nil {
			h.reader.SetTour(t, nil)
		} else {
			ok = false
		}
	}
	if ok && cachedSHA == files.HeadSHA && files.HeadSHA != "" {
		return nil
	}

	r, _ := h.findReviewByKey(k)
	title := r.Title
	if title == "" {
		title = "Pull request #" + itoa(k.pr)
	}
	input := review.BuildTourInput(title, r.Body, files.Files, tourInputBudget)
	dir := h.tourAccountDir(k)
	headSHA := files.HeadSHA

	h.reader.TourWorking()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), tourTimeout)
		defer cancel()

		out, err := claudeaccount.Ask(ctx, dir, review.TourInstruction, input)
		if err != nil {
			// The config dir is in the log because the first failure could not
			// be diagnosed without knowing which subscription it went to.
			debuglog.Logger.Debug("review tour: call failed", "pr", k.pr, "dir", dir, "err", err)
			return reviewTourMsg{key: k, headSHA: headSHA, err: err}
		}
		t, err := review.ParseTour(out)
		return reviewTourMsg{key: k, headSHA: headSHA, tour: t, err: err}
	}
}

// tourAccountDir picks which subscription pays for the tour.
//
// The session already reviewing this pull request wins, when there is one and
// when it can actually answer: the tour is part of the same piece of work, and
// running it elsewhere would split one review across two quotas for no reason.
//
// The "can actually answer" half is not a nicety. A session is allowed to sit
// on a spent account because a person waits and the window resets; a tour is
// fired once when the reader opens and never retried, so handing it a spent
// account is not a delay, it is the feature silently not existing. Select
// filters exhausted accounts for exactly this reason, and preferring the
// session's pin ahead of Select was quietly opting out of that check — which is
// how a tour ended up going, every time, to the one subscription that was at
// 100%.
//
// Otherwise the ordinary strategy decides, honouring the origin's allowlist:
// that is policy about which subscription may be spent on which repo, and a
// call fleet makes for itself is still a call on someone's account.
func (h *Home) tourAccountDir(k reviewKey) string {
	if h.accounts == nil || h.accounts.Len() == 0 {
		return ""
	}
	usage := h.accountUsageSnapshot()
	now := time.Now()
	for _, s := range h.sessions {
		kk, ok := h.reviewKeyFor(s)
		if !ok || kk != k || s.Account == "" {
			continue
		}
		if u, seen := usage[s.Account]; seen && u.Exhausted(now) {
			break
		}
		if dir := h.accounts.ConfigDirFor(s.Account); dir != "" {
			return dir
		}
	}
	acct, ok := claudeaccount.Select(claudeaccount.SelectOpts{
		Accounts: h.accounts.List(),
		Usage:    usage,
		Strategy: h.cfg.GetAccountStrategy(),
		Manual:   h.cfg.DefaultAccount,
		Allowed:  h.allowedAccountsFor(h.repoForOrigin(k.repo)),
	})
	if !ok {
		return ""
	}
	return acct.ConfigDir
}

// handleReviewTour installs a generated tour and caches it.
func (h *Home) handleReviewTour(msg reviewTourMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		debuglog.Logger.Debug("review tour: failed", "pr", msg.key.pr, "err", msg.err)
		h.reader.SetTour(nil, msg.err)
		return h, nil
	}
	// Cached against the commit it describes, so re-opening the same review is
	// free and a push is what makes fleet ask again.
	if raw, err := json.Marshal(msg.tour); err == nil {
		if err := h.storage.SaveReviewTour(msg.key.repo, msg.key.pr, msg.headSHA, string(raw)); err != nil {
			debuglog.Logger.Error("review tour: could not cache", "pr", msg.key.pr, "err", err)
		}
	}
	// Only the reader that asked: a tour landing in a different pull request's
	// reader would be a route through code nobody is looking at.
	if h.reader.Visible() && h.reader.key() == msg.key {
		h.reader.SetTour(msg.tour, nil)
	}
	return h, nil
}
