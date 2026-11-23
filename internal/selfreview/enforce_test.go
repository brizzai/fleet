package selfreview

import (
	"fmt"
	"strings"
	"testing"
)

// patchLines builds a one-hunk patch adding n lines starting at new line start.
func patchLines(start, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "@@ -%d,0 +%d,%d @@\n", start-1, start, n)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "+line %d\n", start+i)
	}
	return b.String()
}

func file(path string, start, n int) FileInput {
	return FileInput{Path: path, Status: "M", Additions: n, Patch: patchLines(start, n)}
}

func stop(id, path string, start, end int) Stop {
	return Stop{ID: id, Kind: "logic", Title: "t " + id, Decide: "ok " + id + "?",
		Hunks: []Hunk{{Path: path, Side: "new", Start: start, End: end}}}
}

// A decision is never hidden: stops past MaxStops are kept (and warned
// about), never dropped into a "+N more" nobody can open.
func TestEnforceKeepsEveryStopAndCounts(t *testing.T) {
	in := Input{Files: []FileInput{file("a.go", 1, 20)}}
	run := &Run{Verdict: "v", Risk: "low", MoreStops: 3}
	for i := 1; i <= 9; i++ {
		run.Stops = append(run.Stops, stop(fmt.Sprintf("s%d", i), "a.go", i, i))
	}
	warns := Enforce(run, in)
	if len(run.Stops) != 9 || run.MoreStops != 0 {
		t.Fatalf("stops = %d +%d, want 9 +0", len(run.Stops), run.MoreStops)
	}
	if !strings.Contains(strings.Join(warns, "\n"), "9 stops, over the 7") {
		t.Errorf("no warning for too many stops: %v", warns)
	}
	if run.StopLines != 9 || run.TotalLines != 20 || run.UncoveredLines != 11 {
		t.Errorf("lines stop=%d total=%d uncovered=%d, want 9/20/11", run.StopLines, run.TotalLines, run.UncoveredLines)
	}
}

func TestEnforceDecideEndsWithQuestionMark(t *testing.T) {
	in := Input{Files: []FileInput{file("a.go", 1, 3)}}
	run := &Run{Verdict: "v", Risk: "low", Stops: []Stop{
		{Kind: "logic", Title: "a", Decide: "Ship it."},
		{Kind: "bug", Title: "b"},
		{Kind: "logic", Title: "c", Decide: strings.Repeat("word ", 30)},
	}}
	Enforce(run, in)
	for _, s := range run.Stops {
		if !strings.HasSuffix(s.Decide, "?") {
			t.Errorf("decide %q does not end with ?", s.Decide)
		}
	}
	// "yes" keeps the code as is, so a bug's default question is "keep it?".
	if run.Stops[0].Decide != "Ship it?" || run.Stops[1].Decide != "Keep it as is?" {
		t.Errorf("decides = %q, %q", run.Stops[0].Decide, run.Stops[1].Decide)
	}
	if n := len(strings.Fields(run.Stops[2].Decide)); n != DecideHardCap {
		t.Errorf("long decide has %d words, want %d", n, DecideHardCap)
	}
}

func TestEnforceUniqueIDs(t *testing.T) {
	in := Input{Files: []FileInput{file("a.go", 1, 3)}}
	run := &Run{Verdict: "v", Risk: "low", Stops: []Stop{
		stop("s1", "a.go", 1, 1), stop("s1", "a.go", 2, 2), stop("", "a.go", 3, 3), stop("s2", "a.go", 1, 1),
	}}
	Enforce(run, in)
	seen := map[string]bool{}
	for _, s := range run.Stops {
		if s.ID == "" || seen[s.ID] {
			t.Errorf("id %q empty or duplicated in %v", s.ID, run.Stops)
		}
		seen[s.ID] = true
	}
	if run.Stops[3].ID != "s2" {
		t.Errorf("a later stop's own id was taken: %q", run.Stops[3].ID)
	}
}

func TestEnforceSkipsAndNotCovered(t *testing.T) {
	in := Input{Files: []FileInput{
		file("main.go", 1, 10),
		file("go.mod", 1, 2),
		{Path: "go.sum", Additions: 40, RuleSkip: "lockfile"},
		file("README.md", 1, 4),
		file("forgotten.go", 1, 5),
		{Path: "gen.pb.go", Additions: 900, RuleSkip: "generated", HighRisk: true},
	}}
	in.Files[1].HighRisk = true
	run := &Run{Verdict: "v", Risk: "low",
		Stops: []Stop{stop("s1", "main.go", 1, 4)},
		Skipped: []Skip{
			{Path: "main.go", Reason: "the rest is logging", Scope: "file"},
			{Path: "go.mod", Reason: "bump", Scope: "file"},
			{Path: "go.sum", Reason: "deps", Scope: "file"},
			{Path: "./README.md", Reason: "docs"},
			{Path: "ghost.go", Reason: "x"},
		}}
	warns := Enforce(run, in)

	got := map[string]Skip{}
	for _, s := range run.Skipped {
		got[s.Path] = s
	}
	if s := got["main.go"]; s.Scope != "rest" || s.By != "guide" {
		t.Errorf("main.go skip = %+v, want rest/guide (it has a stop)", s)
	}
	if _, ok := got["go.mod"]; ok {
		t.Error("high-risk go.mod was skipped by guide")
	}
	if s := got["go.sum"]; s.By != "rule" || s.Reason != "lockfile" {
		t.Errorf("go.sum = %+v, want by rule / lockfile", s)
	}
	if s := got["gen.pb.go"]; s.By != "rule" {
		t.Errorf("rule skip not merged in for an unmentioned file: %+v", s)
	}
	if s := got["README.md"]; s.Scope != "file" {
		t.Errorf("README.md = %+v (path with ./ prefix should resolve)", s)
	}
	if strings.Join(run.NotCovered, ",") != "go.mod,forgotten.go" {
		t.Errorf("not covered = %v, want [go.mod forgotten.go]", run.NotCovered)
	}
	if !strings.Contains(strings.Join(warns, "\n"), "ghost.go") {
		t.Error("an unknown skipped path was dropped silently")
	}
	// main.go: 4 in the stop, 6 skipped as rest.
	if run.StopLines != 4 || run.SkippedFiles != 3 {
		t.Errorf("stop lines %d skipped files %d, want 4 / 3", run.StopLines, run.SkippedFiles)
	}
	if run.TotalLines != run.StopLines+run.SkippedLines+run.UncoveredLines || run.UncoveredLines != 7 {
		t.Errorf("total %d = stop %d + skipped %d + uncovered %d (want uncovered 7)", run.TotalLines, run.StopLines, run.SkippedLines, run.UncoveredLines)
	}
}

func TestEnforceChecked(t *testing.T) {
	mk := func(ci CIInfo, checks ...Check) []Check {
		in := Input{CI: ci, Files: []FileInput{file("a.go", 1, 1)}}
		run := &Run{Verdict: "v", Risk: "low", Stops: []Stop{{Kind: "logic", Title: "t", Decide: "d?", Checked: checks}}}
		Enforce(run, in)
		return run.Stops[0].Checked
	}
	got := mk(CIInfo{},
		Check{Text: "CI is green", Src: "ci"},
		Check{Text: "no source"},
		Check{Text: "made up", Src: "intuition"},
		Check{Text: "unit test covers it", Src: "TEST"},
		Check{Text: "four", Src: "code"},
		Check{Text: "five", Src: "code"},
	)
	if len(got) != MaxChecked {
		t.Fatalf("checked = %+v, want %d items", got, MaxChecked)
	}
	if got[0].Src != "claim" || !got[0].Demoted {
		t.Errorf("unconfirmed ci claim = %+v, want demoted to claim", got[0])
	}
	if got[1].Src != "test" {
		t.Errorf("src not normalised: %+v", got[1])
	}

	// A claim that CI passed while fleet sees it running or failing
	// contradicts the header beside it: dropped, whatever its source.
	for _, ci := range []CIInfo{{State: CIPending}, {State: CIFailure, Failed: []string{"lint"}}} {
		got := mk(ci, Check{Text: "CI is green", Src: "claim"}, Check{Text: "all checks passed", Src: "ci"},
			Check{Text: "lint fails on the new file", Src: "ci"}, Check{Text: "cursor saved first", Src: "code"})
		for _, c := range got {
			if c.Text == "CI is green" || c.Text == "all checks passed" {
				t.Errorf("CI %s: kept %+v", ci.State, c)
			}
		}
		if len(got) != 2 {
			t.Errorf("CI %s: checked = %+v", ci.State, got)
		}
	}

	if c := mk(CIInfo{State: CISuccess}, Check{Text: "all checks pass", Src: "ci"})[0]; c.Src != "ci" {
		t.Errorf("CI success should confirm a passing claim: %+v", c)
	}
	if c := mk(CIInfo{State: CISuccess}, Check{Text: "lint fails on main", Src: "ci"})[0]; c.Src != "claim" {
		t.Errorf("a failure claim against green CI kept as ci: %+v", c)
	}
	if c := mk(CIInfo{State: CIFailure, Failed: []string{"test-go"}}, Check{Text: "test-go is the only red check", Src: "ci"})[0]; c.Src != "ci" {
		t.Errorf("naming a failed check should confirm: %+v", c)
	}
	if c := mk(CIInfo{State: CISuccess}, Check{Text: "required checks covered", Src: "ci"})[0]; c.Src != "ci" {
		t.Errorf("'required'/'covered' read as red: %+v", c)
	}
}

func TestEnforceHunkBinding(t *testing.T) {
	in := Input{Files: []FileInput{
		file("a.go", 10, 5), // new lines 10-14
		{Path: "new.go", OldPath: "old.go", Status: "R", Additions: 1, Patch: patchLines(3, 1)},
		{Path: "logo.png", Status: "M"}, // binary: no patch
	}}
	run := &Run{Verdict: "v", Risk: "low", Stops: []Stop{{Kind: "logic", Title: "t", Decide: "d?", Hunks: []Hunk{
		{Path: "a/a.go", Start: 14, End: 12}, // b/-prefixed, reversed
		{Path: "a.go", Start: 100, End: 120}, // outside the diff
		{Path: "old.go", Start: 3, End: 3},   // old name of a rename
		{Path: "logo.png", Start: 5, End: 9}, // no patch: cannot check, kept
		{Path: "nope.go", Start: 1, End: 1},
	}}}}
	Enforce(run, in)
	hs := run.Stops[0].Hunks
	want := []Hunk{
		{Path: "a.go", Side: "new", Start: 12, End: 14},
		{Path: "a.go", Side: "new", Start: 0, End: 0},
		{Path: "new.go", Side: "new", Start: 3, End: 3},
		{Path: "logo.png", Side: "new", Start: 5, End: 9},
	}
	if fmt.Sprint(hs) != fmt.Sprint(want) {
		t.Errorf("hunks = %+v\nwant    %+v", hs, want)
	}
	// The file-level fallback claims all of a.go.
	if run.Stops[0].Lines != 5+1 {
		t.Errorf("stop lines = %d, want 6 (a.go whole + new.go)", run.Stops[0].Lines)
	}
}

func TestEnforceBudgetsAndSanitises(t *testing.T) {
	in := Input{Files: []FileInput{file("a.go", 1, 1)}}
	run := &Run{
		Verdict:   strings.Repeat("big ", 30),
		Risk:      "Medium",
		IntentGap: "None.",
		Stops: []Stop{{Kind: "Security", Title: "\x1b[31mred\x1b[0m title\nwith newline and many more words here", Why: "w",
			Decide: "d?", Hunks: []Hunk{{Path: "a.go", Start: 1, End: 1}}}},
		Pending: []string{"a", "", "b", "c", "d", "e", "f"},
	}
	Enforce(run, in)
	if n := len(strings.Fields(run.Verdict)); n != VerdictHardCap || !strings.HasSuffix(run.Verdict, "…") {
		t.Errorf("verdict %q: %d words", run.Verdict, n)
	}
	if run.Risk != "med" || run.IntentGap != "" {
		t.Errorf("risk %q intent_gap %q", run.Risk, run.IntentGap)
	}
	s := run.Stops[0]
	if strings.ContainsAny(s.Title, "\x1b\n") || !strings.HasPrefix(s.Title, "red title with") {
		t.Errorf("title not cleaned: %q", s.Title)
	}
	if s.Kind != "security" {
		t.Errorf("kind %q", s.Kind)
	}
	if len(run.Pending) != MaxPending {
		t.Errorf("pending = %v", run.Pending)
	}
}

func TestEnforceRoundTwo(t *testing.T) {
	prev := &Run{Stops: []Stop{
		{ID: "s1", Title: "Closures kept for all orgs", Decide: "Keep them?"},
		{ID: "s2", Title: "Full snapshot every 12h", Decide: "OK for big customers?"},
		{ID: "s3", Title: "Lookback window", Decide: "36h right?"},
	}}
	in := Input{
		Round:    2,
		Files:    []FileInput{file("a.go", 1, 3)},
		Previous: prev,
		PreviousAnswers: []Answer{
			{StopID: "s1", Kind: AnswerFine},
			{StopID: "s2", Kind: AnswerChange, Note: "24h"},
			{StopID: "s3", Kind: AnswerLater},
		},
	}
	run := &Run{Verdict: "v", Risk: "low",
		Stops: []Stop{
			{Kind: "product", Title: "Closures kept for all orgs!", Decide: "Really keep them?"},
			{Kind: "tuning", Title: "Lookback window", Decide: "36h right?", Hunks: []Hunk{{Path: "a.go", Start: 1, End: 1}}},
		},
		Prev: []Followup{{ID: "s9", Status: "addressed"}},
	}
	Enforce(run, in)
	if len(run.Stops) != 1 || run.Stops[0].Title != "Lookback window" {
		t.Errorf("stops = %+v: the fine-answered stop must not come back, the later one may", run.Stops)
	}
	if len(run.Prev) != 1 || run.Prev[0].ID != "s2" || run.Prev[0].Status != "unreported" {
		t.Errorf("prev = %+v, want s2 unreported", run.Prev)
	}
}

// replacePatch builds a one-hunk patch replacing dels old lines with adds new
// ones from line 1.
func replacePatch(adds, dels int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "@@ -1,%d +1,%d @@\n", dels, adds)
	for i := 0; i < dels; i++ {
		fmt.Fprintf(&b, "-old %d\n", i+1)
	}
	for i := 0; i < adds; i++ {
		fmt.Fprintf(&b, "+new %d\n", i+1)
	}
	return b.String()
}

func changed(path string, adds, dels int) FileInput {
	return FileInput{Path: path, Status: "M", Additions: adds, Deletions: dels, Patch: replacePatch(adds, dels)}
}

// The first real run: Sonnet skipped 152 changed lines of ingestion logic as
// "covered by tests", and fleet showed the file as settled.
func TestEnforceRefusesSkippingLogicAsCoveredByTests(t *testing.T) {
	const ingest = "pipelines/ingestors/scanbox-sc-ingestor/src/scanbox_sc_ingestor/ingest.py"
	in := Input{Files: []FileInput{changed(ingest, 131, 21)}}
	run := &Run{Verdict: "v", Risk: "med", Skipped: []Skip{
		{Path: ingest, Reason: "accumulate and materialize logic, covered by tests", Scope: "file"},
	}}
	warns := Enforce(run, in)
	if len(run.Skipped) != 0 {
		t.Fatalf("logic file stayed skipped: %+v", run.Skipped)
	}
	if strings.Join(run.NotCovered, ",") != ingest {
		t.Fatalf("not covered = %v, want the ingest module", run.NotCovered)
	}
	want := "skipped as 'accumulate and materialize logic, covered by tests' — 152 changed lines of logic"
	if got := run.NotCoveredWhy[ingest]; got != want {
		t.Errorf("reason = %q\nwant      %q", got, want)
	}
	if !strings.Contains(strings.Join(warns, "\n"), "152 changed lines of logic") {
		t.Errorf("no warning for the refused skip: %v", warns)
	}
	if run.UncoveredLines != 152 || run.SkippedLines != 0 {
		t.Errorf("uncovered %d skipped %d, want 152 / 0", run.UncoveredLines, run.SkippedLines)
	}
}

func TestEnforceSkipKindRules(t *testing.T) {
	cases := []struct {
		name   string
		file   FileInput
		kind   string
		scope  string // "" = no stop on the file
		keep   bool
		reason string // substring of NotCoveredWhy when refused
	}{
		{"docs on a doc", changed("README.md", 200, 50), "docs", "", true, ""},
		{"docs under docs/", changed("docs/guide/setup.html", 40, 0), "docs", "", true, ""},
		{"docs on code", changed("svc/handler.go", 5, 1), "docs", "", false, "(docs), but it is not a doc file"},
		{"fixture in testdata", changed("pkg/testdata/golden.txt", 400, 0), "fixture", "", true, ""},
		{"fixture json under tests", changed("tests/data/sample.json", 90, 3), "fixture", "", true, ""},
		{"fixture snapshot", changed("web/__snapshots__/a.snap", 60, 60), "fixture", "", true, ""},
		{"fixture on code", changed("src/loader.py", 6, 0), "fixture", "", false, "not a test-data file"},
		// A change of at most SmallSkipMaxLines lines is never refused over its
		// kind: the real EDR run sent a two-line gate-list edit to Not reviewed
		// because the reviewer called it config.
		{"small change, wrong kind", changed("services/integrations/status.py", 2, 1), "config", "", true, ""},
		{"five lines, wrong kind", changed("src/loader.py", 4, 1), "fixture", "", true, ""},
		{"config yaml", changed("deploy/values.yaml", 120, 10), "config", "", true, ""},
		{"config Dockerfile", changed("svc/Dockerfile", 12, 2), "config", "", true, ""},
		{"config Makefile", changed("Makefile", 6, 1), "config", "", true, ""},
		{"config on code", changed("src/settings.py", 10, 0), "config", "", false, "not a config file"},
		{"generated pb", changed("api/v1/api.pb.go", 900, 400), "generated", "", true, ""},
		{"generated on code", changed("api/v1/api.go", 6, 0), "generated", "", false, "not a generated file"},
		{"small source, any reason", changed("svc/util.go", 20, 9), "", "", true, ""},
		{"source at 30, not mechanical", changed("svc/util.go", 20, 10), "docs", "", false, "not a doc file"},
		{"source at 30, no kind", changed("svc/util.go", 20, 10), "", "", false, "30 changed lines of logic"},
		{"source mechanical 79", changed("svc/rename.go", 40, 39), "mechanical", "", true, ""},
		{"source mechanical 80", changed("svc/rename.go", 40, 40), "mechanical", "", false, "(mechanical) — 80 changed lines"},
		{"unknown kind tests", changed("svc/core.go", 50, 5), "tests", "", false, "55 changed lines of logic"},
		// scope rest: the stop takes lines 1..60 of 100, leaving 40 ≥ 30.
		{"rest of logic over 30", changed("svc/big.go", 100, 0), "", "rest", false, ""},
		// scope rest: the stop takes lines 1..80 of 100, leaving 20 < 30.
		{"rest of logic under 30", changed("svc/big2.go", 100, 0), "", "rest-small", true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := Input{Files: []FileInput{c.file}}
			run := &Run{Verdict: "v", Risk: "low", Skipped: []Skip{{Path: c.file.Path, Kind: c.kind, Reason: "looks fine", Scope: "file"}}}
			switch c.scope {
			case "rest":
				run.Stops = []Stop{stop("s1", c.file.Path, 1, 60)}
			case "rest-small":
				run.Stops = []Stop{stop("s1", c.file.Path, 1, 80)}
			}
			Enforce(run, in)
			kept := len(run.Skipped) == 1
			if kept != c.keep {
				t.Fatalf("kept = %v, want %v (skipped %+v, not covered %v why %v)", kept, c.keep, run.Skipped, run.NotCovered, run.NotCoveredWhy)
			}
			if c.keep || c.scope != "" {
				if len(run.NotCovered) != 0 {
					t.Errorf("not covered = %v, want none", run.NotCovered)
				}
				return
			}
			why := run.NotCoveredWhy[c.file.Path]
			if !strings.Contains(why, c.reason) || !strings.HasPrefix(why, "skipped as 'looks fine'") {
				t.Errorf("reason = %q, want it to quote the skip and contain %q", why, c.reason)
			}
		})
	}
}

// A kindless skip of a non-source file is given the file's class.
func TestEnforceInfersAMissingKind(t *testing.T) {
	in := Input{Files: []FileInput{changed("docs/x.md", 3, 0), changed("a.go", 2, 0)}}
	run := &Run{Verdict: "v", Risk: "low", Skipped: []Skip{{Path: "docs/x.md", Reason: "r"}, {Path: "a.go", Reason: "r"}}}
	Enforce(run, in)
	if run.Skipped[0].Kind != SkipDocs || run.Skipped[1].Kind != "" {
		t.Errorf("kinds = %q, %q, want docs and empty", run.Skipped[0].Kind, run.Skipped[1].Kind)
	}
}

func TestEnforceHighRiskSkipReason(t *testing.T) {
	in := Input{Files: []FileInput{{Path: "go.mod", Additions: 1, Deletions: 1, HighRisk: true}}}
	run := &Run{Verdict: "v", Risk: "low", Skipped: []Skip{{Path: "go.mod", Kind: "config", Reason: "patch bump"}}}
	Enforce(run, in)
	if got := run.NotCoveredWhy["go.mod"]; got != "skipped as 'patch bump', but it is a high-risk path" {
		t.Errorf("reason = %q", got)
	}
}

func TestEnforceVerdictCutsAtAClause(t *testing.T) {
	cases := []struct{ in, want string }{
		{ // the real one, past the hard cap: cut before the last "and"
			"Scanbox SC moves onto the 12h data pipeline with a lastFixedTime-windowed closed cursor and aggregation-based closures; hinges on unobserved appliance import lag and the deploy order of both services",
			"Scanbox SC moves onto the 12h data pipeline with a lastFixedTime-windowed closed cursor and aggregation-based closures; hinges on unobserved appliance import lag…",
		},
		{ // within the hard cap: untouched, no ellipsis — Lint, not a cut, holds the budget
			"Scanbox SC moves onto the 12h data pipeline with a lastFixedTime-windowed closed cursor and aggregation-based closures; hinges on unobserved appliance import lag",
			"Scanbox SC moves onto the 12h data pipeline with a lastFixedTime-windowed closed cursor and aggregation-based closures; hinges on unobserved appliance import lag",
		},
		{ // cut before "but"
			"Moves the retry loop into the client so every caller gets backoff for free but the timeout now doubles under load for slow tenants on the shared cluster today",
			"Moves the retry loop into the client so every caller gets backoff for free…",
		},
		{ // only boundary keeps fewer than 8 words: cut at the budget
			"Renames it, then threads the new context through every handler in the service layer without changing any of the observable request behaviour at all for any caller anywhere",
			"Renames it, then threads the new context through every handler in the service layer without changing any of the observable request behaviour at all…",
		},
		{ // em dash token
			"Splits 403 handling so permission errors surface instead of booking an end check — expired windows still return ErrScanWindow as before today for every tenant here",
			"Splits 403 handling so permission errors surface instead of booking an end check…",
		},
	}
	for _, c := range cases {
		run := &Run{Verdict: c.in, Risk: "low"}
		Enforce(run, Input{})
		if run.Verdict != c.want {
			t.Errorf("verdict\n got %q\nwant %q", run.Verdict, c.want)
		}
		if n := len(strings.Fields(strings.TrimSuffix(run.Verdict, "…"))); n > VerdictHardCap {
			t.Errorf("verdict has %d words", n)
		}
	}
}

func TestEnforcePendingIsNotEvidence(t *testing.T) {
	in := Input{Files: []FileInput{file("a.go", 1, 3)}}
	s := stop("s1", "a.go", 1, 3)
	s.Checked = []Check{
		{Text: "unit tests pass locally", Src: "test"},
		{Text: "CI is still running", Src: "ci"},
		{Text: "SC sandbox integration test", Src: "transcript"},
		{Text: "load test NOT YET done on the big tenant", Src: "claim"},
		{Text: "migration in progress on staging", Src: "transcript"},
		{Text: "Waiting for the appliance import to land", Src: "claim"},
	}
	run := &Run{Verdict: "v", Risk: "low", Stops: []Stop{s},
		Pending: []string{"integration test against the SC sandbox not run"}}
	warns := Enforce(run, in)

	checked := run.Stops[0].Checked
	if len(checked) != 1 || checked[0].Text != "unit tests pass locally" {
		t.Errorf("checked = %+v, want only the finished test", checked)
	}
	want := []string{
		"integration test against the SC sandbox not run", // the duplicate is not added twice
		"CI is still running",
		"load test NOT YET done on the big tenant",
		"migration in progress on staging",
		"Waiting for the appliance import to land",
	}
	if strings.Join(run.Pending, "|") != strings.Join(want, "|") {
		t.Errorf("pending = %q\nwant      %q", run.Pending, want)
	}
	if !strings.Contains(strings.Join(warns, "\n"), "pending verification, not evidence") {
		t.Errorf("no warning: %v", warns)
	}
}

func TestNearDuplicate(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"CI running", "ci running.", true},
		{"SC sandbox integration test passes", "integration test against the SC sandbox not run", true},
		{"CI is green", "CI running", false},
		{"cursor saved after upload", "integration test not run", false},
	} {
		if got := nearDuplicate(c.a, c.b); got != c.want {
			t.Errorf("nearDuplicate(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// A rule-skipped test file that deletes assertions keeps its skip but carries
// fleet's note; a guide skip can never carry one, whatever the model wrote.
func TestEnforceFlagsRemovedAssertions(t *testing.T) {
	patch := "@@ -10,4 +10,2 @@\n--- not a header\n-\tassert.Equal(t, 2, got)\n-\tif err != nil { t.Fatalf(\"x\") }\n+\t_ = got\n"
	if n := RemovedAssertions(patch); n != 2 {
		t.Errorf("RemovedAssertions = %d, want 2", n)
	}
	if n := RemovedAssertions("@@ -1 +1 @@\n-x := 1\n+x := 2\n--- a/assert.go\n"); n != 0 {
		t.Errorf("non-assertion lines counted: %d", n)
	}
	in := Input{Files: []FileInput{
		{Path: "a_test.go", Deletions: 2, RuleSkip: "test", RemovedAssertions: 2},
		{Path: "b_test.go", Additions: 5, RuleSkip: "test"},
		file("README.md", 1, 3),
	}}
	run := &Run{Verdict: "v", Risk: "low", Skipped: []Skip{{Path: "README.md", Kind: "docs", Reason: "docs", Note: "model note"}}}
	Enforce(run, in)
	notes := map[string]string{}
	for _, s := range run.Skipped {
		notes[s.Path] = s.Note
	}
	if notes["a_test.go"] != AssertionsRemovedNote || notes["b_test.go"] != "" || notes["README.md"] != "" {
		t.Errorf("notes = %v", notes)
	}
}

// An intent gap is a decision, never a banner: the model's scope stop goes
// first; with none, fleet makes one from intent_gap, never cutting the list.
func TestEnforceScopeDecision(t *testing.T) {
	gap := "The ticket also asks for incremental collection: partial runs, a time-range record for each event stream, gaps that fill back in, and window limits set by operators. None of that is in this change."
	in := Input{Files: []FileInput{changed("a.go", 10, 0)}}

	run := &Run{Verdict: "v", Risk: "low", IntentGap: gap, Stops: []Stop{stop("s1", "a.go", 1, 5)}}
	Enforce(run, in)
	if len(run.Stops) != 2 || !run.Stops[0].IsScope() || run.Stops[0].Why != gap || run.IntentGap != gap {
		t.Fatalf("made scope = %+v (gap %q)", run.Stops, run.IntentGap)
	}
	if strings.Contains(run.Stops[0].Why, "…") || !strings.HasSuffix(run.Stops[0].Decide, "?") {
		t.Errorf("scope why cut or decide not a question: %+v", run.Stops[0])
	}

	model := Stop{ID: "g", Kind: "SCOPE", Title: "Incremental collection not done", Why: gap + " " + gap,
		Decide: "Ship without incremental collection and track it as a follow-up?", Missing: []string{" partial runs ", ""},
		Recommend: &Recommend{Answer: "keep", Text: "track as a follow-up ticket"},
		Hunks:     []Hunk{{Path: "a.go", Start: 1, End: 2}}}
	run = &Run{Verdict: "v", Risk: "low", IntentGap: gap, Stops: []Stop{stop("s1", "a.go", 1, 5), model, model}}
	Enforce(run, in)
	if len(run.Stops) != 2 || run.Stops[0].ID != "g" || len(run.Stops[0].Hunks) != 0 {
		t.Fatalf("model scope = %+v", run.Stops)
	}
	if s := run.Stops[0]; s.Why != gap+" "+gap || len(s.Missing) != 1 || s.Missing[0] != "partial runs" {
		t.Errorf("scope why/missing = %q %q", s.Why, s.Missing)
	}
}

// A recommend is keep or change; prose the person reads in full is never cut.
func TestEnforceRecommendAndProse(t *testing.T) {
	long := strings.Repeat("word ", 80)
	s := stop("s1", "a.go", 1, 5)
	s.Recommend = &Recommend{Answer: "Yes", Text: ""}
	s.How, s.Elsewhere = long, "x — a/b.go"
	t2 := stop("s2", "a.go", 6, 8)
	t2.Recommend = &Recommend{Answer: "perhaps"}
	run := &Run{Verdict: "v", Risk: "low", Summary: long, Stops: []Stop{s, t2}}
	Enforce(run, Input{Files: []FileInput{changed("a.go", 10, 0)}})
	if r := run.Stops[0].Recommend; r == nil || r.Answer != RecommendKeep {
		t.Errorf("recommend = %+v, want keep", r)
	}
	if run.Stops[1].Recommend != nil {
		t.Errorf("unknown recommend kept: %+v", run.Stops[1].Recommend)
	}
	if strings.Contains(run.Summary+run.Stops[0].How, "…") {
		t.Error("summary or how cut")
	}
}

// Every changed file sits in exactly one chapter; chapters read heart →
// consequence → glue → docs, and fleet's "Other changes" comes last.
func TestEnforceChapters(t *testing.T) {
	in := Input{Files: []FileInput{changed("core.go", 40, 0), changed("wire.go", 4, 0), changed("README.md", 3, 0), changed("extra.go", 2, 0)}}
	run := &Run{Verdict: "v", Risk: "low",
		Stops: []Stop{stop("s1", "core.go", 1, 5), stop("s2", "wire.go", 1, 2)},
		Chapters: []Chapter{
			{ID: "d", Title: "Docs", Role: "docs", Text: "t", Files: []string{"README.md"}},
			{ID: "g", Title: "Wiring", Role: "glue", Text: "t", Files: []string{"./wire.go", "core.go"}},
			{ID: "h", Title: "Core", Role: "HEART", Text: "t", Files: []string{"core.go", "nope.go"}, Decisions: []string{"s9"}},
			{ID: "e", Title: "Empty", Role: "consequence", Text: "t", Files: []string{"README.md"}},
		}}
	Enforce(run, in)
	var got []string
	for _, c := range run.Chapters {
		got = append(got, fmt.Sprintf("%s:%s:%v:%v", c.ID, c.Role, c.Files, c.Decisions))
	}
	want := []string{"g:glue:[wire.go core.go]:[s1 s2]", "d:docs:[README.md]:[]", "other:glue:[extra.go]:[]"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("chapters =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !run.Chapters[2].ByFleet {
		t.Error("Other changes not marked as fleet's")
	}

	run = &Run{Verdict: "v", Risk: "low"}
	Enforce(run, in)
	if run.Chapters != nil {
		t.Errorf("a run with no chapters gained some: %+v", run.Chapters)
	}
}

// A stored run from before the scope decision gets one from its gap, once.
func TestUpgradeMakesTheScopeDecision(t *testing.T) {
	run := &Run{IntentGap: "Also asked: retries.", Stops: []Stop{{ID: "s1", Title: "t"}}}
	Upgrade(run)
	Upgrade(run)
	if len(run.Stops) != 2 || !run.Stops[0].IsScope() || run.Stops[0].ID != ScopeStopID {
		t.Fatalf("stops = %+v", run.Stops)
	}
	Upgrade(nil)
}
