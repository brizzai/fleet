package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// typeSwitchCases returns the case type names in every type switch inside the
// named method of a file.
func typeSwitchCases(t *testing.T, file, recvType, method string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	out := map[string]bool{}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name.Name != method || fd.Recv == nil {
			return true
		}
		if !strings.Contains(typeString(fd.Recv.List[0].Type), recvType) {
			return true
		}
		found = true
		ast.Inspect(fd.Body, func(m ast.Node) bool {
			cc, ok := m.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range cc.List {
				if id, ok := expr.(*ast.Ident); ok {
					out[id.Name] = true
				}
			}
			return true
		})
		return false
	})
	if !found {
		t.Fatalf("%s.%s not found in %s — renamed? this guard is now vacuous", recvType, method, file)
	}
	return out
}

func typeString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.StarExpr:
		return typeString(v.X)
	case *ast.Ident:
		return v.Name
	}
	return ""
}

// TestWorktreeDialogAsyncMessagesAreRouted pins the wiring that made the whole
// ticket-suggestion feature dead on arrival.
//
// routeToModal is reached ONLY from handleKey and handlePaste, so it carries
// key and paste messages. Anything a dialog returns as a tea.Cmd — a debounce
// firing, a lookup replying — arrives as a plain message in Home.Update, and
// with no case there it is silently dropped. WorktreeDialog.Update handled both
// ticket messages perfectly and never received either, so every unit test that
// called d.Update directly passed while the app showed no suggestions at all.
//
// Any new message the dialog handles itself must therefore also be forwarded
// from Home.Update. This test fails if one isn't.
func TestWorktreeDialogAsyncMessagesAreRouted(t *testing.T) {
	dialogCases := typeSwitchCases(t, "workspace_picker.go", "WorktreeDialog", "Update")

	var async []string
	for name := range dialogCases {
		// tea.* types arrive through routeToModal; our own message types do not.
		if strings.HasSuffix(name, "Msg") && !strings.HasPrefix(name, "tea") {
			async = append(async, name)
		}
	}
	if len(async) == 0 {
		t.Fatal("found no dialog-owned messages — this guard is vacuous")
	}

	homeCases := typeSwitchCases(t, "app.go", "Home", "Update")
	for _, name := range async {
		if !homeCases[name] {
			t.Errorf("WorktreeDialog.Update handles %s, but Home.Update has no case for it.\n"+
				"routeToModal only carries key and paste messages, so this message is dropped "+
				"and the feature it drives never runs — silently, and with every unit test still passing.", name)
		}
	}
}

// TestTicketInferenceNeverShellsOutFromUpdate keeps the ticket paths off the
// blocking git call.
//
// session.GetRepoRoot runs `git rev-parse` with an 8-second ceiling on a cache
// miss. sessionsByTicket runs on the Bubble Tea Update goroutine — the one that
// paints every frame — and called it once per session, while the ticket paths'
// own comments claimed they did "no I/O beyond a stat". A brand-new worktree is
// exactly the cache miss.
//
// Scoped to these two files on purpose. GetRepoRoot is used widely elsewhere in
// this package and auditing all of it is a separate job; this pins the paths the
// review covered so they cannot quietly regain the call.
func TestTicketInferenceNeverShellsOutFromUpdate(t *testing.T) {
	for _, file := range []string{"ticket.go", "palette_tickets.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			code := line
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx] // comments may name it; calls may not
			}
			if strings.Contains(code, "session.GetRepoRoot(") {
				t.Errorf("%s:%d calls session.GetRepoRoot, which shells out to git "+
					"(8s ceiling) on a cache miss — and this file runs on the Update "+
					"goroutine. Use session.LookupRepoRoot and degrade on a miss.", file, i+1)
			}
		}
	}
}

// TestManualSessionCreationSeedsNoPrompt pins the one-shot rule: a seeded first
// message is the gesture of starting a worktree from a ticket, not a property of
// the directory that worktree happens to be.
//
// handleSessionCreate used to run ticketPromptFor on EVERY creation — inferring
// an identifier from the branch, and, before that, reusing the prompt.txt
// already sitting in .fleet/ticket/<ID>/. The reuse branch never looked at the
// branch at all, so a checkout that once held a ticket re-asked the original
// task on every session added by hand afterwards, forever, including long after
// the checkout had moved on to master.
//
// The invariant now: sessionCreateMsg.prompt is whatever the caller set, and the
// only caller that sets it is the worktree-creation path (plus `fleet worktree
// --ticket`/`-p`, which build the Session directly). Nothing between the message
// and the launch may write to that field.
func TestManualSessionCreationSeedsNoPrompt(t *testing.T) {
	if mentions(t, "handleSessionCreate", "linear") {
		t.Error("handleSessionCreate reaches into internal/linear — the only ticket " +
			"fetch left is at worktree creation, and putting one back here re-seeds " +
			"the prompt into every manually added session")
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", nil, 0)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "prompt" {
				continue
			}
			t.Errorf("app.go:%d assigns to a .prompt field. A first message may only be "+
				"set where the sessionCreateMsg is built (the worktree-creation path); "+
				"writing it on the way to the launch is how every manually created "+
				"session inherited a ticket's prompt.", fset.Position(assign.Pos()).Line)
		}
		return true
	})
}
