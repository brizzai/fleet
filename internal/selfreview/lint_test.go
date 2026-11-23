package selfreview

import (
	"path/filepath"
	"strings"
	"testing"
)

func loadRunFixture(t *testing.T, name string) *Run {
	t.Helper()
	var run Run
	readJSON(t, filepath.Join("testdata", name), &run)
	return &run
}

// The real DELTA-2524 run predates the prose rules and breaks most of them;
// its hand-corrected twin obeys every one.
func TestLintRealRunAndItsCorrection(t *testing.T) {
	got := Lint(loadRunFixture(t, "delta2524_run.json"))
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		`verdict: must start with "Ready"`,
		`verdict: code identifier "lastFixedTime"`,
		`stop s1 decide: "or will"`,
		`stop s2 title: code identifier "collect_closed_issues"`,
		`stop s3 decide: ", and is"`,
		`stop s4 decide: "instead of"`,
		`stop s4 why: 3 sentences`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing problem %q in:\n%s", want, joined)
		}
	}
	if fixed := Lint(loadRunFixture(t, "delta2524_compliant.json")); len(fixed) != 0 {
		t.Errorf("the corrected run still has problems:\n%s", strings.Join(fixed, "\n"))
	}
}

func TestLintRules(t *testing.T) {
	ok := Stop{ID: "s1", Title: "Fixes lost when an upload fails",
		Why:       "A failed upload skips a day of fixes. Customers see stale findings.",
		How:       "The cursor is written as soon as reading ends, before the upload runs.",
		Recommend: &Recommend{Answer: RecommendChange, Text: "Save the cursor after the upload lands"},
		Decide:    "Keep the cursor saved before upload?"}
	chapters := []Chapter{{ID: "c1", Title: "Reading fixes since the last run", Role: RoleHeart,
		Text: "The collector asks only for findings fixed after its cursor.", Files: []string{"collector.go"}}}
	cases := []struct {
		name string
		edit func(*Run)
		want string // "" = clean
	}{
		{"clean", func(*Run) {}, ""},
		{"ready once", func(r *Run) { r.Verdict = "Ready once the deploy order is settled." }, ""},
		{"not ready", func(r *Run) { r.Verdict = "Not ready: the cursor skips failed uploads." }, ""},
		{"verdict prefix", func(r *Run) { r.Verdict = "Moves the collector to 12h." }, `verdict: must start with`},
		{"verdict long", func(r *Run) { r.Verdict = "Ready " + strings.Repeat("word ", 18) }, "verdict: 19 words (max 18)"},
		{"title long", func(r *Run) { r.Stops[0].Title = "one two three four five six seven eight" }, "stop s1 title: 8 words (max 7)"},
		{"title snake", func(r *Run) { r.Stops[0].Title = "Cursor saved in save_cursor" }, `title: code identifier "save_cursor"`},
		{"title camel", func(r *Run) { r.Stops[0].Title = "Window uses lastFixedTime" }, `title: code identifier "lastFixedTime"`},
		{"title innercap", func(r *Run) { r.Stops[0].Title = "ScanboxSC reads twice a day" }, `title: code identifier "ScanboxSC"`},
		{"proper noun ok", func(r *Run) { r.Stops[0].Title = "GitHub checks run twice" }, ""},
		{"why long", func(r *Run) { r.Stops[0].Why = strings.Repeat("word ", 31) }, "stop s1 why: 31 words (max 30)"},
		{"why sentences", func(r *Run) { r.Stops[0].Why = "One. Two. Three." }, "stop s1 why: 3 sentences"},
		{"why call", func(r *Run) { r.Stops[0].Why = "Calls saveCursor() too early." }, `why: code identifier "saveCursor()"`},
		{"why path", func(r *Run) { r.Stops[0].Why = "See scanboxSC/collector.go for it." }, "why: code identifier"},
		{"why and/or ok", func(r *Run) { r.Stops[0].Why = "Uploads and/or ingestion can fail. Fixes are lost." }, ""},
		{"decide long", func(r *Run) { r.Stops[0].Decide = strings.Repeat("word ", 15) + "end?" }, "decide: 16 words (max 15)"},
		{"decide two qs", func(r *Run) { r.Stops[0].Decide = "Keep it? Ship it?" }, "decide: 2 questions"},
		{"decide no q", func(r *Run) { r.Stops[0].Decide = "Keep it" }, `decide: must be a question`},
		{"decide and is", func(r *Run) { r.Stops[0].Decide = "Is it fine, and is the backfill sized?" }, `", and is"`},
		{"decide or will", func(r *Run) { r.Stops[0].Decide = "Ship first or will you delete the row?" }, `"or will"`},
		{"decide instead", func(r *Run) { r.Stops[0].Decide = "Keep it instead of moving the cursor?" }, `"instead of"`},
		{"decide and should", func(r *Run) { r.Stops[0].Decide = "Keep it and should we log it?" }, `"and should"`},
		{"summary missing", func(r *Run) { r.Summary = "" }, "summary: missing"},
		{"summary long", func(r *Run) { r.Summary = strings.Repeat("word ", 61) }, "summary: 61 words (max 60)"},
		{"summary sentences", func(r *Run) { r.Summary = "One. Two. Three. Four." }, "summary: 4 sentences (max 3)"},
		{"summary identifier", func(r *Run) { r.Summary = "Moves save_cursor later." }, `summary: code identifier "save_cursor"`},
		{"recommend missing", func(r *Run) { r.Stops[0].Recommend = nil }, "stop s1 recommend: missing"},
		{"recommend unknown", func(r *Run) { r.Stops[0].Recommend = &Recommend{Answer: "maybe"} }, `recommend: answer "maybe"`},
		{"recommend change no text", func(r *Run) { r.Stops[0].Recommend = &Recommend{Answer: "change"} }, "recommend: name the change"},
		{"recommend keep ok", func(r *Run) { r.Stops[0].Recommend = &Recommend{Answer: "yes"} }, ""},
		{"recommend long", func(r *Run) { r.Stops[0].Recommend.Text = strings.Repeat("word ", 16) }, "recommend: 16 words (max 15)"},
		{"how missing", func(r *Run) { r.Stops[0].How = "" }, "stop s1 how: missing"},
		{"how long", func(r *Run) { r.Stops[0].How = strings.Repeat("word ", 41) }, "how: 41 words (max 40)"},
		{"elsewhere ok", func(r *Run) {
			r.Stops[0].Elsewhere = "EdgeWatcher saves its cursor after upload — collectors/edgewatcher/collector.go"
		}, ""},
		{"elsewhere no path", func(r *Run) { r.Stops[0].Elsewhere = "EdgeWatcher saves its cursor after the upload." }, "elsewhere: cite the path"},
		{"gap without scope", func(r *Run) { r.IntentGap = "The ticket also asks for retries." }, `intent_gap: add a stop of kind "scope"`},
		{"scope ok", func(r *Run) {
			r.IntentGap = "The ticket also asks for retries."
			r.Stops = append([]Stop{{ID: "g", Kind: "scope", Title: "Retries not done", Decide: "Ship without retries and track it as a follow-up?",
				Why: strings.Repeat("item ", 50), Recommend: &Recommend{Answer: "keep", Text: "track as a follow-up ticket"}}}, r.Stops...)
		}, ""},
		{"scope not first", func(r *Run) {
			r.Stops = append(r.Stops, Stop{ID: "g", Kind: "scope", Title: "Retries not done", Decide: "Ship without retries?",
				Recommend: &Recommend{Answer: "keep"}})
		}, "the scope stop must come first"},
		{"chapters missing", func(r *Run) { r.Chapters = nil }, "chapters: missing"},
		{"chapters too many", func(r *Run) {
			for range 7 {
				r.Chapters = append(r.Chapters, chapters[0])
			}
		}, "chapters: 8 (max 7)"},
		{"chapter role", func(r *Run) { r.Chapters[0].Role = "core" }, `chapter c1 role: "core"`},
		{"chapter text long", func(r *Run) { r.Chapters[0].Text = strings.Repeat("word ", 71) }, "chapter c1 text: 71 words (max 70)"},
		{"chapter title long", func(r *Run) { r.Chapters[0].Title = strings.Repeat("word ", 8) }, "chapter c1 title: 8 words"},
		{"chapter no files", func(r *Run) { r.Chapters[0].Files = nil }, "chapter c1 files: none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := ok
			rec := *ok.Recommend
			st.Recommend = &rec
			r := &Run{Verdict: "Ready once the default is settled.", Summary: "Fixes are now read since the last run.",
				Stops: []Stop{st}, Chapters: append([]Chapter(nil), chapters...)}
			c.edit(r)
			got := strings.Join(Lint(r), "\n")
			if c.want == "" {
				if got != "" {
					t.Errorf("want clean, got:\n%s", got)
				}
				return
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("want %q in:\n%s", c.want, got)
			}
		})
	}
}
