package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestAttachRestoresTerminalModes is the drift guard for the regression that
// brought the #268 repaint storm back. fleet's attach ends by killing the tmux
// client rather than detaching it (Ctrl+Q, internal/tmux/pty.go), so tmux never
// runs its terminal cleanup and the mouse reporting it enabled for `mouse on`
// is left switched on in fleet's own terminal. Bubble Tea will not take it back
// off — its renderer emits a mouse-mode change only when the mode differs from
// the last view's, and Home.chrome reports MouseModeNone on every frame — so a
// single attach re-arms the ~270 MouseWheelMsg/sec repaint storm for the rest of
// the process. Nothing about that failure is visible until someone scrolls.
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
			"whatever mouse/key reporting tmux left on, and the next scroll is a " +
			"full-View() repaint storm that nothing in the process will stop")
	}
}

// TestChromeKeepsMouseReportingOff pins the other half of the pair. Reassert
// only holds while the view never asks for reporting: the renderer would emit a
// DECSET the moment MouseMode differs from the previous frame's, and nothing in
// the TUI has ever handled a mouse event, so every such event is a full View()
// bought for nothing.
func TestChromeKeepsMouseReportingOff(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", nil, 0)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}

	var body *ast.BlockStmt
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name == "chrome" && fn.Recv != nil {
			body = fn.Body
		}
		return true
	})
	if body == nil {
		t.Fatal("Home.chrome not found in app.go")
	}

	var assigned string
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		lhs, ok := as.Lhs[0].(*ast.SelectorExpr)
		if !ok || lhs.Sel.Name != "MouseMode" {
			return true
		}
		if rhs, ok := as.Rhs[0].(*ast.SelectorExpr); ok {
			assigned = rhs.Sel.Name
		}
		return true
	})
	if !strings.EqualFold(assigned, "MouseModeNone") {
		t.Errorf("Home.chrome sets MouseMode to %q, want MouseModeNone: v2 re-renders "+
			"after every message, so each ignored mouse event costs a full View() — "+
			"measured at ~270/sec and 250%% CPU while a scroll lasts", assigned)
	}
}
