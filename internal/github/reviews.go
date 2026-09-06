package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/brizzai/fleet/internal/debuglog"
)

const (
	// reviewSearchLimit is GraphQL search's per-page maximum. A queue longer
	// than 100 is a different problem than a missing row.
	reviewSearchLimit = 100

	// reviewCacheTTL keeps repeated `c` presses free. The queue changes on
	// human timescales — someone opening a PR, someone pushing — so a minute
	// of staleness costs nothing and a refetch per keypress costs quota.
	reviewCacheTTL = 60 * time.Second
)

// reviewsQuery asks for the queue in ONE GraphQL call.
//
// GraphQL rather than `gh search prs`, which is the same data over the REST
// search endpoint, because the two draw on completely different budgets:
// REST search allows 30 requests per MINUTE — the scarcest bucket GitHub has,
// and one that a few keypresses exhaust — while GraphQL allows 5000 points per
// HOUR and this query costs one. It also serves fields REST search simply does
// not have: additions, deletions, changedFiles and reviewDecision.
const reviewsQuery = `
query($q: String!, $limit: Int!) {
  search(query: $q, type: ISSUE, first: $limit) {
    nodes {
      ... on PullRequest {
        number
        title
        url
        isDraft
        headRefOid
        updatedAt
        additions
        deletions
        changedFiles
        reviewDecision
        author { login __typename }
        repository { nameWithOwner }
      }
    }
  }
}`

// ReviewRequest is one open pull request waiting on this user's review.
type ReviewRequest struct {
	Number       int
	Title        string
	URL          string
	Repo         string // owner/name
	Author       string
	IsBot        bool
	Additions    int
	Deletions    int
	ChangedFiles int
	Decision     string // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, ""
	UpdatedAt    time.Time
	IsDraft      bool
	// HeadSHA is the commit the diff was read at. Carried on the queue rather
	// than fetched at submit time because it rides this query for free, and a
	// second round trip to learn it would sit between pressing submit and the
	// review landing.
	HeadSHA string
}

type ghReviewNode struct {
	Number       int       `json:"number"`
	Title        string    `json:"title"`
	URL          string    `json:"url"`
	IsDraft      bool      `json:"isDraft"`
	HeadRefOid   string    `json:"headRefOid"`
	UpdatedAt    time.Time `json:"updatedAt"`
	Additions    int       `json:"additions"`
	Deletions    int       `json:"deletions"`
	ChangedFiles int       `json:"changedFiles"`
	Decision     string    `json:"reviewDecision"`
	Author       struct {
		Login    string `json:"login"`
		TypeName string `json:"__typename"`
	} `json:"author"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

type ghReviewsResponse struct {
	Data struct {
		Search struct {
			Nodes []ghReviewNode `json:"nodes"`
		} `json:"search"`
	} `json:"data"`
}

var (
	reviewCacheMu   sync.Mutex
	reviewCache     []ReviewRequest
	reviewCacheAt   time.Time
	reviewCacheOnce bool
)

// InvalidateReviewsCache drops the cached queue so the next fetch is live.
func InvalidateReviewsCache() {
	reviewCacheMu.Lock()
	defer reviewCacheMu.Unlock()
	reviewCacheOnce = false
}

// ReviewsRequested lists the open PRs that have asked for this user's review,
// most recently updated first.
//
// It is a search rather than a per-repo walk on purpose: review requests
// arrive from repos fleet has never opened a session in, so a queue built by
// iterating pinned repos would be silently short — which is the exact failure
// this surface exists to fix.
//
// Results are cached for reviewCacheTTL. Opening the tab is a keypress a user
// repeats, and each open must not cost a round trip.
func ReviewsRequested(ctx context.Context) ([]ReviewRequest, error) {
	reviewCacheMu.Lock()
	if reviewCacheOnce && time.Since(reviewCacheAt) < reviewCacheTTL {
		cached := reviewCache
		reviewCacheMu.Unlock()
		debuglog.Logger.Debug("reviews fetch: served from cache", "total", len(cached))
		return cached, nil
	}
	reviewCacheMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gh", "api", "graphql",
		"-f", "q=is:open is:pr review-requested:@me sort:updated-desc",
		"-F", fmt.Sprintf("limit=%d", reviewSearchLimit),
		"-f", "query="+reviewsQuery,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		if ctx.Err() == context.DeadlineExceeded {
			debuglog.Logger.Debug("reviews fetch: timed out")
			trackGHFailure("timeout")
			return nil, fmt.Errorf("gh timed out")
		}
		if ghErr := classifyGHError(stderrStr); ghErr != nil {
			debuglog.Logger.Debug("reviews fetch: rate-limited", "stderr", stderrStr)
			trackGHFailure("rate_limit")
			return nil, ghErr
		}
		debuglog.Logger.Debug("reviews fetch: gh error", "stderr", stderrStr)
		return nil, fmt.Errorf("gh api graphql: %w", err)
	}

	var resp ghReviewsResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		debuglog.Logger.Debug("reviews fetch: JSON parse failed", "error", err)
		return nil, fmt.Errorf("parse gh output: %w", err)
	}

	out := make([]ReviewRequest, 0, len(resp.Data.Search.Nodes))
	for _, n := range resp.Data.Search.Nodes {
		// type ISSUE returns issues alongside PRs; the inline fragment leaves
		// those as zero values, and a row numbered 0 is not a pull request.
		if n.Number == 0 {
			continue
		}
		out = append(out, ReviewRequest{
			Number: n.Number,
			Title:  n.Title,
			URL:    n.URL,
			Repo:   n.Repository.NameWithOwner,
			Author: n.Author.Login,
			// __typename is GitHub's own answer and is authoritative. The
			// login suffix is kept as a fallback for the rare App that does
			// not report as Bot — REST search's is_bot field, by contrast, is
			// useless here: it reads false for dependabot[bot], brizz-bot[bot]
			// and sentry[bot] alike, so a fold trusting it folds nothing.
			IsBot:        n.Author.TypeName == "Bot" || strings.HasSuffix(n.Author.Login, "[bot]"),
			Additions:    n.Additions,
			Deletions:    n.Deletions,
			ChangedFiles: n.ChangedFiles,
			Decision:     n.Decision,
			UpdatedAt:    n.UpdatedAt,
			IsDraft:      n.IsDraft,
			HeadSHA:      n.HeadRefOid,
		})
	}

	reviewCacheMu.Lock()
	reviewCache = out
	reviewCacheAt = time.Now()
	reviewCacheOnce = true
	reviewCacheMu.Unlock()

	debuglog.Logger.Debug("reviews fetch: ok", "total", len(out), "bots", countBots(out))
	return out, nil
}

func countBots(rs []ReviewRequest) int {
	n := 0
	for _, r := range rs {
		if r.IsBot {
			n++
		}
	}
	return n
}
