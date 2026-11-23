package selfreview

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/claudeaccount"
)

func envelope(result string) string {
	return `{"type":"result","subtype":"success","is_error":false,"num_turns":3,"total_cost_usd":0.25,"session_id":"sid","result":` + jsonString(result) + `}`
}

type fakeAsk struct {
	replies []string
	errs    []error
	calls   []claudeaccount.AskOptions
}

func (f *fakeAsk) ask(_ context.Context, o claudeaccount.AskOptions) (string, error) {
	i := len(f.calls)
	f.calls = append(f.calls, o)
	var err error
	if i < len(f.errs) {
		err = f.errs[i]
	}
	if i < len(f.replies) {
		return f.replies[i], err
	}
	return "", err
}

func runnerInput() Input {
	return Input{Round: 1, Prompts: []string{"do the thing"}, Files: []FileInput{{Path: "a.go", Additions: 1, Patch: patchLines(1, 1)}}}
}

func TestClaudeRunnerCall(t *testing.T) {
	f := &fakeAsk{replies: []string{envelope(minimalReview)}}
	r := ClaudeRunner{ConfigDir: "/acct/1", Ask: f.ask}
	run, meta, err := r.Review(context.Background(), "/wt", runnerInput())
	if err != nil {
		t.Fatal(err)
	}
	if run.Verdict == "" || meta.Attempts != 1 || meta.Model != DefaultModel || meta.CostUSD != 0.25 || meta.SessionID != "sid" {
		t.Errorf("run=%+v meta=%+v", run, meta)
	}
	o := f.calls[0]
	if o.Dir != "/wt" || o.ConfigDir != "/acct/1" || o.Model != "opus" || o.OutputFormat != "json" || !o.Verbose ||
		!o.NoSessionPersistence || o.Timeout != DefaultTimeout {
		t.Errorf("call options = %+v", o)
	}
	if strings.Join(o.AllowedTools, ",") != "Read,Grep,Glob" {
		t.Errorf("allowed tools = %v", o.AllowedTools)
	}
	if !o.RestrictTools || strings.Join(o.Tools, ",") != "Read,Grep,Glob" || !o.StrictMCPConfig {
		t.Errorf("the reviewer must get no shell and no MCP: restrict=%v tools=%v strictMCP=%v",
			o.RestrictTools, o.Tools, o.StrictMCPConfig)
	}
	if strings.Join(o.DisallowedTools, ",") != "Edit,Write,NotebookEdit" {
		t.Errorf("disallowed tools = %v", o.DisallowedTools)
	}
	if strings.Contains(o.Instruction, "do the thing") || !strings.Contains(o.Input, "do the thing") {
		t.Error("prompt text must ride stdin, not argv")
	}
	// Enforce ran: the one changed file, a.go, is skipped by guide.
	if run.TotalFiles != 1 || run.Skipped[0].By != "guide" {
		t.Errorf("Enforce did not run: %+v", run)
	}
}

func TestClaudeRunnerRepairsOnce(t *testing.T) {
	f := &fakeAsk{replies: []string{envelope("Sorry, here is my review in prose."), envelope(minimalReview)}}
	run, meta, err := ClaudeRunner{Ask: f.ask, Model: "opus"}.Review(context.Background(), "/wt", runnerInput())
	if err != nil || run == nil {
		t.Fatalf("err = %v", err)
	}
	if meta.Attempts != 2 || len(f.calls) != 2 {
		t.Errorf("attempts %d calls %d, want 2", meta.Attempts, len(f.calls))
	}
	repair := f.calls[1]
	if repair.Input != "Sorry, here is my review in prose." {
		t.Errorf("repair input = %q, want the model's text unwrapped", repair.Input)
	}
	if len(repair.AllowedTools) != 0 || !repair.RestrictTools || len(repair.Tools) != 0 ||
		!strings.Contains(strings.Join(repair.DisallowedTools, " "), "Bash") {
		t.Errorf("repair must run without tools: %+v", repair)
	}
	if repair.Model != "opus" {
		t.Errorf("repair model = %q", repair.Model)
	}
}

func TestClaudeRunnerGivesUpAfterRepair(t *testing.T) {
	f := &fakeAsk{replies: []string{envelope("nope"), envelope("still nope")}}
	_, meta, err := ClaudeRunner{Ask: f.ask}.Review(context.Background(), "/wt", runnerInput())
	if !errors.Is(err, ErrNoJSON) || len(f.calls) != 2 || meta.Attempts != 2 {
		t.Errorf("err = %v calls = %d", err, len(f.calls))
	}
}

func TestClaudeRunnerPassesFailuresThrough(t *testing.T) {
	boom := errors.New("claude: You've hit your session limit")
	f := &fakeAsk{errs: []error{boom}}
	_, _, err := ClaudeRunner{Ask: f.ask, Timeout: time.Minute}.Review(context.Background(), "/wt", runnerInput())
	if !errors.Is(err, boom) || len(f.calls) != 1 {
		t.Errorf("err = %v, calls = %d (an exec failure is not a JSON problem: no repair)", err, len(f.calls))
	}
	if f.calls[0].Timeout != time.Minute {
		t.Errorf("timeout = %v", f.calls[0].Timeout)
	}
}

// verboseReply is what `claude -p --output-format json --verbose` prints: the
// whole event array, shaped as Claude Code 2.1.328 actually emits it
// (measured), with absolute tool paths under the worktree.
func verboseReply(result string) string {
	return `[{"type":"system","subtype":"init","cwd":"/wt","tools":["Glob","Grep","Read"]},
{"type":"assistant","message":{"content":[{"type":"text","text":"Reading."},
  {"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/wt/src/ingest.py"}},
  {"type":"tool_use","id":"t2","name":"Grep","input":{"pattern":"lastFixedTime","path":"/wt/src"}}]}},
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"..."}]}},
{"type":"assistant","message":{"content":[
  {"type":"tool_use","id":"t3","name":"Read","input":{"file_path":"/wt/src/ingest.py","offset":200}},
  {"type":"tool_use","id":"t4","name":"Read","input":{"file_path":"/wt/src/cursor.py"}},
  {"type":"tool_use","id":"t5","name":"Glob","input":{"pattern":"**/*.sql"}},
  {"type":"tool_use","id":"t6","name":"Read","input":{"file_path":"/elsewhere/notes.md"}}]}},
{"type":"result","subtype":"success","is_error":false,"num_turns":6,"duration_ms":74213,"total_cost_usd":0.42,"session_id":"sid","result":` + jsonString(result) + `}]`
}

func TestClaudeRunnerRecordsWhatTheReviewerRead(t *testing.T) {
	f := &fakeAsk{replies: []string{verboseReply(minimalReview)}}
	run, meta, err := ClaudeRunner{Ask: f.ask}.Review(context.Background(), "/wt", runnerInput())
	if err != nil {
		t.Fatal(err)
	}
	m := run.Reviewer
	if m == nil {
		t.Fatal("run carries no reviewer record")
	}
	if m.Turns != 6 || m.CostUSD != 0.42 || m.DurationMS != 74213 {
		t.Errorf("figures = %+v", m)
	}
	if m.Tools["Read"] != 4 || m.Tools["Grep"] != 1 || m.Tools["Glob"] != 1 || len(m.Tools) != 3 {
		t.Errorf("tools = %v, want Read 4 / Grep 1 / Glob 1", m.Tools)
	}
	// Distinct, worktree-relative, first-touched order; a path outside the
	// worktree stays absolute; a Glob with no path names none.
	if got := strings.Join(m.Files, ","); got != "src/ingest.py,src,src/cursor.py,/elsewhere/notes.md" {
		t.Errorf("files = %s", got)
	}
	if m.FilesRead != 3 || m.ReadLabel() != "3 files read" {
		t.Errorf("files read = %d (%q), want 3", m.FilesRead, m.ReadLabel())
	}
	if meta.CostUSD != 0.42 || meta.NumTurns != 6 {
		t.Errorf("run meta = %+v", meta)
	}
}

// The case the record exists for: a review that opened nothing says so.
func TestClaudeRunnerRecordsAReviewerThatReadNothing(t *testing.T) {
	reply := `[{"type":"system","subtype":"init"},{"type":"assistant","message":{"content":[{"type":"text","text":"{}"}]}},` +
		`{"type":"result","subtype":"success","is_error":false,"num_turns":1,"duration_ms":16900,"total_cost_usd":0.11,"result":` + jsonString(minimalReview) + `}]`
	f := &fakeAsk{replies: []string{reply}}
	run, _, err := ClaudeRunner{Ask: f.ask}.Review(context.Background(), "/wt", runnerInput())
	if err != nil {
		t.Fatal(err)
	}
	if m := run.Reviewer; m == nil || m.FilesRead != 0 || len(m.Tools) != 0 || m.ReadLabel() != "read no files" || m.DurationMS != 16900 {
		t.Errorf("reviewer = %+v", run.Reviewer)
	}
}

// A model that writes its own "reviewer" key gets it overwritten: the record
// is fleet's, read from the events.
func TestReviewerRecordIsNeverTheModels(t *testing.T) {
	forged := `{"v":1,"verdict":"x","risk":"low","stops":[],"skipped":[],"reviewer":{"turns":99,"files_read":40}}`
	run, _, err := Parse([]byte(forged))
	if err != nil {
		t.Fatal(err)
	}
	Enforce(run, runnerInput())
	if run.Reviewer != nil {
		t.Errorf("Enforce kept the model's reviewer record: %+v", run.Reviewer)
	}
}

// badWording breaks the prose rules (verdict prefix, a snake_case title, a
// two-part question); goodWording is the same review fixed.
const badWording = `{"v":1,"verdict":"Moves the cursor; hinges on deploy order.","risk":"med","stops":[{"id":"s1","kind":"deploy","title":"Cursor in collection_marker","why":"Order matters.","how":"The cursor moves first.","recommend":{"answer":"keep"},"checked":[],"decide":"Ship the ingestor first, or will you delete the row?","hunks":[{"path":"a.go","side":"new","start":1,"end":1}]}],"skipped":[],"pending":[],"summary":"Moves the cursor.","chapters":[{"id":"c1","title":"The cursor","role":"heart","text":"It moves.","files":["a.go"]}]}`
const goodWording = `{"v":1,"verdict":"Ready once deploy order is settled.","risk":"med","stops":[{"id":"s1","kind":"deploy","title":"Backfill lost if collector ships first","why":"Order matters.","how":"The cursor moves first.","recommend":{"answer":"keep"},"checked":[],"decide":"Will the ingestor ship first?","hunks":[{"path":"a.go","side":"new","start":1,"end":1}]}],"skipped":[],"pending":[],"summary":"Moves the cursor.","chapters":[{"id":"c1","title":"The cursor","role":"heart","text":"It moves.","files":["a.go"]}]}`
const worseWording = `{"v":1,"verdict":"x_y moves; hinges on fooBar and baz_qux and more and more and more and more and more and more words.","risk":"med","stops":[{"id":"s1","kind":"deploy","title":"a_b c_d","why":"One. Two. Three.","checked":[],"decide":"a_b? c_d? or will instead of","hunks":[{"path":"a.go","side":"new","start":1,"end":1}]}],"skipped":[],"pending":[],"summary":"Moves the cursor.","chapters":[{"id":"c1","title":"The cursor","role":"heart","text":"It moves.","files":["a.go"]}]}`

func TestClaudeRunnerRepairsWordingOnce(t *testing.T) {
	f := &fakeAsk{replies: []string{envelope(badWording), envelope(goodWording)}}
	run, meta, err := ClaudeRunner{Ask: f.ask}.Review(context.Background(), "/wt", runnerInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || meta.Attempts != 2 {
		t.Fatalf("calls %d attempts %d, want 2", len(f.calls), meta.Attempts)
	}
	repair := f.calls[1]
	if !strings.Contains(repair.Input, "PROBLEMS:") || !strings.Contains(repair.Input, "collection_marker") ||
		len(repair.Tools) != 0 || !repair.RestrictTools {
		t.Errorf("wording repair call = %+v", repair)
	}
	if run.Verdict != "Ready once deploy order is settled." || run.Stops[0].Title != "Backfill lost if collector ships first" {
		t.Errorf("kept the wrong version: %+v", run)
	}
	if !strings.Contains(strings.Join(meta.Warnings, "\n"), "(0 left)") {
		t.Errorf("warnings = %v", meta.Warnings)
	}
}

func TestClaudeRunnerKeepsOriginalWhenWordingRepairIsWorse(t *testing.T) {
	for name, reply := range map[string]string{"worse": envelope(worseWording), "unreadable": envelope("sorry"), "empty": ""} {
		t.Run(name, func(t *testing.T) {
			f := &fakeAsk{replies: []string{envelope(badWording), reply}}
			run, meta, err := ClaudeRunner{Ask: f.ask}.Review(context.Background(), "/wt", runnerInput())
			if err != nil {
				t.Fatalf("wording must never fail a review: %v", err)
			}
			if len(f.calls) != 2 || meta.Attempts != 2 {
				t.Errorf("calls %d attempts %d", len(f.calls), meta.Attempts)
			}
			if run.Verdict != "Moves the cursor; hinges on deploy order." {
				t.Errorf("verdict = %q, want the original kept", run.Verdict)
			}
		})
	}
	f := &fakeAsk{replies: []string{envelope(badWording)}, errs: []error{nil, errors.New("rate limited")}}
	if run, _, err := (ClaudeRunner{Ask: f.ask}).Review(context.Background(), "/wt", runnerInput()); err != nil || run == nil {
		t.Errorf("a failed wording call failed the review: %v", err)
	}
}

func TestClaudeRunnerNoWordingCallWhenClean(t *testing.T) {
	f := &fakeAsk{replies: []string{envelope(goodWording)}}
	_, meta, err := ClaudeRunner{Ask: f.ask}.Review(context.Background(), "/wt", runnerInput())
	if err != nil || len(f.calls) != 1 || meta.Attempts != 1 {
		t.Errorf("err %v calls %d attempts %d", err, len(f.calls), meta.Attempts)
	}
}
