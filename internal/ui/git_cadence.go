package ui

import (
	"time"

	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
)

// Git branch/dirty refresh cadence, per repo.
//
// gitWorkerCycle wakes every tickInterval (2s), and it used to refresh EVERY
// repo that held any session on every wake — three git subprocesses per repo,
// one of them a full `git status --porcelain`. On a large monorepo that status
// costs ~0.25 CPU-seconds, so eleven checkouts burned ~1.6 CPU-seconds every 2s
// (~80% of a core) while the TUI sat idle — in child processes, where fleet's
// own %CPU never shows it. Most of that work refreshed repos where nothing
// could be changing: every session finished, or suspended with no process.
//
// So the interval follows what can move the answer:
//   - active: a session is running / waiting / starting, or was running within
//     gitHotWindow (an agent's commit lands while it runs, and the refresh just
//     after it finishes is the one that shows it). Every wake, as before.
//   - live:   a session has a process but is at rest (idle, finished). The user
//     might be editing by hand, so it still refreshes, just not every 2s.
//   - dormant: every session is suspended or errored — no process left that
//     could change the checkout. Kept on a slow timer rather than dropped, so a
//     PR merged meanwhile still turns its badge purple.
//
// An explicit trigger (detach, branch checkout, session create) forces a full
// pass, so an action the user just took is never hidden behind a cold timer.
const (
	gitActiveInterval  = tickInterval
	gitLiveInterval    = 30 * time.Second
	gitDormantInterval = 5 * time.Minute
	gitHotWindow       = time.Minute
)

// repoActivity is the most active state among one repo's sessions.
type repoActivity int

const (
	repoDormant repoActivity = iota
	repoLive
	repoActive
)

// activityForStatus classifies one session's status.
func activityForStatus(st session.Status) repoActivity {
	switch st {
	case session.StatusRunning, session.StatusWaiting, session.StatusStarting:
		return repoActive
	case session.StatusSuspended, session.StatusError:
		return repoDormant
	default:
		return repoLive
	}
}

// gitRefreshInterval is how long a repo's branch/dirty reading stays good.
func gitRefreshInterval(a repoActivity, lastHot, now time.Time) time.Duration {
	if a == repoActive || (!lastHot.IsZero() && now.Sub(lastHot) < gitHotWindow) {
		return gitActiveInterval
	}
	if a == repoLive {
		return gitLiveInterval
	}
	return gitDormantInterval
}

// gitRefreshDue reports whether a repo last refreshed at `last` needs another
// pass. The half-tick of slack absorbs ticker jitter: without it an active
// repo refreshed at t=0 reads 1.99s old at the next 2s wake and skips a beat.
func gitRefreshDue(last time.Time, interval time.Duration, now time.Time, force bool) bool {
	if force || last.IsZero() {
		return true
	}
	return now.Sub(last) >= interval-tickInterval/2
}

// repoSample is one session's contribution to its repo's cadence.
type repoSample struct {
	repo   string
	status session.Status
}

// gitRefreshTargets picks the repos due this cycle, in first-seen order.
// Pure: the caller snapshots sessions, git info and hotness stamps.
func gitRefreshTargets(samples []repoSample, infos map[string]*git.RepoInfo, lastHot map[string]time.Time, now time.Time, force bool) []string {
	order := make([]string, 0, len(samples))
	activity := make(map[string]repoActivity, len(samples))
	for _, s := range samples {
		a := activityForStatus(s.status)
		prev, seen := activity[s.repo]
		if !seen {
			order = append(order, s.repo)
		}
		if !seen || a > prev {
			activity[s.repo] = a
		}
	}

	due := make([]string, 0, len(order))
	for _, repo := range order {
		var last time.Time
		if info := infos[repo]; info != nil {
			last = info.LastGitRefresh
		}
		interval := gitRefreshInterval(activity[repo], lastHot[repo], now)
		if gitRefreshDue(last, interval, now, force) {
			due = append(due, repo)
		}
	}
	return due
}

// prCacheRowChanged reports whether a refresh produced anything the persisted
// PR cache row doesn't already hold. The row used to be rewritten for every
// repo on every 2s cycle, changed or not — ~5 writes a second with a dozen
// repos, which is what kept SQLite's single write lock busy enough to fail.
// A row only changes when the branch or origin moves or a PR fetch ran (which
// stamps LastPRRefresh / PRRateLimitedAt); the PR payload never changes
// without one of those, because it is carried forward otherwise.
func prCacheRowChanged(old, next *git.RepoInfo) bool {
	if old == nil || next == nil {
		return true
	}
	return old.Branch != next.Branch ||
		old.OriginKey != next.OriginKey ||
		!old.LastPRRefresh.Equal(next.LastPRRefresh) ||
		!old.PRRateLimitedAt.Equal(next.PRRateLimitedAt)
}
