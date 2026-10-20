package github

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// GitHub requires a body for two of the three events and not the third. Getting
// this backwards means either a 422 the user sees only after pressing submit, or
// a refusal on the one review you leave with nothing to say.
func TestNeedsBodyMatchesTheEndpoint(t *testing.T) {
	for ev, want := range map[ReviewEvent]bool{
		EventComment:        true,
		EventRequestChanges: true,
		EventApprove:        false,
	} {
		if got := ev.NeedsBody(); got != want {
			t.Errorf("%s.NeedsBody() = %v, want %v", ev, got, want)
		}
	}
}

// The check runs before the subprocess, so a missing summary never costs a
// round trip and never arrives as GitHub's own wording for our omission.
func TestSubmitRefusesAnEmptyCommentBodyWithoutCallingGH(t *testing.T) {
	err := SubmitReview(context.Background(), "brizzai/fleet", 1,
		ReviewSubmission{Event: EventComment, Body: "   "})
	if err == nil {
		t.Fatal("a COMMENT review with a whitespace body was accepted")
	}
	if !strings.Contains(err.Error(), "summary") {
		t.Errorf("error %q does not name the missing summary", err)
	}
}

func TestSubmitRejectsABadRepo(t *testing.T) {
	if err := SubmitReview(context.Background(), "fleet", 1,
		ReviewSubmission{Event: EventApprove}); err == nil {
		t.Fatal("a repo with no owner was accepted")
	}
}

// The payload is what GitHub reads, so its field names are part of the contract.
func TestSubmissionEncodesTheFieldsGitHubExpects(t *testing.T) {
	b, err := json.Marshal(ReviewSubmission{
		CommitID: "deadbeef",
		Body:     "looks good",
		Event:    EventApprove,
		Comments: []ReviewComment{{Path: "a.go", Line: 4, Side: "RIGHT", Body: "nit: x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"commit_id":"deadbeef"`, `"event":"APPROVE"`,
		`"path":"a.go"`, `"line":4`, `"side":"RIGHT"`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("payload is missing %s\ngot: %s", want, b)
		}
	}

	// An empty comment list must be absent rather than null: the endpoint reads
	// a present-but-null comments array as a malformed request.
	b, _ = json.Marshal(ReviewSubmission{Event: EventApprove})
	if strings.Contains(string(b), "comments") {
		t.Errorf("an approve with no comments still sends a comments field: %s", b)
	}
}

// GitHub's top-level message is always "Validation Failed"; the per-field entry
// is the one that says what to do about it.
func TestAPIErrorReasonPrefersTheFieldEntry(t *testing.T) {
	body := []byte(`{"message":"Validation Failed","errors":[{"resource":"PullRequestReviewComment","field":"line","code":"custom","message":"line must be part of the diff"}]}`)
	if got := apiErrorReason(body, "gh: Validation Failed (HTTP 422)"); got != "line must be part of the diff" {
		t.Errorf("reason = %q, want the per-field diagnosis", got)
	}

	// A field entry with no prose still beats "Validation Failed".
	body = []byte(`{"message":"Validation Failed","errors":[{"field":"commit_id","code":"invalid"}]}`)
	if got := apiErrorReason(body, ""); got != "commit_id: invalid" {
		t.Errorf("reason = %q, want the field and its code", got)
	}

	// Nothing parseable: fall back to gh's own first line, not a wall of HTML.
	if got := apiErrorReason([]byte("<html>"), "gh: Not Found (HTTP 404)\nsomething else"); got != "gh: Not Found (HTTP 404)" {
		t.Errorf("reason = %q, want gh's first line", got)
	}
}
