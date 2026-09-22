package ui

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brizzai/fleet/internal/analytics"
	"github.com/brizzai/fleet/internal/config"
)

// traceHome builds a Home recording into a temp HOME, plus a sink holding
// whatever the trace sent.
func traceHome(t *testing.T) (*Home, *[]map[string]interface{}) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	var sent []map[string]interface{}
	orig := traceTrack
	traceTrack = func(event string, props map[string]interface{}) {
		if event != analytics.EventOnboardingFirstRunTrace {
			return
		}
		sent = append(sent, props)
	}
	t.Cleanup(func() { traceTrack = orig })

	h := &Home{cfg: &config.Config{TelemetryMode: config.TelemetryFull}}
	h.firstRun = newFirstRunTrace(time.Now())
	return h, &sent
}

// at records an action as though it happened `sec` seconds into the run.
func at(h *Home, sec int, action string) {
	h.firstRun.startedAt = time.Now().Add(-time.Duration(sec) * time.Second)
	h.trace(action)
}

// TestFirstRunTraceSerializes walks a plausible first run and pins the array the
// event carries: one "<seconds>:<action>" per entry, in order, with a run of
// navigation keys collapsed into a single counted entry.
func TestFirstRunTraceSerializes(t *testing.T) {
	h, _ := traceHome(t)

	at(h, 0, traceConsentFull)
	at(h, 4, traceThemeKeep)
	at(h, 6, traceLaunchpadShown)
	for i := 0; i < 12; i++ {
		at(h, 9+i/6, traceNav) // a scroll: one entry, stamped when it began
	}
	at(h, 23, traceLaunchpadEnter)
	at(h, 36, traceAttach)
	at(h, 40, traceDetachBail)
	at(h, 70, traceSessionError)
	at(h, 145, traceQuit)

	got := h.firstRun.stopAndTake().Trace
	want := []string{
		"0:consent_full", "4:theme_keep", "6:launchpad_shown", "9:nav_x12",
		"23:launchpad_enter", "36:attach", "40:detach_bail", "70:session_error",
		"145:quit",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("trace:\n got %q\nwant %q", got, want)
	}
}

// TestFirstRunTraceCapsAndCounts pins the overflow behaviour: the oldest entries
// go, the newest survive, and the count of what was lost rides along — so a long
// first run reports the truncation rather than hiding it.
func TestFirstRunTraceCapsAndCounts(t *testing.T) {
	h, _ := traceHome(t)

	// Alternate two tokens so nothing collapses and each press costs a slot.
	for i := 0; i < firstRunTraceMaxEntries+25; i++ {
		if i%2 == 0 {
			h.trace(traceEsc)
		} else {
			h.trace(traceHelp)
		}
	}
	p := h.firstRun.stopAndTake()
	if len(p.Trace) != firstRunTraceMaxEntries {
		t.Errorf("kept %d entries, want the cap of %d", len(p.Trace), firstRunTraceMaxEntries)
	}
	if p.EntriesDropped != 25 {
		t.Errorf("entries_dropped = %d, want 25", p.EntriesDropped)
	}
	// Press 0 was an esc and presses alternate, so after 25 were dropped the
	// window runs from press 25 (a help) to press 324 (an esc).
	if !strings.HasSuffix(p.Trace[0], ":"+traceHelp) {
		t.Errorf("first kept entry is %q — the cap dropped more than the oldest 25", p.Trace[0])
	}
	if !strings.HasSuffix(p.Trace[len(p.Trace)-1], ":"+traceEsc) {
		t.Errorf("last entry is %q — the cap dropped the newest, not the oldest", p.Trace[len(p.Trace)-1])
	}
}

// TestFirstRunTraceOnceRecordsOnce covers session_waiting: the first time any
// session needs the user is the signal, not every time one does.
func TestFirstRunTraceOnceRecordsOnce(t *testing.T) {
	h, _ := traceHome(t)
	for i := 0; i < 5; i++ {
		h.traceOnce(traceSessionWaiting)
	}
	if got := h.firstRun.stopAndTake().Trace; len(got) != 1 {
		t.Fatalf("traceOnce recorded %d entries (%q), want 1", len(got), got)
	}
}

// TestFirstRunTraceRefusesNonEnums is the last line of the privacy rule: a call
// site that builds a token out of something it shouldn't gets it dropped rather
// than merely flagged in review.
func TestFirstRunTraceRefusesNonEnums(t *testing.T) {
	h, _ := traceHome(t)
	for _, bad := range []string{
		"/Users/someone/code/secret-repo",
		"fix the login bug",
		"Session Title",
		"open PR",  // a space
		"nav_x1a",  // not a count
		"NAV",      // not lowercase
		"attach-2", // not a word character
		"",
	} {
		h.trace(bad)
	}
	if got := h.firstRun.stopAndTake().Trace; len(got) != 0 {
		t.Fatalf("recorded %q — every one of those should have been refused", got)
	}
}

// TestFirstRunTraceCleanSendMarksAndDeletes covers the quit path: one event, the
// milestone marked, and the file gone so the next launch has nothing to resend.
func TestFirstRunTraceCleanSendMarksAndDeletes(t *testing.T) {
	h, sent := traceHome(t)
	h.trace(traceAttach)
	if _, err := os.Stat(firstRunTracePath()); err != nil {
		t.Fatalf("recording wrote no file: %v", err)
	}

	h.sendFirstRunTrace(145)
	h.sendFirstRunTrace(145) // a second quit path must not resend

	if len(*sent) != 1 {
		t.Fatalf("sent %d events, want exactly 1", len(*sent))
	}
	props := (*sent)[0]
	if props["ended"] != "clean" {
		t.Errorf(`ended = %v, want "clean"`, props["ended"])
	}
	if props["uptime_seconds"] != 145 {
		t.Errorf("uptime_seconds = %v, want 145", props["uptime_seconds"])
	}
	if !analytics.MilestoneReached(analytics.MilestoneFirstRunTrace) {
		t.Error("milestone not marked — the next launch would send the run again")
	}
	if _, err := os.Stat(firstRunTracePath()); !os.IsNotExist(err) {
		t.Errorf("trace file survived a clean send: %v", err)
	}
}

// TestFirstRunTraceUncleanSendsOnceAndDeletes is the case the file exists for: a
// newcomer who closed the terminal instead of quitting. The next launch picks
// the trace up, sends it once, and leaves nothing behind.
func TestFirstRunTraceUncleanSendsOnceAndDeletes(t *testing.T) {
	h, sent := traceHome(t)

	// A previous run's leftover.
	leftover := firstRunTracePayload{Trace: []string{"0:consent_basic", "12:esc"}, UptimeSeconds: 31}
	writeTraceFile(t, leftover)

	h.sendUncleanFirstRunTrace()
	h.sendUncleanFirstRunTrace()

	if len(*sent) != 1 {
		t.Fatalf("sent %d events, want exactly 1", len(*sent))
	}
	props := (*sent)[0]
	if props["ended"] != "unclean" {
		t.Errorf(`ended = %v, want "unclean"`, props["ended"])
	}
	if got := props["trace"].([]string); !reflect.DeepEqual(got, leftover.Trace) {
		t.Errorf("trace = %q, want %q", got, leftover.Trace)
	}
	if props["uptime_seconds"] != 31 {
		t.Errorf("uptime_seconds = %v, want the previous run's 31", props["uptime_seconds"])
	}
	if _, err := os.Stat(firstRunTracePath()); !os.IsNotExist(err) {
		t.Errorf("trace file survived an unclean send: %v", err)
	}
}

// TestFirstRunTraceStopsAtTheMilestone: once the run has been reported, nothing
// more is recorded — including by the launch that reported someone else's.
func TestFirstRunTraceStopsAtTheMilestone(t *testing.T) {
	h, _ := traceHome(t)
	writeTraceFile(t, firstRunTracePayload{Trace: []string{"0:consent_basic"}})

	h.sendUncleanFirstRunTrace()
	h.trace(traceAttach)
	h.trace(traceQuit)

	if got := h.firstRun.stopAndTake().Trace; len(got) != 0 {
		t.Fatalf("recorded %q after the milestone was marked", got)
	}
	if _, err := os.Stat(firstRunTracePath()); !os.IsNotExist(err) {
		t.Error("a stopped recorder rewrote the trace file")
	}
}

// TestFirstRunTraceSilentWhenTelemetryOff is the consent gate. The milestone is
// still marked and the file still deleted: an install that never reports is
// one-shot too, rather than retrying on every launch forever.
func TestFirstRunTraceSilentWhenTelemetryOff(t *testing.T) {
	h, sent := traceHome(t)
	h.cfg.TelemetryMode = config.TelemetryOff
	h.trace(traceAttach)

	h.sendFirstRunTrace(20)

	if len(*sent) != 0 {
		t.Fatalf("sent %d events with telemetry off, want 0", len(*sent))
	}
	if !analytics.MilestoneReached(analytics.MilestoneFirstRunTrace) {
		t.Error("milestone not marked — this would retry every launch")
	}
	if _, err := os.Stat(firstRunTracePath()); !os.IsNotExist(err) {
		t.Error("trace file left behind with telemetry off")
	}
}

// TestLeftoverTraceIsNotRecordedOver: a launch that finds a previous run's
// trace reports it rather than recording a new one over it — the recorder
// persists on a debounce and would replace the file before the send reads it.
func TestLeftoverTraceIsNotRecordedOver(t *testing.T) {
	traceHome(t)

	if !firstRunTraceShouldRecord() {
		t.Fatal("a clean first launch should record")
	}
	writeTraceFile(t, firstRunTracePayload{Trace: []string{"0:consent_basic"}})
	if firstRunTraceShouldRecord() {
		t.Error("recorded over a leftover trace, which the next send would then report as the previous run")
	}
	removeFirstRunTraceFile()
	if !analytics.MarkOnboardingMilestone(analytics.MilestoneFirstRunTrace) {
		t.Fatal("could not mark the milestone")
	}
	if firstRunTraceShouldRecord() {
		t.Error("kept recording after the run had been reported")
	}
}

// TestNilRecorderCostsNothing: past the first run h.firstRun is nil and every
// entry point has to be a no-op rather than a panic.
func TestNilRecorderCostsNothing(t *testing.T) {
	h := &Home{}
	h.trace(traceAttach)
	h.traceOnce(traceSessionWaiting)
	h.traceKey("j")
	h.sendFirstRunTrace(1)
}

func writeTraceFile(t *testing.T, p firstRunTracePayload) {
	t.Helper()
	path := firstRunTracePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// --- Source guards -------------------------------------------------------

// TestFirstRunTraceEmitsOnlyEnums is the guard the privacy line rests on: every
// string the recorder can be handed is a vocabulary constant or one of the three
// constructors, and every one of those is a bare enum token. A call site passing
// a variable — a path, a title, a typed character — fails here, long before the
// runtime check in add() would quietly drop it.
func TestFirstRunTraceEmitsOnlyEnums(t *testing.T) {
	constants := traceConstants(t)
	if len(constants) == 0 {
		t.Fatal("found no trace constants — the source scan is broken, not the code")
	}
	for name, value := range constants {
		if !traceTokenPattern.MatchString(value) {
			t.Errorf("%s = %q is not a trace token", name, value)
		}
	}

	// The constructors, over every input they can actually be given.
	for _, name := range traceDialogNames(t) {
		for _, phase := range []string{"open", "enter", "esc"} {
			if tok := traceDialog(name, phase); !traceTokenPattern.MatchString(tok) {
				t.Errorf("traceDialog(%q, %q) = %q is not a trace token", name, phase, tok)
			}
		}
	}
	for id := range dispatchCommandIDs(t) {
		if tok := traceCommand(id); !traceTokenPattern.MatchString(tok) {
			t.Errorf("traceCommand(%q) = %q is not a trace token", id, tok)
		}
		if tok := traceMenu(id); !traceTokenPattern.MatchString(tok) {
			t.Errorf("traceMenu(%q) = %q is not a trace token", id, tok)
		}
	}

	isEnum := func(where string, e ast.Expr) {
		switch arg := e.(type) {
		case *ast.Ident:
			if _, ok := constants[arg.Name]; !ok {
				t.Errorf("%s: %s is not a trace vocabulary constant", where, arg.Name)
			}
		case *ast.CallExpr:
			fn, ok := arg.Fun.(*ast.Ident)
			if !ok || (fn.Name != "traceDialog" && fn.Name != "traceCommand" && fn.Name != "traceMenu") {
				t.Errorf("%s: a call to something other than traceDialog/traceCommand/traceMenu", where)
			}
		default:
			t.Errorf("%s: neither a vocabulary constant nor a constructor call, "+
				"so it could carry anything", where)
		}
	}

	// Every call site hands one of those in...
	calls := traceCallSites(t)
	if len(calls) == 0 {
		t.Fatal("found no h.trace call sites — the source scan is broken, not the code")
	}
	for _, c := range calls {
		isEnum(c.pos+": "+c.fn+"(...) argument", c.arg)
	}

	// ...and so do the two tables the forwarders read from, which are the only
	// other way a string reaches the recorder.
	for _, table := range []string{"traceForAction", "traceKeyToken"} {
		results := traceTableReturns(t, table)
		if len(results) == 0 {
			t.Fatalf("found no returns in %s — the source scan is broken, not the code", table)
		}
		for _, r := range results {
			isEnum(table+" return", r)
		}
	}
}

// traceTableReturns returns every non-empty-string expression a lookup table
// can return.
func traceTableReturns(t *testing.T, name string) []ast.Expr {
	t.Helper()
	fn := uiFunc(t, "firstrun_trace.go", name)
	var out []ast.Expr
	ast.Inspect(fn, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		if lit, ok := ret.Results[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if v, err := strconv.Unquote(lit.Value); err == nil && v == "" {
				return true // the "no entry" answer
			}
		}
		out = append(out, ret.Results[0])
		return true
	})
	return out
}

// TestTraceForActionReadsOnlyKnownActions: the ActionLog table is a mapping into
// the vocabulary, not a transformation of the action string, so an action nobody
// mapped records nothing rather than whatever it happens to spell.
func TestTraceForActionReadsOnlyKnownActions(t *testing.T) {
	mapped := map[string]string{
		"attach session":       traceAttach,
		"attach via slot":      traceAttachViaSlot,
		"create session":       traceNewSession,
		"fork to worktree":     traceFork,
		"delete origin":        traceDelete,
		"undo delete":          traceUndo,
		"restart session":      traceRestart,
		"quick approve":        traceQuickApprove,
		"open terminal drawer": traceDrawer,
		"command: open_pr":     "command_open_pr",
		"context menu: resume": "menu_resume",
	}
	for action, want := range mapped {
		if got := traceForAction(action); got != want {
			t.Errorf("traceForAction(%q) = %q, want %q", action, got, want)
		}
	}
	// Actions that carry a name, a path or a title in the action itself stay
	// out of the vocabulary entirely.
	for _, unmapped := range []string{"palette jump", "ticket jump", "move account", "open editor"} {
		if got := traceForAction(unmapped); got != "" {
			t.Errorf("traceForAction(%q) = %q, want no entry", unmapped, got)
		}
	}
}

// TestTraceNamesEveryRoutedDialog keeps traceDialogName and routeToModal in
// step. They are two copies of one precedence order: a dialog added to
// routeToModal without a name here would trace as whichever dialog sits below
// it, reporting a screen the user never saw.
func TestTraceNamesEveryRoutedDialog(t *testing.T) {
	routed := dialogFieldsInOrder(t, "routeToModal")
	named := dialogFieldsInOrder(t, "traceDialogName")
	if len(routed) == 0 || len(named) == 0 {
		t.Fatal("found no dialogs — the source scan is broken, not the code")
	}
	if !reflect.DeepEqual(routed, named) {
		t.Errorf("routeToModal and traceDialogName disagree:\n routeToModal: %v\n traceDialogName: %v",
			routed, named)
	}
}

type traceCall struct {
	fn  string
	arg ast.Expr
	pos string
}

// traceCallSites finds every h.trace / h.traceOnce / <recorder>.record /
// <recorder>.recordOnce call in the package's non-test sources.
func traceCallSites(t *testing.T) []traceCall {
	t.Helper()
	// The recorder's own forwarders take the token as a parameter; what they
	// are handed is checked at the real call sites, and at the two tables
	// below (traceEnumTables), so scanning them would only flag the plumbing.
	forwarder := map[string]bool{"trace": true, "traceOnce": true, "traceKey": true, "logAction": true}

	var out []traceCall
	fset := token.NewFileSet()
	for _, path := range uiSourceFiles(t) {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if fn, ok := n.(*ast.FuncDecl); ok && forwarder[fn.Name.Name] {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "trace", "traceOnce", "record", "recordOnce":
			default:
				return true
			}
			out = append(out, traceCall{
				fn:  sel.Sel.Name,
				arg: call.Args[0],
				pos: fset.Position(call.Pos()).String(),
			})
			return true
		})
	}
	return out
}

// traceConstants returns the trace vocabulary: every trace* string constant
// declared in firstrun_trace.go, by name.
func traceConstants(t *testing.T) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "firstrun_trace.go", nil, 0)
	if err != nil {
		t.Fatalf("parse firstrun_trace.go: %v", err)
	}
	out := map[string]string{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "trace") || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if v, err := strconv.Unquote(lit.Value); err == nil {
					out[name.Name] = v
				}
			}
		}
	}
	return out
}

// traceDialogNames returns the non-empty names traceDialogName can return.
func traceDialogNames(t *testing.T) []string {
	t.Helper()
	fn := uiFunc(t, "firstrun_trace.go", "traceDialogName")
	var out []string
	ast.Inspect(fn, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		lit, ok := ret.Results[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if v, err := strconv.Unquote(lit.Value); err == nil && v != "" {
			out = append(out, v)
		}
		return true
	})
	return out
}

// dialogFieldsInOrder reads a switch of `case h.<field>.IsVisible():` clauses
// and returns the field names in the order they are tested.
func dialogFieldsInOrder(t *testing.T, fnName string) []string {
	t.Helper()
	file := "app.go"
	if fnName == "traceDialogName" {
		file = "firstrun_trace.go"
	}
	fn := uiFunc(t, file, fnName)

	var out []string
	ast.Inspect(fn, func(n ast.Node) bool {
		cc, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, e := range cc.List {
			call, ok := e.(*ast.CallExpr)
			if !ok {
				continue
			}
			method, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || method.Sel.Name != "IsVisible" {
				continue
			}
			if recv, ok := method.X.(*ast.SelectorExpr); ok {
				out = append(out, recv.Sel.Name)
			}
		}
		return true
	})
	return out
}

func uiFunc(t *testing.T, file, name string) *ast.FuncDecl {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	t.Fatalf("%s not found in %s — this guard needs rewiring", name, file)
	return nil
}

func uiSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !strings.HasSuffix(e, "_test.go") {
			out = append(out, e)
		}
	}
	return out
}
