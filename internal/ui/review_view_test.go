package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/review"
	"github.com/brizzai/fleet/internal/selfreview"
	"github.com/brizzai/fleet/internal/session"
)

// scanboxDir is selfreview's enforced golden run: a realistic Scanbox SC
// collector change with eight stops, a go.mod nobody reviewed, rule and guide
// skips, and pending CI.
var scanboxDir = filepath.Join("..", "selfreview", "testdata", "scanbox")

func loadScanboxRun(t *testing.T) *selfreview.Run {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(scanboxDir, "enforced.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var run selfreview.Run
	if err := json.Unmarshal(b, &run); err != nil {
		t.Fatal(err)
	}
	return &run
}

// scanboxFiles splits the fixture's diff.patch into review.Files — the test's
// stand-in for git.DiffFiles — plus the patchless generated fixture.
func scanboxFiles(t *testing.T) []review.File {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(scanboxDir, "diff.patch"))
	if err != nil {
		t.Fatal(err)
	}
	var out []review.File
	for _, chunk := range strings.Split(string(b), "diff --git ")[1:] {
		f := review.File{Status: "modified"}
		lines := strings.Split(chunk, "\n")
		at := -1
		for i, l := range lines {
			switch {
			case strings.HasPrefix(l, "new file mode"):
				f.Status = "added"
			case strings.HasPrefix(l, "+++ b/"):
				f.Path = strings.TrimPrefix(l, "+++ b/")
			case strings.HasPrefix(l, "@@") && at < 0:
				at = i
			}
			if at >= 0 && i > at && l != "" {
				switch l[0] {
				case '+':
					f.Additions++
				case '-':
					f.Deletions++
				}
			}
		}
		if at >= 0 {
			f.Patch = strings.Join(lines[at:], "\n")
		}
		out = append(out, f)
	}
	return append(out, review.File{
		Path:   "pipelines/collectors/internal/collectors/scanboxSC/testdata/scan_results.json",
		Status: "modified", Additions: 412, Deletions: 380,
	})
}

func scanboxData(t *testing.T) ReviewData {
	t.Helper()
	ApplyPalette(PaletteFleetPink)
	run := loadScanboxRun(t)
	run.Reviewer = &selfreview.ReviewerMeta{Turns: 9, CostUSD: 0.81, DurationMS: 74213,
		Tools: map[string]int{"Read": 11, "Grep": 3}, FilesRead: 9}
	return ReviewData{
		RunID: "run-1", Checkout: "/wt/vulns", CheckoutLabel: "vulns cursor",
		Targets: []reviewTarget{{ID: "sess-1", Title: "api", Glyph: "✻", Live: true}},
		Round:   1, Model: "opus", Elapsed: 74 * time.Second,
		Snapshot: "62055a2 + 2 uncommitted", Freshness: ReviewFresh,
		Run:     run,
		Answers: map[string]selfreview.Answer{"s1": {StopID: "s1", Kind: selfreview.AnswerFine}},
		Doc:     review.NewDoc(scanboxFiles(t), ""),
	}
}

// screenText is the frame as plain text, trailing blanks trimmed.
func screenText(t *testing.T, v *ReviewView) string {
	t.Helper()
	frame := v.View()
	lines := strings.Split(frame, "\n")
	if len(lines) != v.height {
		t.Errorf("frame is %d lines, want exactly %d", len(lines), v.height)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != v.width {
			t.Errorf("line %d is %d cells wide, want exactly %d: %q", i, w, v.width, ansi.Strip(l))
		}
	}
	return plainLines(lines)
}

func rvPress(v *ReviewView, keys ...string) (*ReviewView, []tea.Msg) {
	var msgs []tea.Msg
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "ctrl+q":
			msg = tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}
		default:
			msg = tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
		}
		typing := v.inputOpen && k != "enter" && k != "ctrl+q"
		var cmd tea.Cmd
		v, cmd = v.Update(msg)
		// While the note is open, the input answers keys with its caret-blink
		// tick, which sleeps; nothing the view emits rides those keys.
		if cmd != nil && !typing {
			if m := cmd(); m != nil {
				msgs = append(msgs, m)
			}
		}
	}
	return v, msgs
}

func TestReviewViewGoldens(t *testing.T) {
	cases := []struct {
		name string
		w, h int
		edit func(*ReviewData)
		keys []string
	}{
		{name: "wide_list_card_120x40", w: 120, h: 40, keys: []string{"j"}},
		{name: "wide_card_focused_120x40", w: 120, h: 40, keys: []string{"k", "enter"}},
		{name: "narrow_list_80x24", w: 80, h: 24},
		{name: "narrow_card_80x24", w: 80, h: 24, keys: []string{"k", "enter"}},
		{name: "wide_skipped_120x40", w: 120, h: 40, keys: []string{"s"}},
		{name: "narrow_skipped_80x24", w: 80, h: 24, keys: []string{"s"}},
		{name: "wide_diff_120x40", w: 120, h: 40, keys: []string{"d"}},
		{name: "keys_overlay_80x24", w: 80, h: 24, keys: []string{"?"}},
		{name: "keys_overlay_120x40", w: 120, h: 40, keys: []string{"?"}},
		{name: "long_verdict_80x24", w: 80, h: 24, edit: func(d *ReviewData) {
			d.Run.Verdict = "Not ready: closed findings are now kept for every org, even with closures turned off, and the collector runs twice as often as before on every appliance."
			d.Run.Risk = "high"
		}},
		{name: "all_answered_finish_120x40", w: 120, h: 40, edit: func(d *ReviewData) { answerAll(d) }},
		{name: "sent_120x40", w: 120, h: 40, edit: func(d *ReviewData) {
			answerAll(d)
			d.Status, d.AgentWorking = reviewStatusSent, true
		}},
		{name: "nothing_needs_you_120x40", w: 120, h: 40, edit: func(d *ReviewData) {
			d.Run.Stops, d.Run.NotCovered, d.Answers = nil, nil, nil
			d.Run.StopLines, d.Run.UncoveredLines = 0, 0
			d.Run.Verdict = "Ready: docs and a lockfile only, nothing here needs a decision."
		}},
		{name: "nothing_needs_you_80x24", w: 80, h: 24, edit: func(d *ReviewData) {
			d.Run.Stops, d.Run.NotCovered, d.Answers = nil, nil, nil
			d.Run.StopLines, d.Run.UncoveredLines = 0, 0
			d.Run.Verdict = "Ready: docs and a lockfile only, nothing here needs a decision."
		}},
		{name: "preparing_80x24", w: 80, h: 24, edit: func(d *ReviewData) {
			d.Run, d.Freshness, d.Answers = nil, ReviewPreparing, nil
		}},
		{name: "preparing_120x40", w: 120, h: 40, edit: func(d *ReviewData) {
			d.Run, d.Freshness, d.Answers = nil, ReviewPreparing, nil
		}},
		{name: "failed_80x24", w: 80, h: 24, edit: func(d *ReviewData) {
			d.Run, d.Freshness, d.Answers = nil, ReviewFailed, nil
			d.FailReason = "reviewer reply holds no review JSON"
		}},
		{name: "stale_ci_failed_120x40", w: 120, h: 40, edit: func(d *ReviewData) {
			d.Freshness = ReviewStale
			d.CI, d.CIFailed = selfreview.CIFailure, []string{"unit"}
		}},
		{name: "stale_80x24", w: 80, h: 24, edit: func(d *ReviewData) {
			d.Freshness = ReviewStale
			d.CI = selfreview.CISuccess
		}},
		{name: "change_prefilled_120x40", w: 120, h: 40, keys: []string{"n"}},
		{name: "scope_decision_120x40", w: 120, h: 40, edit: withScope, keys: []string{"k", "k"}},
		{name: "scope_decision_80x24", w: 80, h: 24, edit: withScope, keys: []string{"k", "k"}},
		{name: "walk_list_card_120x40", w: 120, h: 40, edit: withScope, keys: []string{"2", "j"}},
		{name: "walk_card_focused_120x40", w: 120, h: 40, edit: withScope, keys: []string{"2", "j", "j", "enter", "j", "j"}},
		{name: "walk_scope_120x40", w: 120, h: 40, edit: withScope, keys: []string{"2"}},
		{name: "walk_narrow_80x24", w: 80, h: 24, edit: withScope, keys: []string{"2"}},
		{name: "walk_narrow_card_80x24", w: 80, h: 24, edit: withScope, keys: []string{"2", "j", "enter"}},
		{name: "walk_file_diff_120x40", w: 120, h: 40, keys: []string{"2", "enter", "j", "j", "j", "enter"}},
		{name: "walk_no_chapters_80x24", w: 80, h: 24, edit: func(d *ReviewData) { d.Run.Chapters = nil }, keys: []string{"2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := scanboxData(t)
			if tc.edit != nil {
				tc.edit(&d)
			}
			v := NewReviewView(d, tc.w, tc.h)
			v, _ = rvPress(v, tc.keys...)
			checkGolden(t, "review_view_"+tc.name+".golden", screenText(t, v))
		})
	}
}

// withScope gives the run the real EDR intent gap, made into the scope
// decision as Enforce makes it from a model that gave no scope stop.
func withScope(d *ReviewData) {
	d.Run.IntentGap = "The ticket also asks for incremental collection: partial runs, a time-range record for each event stream, gaps that fill back in, and window limits set by operators. None of that is in this change."
	selfreview.Upgrade(d.Run)
}

// The Walkthrough is a way into the decisions and the code, never a second
// place to answer: ⏎ on the scope row, or on a decision in a chapter, lands on
// that decision on Decide; ⏎ on a file opens its own diff, and esc comes back
// to the chapter it was opened from.
func TestReviewWalkthroughJumps(t *testing.T) {
	d := scanboxData(t)
	withScope(&d)
	v := NewReviewView(d, 120, 40)
	v, _ = rvPress(v, "2")
	if v.tab != reviewTabWalk {
		t.Fatal("2 did not open the Walkthrough")
	}
	if _, msgs := rvPress(v, "y"); len(msgs) != 0 {
		t.Fatal("y answered from the Walkthrough")
	}
	v, _ = rvPress(v, "enter") // the scope row
	if it, _ := v.selectedItem(); v.tab != reviewTabDecide || it.stop == nil || !it.stop.IsScope() {
		t.Fatalf("⏎ on the scope row landed on tab %v, %+v", v.tab, it)
	}

	v, _ = rvPress(v, "2", "j", "enter") // chapter 1, into its card
	if !v.cardFocus {
		t.Fatal("⏎ on a chapter did not move the keyboard into its card")
	}
	r, _ := v.selectedWalkRow()
	first := v.walkEntries(r)[0]
	v, _ = rvPress(v, "enter")
	if it, _ := v.selectedItem(); v.tab != reviewTabDecide || it.id != first.id {
		t.Fatalf("⏎ on decision %s landed on %q (tab %v)", first.id, it.id, v.tab)
	}

	v, _ = rvPress(v, "2")
	es := v.walkEntries(r)
	file := es[len(es)-1]
	v.cardFocus, v.walkItem = true, len(es)-1
	v, _ = rvPress(v, "enter")
	if v.screen != reviewScreenDiff || v.diffFile != file.path {
		t.Fatalf("⏎ on a file: screen %v file %q, want the diff of %q", v.screen, v.diffFile, file.path)
	}
	for _, row := range v.diffRows() {
		if f := v.d.Doc.Stream.Lines[row.primary()].File; f != file.path {
			t.Fatalf("the file's diff shows %q", f)
		}
	}
	v, _ = rvPress(v, "esc")
	if v.screen == reviewScreenDiff || v.tab != reviewTabWalk || !v.cardFocus {
		t.Errorf("esc from the file's diff: screen %v tab %v card %v, want the chapter card", v.screen, v.tab, v.cardFocus)
	}
}

// An intent gap is a decision, not a banner: no red line in the header, a dim
// pointer on the status line, the decision first, and the header's prose
// wrapped whole — never "…".
func TestReviewIntentGapIsADecisionNotABanner(t *testing.T) {
	d := scanboxData(t)
	withScope(&d)
	d.Run.Summary = strings.Repeat("The collector now reads each stream on its own and keeps going. ", 3)
	v := NewReviewView(d, 80, 30)
	head := plainLines(v.headerLines())
	if strings.Contains(head, "Not what was asked") || strings.Contains(head, "…") {
		t.Errorf("header:\n%s", head)
	}
	if !strings.Contains(head, "scope: decision 1") || !strings.Contains(head, "keeps going.") {
		t.Errorf("header lacks the scope pointer or the summary's end:\n%s", head)
	}
	if it, _ := v.selectedItem(); it.stop == nil || !it.stop.IsScope() {
		t.Errorf("the view opens on %+v, want the scope decision", it)
	}
}

// answerAll gives every stop an answer: a change on the first, a question on
// the second, ok on the rest.
func answerAll(d *ReviewData) {
	d.Answers = map[string]selfreview.Answer{}
	for i, s := range d.Run.Stops {
		a := selfreview.Answer{StopID: s.ID, Kind: selfreview.AnswerFine}
		switch i {
		case 0:
			a = selfreview.Answer{StopID: s.ID, Kind: selfreview.AnswerChange, Note: "save the cursor after the upload lands"}
		case 1:
			a = selfreview.Answer{StopID: s.ID, Kind: selfreview.AnswerAsk, Note: "does Dana know?"}
		}
		d.Answers[s.ID] = a
	}
}

// Every frame is exactly the terminal's size, at any size, on every screen —
// a line one cell too wide wraps and shears the whole layout.
func TestReviewViewNeverOverflows(t *testing.T) {
	for _, sz := range [][2]int{{30, 10}, {60, 16}, {80, 24}, {99, 30}, {100, 30}, {140, 50}, {220, 60}} {
		for _, keys := range [][]string{nil, {"enter"}, {"s"}, {"s", "enter"}, {"d"}, {"n"}, {"?"}, {"A"},
			{"G", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j"},
			{"2"}, {"2", "j", "enter"}, {"2", "j", "enter", "j", "j", "j", "j", "enter"}} {
			for _, edit := range []func(*ReviewData){nil, answerAll, withScope, func(d *ReviewData) {
				answerAll(d)
				d.Status = reviewStatusSent
			}} {
				d := scanboxData(t)
				if edit != nil {
					edit(&d)
				}
				v := NewReviewView(d, sz[0], sz[1])
				v, _ = rvPress(v, keys...)
				screenText(t, v)
			}
		}
	}
}

// The view opens on the first decision still waiting, and an answer moves on
// to the next one.
func TestReviewAnswerAdvances(t *testing.T) {
	v := NewReviewView(scanboxData(t), 120, 40)
	if it, _ := v.selectedItem(); it.id != "s2" {
		t.Fatalf("opened on %q, want s2 (s1 is already answered)", it.id)
	}
	v, msgs := rvPress(v, "y")
	if len(msgs) != 1 {
		t.Fatalf("got %d msgs, want 1", len(msgs))
	}
	m, ok := msgs[0].(reviewAnswerMsg)
	if !ok || m.RunID != "run-1" || m.Answer != (selfreview.Answer{StopID: "s2", Kind: selfreview.AnswerFine}) {
		t.Fatalf("msg = %#v", msgs[0])
	}
	if v.d.Answers["s2"].Kind != selfreview.AnswerFine {
		t.Error("the answer did not land in the view's own copy, so the row would not repaint")
	}
	if it, _ := v.selectedItem(); it.id != "s3" {
		t.Errorf("after y the cursor is on %q, want s3", it.id)
	}
	v, msgs = rvPress(v, "l")
	if a := msgs[0].(reviewAnswerMsg).Answer; a.Kind != selfreview.AnswerLater || a.StopID != "s3" {
		t.Errorf("l answered %+v, want later on s3", a)
	}
}

// The last answer lands the cursor on the finish row, and Enter there is S.
func TestReviewFinishRow(t *testing.T) {
	d := scanboxData(t)
	answerAll(&d)
	delete(d.Answers, "s8")
	v := NewReviewView(d, 120, 40)
	v, _ = rvPress(v, "y")
	it, _ := v.selectedItem()
	if it.kind != rowFinish {
		t.Fatalf("after the last answer the cursor is on %+v, want the finish row", it)
	}
	if !strings.Contains(screenText(t, v), "All 8 decisions answered") {
		t.Error("the finish row does not say every decision is answered")
	}
	_, msgs := rvPress(v, "enter")
	if len(msgs) != 1 || msgs[0] != (reviewSendMsg{RunID: "run-1"}) {
		t.Fatalf("enter on the finish row = %#v, want a send", msgs)
	}
}

func TestReviewChangeRequiresNote(t *testing.T) {
	v := NewReviewView(scanboxData(t), 120, 40)
	v, msgs := rvPress(v, "n")
	if !v.inputOpen || v.inputKind != selfreview.AnswerChange || v.inputFor != "s2" {
		t.Fatalf("n did not open a change note for s2 (open=%v kind=%q for=%q)", v.inputOpen, v.inputKind, v.inputFor)
	}
	// s2's reviewer recommends a change: the note starts as that change.
	if got := v.input.Value(); got != "Keep the daily full snapshot; run only fixes every 12 hours" {
		t.Errorf("note prefill = %q, want the recommended change", got)
	}
	v.input.SetValue("")
	v, msgs = rvPress(v, "enter")
	if len(msgs) != 0 || !v.inputOpen {
		t.Fatal("an empty note was accepted; it must be refused with the input left open")
	}
	if !strings.Contains(ansi.Strip(v.renderFooterLine()), "type a note first") {
		t.Errorf("the refusal is not on screen: %q", ansi.Strip(v.renderFooterLine()))
	}
	v, msgs = rvPress(v, "1", "2", "h", " ", "o", "n", "l", "y", "enter")
	if len(msgs) != 1 {
		t.Fatalf("got %d msgs, want 1", len(msgs))
	}
	got := msgs[0].(reviewAnswerMsg).Answer
	if got.Kind != selfreview.AnswerChange || got.Note != "12h only" || got.StopID != "s2" {
		t.Errorf("answer = %+v", got)
	}
	if v.inputOpen {
		t.Error("the input stayed open after saving")
	}
}

// n on a decision the reviewer recommends keeping opens an empty note; on the
// scope decision it opens with what is missing; ⏎ on a prefilled note sends it
// as it is.
func TestReviewChangePrefill(t *testing.T) {
	d := scanboxData(t)
	d.Answers = nil
	v := NewReviewView(d, 120, 40)
	if v, _ = rvPress(v, "n"); v.input.Value() != "" { // s1: recommend keep
		t.Errorf("keep-recommended note prefilled %q", v.input.Value())
	}
	v, _ = rvPress(v, "esc", "j", "n")
	v, msgs := rvPress(v, "enter")
	if len(msgs) != 1 || msgs[0].(reviewAnswerMsg).Answer.Note != "Keep the daily full snapshot; run only fixes every 12 hours" {
		t.Fatalf("prefilled change not sent as is: %#v", msgs)
	}
	sc := selfreview.Stop{ID: "g", Kind: selfreview.KindScope, Missing: []string{"partial runs", "operator window limits"}}
	if got := reviewChangePrefill(sc); got != "Also implement: partial runs, operator window limits" {
		t.Errorf("scope prefill = %q", got)
	}
}

func TestReviewAskAndCancel(t *testing.T) {
	v := NewReviewView(scanboxData(t), 80, 24)
	v, _ = rvPress(v, "a")
	if v.inputKind != selfreview.AnswerAsk {
		t.Fatalf("a opened %q", v.inputKind)
	}
	v, msgs := rvPress(v, "w", "h", "y", "esc")
	if len(msgs) != 0 || v.inputOpen {
		t.Fatal("esc must cancel without answering")
	}
	if !v.IsVisible() {
		t.Fatal("esc on the note closed the whole view")
	}
	v.d.Answers["s2"] = selfreview.Answer{StopID: "s2", Kind: selfreview.AnswerAsk, Note: "why?"}
	v, _ = rvPress(v, "a")
	if v.input.Value() != "why?" {
		t.Errorf("reopening ask did not prefill the note: %q", v.input.Value())
	}
}

// S is dimmed with the clause that failed, and the key refuses on exactly that
// clause — never a lit key that dead-clicks, never a dim one that acts.
func TestReviewSendGuards(t *testing.T) {
	d := scanboxData(t)
	d.Answers = nil
	v := NewReviewView(d, 140, 40)
	if got := ansi.Strip(v.renderFooterLine()); !strings.Contains(got, "S send (answer one first)") {
		t.Errorf("footer = %q, want S dimmed with 'answer one first'", got)
	}
	if _, msgs := rvPress(v, "S"); len(msgs) != 0 {
		t.Fatal("S sent with nothing answered")
	}

	v, _ = rvPress(v, "n", "x", "enter")
	v.SetTargets([]reviewTarget{{ID: "sess-1", Title: "api", Glyph: "✻", Live: true, Running: true}})
	if got := ansi.Strip(v.renderFooterLine()); !strings.Contains(got, "S send (api is working)") {
		t.Errorf("footer = %q, want S dimmed with 'api is working'", got)
	}
	if _, msgs := rvPress(v, "S"); len(msgs) != 0 {
		t.Fatal("S sent while the agent was running")
	}

	v.SetTargets([]reviewTarget{{ID: "sess-1", Title: "api", Glyph: "✻", Live: true}})
	if got := ansi.Strip(v.renderFooterLine()); !strings.Contains(got, "S send to ✻ api") {
		t.Errorf("footer = %q, want S naming its target", got)
	}
	_, msgs := rvPress(v, "S")
	if len(msgs) != 1 || msgs[0] != (reviewSendMsg{RunID: "run-1"}) {
		t.Fatalf("S = %#v, want one reviewSendMsg", msgs)
	}
}

// Answers that are all "ok" give the agent nothing to do: S offers to approve
// instead of waking it, and the footer offers A in S's place.
func TestReviewOnlyOKOffersApprove(t *testing.T) {
	d := scanboxData(t) // s1 is answered ok
	v := NewReviewView(d, 140, 40)
	if got := ansi.Strip(v.renderFooterLine()); !strings.Contains(got, "A approve") || strings.Contains(got, "S send") {
		t.Errorf("footer = %q, want A approve in S's place", got)
	}
	v, msgs := rvPress(v, "S")
	if len(msgs) != 0 || v.confirm == nil || !strings.Contains(v.confirm.question, "Nothing to change. Approve instead?") {
		t.Fatalf("S with only ok answers: msgs %#v confirm %+v", msgs, v.confirm)
	}
	_, msgs = rvPress(v, "y")
	if len(msgs) != 1 || msgs[0] != (reviewApproveMsg{RunID: "run-1"}) {
		t.Fatalf("y = %#v, want an approve", msgs)
	}
}

// S names its target, t cycles it (persisting the pick), and a target that
// isn't running refuses with the way out.
func TestReviewSendTargetCycles(t *testing.T) {
	d := scanboxData(t)
	d.Answers = map[string]selfreview.Answer{"s1": {StopID: "s1", Kind: selfreview.AnswerChange, Note: "x"}}
	d.Targets = []reviewTarget{
		{ID: "a", Title: "api", Glyph: "✻", Live: true},
		{ID: "b", Title: "tests", Glyph: "◇", Live: false},
	}
	v := NewReviewView(d, 160, 40)
	if got := ansi.Strip(v.renderFooterLine()); !strings.Contains(got, "S send to ✻ api") {
		t.Errorf("footer = %q", got)
	}
	v, msgs := rvPress(v, "t")
	if len(msgs) != 1 || msgs[0] != (reviewTargetMsg{RunID: "run-1", Checkout: "/wt/vulns", SessionID: "b"}) {
		t.Fatalf("t = %#v", msgs)
	}
	if got := ansi.Strip(v.renderFooterLine()); !strings.Contains(got, "tests has stopped: t picks another") {
		t.Errorf("dead target footer = %q", got)
	}
	if _, msgs := rvPress(v, "S"); len(msgs) != 0 {
		t.Fatal("S sent to a session that isn't running")
	}
	v.SetTargets([]reviewTarget{d.Targets[1], d.Targets[0]})
	if tg, _ := v.target(); tg.ID != "b" {
		t.Errorf("target after refresh = %q, want b", tg.ID)
	}
}

// With no live session in the checkout, S asks to start one; y asks the wiring
// to, n backs out, and nothing else answers the question.
func TestReviewSendOffersANewSession(t *testing.T) {
	d := scanboxData(t)
	d.Answers = map[string]selfreview.Answer{"s1": {StopID: "s1", Kind: selfreview.AnswerChange, Note: "x"}}
	d.Targets, d.NewSessionGlyph = nil, "✻"
	v := NewReviewView(d, 160, 40)
	if got := ansi.Strip(v.renderFooterLine()); !strings.Contains(got, "S send to a new ✻ session") {
		t.Errorf("footer = %q", got)
	}
	v, msgs := rvPress(v, "S")
	if len(msgs) != 0 || v.confirm == nil {
		t.Fatalf("S should ask first: %#v", msgs)
	}
	if got := ansi.Strip(v.renderFooterLine()); !strings.Contains(got, "Start a new ✻ session with your answers?") {
		t.Errorf("confirm footer = %q", got)
	}
	v, msgs = rvPress(v, "j")
	if len(msgs) != 0 || v.confirm == nil {
		t.Fatal("a stray key dismissed the question")
	}
	v, _ = rvPress(v, "n")
	if v.confirm != nil {
		t.Fatal("n did not cancel")
	}
	v, _ = rvPress(v, "S")
	_, msgs = rvPress(v, "y")
	if len(msgs) != 1 || msgs[0] != (reviewStartSessionMsg{RunID: "run-1", Checkout: "/wt/vulns"}) {
		t.Fatalf("y = %#v", msgs)
	}
}

// A asks first while decisions are unanswered; r asks first while answers
// would be left unsent; a stale run can't be approved.
func TestReviewApproveAndRegenerateConfirm(t *testing.T) {
	d := scanboxData(t)
	d.Freshness = ReviewStale
	v := NewReviewView(d, 120, 40)
	if _, msgs := rvPress(v, "A"); len(msgs) != 0 || v.confirm != nil {
		t.Fatal("A acted on a stale snapshot")
	}
	v, msgs := rvPress(v, "r")
	if len(msgs) != 0 || v.confirm == nil || !strings.Contains(v.confirm.question, "won't be sent") {
		t.Fatalf("r with an unsent answer should ask first: %#v %+v", msgs, v.confirm)
	}
	v, msgs = rvPress(v, "y")
	if len(msgs) != 1 || msgs[0] != (reviewRegenerateMsg{Checkout: "/wt/vulns"}) {
		t.Fatalf("r then y = %#v", msgs)
	}

	d.Freshness = ReviewFresh
	v.SetData(d)
	v, msgs = rvPress(v, "A")
	if len(msgs) != 0 || v.confirm == nil || !strings.Contains(v.confirm.question, "Approve with 7 decisions unanswered?") {
		t.Fatalf("A with unanswered decisions should ask first: %#v %+v", msgs, v.confirm)
	}
	if _, msgs = rvPress(v, "y"); len(msgs) != 1 || msgs[0] != (reviewApproveMsg{RunID: "run-1"}) {
		t.Fatalf("A then y = %#v", msgs)
	}

	answerAll(&d)
	v.SetData(d)
	if _, msgs := rvPress(v, "A"); len(msgs) != 1 || msgs[0] != (reviewApproveMsg{RunID: "run-1"}) {
		t.Fatalf("A with everything answered = %#v, want no question", msgs)
	}
	d.Freshness = ReviewPreparing
	v.SetData(d)
	if _, msgs := rvPress(v, "r"); len(msgs) != 0 {
		t.Fatal("r regenerated while a run was already preparing")
	}
}

// After a send the view is a closing screen: what went where, and v for the
// next round once the agent is done — refused while it works.
func TestReviewSentScreen(t *testing.T) {
	d := scanboxData(t)
	answerAll(&d)
	d.Status, d.AgentWorking = reviewStatusSent, true
	v := NewReviewView(d, 120, 40)
	frame := screenText(t, v)
	for _, want := range []string{"Sent to ✻ api: 1 change, 1 question, 6 ok.", "The agent is working on your answers.",
		"save the cursor after the upload lands"} {
		if !strings.Contains(frame, want) {
			t.Errorf("sent screen lacks %q:\n%s", want, frame)
		}
	}
	if _, msgs := rvPress(v, "v"); len(msgs) != 0 {
		t.Fatal("v started round 2 while the agent was still working")
	}
	v.SetAgentWorking(false)
	if _, msgs := rvPress(v, "v"); len(msgs) != 1 || msgs[0] != (reviewRoundMsg{Checkout: "/wt/vulns"}) {
		t.Fatalf("v = %#v, want the next round", msgs)
	}
}

// Esc unwinds one layer at a time: note, card, skipped, then the view.
func TestReviewEscUnwinds(t *testing.T) {
	v := NewReviewView(scanboxData(t), 80, 24)
	v, _ = rvPress(v, "enter")
	if !v.cardFocus {
		t.Fatal("enter did not open the card")
	}
	v, msgs := rvPress(v, "esc")
	if v.cardFocus || len(msgs) != 0 {
		t.Fatal("esc in the card should return to the list, not close")
	}
	v, _ = rvPress(v, "s")
	if v.screen != reviewScreenSkipped {
		t.Fatal("s did not open the skip list")
	}
	v, _ = rvPress(v, "esc")
	if v.screen != reviewScreenDecisions {
		t.Fatal("esc on the skip list should return to the decisions")
	}
	v, _ = rvPress(v, "?")
	if !v.helpOpen {
		t.Fatal("? did not open the keys overlay")
	}
	v, _ = rvPress(v, "esc")
	if v.helpOpen || !v.IsVisible() {
		t.Fatal("esc on the overlay should close only the overlay")
	}
	v, msgs = rvPress(v, "esc")
	if v.IsVisible() || len(msgs) != 1 || msgs[0] != (reviewCloseMsg{}) {
		t.Fatalf("esc at the top = visible %v, msgs %#v", v.IsVisible(), msgs)
	}
}

// x on a skipped file raises it as the human's own change: an answer keyed by
// FileStopID, which then shows in the list as "added by you".
func TestReviewRaiseSkippedFile(t *testing.T) {
	v := NewReviewView(scanboxData(t), 120, 40)
	v, _ = rvPress(v, "s")
	f, _ := v.selectedFile()
	if f.skip == nil {
		t.Fatal("s opened the file list on a file nobody reviewed, not on the skips")
	}
	s := *f.skip
	v, _ = rvPress(v, "x")
	if !v.inputOpen || v.inputFor != selfreview.FileStopID(s.Path) {
		t.Fatalf("x did not open a note for %q (for=%q)", selfreview.FileStopID(s.Path), v.inputFor)
	}
	v, msgs := rvPress(v, "c", "h", "e", "c", "k", "enter")
	if len(msgs) != 1 {
		t.Fatalf("got %d msgs", len(msgs))
	}
	if a := msgs[0].(reviewAnswerMsg).Answer; a.StopID != "file:"+s.Path || a.Kind != selfreview.AnswerChange || a.Note != "check" {
		t.Errorf("answer = %+v", a)
	}
	var found bool
	for _, it := range v.items() {
		if it.kind == rowAddedByYou && it.path == s.Path {
			found = true
		}
	}
	if !found {
		t.Error("the raised file is not in the list")
	}
	if !strings.Contains(screenText(t, v), "→ you raised it") {
		t.Error("the skip row does not say it was raised")
	}
}

// The files the reviewer covered nowhere fold into one "Not reviewed" row on
// Decide — never a decision; ⏎ lists them, first, where x raises one.
func TestReviewNotReviewedIsNotADecision(t *testing.T) {
	d := scanboxData(t)
	v := NewReviewView(d, 120, 40)
	for i, it := range v.items() {
		if it.kind != rowNotReviewed {
			continue
		}
		v.cursor = i
		if _, msgs := rvPress(v, "y"); len(msgs) != 0 {
			t.Fatal("y answered the Not reviewed row as if it were a decision")
		}
		if c := v.counts(); c.Decisions != len(d.Run.Stops) || c.NotReviewed != 1 {
			t.Errorf("counts = %+v: Not reviewed must not be a decision", c)
		}
		if got := ansi.Strip(v.renderFooterLine()); !strings.Contains(got, "⏎ see the files") {
			t.Errorf("footer = %q", got)
		}
		v, _ = rvPress(v, "enter")
		if f, ok := v.selectedFile(); !ok || f.path != "go.mod" || f.skip != nil {
			t.Fatalf("⏎ opened on %+v, want go.mod (not reviewed)", f)
		}
		if got := ansi.Strip(v.renderFooterLine()); !strings.Contains(got, "x review it myself") {
			t.Errorf("file list footer = %q", got)
		}
		return
	}
	t.Fatal("no Not reviewed row")
}

// A skip fleet refused shows fleet's reason on the card.
func TestReviewNotReviewedCardShowsFleetsReason(t *testing.T) {
	d := scanboxData(t)
	const ingest = "pipelines/ingestors/scanbox-sc-ingestor/src/scanbox_sc_ingestor/ingest.py"
	d.Run.NotCovered = append(d.Run.NotCovered, ingest)
	if d.Run.NotCoveredWhy == nil {
		d.Run.NotCoveredWhy = map[string]string{}
	}
	d.Run.NotCoveredWhy[ingest] = "skipped as 'covered by tests' — 152 changed lines of logic"
	v := NewReviewView(d, 140, 50)
	v.openFiles(true)
	for i, f := range reviewFiles(d.Run) {
		if f.path != ingest {
			continue
		}
		v.skipCursor = i
		if frame := screenText(t, v); !strings.Contains(frame, "152 changed lines of logic") {
			t.Errorf("the card does not show the reason:\n%s", frame)
		}
		return
	}
	t.Fatal("ingest.py is not in the file list")
}

// The cursor stays on the same decision when new data arrives — the highlight
// never moves on its own.
func TestReviewSetDataKeepsCursor(t *testing.T) {
	d := scanboxData(t)
	v := NewReviewView(d, 120, 40)
	v, _ = rvPress(v, "j")
	run := *d.Run
	run.Stops = append([]selfreview.Stop{{ID: "s0", Kind: "edge", Title: "new"}}, run.Stops...)
	d.Run = &run
	v.SetData(d)
	if it, _ := v.selectedItem(); it.id != "s3" {
		t.Errorf("cursor moved to %q, want s3", it.id)
	}
}

// J/K walks the card's hunks.
func TestReviewCardHunks(t *testing.T) {
	v := NewReviewView(scanboxData(t), 120, 40)
	v, _ = rvPress(v, "k", "enter") // s1 has two hunks
	lay := v.cardLayout()
	if len(lay.hunkLines) != 2 {
		t.Fatalf("s1 card has %d hunks, want 2", len(lay.hunkLines))
	}
	v, _ = rvPress(v, "J")
	if v.hunk != 1 {
		t.Errorf("J left the hunk at %d", v.hunk)
	}
	v, _ = rvPress(v, "K")
	if v.hunk != 0 {
		t.Errorf("K left the hunk at %d", v.hunk)
	}
}

// The card shows the cited lines and a little context, not the whole hunk —
// a new file is one hunk, and a citation deep in it must not open on line 1.
// e then widens the window.
func TestReviewCardTrimsToCitedLines(t *testing.T) {
	var src strings.Builder
	src.WriteString("@@ -0,0 +1,200 @@\n")
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&src, "+line %d\n", i)
	}
	doc := review.NewDoc([]review.File{{Path: "pkg/new.go", Status: "added", Additions: 200, Patch: src.String()}}, "")
	run := &selfreview.Run{Verdict: "Ready", Stops: []selfreview.Stop{{ID: "s1", Kind: "logic", Title: "t", Decide: "OK as is?",
		Hunks: []selfreview.Hunk{{Path: "pkg/new.go", Side: "new", Start: 120, End: 124}}}}}
	v := NewReviewView(ReviewData{RunID: "r", Run: run, Doc: doc}, 120, 40)
	text := ansi.Strip(strings.Join(v.cardLayout().lines, "\n"))
	if strings.Contains(text, "line 1\n") || !strings.Contains(text, "line 117") || strings.Contains(text, "line 116") ||
		!strings.Contains(text, "line 127") || strings.Contains(text, "line 128") {
		t.Errorf("card is not the cited lines ±%d:\n%s", reviewCardContext, text)
	}
	v, _ = rvPress(v, "enter", "e")
	if text := ansi.Strip(strings.Join(v.cardLayout().lines, "\n")); !strings.Contains(text, "line 110") {
		t.Errorf("e did not widen the window:\n%s", text)
	}
}

// With a worktree behind the doc and the whole hunk on screen, e opens
// unchanged lines around it.
func TestReviewExpandRebinds(t *testing.T) {
	dir := t.TempDir()
	var src strings.Builder
	for i := 1; i <= 80; i++ {
		if i == 42 {
			src.WriteString("\tretryTimeout := 5 * time.Second\n")
			continue
		}
		src.WriteString("// line\n")
	}
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "demo.go"), []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	doc := review.NewDoc([]review.File{{Path: "pkg/demo.go", Status: "modified", Additions: 1, Deletions: 1,
		Patch: "@@ -41,3 +41,3 @@\n // line\n-\tretryDelay := 2 * time.Second\n+\tretryTimeout := 5 * time.Second\n // line"}}, dir)
	if !doc.CanExpand() {
		t.Skip("doc could not load the worktree source")
	}
	run := &selfreview.Run{Verdict: "v", Stops: []selfreview.Stop{{ID: "s1", Kind: "tuning", Title: "t", Decide: "ok?",
		Hunks: []selfreview.Hunk{{Path: "pkg/demo.go", Side: "new", Start: 42, End: 42}}}}}
	v := NewReviewView(ReviewData{RunID: "r", Run: run, Doc: doc}, 120, 40)
	v, _ = rvPress(v, "enter")
	before := len(v.cardLayout().lines)
	v, _ = rvPress(v, "e")
	if after := len(v.cardLayout().lines); after <= before {
		t.Errorf("e did not grow the card (%d → %d lines)", before, after)
	}
}

func TestReviewRowText(t *testing.T) {
	ApplyPalette(PaletteFleetPink)
	four := selfreview.Counts{Decisions: 4, Unanswered: 4}
	for _, tc := range []struct {
		b    reviewBadge
		want string
	}{
		{reviewBadge{Status: ReviewBadgePreparing}, "Review · preparing…"},
		{reviewBadge{Status: ReviewBadgeReady, Counts: four, Model: "opus", Elapsed: 61 * time.Second}, "Review · 4 to decide"},
		{reviewBadge{Status: ReviewBadgeReady, Counts: selfreview.Counts{Decisions: 4, Answered: 4, Changes: 1}}, "Review · ready to send"},
		{reviewBadge{Status: ReviewBadgeReady, Counts: selfreview.Counts{Decisions: 4, Answered: 4, OK: 4}}, "Review · nothing to change"},
		{reviewBadge{Status: ReviewBadgeReady, Counts: selfreview.Counts{NotReviewed: 2}}, "Review · 2 files not reviewed"},
		{reviewBadge{Status: ReviewBadgeReady}, "Review · nothing needs you"},
		{reviewBadge{Status: ReviewBadgeStale, Counts: four}, "Review · ⚠ code changed"},
		{reviewBadge{Status: ReviewBadgeFailed, Reason: "timed out\nmore"}, "Review · failed"},
		{reviewBadge{Status: ReviewBadgeSent, Working: true}, "Review · sent · agent working"},
		{reviewBadge{Status: ReviewBadgeSent}, "Review · press v for round 2"},
	} {
		if got := reviewRowText(tc.b); got != tc.want {
			t.Errorf("%+v → %q, want %q", tc.b, got, tc.want)
		}
	}
	for _, b := range []reviewBadge{{}, {Status: ReviewBadgeApproved, Counts: four}} {
		if reviewRowShown(b) {
			t.Errorf("%+v must have no row", b)
		}
	}
	unseen := renderReviewRow(SidebarItem{IsReview: true, Review: reviewBadge{Status: ReviewBadgeReady, Counts: four}}, 40, false)
	seen := renderReviewRow(SidebarItem{IsReview: true, Review: reviewBadge{Status: ReviewBadgeReady, Counts: four, Seen: true}}, 40, false)
	if unseen == seen {
		t.Error("an unopened review must read louder than an opened one")
	}
	if !strings.Contains(unseen, "1;") && !strings.Contains(unseen, "[1m") {
		t.Errorf("unseen row is not bold: %q", unseen)
	}
	if !strings.HasPrefix(ansi.Strip(seen), "    ✎ Review · 4 to decide") {
		t.Errorf("row = %q (session-row indent, glyph first)", ansi.Strip(seen))
	}
}

// The one definition of every count: the sidebar row, the view's header and
// footer, and the CLI (cmd/fleet's TestPrintReviewCountsMatchTheOneDefinition)
// all print selfreview.CountsOf's phrases — pinned here on the real DELTA-2524
// run, where they once said 9 (row), 6 (CLI) and 463 vs 106 lines.
func TestReviewCountsAgreeEverywhere(t *testing.T) {
	ApplyPalette(PaletteFleetPink)
	b, err := os.ReadFile(filepath.Join("..", "selfreview", "testdata", "delta2524_run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var run selfreview.Run
	if err := json.Unmarshal(b, &run); err != nil {
		t.Fatal(err)
	}
	c := selfreview.CountsOf(&run, nil)
	if c.ToDecide() != "4 to decide" || c.LinesNeedYou() != "106 of 1,489 changed lines need you" {
		t.Fatalf("CountsOf = %q, %q", c.ToDecide(), c.LinesNeedYou())
	}
	row := reviewRowText(reviewBadge{Status: ReviewBadgeReady, Counts: c})
	if row != "Review · "+c.ToDecide() {
		t.Errorf("row = %q, want %q", row, "Review · "+c.ToDecide())
	}
	v := NewReviewView(ReviewData{RunID: "r", Run: &run, Freshness: ReviewFresh}, 120, 40)
	frame := screenText(t, v)
	for _, want := range []string{c.LinesNeedYou(), c.Progress(), c.SafeToSkipLabel()} {
		if !strings.Contains(frame, want) {
			t.Errorf("header lacks %q:\n%s", want, frame)
		}
	}
	// An answer moves the row, the header and the footer together.
	v, _ = rvPress(v, "y")
	c2 := v.counts()
	if c2.ToDecide() != "3 to decide" || !strings.Contains(screenText(t, v), c2.Progress()) {
		t.Errorf("after one answer: %q / %q", c2.ToDecide(), c2.Progress())
	}
}

// The glyph is one cell everywhere it is measured.
func TestReviewGlyphIsOneCell(t *testing.T) {
	for _, g := range []string{ReviewGlyph, "◆", "✓", "⚠"} {
		if w := ansi.StringWidth(g); w != 1 {
			t.Errorf("%s is %d cells (x/ansi), want 1", g, w)
		}
		if w := lipgloss.Width(g); w != 1 {
			t.Errorf("%s is %d cells (lipgloss), want 1", g, w)
		}
	}
}

func TestReviewPreviewLine(t *testing.T) {
	ApplyPalette(PaletteFleetPink)
	if got := renderReviewPreviewLine(reviewPreview{}, 80); got != "" {
		t.Errorf("zero value rendered %q", got)
	}
	for _, tc := range []struct {
		p    reviewPreview
		w    int
		want string
	}{
		{reviewPreview{Status: ReviewBadgeReady, Verdict: "Ready once you settle deploy order."}, 80, "  ✎ Ready once you settle deploy order.  v review"},
		{reviewPreview{Status: ReviewBadgePreparing}, 80, "  ✎ preparing review…"},
		{reviewPreview{Status: ReviewBadgeFailed, Reason: "timed out"}, 80, "  ✎ review failed: timed out"},
		{reviewPreview{Status: ReviewBadgeReady, Verdict: strings.Repeat("long verdict ", 10)}, 40, "  ✎ long verdict long verdic…  v review"},
	} {
		got := renderReviewPreviewLine(tc.p, tc.w)
		if ansi.Strip(got) != tc.want {
			t.Errorf("%+v @%d = %q, want %q", tc.p, tc.w, ansi.Strip(got), tc.want)
		}
		if lipgloss.Width(got) > tc.w {
			t.Errorf("%+v overflows %d", tc.p, tc.w)
		}
	}

	s := &session.Session{ID: "a", FirstPrompt: "do the thing"}
	base := RenderPreview(s, "out", nil, 60, 10, false)
	if RenderPreviewWithReview(s, "out", nil, 60, 10, false, reviewPreview{}) != base {
		t.Error("the zero reviewPreview changed the preview")
	}
	got := ansi.Strip(RenderPreviewWithReview(s, "out", nil, 60, 10, false, reviewPreview{Status: ReviewBadgePreparing}))
	lines := strings.Split(got, "\n")
	if len(lines) < 2 || !strings.Contains(lines[1], "✎ preparing review…") {
		t.Errorf("review line is not directly under the prompt strip:\n%s", got)
	}
}

// An elsewhere line's cited path gets a line of its own; prose without one
// stays whole.
func TestReviewSplitCitation(t *testing.T) {
	for in, want := range map[string][2]string{
		"EdgeWatcher retries once — collectors/edgewatcher/collector.go": {"EdgeWatcher retries once", "collectors/edgewatcher/collector.go"},
		"Budgets range from 2 to 25 minutes — docs/dry-runs.md.":         {"Budgets range from 2 to 25 minutes", "docs/dry-runs.md"},
		"Nothing like it — anywhere in the repo":                         {"Nothing like it — anywhere in the repo", ""},
	} {
		if text, cite := reviewSplitCitation(in); text != want[0] || cite != want[1] {
			t.Errorf("%q → %q / %q, want %q / %q", in, text, cite, want[0], want[1])
		}
	}
}

// ctrl+q leaves the review from any depth in one press — the same chord that
// detaches an attached session — and drops an unsaved note rather than
// typing a control character into it.
func TestReviewCtrlQClosesFromAnywhere(t *testing.T) {
	for name, keys := range map[string][]string{
		"list":        {},
		"card":        {"enter"},
		"note open":   {"n"},
		"help open":   {"?"},
		"walkthrough": {"2", "enter"},
		"full diff":   {"d"},
	} {
		t.Run(name, func(t *testing.T) {
			v := NewReviewView(scanboxData(t), 120, 40)
			v, _ = rvPress(v, keys...)
			v, msgs := rvPress(v, "ctrl+q")
			if v.IsVisible() || len(msgs) != 1 || msgs[0] != (reviewCloseMsg{}) {
				t.Fatalf("ctrl+q from %s = visible %v, msgs %#v", name, v.IsVisible(), msgs)
			}
			if v.inputOpen || v.helpOpen || v.confirm != nil {
				t.Fatalf("ctrl+q from %s left state behind: input %v help %v confirm %v", name, v.inputOpen, v.helpOpen, v.confirm != nil)
			}
		})
	}
}

// esc steps out one level at a time. A jump out of a chapter — to a decision
// on Decide, or to a file's diff — comes back to that chapter, and the
// Walkthrough's top steps back to Decide; only esc on Decide's list closes.
func TestReviewEscStepsOutOneLevel(t *testing.T) {
	v := NewReviewView(scanboxData(t), 120, 40)
	v, _ = rvPress(v, "2", "enter")
	if v.tab != reviewTabWalk || !v.cardFocus {
		t.Fatal("2 then enter should open the first chapter")
	}
	v, msgs := rvPress(v, "esc")
	if !v.IsVisible() || v.tab != reviewTabWalk || v.cardFocus || len(msgs) != 0 {
		t.Fatalf("esc in a chapter should return to the chapter list: tab %v card %v msgs %v", v.tab, v.cardFocus, msgs)
	}
	v, msgs = rvPress(v, "esc")
	if !v.IsVisible() || v.tab != reviewTabDecide || len(msgs) != 0 {
		t.Fatalf("esc on the chapter list should go to Decide, not close: tab %v msgs %v", v.tab, msgs)
	}
	v, msgs = rvPress(v, "esc")
	if v.IsVisible() || len(msgs) != 1 || msgs[0] != (reviewCloseMsg{}) {
		t.Fatalf("esc on Decide's list should close: visible %v msgs %v", v.IsVisible(), msgs)
	}
}

func TestReviewEscReturnsToTheChapterAfterAJump(t *testing.T) {
	v := NewReviewView(scanboxData(t), 120, 40)
	v, _ = rvPress(v, "2", "enter")
	entries := v.walkEntries(func() walkRow { r, _ := v.selectedWalkRow(); return r }())
	jumped := map[walkEntryKind]bool{}
	for i, e := range entries {
		if jumped[e.kind] || (e.kind == walkEntryFile && v.d.Doc == nil) {
			continue
		}
		jumped[e.kind] = true
		v.walkItem = i
		walkCursor := v.walkCursor
		v, _ = rvPress(v, "enter")
		if v.tab == reviewTabWalk && v.screen != reviewScreenDiff {
			t.Fatalf("enter on entry %d (%v) didn't leave the chapter", i, e.kind)
		}
		v, msgs := rvPress(v, "esc")
		if !v.IsVisible() || len(msgs) != 0 || v.tab != reviewTabWalk || !v.cardFocus || v.walkCursor != walkCursor || v.walkItem != i {
			t.Fatalf("esc after jumping to %v should land back in the chapter on the same entry: tab %v card %v cursor %d/%d item %d/%d msgs %v",
				e.kind, v.tab, v.cardFocus, v.walkCursor, walkCursor, v.walkItem, i, msgs)
		}
	}
	if !jumped[walkEntryDecision] {
		t.Fatal("fixture's first chapter has no decision to jump to")
	}
}
