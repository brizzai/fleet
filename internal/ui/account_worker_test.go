package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Every path that reveals the UI must start the quota poller.
//
// There are two and they do not overlap: a fleet with any pinned repo or session
// boots through bootstrapDoneMsg, an empty one through discoveryMsg (dispatched
// only when bootstrapRepoSet() is empty). The poller was started from the empty
// path alone — so for everyone with a repo, which is everyone who would bother
// configuring a second account, it never ran at all for the process lifetime.
//
// That is not a small degradation. accountUsage stays empty, so Usage.Known() is
// false everywhere: the header readout renders nothing, least_used scores every
// account at the unknown midpoint and silently collapses into configured order,
// ErrNotLoggedIn is never observed so dropLoggedOut never fires, and the heal on
// restart can never see a spent or logged-out pin. The whole quota half of the
// feature is dead and nothing says so.
//
// Asserted structurally because there is nothing to observe at runtime: the miss
// is a goroutine that was never started, which looks exactly like a fleet whose
// accounts happen to be unpolled.
func TestBothRevealPathsStartTheAccountWorker(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", nil, 0)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}

	// The two case clauses that reveal the UI, by the message type they match.
	want := map[string]bool{"bootstrapDoneMsg": false, "discoveryMsg": false}

	ast.Inspect(f, func(n ast.Node) bool {
		cc, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, e := range cc.List {
			id, ok := e.(*ast.Ident)
			if !ok {
				continue
			}
			if _, tracked := want[id.Name]; !tracked {
				continue
			}
			ast.Inspect(cc, func(m ast.Node) bool {
				sel, ok := m.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == "startAccountWorker" {
					want[id.Name] = true
				}
				return true
			})
		}
		return true
	})

	for branch, found := range want {
		if !found {
			t.Errorf("the %s branch never calls startAccountWorker — account quota "+
				"is never polled on that boot path", branch)
		}
	}
}

// The Once is what makes calling it from both branches safe. Without it a launch
// that somehow hit both would run two pollers, doubling the `security` and HTTP
// traffic and racing on writeAccountUsage.
func TestAccountWorkerStartsAtMostOnce(t *testing.T) {
	h := &Home{}
	started := 0
	// Stand in for the goroutine: the Once is the thing under test, not what it
	// launches.
	for range 3 {
		h.accountWorkerOnce.Do(func() { started++ })
	}
	if started != 1 {
		t.Errorf("account worker started %d times, want 1", started)
	}
}
