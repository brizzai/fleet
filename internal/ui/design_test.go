package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// designSystemHome is the one file allowed to construct an accent fill.
const designSystemHome = "design.go"

// TestAccentFillIsConstructedInOneFile is the tripwire under the rule that
// makes fleet's surfaces legible: Background(ColorAccent) — the inverted accent
// fill — is the heaviest treatment the UI has, and it means exactly one thing,
// "the keyboard is here".
//
// The command palette is why this test exists. It drew its active tab as a
// filled accent chip, so the tab (a mode, which never holds the keyboard) was
// louder than the selected row (the cursor) and louder than the caret (the
// actual focus). Nothing was wrong with any one of those three renders on its
// own — the bug only existed between them, which is precisely the kind a
// per-dialog test cannot see and a scarcity rule can.
//
// Adding a fill therefore means adding a named role in design.go and saying
// what it outranks, rather than reaching for the color inline. Fills in other
// colors are unrestricted: a green Yes button or a muted Border band cannot
// out-shout focus, so they need no budget.
func TestAccentFillIsConstructedInOneFile(t *testing.T) {
	for _, file := range packageSourceFiles(t) {
		if filepath.Base(file) == designSystemHome {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Background" {
				return true
			}
			arg, ok := call.Args[0].(*ast.Ident)
			if !ok || arg.Name != "ColorAccent" {
				return true
			}
			t.Errorf("%s: constructs an accent fill inline.\n"+
				"Background(ColorAccent) is fleet's focus treatment and lives only in %s.\n"+
				"Use SelectionPill/PrimaryAction/FocusCaret, or add a named role there and\n"+
				"document what it outranks (docs/design-system.md).",
				fset.Position(call.Pos()), designSystemHome)
			return true
		})
	}
}

// TestModeNeverFills pins the half of the rule that a scarcity check alone
// would miss. Demoting the palette's tab to accent text fixed nothing on its
// own if the next mode indicator reaches for Background(ColorBorder) instead:
// a muted fill still outranks the plain text every other tab renders as, and
// still competes with the selected row for "the thing that looks picked".
//
// A mode is carried by color and weight. It does not get a background at all.
func TestModeNeverFills(t *testing.T) {
	for name, s := range map[string]string{
		"ModeOn":  ModeOn().Render("tickets 3"),
		"ModeOff": ModeOff().Render("tickets 3"),
	} {
		// 48;2;r;g;b and 48;5;n are the SGR forms lipgloss emits for a
		// background; either one means this style filled.
		if strings.Contains(s, "48;2;") || strings.Contains(s, "48;5;") {
			t.Errorf("%s fills a background (%q) — a mode indicator must not.\n"+
				"Weight and color carry it; a fill belongs to the selection and the caret.", name, s)
		}
	}
}

// TestSelectionDistinguishesFocus keeps the two selection weights actually
// distinct. They exist so a list that has lost the keyboard still shows where
// its cursor is without competing with whatever took it; collapsing them (by
// making both accent, or both muted) silently removes that signal while every
// other test still passes.
func TestSelectionDistinguishesFocus(t *testing.T) {
	focused := SelectionPill(true).Render("session")
	blurred := SelectionPill(false).Render("session")
	if focused == blurred {
		t.Fatal("SelectionPill renders identically focused and unfocused — " +
			"a blurred list must not look like it owns the keyboard")
	}
	if !strings.Contains(blurred, "48;") {
		t.Errorf("unfocused SelectionPill dropped its fill (%q) — it should stay a "+
			"muted band, not vanish; the cursor is still there", blurred)
	}
}

// packageSourceFiles lists the package's non-test Go sources.
func packageSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		t.Fatal("no package sources found")
	}
	return out
}
