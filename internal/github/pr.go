package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/debuglog"
)

// gh failures are polled — the status worker refetches PRs every ~60s per repo,
// so a persistent timeout or rate-limit would otherwise emit an analytics event
// on every poll for every repo. trackGHFailure coarsens that to at most one
// event per ghFailTrackInterval across the whole process: enough to surface
// "gh is unhealthy" without flooding the endpoint. reason is a low-cardinality
// label ("timeout" / "rate_limit"), never command args or paths.
const ghFailTrackInterval = 10 * time.Minute

var (
	ghFailMu   sync.Mutex
	ghFailLast time.Time
)

func trackGHFailure(reason string) {
	ghFailMu.Lock()
	if !ghFailLast.IsZero() && time.Since(ghFailLast) < ghFailTrackInterval {
		ghFailMu.Unlock()
		return
	}
	ghFailLast = time.Now()
	ghFailMu.Unlock()
	analytics.Track(analytics.EventGhCommandFailure, map[string]interface{}{
		"command": "pr_view",
		"reason":  reason,
	})
}

// ghTimeout bounds every gh subprocess. gh makes network calls to GitHub, so a
// hung request (network stall, API hang, blocked auth refresh) must not freeze
// the caller. The status worker waits synchronously on these during its
// per-cycle PR fan-out, so one indefinite gh call would stall every session's
// status update until the OS finally tore the connection down.
const ghTimeout = 15 * time.Second

// ErrRateLimited is returned when gh reports a GitHub API rate-limit error.
// Callers use this to back off subsequent PR refreshes instead of hammering
// the API every TTL window.
var ErrRateLimited = errors.New("github: API rate limit exceeded")

// classifyGHError inspects gh's stderr for known error patterns. The output
// is matched case-insensitively against substrings GitHub returns on the
// "API rate limit exceeded" / "secondary rate limit" paths. Returns
// ErrRateLimited when it matches, nil otherwise (treated as "no PR" or
// other benign error by the caller).
func classifyGHError(stderr string) error {
	low := strings.ToLower(stderr)
	if strings.Contains(low, "rate limit") || strings.Contains(low, "rate-limit") {
		return ErrRateLimited
	}
	return nil
}

// PR represents a GitHub pull request.
type PR struct {
	Number            int
	Title             string
	URL               string
	State             string // OPEN, CLOSED, MERGED
	ReviewDecision    string // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, ""
	CIStatus          string // SUCCESS, FAILURE, PENDING, ""
	UnresolvedThreads int    // count of unresolved review threads
	HasConflicts      bool   // true when GitHub reports merge conflicts
	IsDraft           bool   // true while the PR is a draft (not ready for review)
}

// IsGHAvailable checks if the gh CLI is installed and accessible.
func IsGHAvailable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "gh", "--version").Run() == nil
}

// ghPRResponse matches the JSON output of gh pr view.
type ghPRResponse struct {
	Number            int                `json:"number"`
	Title             string             `json:"title"`
	URL               string             `json:"url"`
	State             string             `json:"state"`
	ReviewDecision    string             `json:"reviewDecision"`
	StatusCheckRollup []statusCheckEntry `json:"statusCheckRollup"`
	Mergeable         string             `json:"mergeable"` // MERGEABLE, CONFLICTING, UNKNOWN
	IsDraft           bool               `json:"isDraft"`
}

type statusCheckEntry struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	StartedAt    string `json:"startedAt"`    // RFC3339; used to pick the latest run when a check has several
	WorkflowName string `json:"workflowName"` // empty for status contexts; called workflows report their caller
}

// GetPRForBranch returns the PR associated with the current branch, or nil if none.
// ignorePatterns are path.Match globs applied to check names; matching checks are
// dropped from the rollup before CI status is derived (lets repos suppress noisy
// gates without affecting real check failures).
//
// Returns (nil, ErrRateLimited) when gh reports a GitHub API rate limit; the
// caller should back off rather than refire the request. Other failures
// (including "no PR for this branch", which gh signals via exit code 1)
// return (nil, nil).
func GetPRForBranch(repoPath, branch string, ignorePatterns []string) (*PR, error) {
	if branch == "" || branch == "HEAD" {
		return nil, nil
	}

	debuglog.Logger.Debug("PR fetch: start", "path", repoPath, "branch", branch)

	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", branch,
		"--json", "number,title,url,state,reviewDecision,statusCheckRollup,mergeable,isDraft",
	)
	cmd.Dir = repoPath
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		// A timeout must be distinguishable from "no PR" — on the deadline
		// cmd.Output() errors with empty stderr, which would otherwise fall
		// through to (nil, nil) and let RefreshPRInfo clear a real PR's badge.
		if ctx.Err() == context.DeadlineExceeded {
			debuglog.Logger.Debug("PR fetch: timed out", "path", repoPath, "branch", branch)
			trackGHFailure("timeout")
			return nil, fmt.Errorf("gh pr view timed out: %w", ctx.Err())
		}
		stderrStr := strings.TrimSpace(stderr.String())
		if rlErr := classifyGHError(stderrStr); rlErr != nil {
			debuglog.Logger.Debug("PR fetch: rate-limited", "path", repoPath, "branch", branch, "stderr", stderrStr)
			trackGHFailure("rate_limit")
			return nil, rlErr
		}
		// Most common case: gh exits 1 when no PR exists for the branch. Log
		// at DEBUG with stderr so unexpected failures (auth, network) are
		// still inspectable when FLEET_DEBUG=1.
		debuglog.Logger.Debug("PR fetch: no PR or gh error", "path", repoPath, "branch", branch, "stderr", stderrStr)
		return nil, nil
	}

	var resp ghPRResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		debuglog.Logger.Debug("PR fetch: JSON parse failed", "path", repoPath, "branch", branch, "error", err)
		return nil, nil
	}

	pr := &PR{
		Number:            resp.Number,
		Title:             resp.Title,
		URL:               resp.URL,
		State:             resp.State,
		ReviewDecision:    resp.ReviewDecision,
		CIStatus:          deriveCIStatus(resp.StatusCheckRollup, ignorePatterns),
		UnresolvedThreads: getUnresolvedThreadCount(repoPath, resp.Number, resp.URL),
		HasConflicts:      resp.Mergeable == "CONFLICTING",
		IsDraft:           resp.IsDraft,
	}

	debuglog.Logger.Debug("PR fetch: ok", "path", repoPath, "branch", branch, "pr", pr.Number, "state", pr.State, "ci", pr.CIStatus)
	return pr, nil
}

// getUnresolvedThreadCount queries GitHub GraphQL API for unresolved review thread count.
func getUnresolvedThreadCount(repoPath string, prNumber int, prURL string) int {
	// Parse owner/repo from PR URL: https://github.com/owner/repo/pull/123
	trimmed := strings.TrimPrefix(prURL, "https://github.com/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 3 {
		debuglog.Logger.Debug("getUnresolvedThreadCount failed to parse PR URL", "url", prURL)
		return 0
	}
	owner, repo := parts[0], parts[1]

	query := fmt.Sprintf(`query {
		repository(owner: "%s", name: "%s") {
			pullRequest(number: %d) {
				reviewThreads(first: 100) {
					nodes { isResolved }
				}
			}
		}
	}`, owner, repo, prNumber)

	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "api", "graphql", "-f", "query="+query)
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		debuglog.Logger.Debug("getUnresolvedThreadCount GraphQL query failed", "pr", prNumber, "error", err)
		return 0
	}

	var result struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							IsResolved bool `json:"isResolved"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		debuglog.Logger.Debug("getUnresolvedThreadCount JSON parse failed", "pr", prNumber, "error", err)
		return 0
	}

	count := 0
	for _, t := range result.Data.Repository.PullRequest.ReviewThreads.Nodes {
		if !t.IsResolved {
			count++
		}
	}
	return count
}

// latestRunPerCheck collapses the rollup to the most recently started run per
// check name. A single check can appear multiple times on one commit when its
// workflow is re-run in place (rather than fixed by a new push) — GitHub keeps
// every run in the rollup, including superseded ones. Without this, a check
// that failed and was later re-run green would still mark the PR as failed.
// This mirrors how `gh pr checks` and the GitHub UI report status.
//
// Ghost entries with no name (e.g. status contexts, which expose their name
// under a different field) are dropped, matching prior behavior. On equal or
// missing StartedAt, the later entry in rollup order wins (GitHub returns runs
// roughly chronologically).
func latestRunPerCheck(checks []statusCheckEntry) []statusCheckEntry {
	latest := make(map[string]statusCheckEntry, len(checks))
	for _, c := range checks {
		if c.Name == "" {
			continue
		}
		if prev, ok := latest[c.Name]; ok && c.StartedAt < prev.StartedAt {
			continue // an existing, newer run wins
		}
		latest[c.Name] = c
	}
	out := make([]statusCheckEntry, 0, len(latest))
	for _, c := range latest {
		out = append(out, c)
	}
	return out
}

func isFailedCheck(c statusCheckEntry) bool {
	switch strings.ToUpper(c.Conclusion) {
	case "FAILURE", "ERROR", "TIMED_OUT":
		return true
	}
	return false
}

func isInFlightCheck(c statusCheckEntry) bool {
	switch strings.ToUpper(c.Status) {
	case "IN_PROGRESS", "QUEUED", "PENDING":
		return true
	}
	return c.Conclusion == ""
}

// newestInFlightPerWorkflow returns, per workflow, the StartedAt of the most
// recently started check still running. Status contexts (no workflow) are
// excluded: they'd all share the "" key and suppress each other unrelated.
func newestInFlightPerWorkflow(checks []statusCheckEntry) map[string]string {
	newest := make(map[string]string)
	for _, c := range checks {
		if c.WorkflowName == "" || !isInFlightCheck(c) {
			continue
		}
		if c.StartedAt > newest[c.WorkflowName] {
			newest[c.WorkflowName] = c.StartedAt
		}
	}
	return newest
}

// deriveCIStatus determines overall CI status from status check rollup.
// Checks whose name matches any ignorePatterns glob are dropped before rollup.
//
// FAILURE is reserved for failures worth acting on. A workflow that re-runs
// under a different job name — a gate that publishes a verdict before its
// suites are triggered, say — leaves a red job behind from the earlier run,
// and latestRunPerCheck can't collapse it because the names differ. Once that
// same workflow is running again, its old verdict is being recomputed, so it
// reports PENDING (wait) rather than FAILURE (go look).
func deriveCIStatus(checks []statusCheckEntry, ignorePatterns []string) string {
	if len(checks) == 0 {
		return ""
	}

	considered := make([]statusCheckEntry, 0, len(checks))
	for _, c := range latestRunPerCheck(checks) {
		if matchesAnyPattern(c.Name, ignorePatterns) {
			continue
		}
		considered = append(considered, c)
	}
	inFlight := newestInFlightPerWorkflow(considered)

	hasFailure := false
	hasPending := false

	for _, check := range considered {
		switch {
		case isFailedCheck(check):
			if since, ok := inFlight[check.WorkflowName]; ok && check.StartedAt < since {
				continue // superseded by a newer run of the same workflow
			}
			hasFailure = true
		case isInFlightCheck(check):
			hasPending = true
		}
	}

	if hasFailure {
		return "FAILURE"
	}
	if hasPending {
		return "PENDING"
	}
	return "SUCCESS"
}

// matchesAnyPattern reports whether name matches any of the path.Match globs in
// patterns. Bad globs are silently skipped here as defense-in-depth — they're
// validated and warn-logged once at config load (workspace.validateGlobs), so
// nothing malformed should reach this hot path under normal flow.
func matchesAnyPattern(name string, patterns []string) bool {
	for _, p := range patterns {
		if matched, err := path.Match(p, name); err == nil && matched {
			return true
		}
	}
	return false
}
