package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/brizzai/fleet/internal/debuglog"
)

// prFilesTimeout is longer than ghTimeout: this pages through every changed
// file and carries the patch text, so a big PR is a real download rather than
// a metadata lookup.
const prFilesPerPage = 100

// PRFile is one changed file in a pull request.
type PRFile struct {
	Path      string
	Previous  string // set on a rename
	Status    string // added, modified, removed, renamed, copied, changed
	Additions int
	Deletions int
	// Patch is the unified diff for this file. Empty for binaries, and for
	// files GitHub declines to diff because they are too large — both are
	// real states the reader has to render rather than treat as absent.
	Patch string
}

// PRFiles is a pull request's changed files, and the commit they describe.
type PRFiles struct {
	Files []PRFile
	Total int
	// HeadSHA is the commit these patches were read at, and the only honest
	// anchor for a review comment written against them. It is fetched here
	// rather than taken from the review queue because the queue is a cache of a
	// different age, and because a pull request drops out of it entirely once
	// you have reviewed it — leaving the anchor empty exactly when someone goes
	// back to add a second round of comments.
	HeadSHA string
}

type ghRESTFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Patch            string `json:"patch"`
}

// FetchPRFiles lists a pull request's changed files with their patches.
//
// REST rather than GraphQL: GraphQL's file connection serves no patch field at
// all, so the diff can only come from here. One paginated call carries the
// whole changeset — measured at ~70KB for a 34-file PR, which is cheaper than
// the round trips a per-file fetch would cost.
//
// Viewed state is deliberately NOT read. fleet keeps its own record of what you
// have read, against the commit you read it at, so it can answer "this changed
// since you looked" — a question GitHub's own checkbox cannot represent, since
// it collapses to a bare viewed/unviewed and resets silently on a push.
func FetchPRFiles(ctx context.Context, ownerRepo string, pr int) (PRFiles, error) {
	if !strings.Contains(ownerRepo, "/") {
		return PRFiles{}, fmt.Errorf("bad repo %q, want owner/name", ownerRepo)
	}

	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()

	// The head SHA is read FIRST, and the ordering is the whole point.
	//
	// No endpoint serves the patches and the commit together — the files route
	// carries no SHA, and GraphQL, which does, serves no patch text — so there
	// is a window between the two calls in which the author can push. Reading
	// the SHA first means that window leaves us with an OLD sha and NEW line
	// numbers, which GitHub renders as an outdated comment: visibly wrong, and
	// an invitation to look again. The other order leaves a new sha with old
	// line numbers, which lands silently on whatever code is at that line now.
	//
	// Both are wrong; only one says so. The window is about a second, and it is
	// dwarfed by the minutes the reader then spends holding these patches while
	// the author keeps working — which is the staleness this SHA exists to
	// record.
	headSHA, err := fetchPRHead(ctx, ownerRepo, pr)
	if err != nil {
		// Not fatal: an absent anchor means GitHub falls back to the current
		// head, which is what it did before this was recorded at all. A diff
		// you can read beats a diff withheld over a missing commit id.
		debuglog.Logger.Debug("pr files: head sha unavailable", "pr", pr, "error", err)
	}

	endpoint := fmt.Sprintf("repos/%s/pulls/%d/files?per_page=%d", ownerRepo, pr, prFilesPerPage)
	cmd := exec.CommandContext(ctx, "gh", "api", "--paginate", endpoint)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		if ctx.Err() == context.DeadlineExceeded {
			trackGHFailure("timeout")
			return PRFiles{}, fmt.Errorf("gh timed out")
		}
		if ghErr := classifyGHError(stderrStr); ghErr != nil {
			trackGHFailure("rate_limit")
			return PRFiles{}, ghErr
		}
		debuglog.Logger.Debug("pr files: gh error", "pr", pr, "stderr", stderrStr)
		return PRFiles{}, fmt.Errorf("gh api: %w", err)
	}

	// --paginate concatenates one JSON array per page, so a multi-page result
	// is a stream of arrays rather than one array. A plain Unmarshal reads the
	// first page and silently drops the rest — which on a large PR looks like
	// a short diff rather than an error.
	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var out PRFiles
	for {
		var page []ghRESTFile
		if err := dec.Decode(&page); err != nil {
			if err.Error() == "EOF" {
				break
			}
			debuglog.Logger.Debug("pr files: JSON parse failed", "pr", pr, "error", err)
			return PRFiles{}, fmt.Errorf("parse gh output: %w", err)
		}
		for _, f := range page {
			out.Files = append(out.Files, PRFile{
				Path:      f.Filename,
				Previous:  f.PreviousFilename,
				Status:    f.Status,
				Additions: f.Additions,
				Deletions: f.Deletions,
				Patch:     f.Patch,
			})
		}
	}
	out.Total = len(out.Files)
	out.HeadSHA = headSHA

	debuglog.Logger.Debug("pr files: ok", "pr", pr, "files", out.Total, "head", headSHA)
	return out, nil
}

// fetchPRHead reads the commit a pull request currently points at.
//
// Parsed in Go rather than through gh's --jq, matching every other call in this
// package: one way to read a response is one place for it to be wrong.
func fetchPRHead(ctx context.Context, ownerRepo string, pr int) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "api", fmt.Sprintf("repos/%s/pulls/%d", ownerRepo, pr))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gh api: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	var resp struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return "", fmt.Errorf("parse gh output: %w", err)
	}
	return resp.Head.SHA, nil
}

// PRNumberString is a small helper for callers building paths and titles.
func PRNumberString(pr int) string { return strconv.Itoa(pr) }
