package session

import (
	"testing"
)

// PR #12 exists in most repositories. A store that keyed on the number alone
// would hand one repo's queued comments to another repo's pull request — the
// failure this table's composite key exists to make impossible.
func TestReviewCommentsAreScopedToTheRepo(t *testing.T) {
	db := openTestDB(t)

	if err := db.SaveReviewComments("brizzai/fleet", 12, []ReviewComment{
		{Repo: "brizzai/fleet", PR: 12, Path: "a.go", Line: 3, Kind: "NIT", Body: "fleet"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveReviewComments("other/repo", 12, []ReviewComment{
		{Repo: "other/repo", PR: 12, Path: "b.go", Line: 9, Kind: "ISSUE", Body: "other"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := db.LoadReviewComments()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("loaded %d comments, want 2 — the second save overwrote the first", len(got))
	}
	byRepo := map[string]ReviewComment{}
	for _, c := range got {
		byRepo[c.Repo] = c
	}
	if byRepo["brizzai/fleet"].Body != "fleet" || byRepo["other/repo"].Body != "other" {
		t.Errorf("comments crossed repos: %+v", byRepo)
	}
}

// The reader is where a comment is edited and deleted, so a save is a
// replacement. Appending would resurrect every comment the user threw away and
// hand it to the next submit.
func TestSaveReviewCommentsReplaces(t *testing.T) {
	db := openTestDB(t)
	repo, pr := "brizzai/fleet", 283

	for _, body := range []string{"first", "second"} {
		if err := db.SaveReviewComments(repo, pr, []ReviewComment{
			{Repo: repo, PR: pr, Path: "a.go", Line: 1, Kind: "ISSUE", Body: body},
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.LoadReviewComments()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "second" {
		t.Fatalf("got %+v, want exactly the latest set", got)
	}

	// An empty set is a real state — every comment deleted — and must clear the
	// table rather than being ignored as "nothing to write".
	if err := db.SaveReviewComments(repo, pr, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.LoadReviewComments(); len(got) != 0 {
		t.Errorf("deleting every comment left %d behind", len(got))
	}
}

// Everything a comment carries has to survive the trip, including the head SHA
// that says which commit its line numbers refer to.
func TestReviewCommentRoundTrip(t *testing.T) {
	db := openTestDB(t)
	want := ReviewComment{
		Repo: "brizzai/fleet", PR: 283, Path: "internal/ui/reader.go", Line: 412,
		Kind: "QUESTION", Body: "why RIGHT here?", HeadSHA: "deadbeef",
	}
	if err := db.SaveReviewComments(want.Repo, want.PR, []ReviewComment{want}); err != nil {
		t.Fatal(err)
	}
	got, err := db.LoadReviewComments()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("loaded %d comments, want 1", len(got))
	}
	if got[0] != want {
		t.Errorf("round trip changed the comment:\n got %+v\nwant %+v", got[0], want)
	}
}

func TestReviewReadMarksRoundTripAndReplace(t *testing.T) {
	db := openTestDB(t)
	repo, pr := "brizzai/fleet", 283

	if err := db.SaveReviewRead(repo, pr, "deadbeef", []string{"a.go", "b.go"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.LoadReviewRead()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("loaded %d marks, want 2", len(got))
	}
	if got[0].HeadSHA != "deadbeef" {
		t.Errorf("head SHA lost (%q) — it is what a later pass needs to tell "+
			"what moved since you read the file", got[0].HeadSHA)
	}

	// Un-marking a file has to remove it, not leave it read forever.
	if err := db.SaveReviewRead(repo, pr, "deadbeef", []string{"a.go"}); err != nil {
		t.Fatal(err)
	}
	got, _ = db.LoadReviewRead()
	if len(got) != 1 || got[0].Path != "a.go" {
		t.Errorf("got %+v, want only a.go", got)
	}
}

// A repo with no name, or a PR numbered zero, is not something to write a row
// about — and a zero key would collide with every other one.
func TestReviewSavesIgnoreAnEmptyKey(t *testing.T) {
	db := openTestDB(t)
	if err := db.SaveReviewComments("", 1, []ReviewComment{{Body: "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveReviewRead("brizzai/fleet", 0, "", []string{"a.go"}); err != nil {
		t.Fatal(err)
	}
	if cs, _ := db.LoadReviewComments(); len(cs) != 0 {
		t.Errorf("wrote %d comments for an unnamed repo", len(cs))
	}
	if ms, _ := db.LoadReviewRead(); len(ms) != 0 {
		t.Errorf("wrote %d read marks for PR 0", len(ms))
	}
}
