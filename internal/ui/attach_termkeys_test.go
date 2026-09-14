package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestAttachRestoresTerminalModes is the drift guard for the regression that
// brought the #268 repaint storm back. fleet's attach ends by killing the tmux
// client rather than detaching it (Ctrl+Q, internal/tmux/pty.go), so tmux never
// runs its terminal cleanup and the mouse reporting it enabled for `mouse on`
// is left switched on in fleet's own terminal. Bubble Tea does not clear it
// either: on resume its renderer only *sets* the modes the view asks for
// (1002 + SGR 1006), so tmux's 1000 and — the expensive one — 1003 any-motion
// survive, and fleet would then be woken by every pointer movement over the
// window for the rest of the process. Nothing about that failure is visible
// until someone moves the mouse.
//
// The three attach paths (attachSession, the drawer's full attach, the account
// login pane) all run through attachCmd.Run, so this asserts the restore lives
// there rather than in any one tea.Exec callback: a fourth path added later
// gets it for free, and cannot forget it.
func TestAttachRestoresTerminalModes(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", nil, 0)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}

	var body *ast.BlockStmt
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Run" || fn.Recv == nil {
			return true
		}
		if len(fn.Recv.List) != 1 {
			return true
		}
		if ident, ok := fn.Recv.List[0].Type.(*ast.Ident); ok && ident.Name == "attachCmd" {
			body = fn.Body
		}
		return true
	})
	if body == nil {
		t.Fatal("attachCmd.Run not found in app.go — if the attach moved, move this guard with it")
	}

	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "termkeys" && sel.Sel.Name == "Reassert" {
			found = true
		}
		return true
	})
	if !found {
		t.Error("attachCmd.Run does not call termkeys.Reassert: the terminal keeps " +
			"whatever mouse reporting tmux left on, and the next scroll is a " +
			"full-View() repaint storm that nothing in the process will stop")
	}
}
