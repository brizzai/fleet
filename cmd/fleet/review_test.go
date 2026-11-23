package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/reviewjob"
	"github.com/brizzai/fleet/internal/selfreview"
	"github.com/brizzai/fleet/internal/session"
)

func TestParseReviewArgs(t *testing.T) {
	o, err := parseReviewArgs([]string{"api", "--json"})
	if err != nil || o.selector != "api" || !o.json || o.timeout != 10*time.Minute {
		t.Fatalf("o=%+v err=%v", o, err)
	}
	if o, err := parseReviewArgs([]string{"--show", "--round", "2", "@1", "--wait"}); err != nil || o.round != 2 || !o.wait {
		t.Fatalf("o=%+v err=%v", o, err)
	}
	for _, bad := range [][]string{
		{},
		{"a", "b"},
		{"a", "--round", "2"},
		{"a", "--wait"},
		{"a", "--show", "--regenerate"},
		{"a", "--show", "--round", "9"},
		{"a", "--timeout", "0s"},
	} {
		if _, err := parseReviewArgs(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func sampleRecord(status string) *reviewjob.Record {
	run := &selfreview.Run{
		Verdict: "Safe after one product call: closed findings now kept for every org.",
		Stops: []selfreview.Stop{
			{ID: "s1", Kind: "product", Title: "Closures kept for all orgs", Decide: "Keep closed findings for orgs with closures off?",
				Hunks: []selfreview.Hunk{{Path: "services/vulns/closures/store.py", Start: 88, End: 92}}},
			{ID: "s2", Kind: "tuning", Title: "Full snapshot every 12h", Decide: "Is a full snapshot every 12h OK?",
				Hunks: []selfreview.Hunk{{Path: "libs/schemas/pipelines.py", Start: 270, End: 270}}},
		},
		Pending: []string{"CI running"}, NotCovered: []string{"go.mod", "ingest.py"},
		NotCoveredWhy: map[string]string{"ingest.py": "skipped as 'covered by tests' — 152 changed lines of logic"},
		TotalLines:    1283, StopLines: 251, SkippedFiles: 17,
		Reviewer: &selfreview.ReviewerMeta{Turns: 9, CostUSD: 0.81, DurationMS: 74213,
			Tools: map[string]int{"Read": 11, "Grep": 2, "Glob": 1}, Files: []string{"a.py"}, FilesRead: 9},
	}
	return &reviewjob.Record{
		Row: &session.ReviewRunRow{ID: "r1", Checkout: "/wt", SessionID: "s", Round: 1, TreeSHA: "3f2a1c9d8e7b", BaseRef: "origin/master",
			Model: "opus", ElapsedMS: 74000, Status: status},
		Status:   status,
		Run:      run,
		Answers:  []selfreview.Answer{{StopID: "s1", Kind: "fine"}},
		Snapshot: reviewjob.Snapshot{Dirty: true},
	}
}

func TestPrintReview(t *testing.T) {
	var b bytes.Buffer
	rec := sampleRecord(session.ReviewReady)
	printReview(&b, "vulns cursor", rec)
	out := b.String()
	for _, want := range []string{
		"── vulns cursor · round 1 · tree 3f2a1c9 (uncommitted) vs origin/master · opus · 74s · 9 files read, 3 searches, 9 turns, $0.81 · ready",
		"1 to decide · 251 of 1,283 changed lines need you · 0 files safe to skip · 2 files not reviewed",
		" 1. Closures kept for all orgs  (product call)  [ok as is]",
		" 2. Full snapshot every 12h  (tuning)\n",
		"    Decide: Is a full snapshot every 12h OK?",
		"    libs/schemas/pipelines.py:270",
		"    services/vulns/closures/store.py:88-92",
		"Not verified yet: CI running",
		"Not reviewed: go.mod\n",
		"Not reviewed: ingest.py — skipped as 'covered by tests' — 152 changed lines of logic",
		"the Review row under the checkout",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if code := reviewExitCode(rec); code != reviewExitDecide {
		t.Errorf("exit = %d", code)
	}
	rec.Answers = append(rec.Answers, selfreview.Answer{StopID: "s2", Kind: "later"})
	if code := reviewExitCode(rec); code != reviewExitOK {
		t.Errorf("all answered: exit = %d", code)
	}
	if code := reviewExitCode(sampleRecord(session.ReviewFailed)); code != reviewExitFailed {
		t.Errorf("failed: exit = %d", code)
	}
	if code := reviewExitCode(sampleRecord(session.ReviewPreparing)); code != reviewExitPending {
		t.Errorf("preparing: exit = %d", code)
	}
	j := reviewJSONOf(sampleRecord(session.ReviewReady))
	if !j.NeedsYou || j.Unanswered != 1 || j.Review == nil || j.Reviewer == nil || j.Reviewer.FilesRead != 9 {
		t.Errorf("json = %+v", j)
	}
	raw, _ := json.Marshal(j)
	for _, want := range []string{`"checkout":"/wt"`, `"target":"s"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("--json lacks %s:\n%s", want, raw)
		}
	}
	for _, want := range []string{`"reviewer":{"turns":9,"cost_usd":0.81,"duration_ms":74213,"tools":{"Glob":1,"Grep":2,"Read":11},"files":["a.py"],"files_read":9}`, `"not_covered_why":{"ingest.py":`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("--json lacks %s:\n%s", want, raw)
		}
	}
}

// `fleet review` names a checkout three ways — a path, a branch, a session —
// and two sessions in one worktree never make their branch ambiguous.
func TestResolveReviewCheckout(t *testing.T) {
	rows := []*session.SessionRow{
		{ID: "aaaa1111-1", Title: "api", ProjectPath: "/repo-wt/sub"},
		{ID: "bbbb2222-2", Title: "api tests", ProjectPath: "/repo-wt"},
		{ID: "cccc3333-3", Title: "main work", ProjectPath: "/repo"},
	}
	io := reviewResolvers{
		branchOf: func(p string) string {
			return map[string]string{"/repo-wt": "feat/x", "/repo": "master", "/pinned": "side", "/dup": "master"}[p]
		},
		rootOf: func(p string) string {
			if strings.HasPrefix(p, "/repo-wt") {
				return "/repo-wt"
			}
			return p
		},
		isDir: func(p string) bool { return p == "/repo-wt/sub" || p == "/pinned" },
	}
	for _, c := range []struct{ sel, want string }{
		{"feat/x", "/repo-wt"},       // branch (with a slash) of a checkout with two sessions
		{"side", "/pinned"},          // branch of a pinned repo with no session
		{"api", "/repo-wt"},          // session title → its checkout
		{"cccc3333", "/repo"},        // id prefix
		{"/repo-wt/sub", "/repo-wt"}, // path → its root
	} {
		got, err := resolveReviewCheckout(rows, nil, []string{"/pinned"}, c.sel, io)
		if err != nil || got != c.want {
			t.Errorf("%q → %q, %v; want %q", c.sel, got, err, c.want)
		}
	}
	if _, err := resolveReviewCheckout(rows, nil, []string{"/dup"}, "master", io); err == nil ||
		!strings.Contains(err.Error(), "2 checkouts") {
		t.Errorf("a branch in two checkouts must be refused: %v", err)
	}
	if _, err := resolveReviewCheckout(rows, nil, nil, "/nope", io); err == nil {
		t.Error("a missing path must be refused")
	}
	if _, err := resolveReviewCheckout(rows, nil, nil, "zzz", io); err == nil {
		t.Error("an unknown name must be refused")
	}
}

// The CLI prints the same counts, from the same function, as the sidebar row
// and the Review view header — pinned on the real DELTA-2524 run, where the
// three once said 6, 9 and 4 decisions and 106 against 463 lines.
func TestPrintReviewCountsMatchTheOneDefinition(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "selfreview", "testdata", "delta2524_run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var run selfreview.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	rec := &reviewjob.Record{
		Row:    &session.ReviewRunRow{ID: "r", Checkout: "/wt", Round: 1, TreeSHA: "b767581d9b38", Status: session.ReviewReady},
		Status: session.ReviewReady, Run: &run,
	}
	var b bytes.Buffer
	printReview(&b, "DELTA-2524", rec)
	out := b.String()
	c := selfreview.CountsOf(&run, nil)
	for _, want := range []string{"4 to decide", "106 of 1,489 changed lines need you", "14 files safe to skip",
		c.ToDecide(), c.LinesNeedYou(), c.SafeToSkipLabel()} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "not reviewed") {
		t.Errorf("no file is unreviewed in this run:\n%s", out)
	}
	if rec.Unanswered() != 4 || reviewJSONOf(rec).Unanswered != 4 {
		t.Errorf("unanswered = %d", rec.Unanswered())
	}
}

// The CLI prints what the TUI shows: the summary, each decision's
// recommendation and how it works, the scope decision first — and with
// --walkthrough, every chapter with each file's fate.
func TestPrintReviewSummaryRecommendAndWalkthrough(t *testing.T) {
	rec := sampleRecord(session.ReviewReady)
	run := rec.Run
	run.Summary = "Scanbox SC now keeps closed findings for every org."
	run.IntentGap = "The ticket also asks for retries."
	run.Stops[0].Recommend = &selfreview.Recommend{Answer: "change", Text: "Keep them only for orgs with closures on"}
	run.Stops[0].How = "The store writes every closed finding it is given."
	run.Stops[0].Elsewhere = "Vulnix checks the setting first — collectors/vulnix/store.py"
	run.Chapters = []selfreview.Chapter{{ID: "c1", Title: "Keeping closed findings", Role: "heart", Text: "The store writes them all.",
		Files: []string{"services/vulns/closures/store.py", "go.mod"}, Decisions: []string{"s1"}}}
	selfreview.Upgrade(run)

	var b bytes.Buffer
	printReview(&b, "vulns cursor", rec)
	printWalkthrough(&b, rec)
	out := b.String()
	for _, want := range []string{
		"What it does: Scanbox SC now keeps closed findings for every org.",
		" 1. Not everything asked for is done  (scope)",
		"    Missing: The ticket also asks for retries.",
		"    Recommend: change — Keep them only for orgs with closures on",
		"    How it works: The store writes every closed finding it is given.",
		"    Elsewhere: Vulnix checks the setting first — collectors/vulnix/store.py",
		"── Walkthrough\n1. Keeping closed findings  (heart)\n   The store writes them all.\n   Decisions here: 2. Closures kept for all orgs",
		"   - services/vulns/closures/store.py — in decision 2",
		"   - go.mod — not reviewed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "!!") {
		t.Errorf("an intent gap printed as an alert:\n%s", out)
	}
	if _, err := parseReviewArgs([]string{"--walkthrough", "--json", "x"}); err == nil {
		t.Error("--walkthrough with --json accepted")
	}
}
