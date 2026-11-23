package selfreview

import "testing"

// The one definition, pinned on the real DELTA-2524 run: the numbers the CLI,
// the sidebar row and the Review view header all print.
func TestCountsOnTheRealRun(t *testing.T) {
	run := loadRunFixture(t, "delta2524_run.json")
	c := CountsOf(run, nil)
	if c.Decisions != 4 || c.Unanswered != 4 || c.NotReviewed != 0 || c.SafeToSkip != 14 {
		t.Errorf("counts = %+v", c)
	}
	for got, want := range map[string]string{
		c.ToDecide():        "4 to decide",
		c.LinesNeedYou():    "106 of 1,489 changed lines need you",
		c.SafeToSkipLabel(): "14 files safe to skip",
		c.Progress():        "0 of 4 answered",
	} {
		if got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}

	c = CountsOf(run, []Answer{
		{StopID: "s1", Kind: AnswerChange, Note: "x"}, {StopID: "s2", Kind: AnswerFine},
		{StopID: "s3", Kind: AnswerLater}, {StopID: "gone", Kind: AnswerFine},
		{StopID: FileStopID("README.md"), Kind: AnswerChange, Note: "y"},
	})
	if c.Answered != 3 || c.Unanswered != 1 || c.Changes != 2 || c.OK != 1 || c.Later != 1 || c.AddedByYou != 1 || c.Acting() != 2 {
		t.Errorf("answered counts = %+v", c)
	}
	if c.ToDecide() != "1 to decide" || c.Progress() != "3 of 4 answered" {
		t.Errorf("%q / %q", c.ToDecide(), c.Progress())
	}
	if got := c.AnswerSummary(); got != "2 changes, 1 ok, 1 left for later" {
		t.Errorf("summary = %q", got)
	}
	// Not reviewed files are never decisions.
	run.NotCovered = []string{"go.mod", "README.md"}
	if c := CountsOf(run, nil); c.Decisions != 4 || c.NotReviewed != 2 || c.NotReviewedLabel() != "2 files not reviewed" {
		t.Errorf("not reviewed = %+v", c)
	}
}
