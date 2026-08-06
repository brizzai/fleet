package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/discovery"
)

// TestNormalizeKeyLeavesLatinAlone: the remap must be invisible to everyone on a
// Latin layout. Every case here is a key that already reaches the right handler,
// so touching it could only break something that works today.
func TestNormalizeKeyLeavesLatinAlone(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.KeyPressMsg
	}{
		{"ascii letter", tea.KeyPressMsg{Code: 'j', Text: "j"}},
		{"ascii uppercase", tea.KeyPressMsg{Code: 'S', Text: "S"}},
		{"ascii punctuation", tea.KeyPressMsg{Code: '.', Text: "."}},
		{"ctrl chord", tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}},
		{"alt chord", tea.KeyPressMsg{Code: '3', Mod: tea.ModAlt}},
		{"named key", tea.KeyPressMsg{Code: tea.KeyEnter}},
		{"arrow", tea.KeyPressMsg{Code: tea.KeyDown}},
		{"empty text", tea.KeyPressMsg{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeKey(tt.msg); got != tt.msg {
				t.Errorf("normalizeKey(%v) = %v, want it unchanged", tt.msg, got)
			}
		})
	}
}

// TestNormalizeKeyRemapsNonLatin asserts on String(), not on Code, because
// String() is what every switch in this package matches against — a rewrite that
// set only Code would pass a Code-based test and still dead-press in the app.
func TestNormalizeKeyRemapsNonLatin(t *testing.T) {
	tests := []struct {
		name string
		in   rune
		want string
	}{
		{"hebrew j", 'ח', "j"},
		{"hebrew k", 'ל', "k"},
		{"hebrew a", 'ש', "a"},
		{"hebrew delete", 'ג', "d"},
		{"hebrew context menu", 'ץ', "."},
		{"russian j", 'о', "j"},
		{"russian uppercase settings", 'Ы', "S"},
		{"arabic j", 'ت', "j"},
		{"greek j", 'ξ', "j"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeKey(tea.KeyPressMsg{Code: tt.in, Text: string(tt.in)})
			if got.String() != tt.want {
				t.Errorf("normalizeKey(%q).String() = %q, want %q", tt.in, got.String(), tt.want)
			}
		})
	}
}

// TestNonLatinKeyDrivesHandleKey runs a Hebrew keypress through the real key
// switch on a fully-wired Home and requires it to land exactly where the US key
// at the same physical position lands. Before this change the press fell through
// the switch and did nothing at all.
func TestNonLatinKeyDrivesHandleKey(t *testing.T) {
	h := newPersistTestHome(t)
	seed, s := headerJumpHome()
	h.sessions = seed.sessions
	h.gitInfoCache.Store(seed.gitInfoCache.Load())
	h.rebuildFlatItems()

	start := idxOfSession(t, h, s["a1"].ID)

	h.cursor = start
	h.handleKey(tea.KeyPressMsg{Code: 'j', Text: "j"}) //nolint:errcheck // cursor is the assertion
	want := h.cursor
	if want == start {
		t.Fatal("precondition failed: `j` did not move the cursor, so this proves nothing")
	}

	h.cursor = start
	h.handleKey(tea.KeyPressMsg{Code: 'ח', Text: "ח"}) //nolint:errcheck // cursor is the assertion
	if h.cursor != want {
		t.Errorf("Hebrew 'ח' moved the cursor to %d, want %d (same as `j`)", h.cursor, want)
	}
}

// TestNonLatinKeyClosesDrawer pins the symmetry that makes the drawer key safe
// to remap at all. The drawer intercepts keys above the remap (it forwards them
// to a shell verbatim), so without a layout-aware chrome check Russian 'ё' would
// open the drawer and then be typed into it — leaving no way back out.
func TestNonLatinKeyClosesDrawer(t *testing.T) {
	h := newPersistTestHome(t)
	h.drawerMode = drawerTyping

	h.handleTypingKey(tea.KeyPressMsg{Code: 'ё', Text: "ё"}) //nolint:errcheck // drawerMode is the assertion
	if h.drawerMode != drawerHidden {
		t.Error("Russian 'ё' did not close the drawer — the key that opens it must also close it")
	}
}

// TestNonLatinKeyDrivesLaunchpad covers the other branch that matches above the
// remap. The launchpad is the only one that does not return on a miss, so a key
// it failed to match went on to drive the main switch: Hebrew 'ח' moved the
// sidebar cursor hidden behind the launchpad, and Russian 'Ф' opened the
// session-create dialog on top of it instead of checking every row.
func TestNonLatinKeyDrivesLaunchpad(t *testing.T) {
	h := newPersistTestHome(t)
	h.booted = true
	h.launchpad.SetItems([]discovery.Recent{
		{Path: "/tmp/one", OriginKey: "one"},
		{Path: "/tmp/two", OriginKey: "two"},
	})
	if !h.launchpadActive() {
		t.Fatal("precondition failed: the launchpad is not showing, so this proves nothing")
	}

	h.handleKey(tea.KeyPressMsg{Code: 'ח', Text: "ח"}) //nolint:errcheck // the cursor is the assertion
	if h.launchpad.cursor != 1 {
		t.Errorf("Hebrew 'ח' left the launchpad cursor at %d, want 1 (same as `j`)", h.launchpad.cursor)
	}

	h.handleKey(tea.KeyPressMsg{Code: 'Ф', Text: "Ф"}) //nolint:errcheck // toggle-all is the assertion
	if h.launchpad.SelectedCount() != 0 {
		t.Errorf("Russian 'Ф' left %d rows checked — it means toggle-all, and SetItems pre-checks every row", h.launchpad.SelectedCount())
	}
	if h.newDialog.IsVisible() {
		t.Error("Russian 'Ф' fell through and opened the new-session dialog on top of the launchpad")
	}
}

// TestNonLatinKeyReachesTextFreeDialog covers the routeToModal half: a delete
// confirm has to answer to the Hebrew key in the `y` position, since the dialog
// offers no other single-key yes.
func TestNonLatinKeyReachesTextFreeDialog(t *testing.T) {
	h := newPersistTestHome(t)
	confirmed := false
	h.confirmDialog.Show("delete?", func() tea.Msg {
		confirmed = true
		return nil
	})

	cmd, handled := h.routeToModal(tea.KeyPressMsg{Code: 'ט', Text: "ט"})
	if !handled {
		t.Fatal("the visible confirm dialog did not consume the keypress")
	}
	if cmd == nil {
		t.Fatal("Hebrew 'ט' did not confirm the dialog — it should act as `y`")
	}
	cmd()
	if !confirmed {
		t.Error("the confirm callback never ran")
	}
	if h.confirmDialog.IsVisible() {
		t.Error("the dialog is still visible after confirming")
	}
}

// TestNormalizedDialogsHoldNoTextInput is the drift guard, and the only reason
// the two call sites can stay comment-free of caveats. It reads routeToModal's
// source, collects every dialog handed the remapped key, and fails if one of
// them owns a text field — the single way this change could start silently
// turning a user's Hebrew into Latin as they type.
func TestNormalizedDialogsHoldNoTextInput(t *testing.T) {
	fields := dialogsReceivingNormalizedKeys(t)
	if len(fields) == 0 {
		t.Fatal("found no dialogs receiving the remapped key — the source scan is broken, not the code")
	}

	homeType := reflect.TypeFor[Home]()
	for _, name := range fields {
		field, ok := homeType.FieldByName(name)
		if !ok {
			t.Errorf("routeToModal passes the remapped key to h.%s, which is not a Home field", name)
			continue
		}
		if path := findTextInput(field.Type, map[reflect.Type]bool{}); path != "" {
			t.Errorf("h.%s owns a text field (%s) but receives remapped keys — it would eat non-Latin typing", name, path)
		}
	}
}

// TestFindTextInputDetects is the positive control for the guard above. Without
// it, a findTextInput that silently returned "" for everything would make
// TestNormalizedDialogsHoldNoTextInput pass no matter what routeToModal did.
func TestFindTextInputDetects(t *testing.T) {
	withInput := reflect.TypeFor[RenameDialog]()
	if got := findTextInput(withInput, map[reflect.Type]bool{}); got == "" {
		t.Error("RenameDialog owns a textinput but findTextInput missed it — the guard is blind")
	}
	withoutInput := reflect.TypeFor[ConfirmDialog]()
	if got := findTextInput(withoutInput, map[reflect.Type]bool{}); got != "" {
		t.Errorf("findTextInput reports ConfirmDialog owning %q — false positives make the guard unusable", got)
	}

	// A dialog with several inputs is the likely way this guard goes blind: it
	// would hold them in a slice, and a search that only followed pointers would
	// call it clean.
	type multiInput struct{ Inputs []textinput.Model }
	if got := findTextInput(reflect.TypeFor[multiInput](), map[reflect.Type]bool{}); got == "" {
		t.Error("findTextInput missed a []textinput.Model — a multi-input dialog would pass the guard")
	}
}

// dialogsReceivingNormalizedKeys returns the Home field names whose Update is
// called with cmdMsg (the remapped key) inside routeToModal.
func dialogsReceivingNormalizedKeys(t *testing.T) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "app.go", nil, 0)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "routeToModal" {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatal("routeToModal not found in app.go — this guard needs rewiring")
	}

	var fields []string
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		arg, ok := call.Args[0].(*ast.Ident)
		if !ok || arg.Name != "cmdMsg" {
			return true
		}
		// h.<field>.Update(cmdMsg)
		method, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || method.Sel.Name != "Update" {
			return true
		}
		if recv, ok := method.X.(*ast.SelectorExpr); ok {
			fields = append(fields, recv.Sel.Name)
		}
		return true
	})
	return fields
}

// findTextInput reports the field path at which t holds a bubbles textinput, or
// "" if it holds none. Recursive: a dialog could nest one inside a sub-struct.
func findTextInput(t reflect.Type, seen map[reflect.Type]bool) string {
	t = unwrapContainers(t)
	if t.Kind() != reflect.Struct || seen[t] {
		return ""
	}
	seen[t] = true

	for _, f := range reflect.VisibleFields(t) {
		if unwrapContainers(f.Type).String() == "textinput.Model" {
			return f.Name
		}
		if nested := findTextInput(f.Type, seen); nested != "" {
			return f.Name + "." + nested
		}
	}
	return ""
}

// unwrapContainers strips pointer, slice, array and map layers. A dialog with
// several inputs is likely to hold them as a []textinput.Model, and a guard that
// only followed pointers would report such a dialog clean — passing
// TestNormalizedDialogsHoldNoTextInput while eating the user's non-Latin typing.
// Interface-typed fields stay invisible: their dynamic type is not knowable here.
func unwrapContainers(t reflect.Type) reflect.Type {
	for {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			t = t.Elem()
		default:
			return t
		}
	}
}
