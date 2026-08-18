package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
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
