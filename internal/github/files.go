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

// PRFiles is a pull request's changed files.
type PRFiles struct {
	Files []PRFile
	Total int
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

	debuglog.Logger.Debug("pr files: ok", "pr", pr, "files", out.Total)
	return out, nil
}

// PRNumberString is a small helper for callers building paths and titles.
func PRNumberString(pr int) string { return strconv.Itoa(pr) }
