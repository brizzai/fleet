package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/brizzai/fleet/internal/debuglog"
)

// ReviewEvent is what submitting the batch does to the pull request.
//
// The three GitHub offers, and all three ship: approving is the outcome of most
// reviews, and a reader that could only ever leave notes would still send you
// to the browser for the one keystroke that ends the review.
type ReviewEvent string

const (
	EventComment        ReviewEvent = "COMMENT"
	EventApprove        ReviewEvent = "APPROVE"
	EventRequestChanges ReviewEvent = "REQUEST_CHANGES"
)

// NeedsBody reports whether GitHub refuses this event without a summary.
//
// Documented on the endpoint: body is required for COMMENT and REQUEST_CHANGES.
// Checking it here rather than letting the API answer is the difference between
// a footer that says what is missing and a 422 after you pressed submit.
func (e ReviewEvent) NeedsBody() bool {
	return e == EventComment || e == EventRequestChanges
}

// ReviewComment is one inline note bound for a line of the head.
type ReviewComment struct {
	Path string `json:"path"`
	// Line is the line number in the file, not an offset into the diff.
	// GitHub's older position field counted hunk rows, which meant a comment's
	// address changed whenever the diff around it did.
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

// ReviewSubmission is one whole review: the verdict, the summary, and every
// inline comment, in a single call.
//
// One call and not one per comment, because a review is atomic on GitHub: N
// separate comment POSTs arrive as N notifications and leave a half-posted
// review behind if the third one fails.
type ReviewSubmission struct {
	// CommitID anchors the review to the commit that was actually read. Left
	// empty GitHub defaults to the current head — which silently re-points
	// every line number at code we never saw if the author pushed while the
	// reader was open. Sending it means an overtaken review renders as
	// outdated, which is true, instead of as a comment on the wrong line.
	CommitID string          `json:"commit_id,omitempty"`
	Body     string          `json:"body,omitempty"`
	Event    ReviewEvent     `json:"event"`
	Comments []ReviewComment `json:"comments,omitempty"`
}

// ghAPIError is the error body GitHub returns, which gh prints to stdout.
type ghAPIError struct {
	Message string `json:"message"`
	Errors  []struct {
		Resource string `json:"resource"`
		Field    string `json:"field"`
		Code     string `json:"code"`
		Message  string `json:"message"`
	} `json:"errors"`
}

// SubmitReview posts a review to a pull request.
//
// The body travels on STDIN rather than as -f flags: the payload nests an array
// of comments, which gh's field flags cannot express at all, and a comment is
// arbitrary text the user typed — argv is world-readable through ps, and a
// review of a private repo is not something to publish to every process on the
// machine.
func SubmitReview(ctx context.Context, ownerRepo string, pr int, sub ReviewSubmission) error {
	if !strings.Contains(ownerRepo, "/") {
		return fmt.Errorf("bad repo %q, want owner/name", ownerRepo)
	}
	if sub.Event.NeedsBody() && strings.TrimSpace(sub.Body) == "" {
		return fmt.Errorf("a %s review needs a summary", strings.ToLower(string(sub.Event)))
	}

	payload, err := json.Marshal(sub)
	if err != nil {
		return fmt.Errorf("encode review: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()

	endpoint := fmt.Sprintf("repos/%s/pulls/%d/reviews", ownerRepo, pr)
	cmd := exec.CommandContext(ctx, "gh", "api", "--method", "POST", endpoint, "--input", "-")
	cmd.Stdin = bytes.NewReader(payload)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			trackGHFailure("timeout")
			return fmt.Errorf("gh timed out — the review was not submitted")
		}
		stderrStr := strings.TrimSpace(stderr.String())
		if ghErr := classifyGHError(stderrStr); ghErr != nil {
			trackGHFailure("rate_limit")
			return ghErr
		}
		reason := apiErrorReason(stdout.Bytes(), stderrStr)
		debuglog.Logger.Debug("review submit: failed", "pr", pr, "event", sub.Event,
			"comments", len(sub.Comments), "reason", reason)
		return fmt.Errorf("%s", reason)
	}

	// The queue is now wrong: an approved PR leaves review-requested:@me, and
	// the cached copy would keep offering it until the TTL expires.
	InvalidateReviewsCache()

	debuglog.Logger.Debug("review submit: ok", "pr", pr, "event", sub.Event, "comments", len(sub.Comments))
	return nil
}

// apiErrorReason turns GitHub's error body into one line worth showing.
//
// The per-field entry is preferred over the top-level message because the top
// level is always the useless "Validation Failed" while the entry is the actual
// diagnosis — most often that a line is not part of the diff, which is a thing
// the user can act on.
func apiErrorReason(body []byte, stderrStr string) string {
	var e ghAPIError
	if err := json.Unmarshal(body, &e); err == nil {
		for _, item := range e.Errors {
			if item.Message != "" {
				return item.Message
			}
			if item.Field != "" {
				return fmt.Sprintf("%s: %s", item.Field, item.Code)
			}
		}
		if e.Message != "" {
			return e.Message
		}
	}
	if stderrStr != "" {
		return firstLine(stderrStr)
	}
	return "gh api failed"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
