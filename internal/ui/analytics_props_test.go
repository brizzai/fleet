package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"testing"
)

// A category is a stable enum a funnel is grouped by, and it is the only thing
// about an error the backend ever sees. snake_case ASCII keeps it greppable and
// keeps a stray path, branch name or fmt verb from being pasted in by accident.
//
// That a category is passed at all is the compiler's job — setError takes one, so
// a bare setError(err) does not build. What the compiler can't check is that the
// literal is a literal and reads like an enum, which is this: "other" is the
// value derived categories used to collapse to, and a category assembled with
// fmt.Sprintf would put the error's own text back on the wire.
func TestErrorCategoriesAreSnakeCaseEnums(t *testing.T) {
	fset := token.NewFileSet()
	snakeCase := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	seen := map[string]bool{}
	for _, name := range []string{"app.go", "accounts.go", "drawer.go"} {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "setError" || len(call.Args) == 0 {
				return true
			}
			at := fset.Position(call.Pos())
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Errorf("%s: setError's category must be a string literal, not %T", at, call.Args[0])
				return true
			}
			cat, _ := strconv.Unquote(lit.Value)
			switch {
			case !snakeCase.MatchString(cat):
				t.Errorf("%s: category %q is not snake_case", at, cat)
			case cat == "other":
				t.Errorf("%s: %q is the value a named category replaces", at, cat)
			}
			seen[cat] = true
			return true
		})
	}
	// A guard that silently matched nothing (a rename, a moved file, a helper that
	// stopped being a method call) would pass while every category went unchecked.
	if len(seen) < 20 {
		t.Fatalf("found only %d categories — the scan stopped seeing call sites", len(seen))
	}
}

func TestEditorNameDropsPathAndFlags(t *testing.T) {
	for spec, want := range map[string]string{
		"code":                            "code",
		"code -n":                         "code",
		"/Users/ada/bin/nvim -u ~/.vimrc": "nvim",
		"":                                "",
	} {
		if got := editorName(spec); got != want {
			t.Errorf("editorName(%q) = %q, want %q", spec, got, want)
		}
	}
}
