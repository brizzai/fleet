package session

import (
	"path/filepath"
	"testing"
	"time"
)

func openReviewTestDB(t *testing.T) *StateDB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestReviewRunCRUD(t *testing.T) {
	db := openReviewTestDB(t)
	t0 := time.UnixMilli(1_700_000_000_000)

	r1 := &ReviewRunRow{ID: "r1", Checkout: "/c1", SessionID: "s1", Worktree: "/wt", Round: 1, BaseRef: "origin/master",
		BaseSHA: "b", TreeSHA: "t1", HeadSHA: "h", CacheKey: "k1", Status: ReviewPreparing, CreatedAt: t0}
	if err := db.InsertReviewRun(r1); err != nil {
		t.Fatal(err)
	}
	// Same millisecond: the later insert must still be "latest".
	r2 := &ReviewRunRow{ID: "r2", Checkout: "/c1", Round: 1, TreeSHA: "t2", Status: ReviewPreparing, CreatedAt: t0}
	if err := db.InsertReviewRun(r2); err != nil {
		t.Fatal(err)
	}
	r3 := &ReviewRunRow{ID: "r3", Checkout: "/c2", SessionID: "s2", Round: 2, Status: ReviewReady, CreatedAt: t0.Add(time.Second)}
	if err := db.InsertReviewRun(r3); err != nil {
		t.Fatal(err)
	}

	latest, err := db.LatestReviewRun("/c1")
	if err != nil || latest == nil || latest.ID != "r2" {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
	if none, err := db.LatestReviewRun("nope"); none != nil || err != nil {
		t.Fatalf("latest of unknown = %+v, %v", none, err)
	}

	r1.Status, r1.JSON, r1.Model, r1.Account, r1.ElapsedMS = ReviewReady, `{"v":1}`, "sonnet", "a@b", 1234
	if err := db.UpdateReviewRun(r1); err != nil {
		t.Fatal(err)
	}
	got, err := db.LoadReviewRun("r1")
	if err != nil || got.Status != ReviewReady || got.JSON != `{"v":1}` || got.ElapsedMS != 1234 ||
		got.Account != "a@b" || got.BaseRef != "origin/master" || !got.CreatedAt.Equal(t0) || !got.SeenAt.IsZero() {
		t.Fatalf("reloaded = %+v, %v", got, err)
	}
	if err := db.UpdateReviewRun(&ReviewRunRow{ID: "missing"}); err == nil {
		t.Error("updating a missing run should fail")
	}

	m, err := db.ReviewRunsForCheckouts([]string{"/c1", "/c2", "/c3"})
	if err != nil || len(m) != 2 || m["/c1"].ID != "r2" || m["/c2"].ID != "r3" {
		t.Fatalf("for checkouts = %v, %v", m, err)
	}
	if m["/c1"].SessionID != "" || got.SessionID != "s1" {
		t.Errorf("target: r2 %q (none set), r1 %q", m["/c1"].SessionID, got.SessionID)
	}
	if err := db.SetReviewTarget("r2", "s9"); err != nil {
		t.Fatal(err)
	}
	if r, _ := db.LoadReviewRun("r2"); r.SessionID != "s9" {
		t.Errorf("target not set: %q", r.SessionID)
	}
	all, err := db.ReviewRunsForCheckouts(nil)
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %v, %v", all, err)
	}
	list, err := db.ListReviewRuns("/c1")
	if err != nil || len(list) != 2 || list[0].ID != "r2" || list[1].ID != "r1" {
		t.Fatalf("list = %v, %v", list, err)
	}

	seen := t0.Add(time.Minute)
	if err := db.MarkReviewSeen("r1", seen); err != nil {
		t.Fatal(err)
	}
	_ = db.MarkReviewSeen("r1", seen.Add(time.Hour)) // first opening wins
	got, _ = db.LoadReviewRun("r1")
	if !got.SeenAt.Equal(seen) {
		t.Errorf("seen = %v", got.SeenAt)
	}
}

func TestReviewAnswersAndSent(t *testing.T) {
	db := openReviewTestDB(t)
	if err := db.InsertReviewRun(&ReviewRunRow{ID: "r1", Checkout: "/c1", SessionID: "s1", Status: ReviewReady}); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.SaveReviewAnswer(ReviewAnswerRow{RunID: "r1", StopID: "s2", Answer: "change", Note: "x"}))
	must(db.SaveReviewAnswer(ReviewAnswerRow{RunID: "r1", StopID: "s1", Answer: "fine"}))
	must(db.SaveReviewAnswer(ReviewAnswerRow{RunID: "r1", StopID: "s3", Answer: "later"}))
	must(db.SaveReviewAnswer(ReviewAnswerRow{RunID: "r1", StopID: "s3", Answer: ""})) // cleared

	at := time.UnixMilli(1_700_000_123_000)
	must(db.MarkReviewSent("r1", at))
	as, err := db.LoadReviewAnswers("r1")
	if err != nil || len(as) != 2 || as[0].StopID != "s2" || as[0].Note != "x" || !as[0].SentAt.Equal(at) {
		t.Fatalf("answers = %+v, %v", as, err)
	}
	run, _ := db.LoadReviewRun("r1")
	if run.Status != ReviewSent {
		t.Errorf("status = %s", run.Status)
	}

	// Changing an answer after sending makes it unsent again.
	must(db.SaveReviewAnswer(ReviewAnswerRow{RunID: "r1", StopID: "s1", Answer: "change", Note: "on second thought"}))
	as, _ = db.LoadReviewAnswers("r1")
	if as[1].Answer != "change" || !as[1].SentAt.IsZero() {
		t.Errorf("re-answered = %+v", as[1])
	}

	must(db.MarkReviewApproved("r1"))
	run, _ = db.LoadReviewRun("r1")
	if run.Status != ReviewApproved {
		t.Errorf("status = %s", run.Status)
	}
}

// Review history belongs to the checkout: it survives SaveSession (an INSERT
// OR REPLACE) and DeleteSession — which only forgets the session as a target —
// and goes with DeleteReviewRunsForCheckout (the review row's discard).
func TestReviewRowsBelongToTheCheckout(t *testing.T) {
	db := openReviewTestDB(t)
	row := &SessionRow{ID: "s1", CreatedAt: time.Now(), LastAccessed: time.Now()}
	if err := db.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertReviewRun(&ReviewRunRow{ID: "r1", Checkout: "/c1", SessionID: "s1", Status: ReviewReady}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveReviewAnswer(ReviewAnswerRow{RunID: "r1", StopID: "s1", Answer: "fine"}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertReviewRun(&ReviewRunRow{ID: "other", Checkout: "/c2", SessionID: "s2", Status: ReviewReady}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertReviewRun(&ReviewRunRow{ID: "nochk", SessionID: "s2"}); err == nil {
		t.Error("a run with no checkout must be refused")
	}

	if err := db.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSession("s1"); err != nil {
		t.Fatal(err)
	}
	r, _ := db.LatestReviewRun("/c1")
	if r == nil || r.SessionID != "" {
		t.Fatalf("DeleteSession must keep the run and clear its target: %+v", r)
	}
	if as, _ := db.LoadReviewAnswers("r1"); len(as) != 1 {
		t.Error("DeleteSession took the run's answers")
	}

	if err := db.DeleteReviewRunsForCheckout("/c1"); err != nil {
		t.Fatal(err)
	}
	if r, _ := db.LatestReviewRun("/c1"); r != nil {
		t.Error("discard left a run behind")
	}
	if as, _ := db.LoadReviewAnswers("r1"); len(as) != 0 {
		t.Error("discard left answers behind")
	}
	if r, _ := db.LatestReviewRun("/c2"); r == nil {
		t.Error("discard took another checkout's runs")
	}
}

// A database from before runs were keyed by checkout gains the column, and its
// rows are placed under their worktree's repo root.
func TestReviewRunsMigrateToCheckout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()
	if _, err := db.db.Exec(`INSERT INTO review_runs (id, checkout, session_id, worktree, status, created_at)
		VALUES ('old', '', 's1', ?, 'ready', 1)`, wt); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r, err := db.LoadReviewRun("old")
	if err != nil || r == nil || r.Checkout != GetRepoRoot(wt) {
		t.Fatalf("migrated = %+v, %v (want checkout %q)", r, err, GetRepoRoot(wt))
	}
}
