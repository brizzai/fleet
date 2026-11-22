package ui

import (
	"testing"

	"github.com/brizzai/fleet/internal/github"
)

// The sidebar renders an approved-and-green PR GitHub won't merge as a bare
// yellow #N, so the preview line is where the reason is spelled out — and
// only when nothing more specific (pending CI, a missing review) already is.
func TestPreviewPRSummaryNamesAMergeBlock(t *testing.T) {
	cases := []struct {
		name string
		pr   *github.PR
		want string
	}{
		{"blocked", &github.PR{Number: 1, State: "OPEN", ReviewDecision: "APPROVED", CIStatus: "SUCCESS", MergeStateStatus: "BLOCKED"}, "PR #1 (blocked by branch rules)"},
		{"behind", &github.PR{Number: 1, State: "OPEN", ReviewDecision: "APPROVED", CIStatus: "SUCCESS", MergeStateStatus: "BEHIND"}, "PR #1 (behind base)"},
		{"clean", &github.PR{Number: 1, State: "OPEN", ReviewDecision: "APPROVED", CIStatus: "SUCCESS", MergeStateStatus: "CLEAN"}, "PR #1 (ready)"},
		{"blocked while CI pending says CI", &github.PR{Number: 1, State: "OPEN", ReviewDecision: "APPROVED", CIStatus: "PENDING", MergeStateStatus: "BLOCKED"}, "PR #1 (CI pending)"},
		{"blocked awaiting review says review", &github.PR{Number: 1, State: "OPEN", ReviewDecision: "REVIEW_REQUIRED", CIStatus: "SUCCESS", MergeStateStatus: "BLOCKED"}, "PR #1 (review pending)"},
	}
	for _, tc := range cases {
		if got := previewPRSummary(tc.pr); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
