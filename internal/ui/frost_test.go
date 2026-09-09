package ui

import (
	"hash/fnv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/brizzai/fleet/internal/frost"
)

// trailOf is the detector's own hash of a key run, so a test can install a
// run of its choosing.
func trailOf(keys ...string) uint64 {
	f := fnv.New64a()
	for _, k := range keys {
		_, _ = f.Write([]byte(k))
		_, _ = f.Write([]byte{0})
	}
	return f.Sum64()
}

func TestTrailFiresOnlyOnTheFullRun(t *testing.T) {
	run := []string{"x1", "x2", "x3", "x4", "x5", "x6", "x7", "x8"}
	old := trailKey
	trailKey = trailOf(run...)
	defer func() { trailKey = old }()

	h := &Home{}
	for i, k := range run {
		if fire := h.noteTrail(k); fire != (i == len(run)-1) {
			t.Fatalf("key %d %q: fire=%v", i, k, fire)
		}
	}
	if len(h.keyTrail) != 0 {
		t.Fatalf("detector should reset after firing, got %v", h.keyTrail)
	}
	// A stray key mid-run breaks it; repeating the run from the top completes.
	for _, k := range append([]string{"x1", "x2", "j"}, run...) {
		if h.noteTrail(k) && k != "x8" {
			t.Fatalf("fired early at %q", k)
		}
	}
	// A stutter on the first key still completes: the last eight are the run.
	h.keyTrail = nil
	for _, k := range append([]string{"x1"}, run...) {
		if h.noteTrail(k) && k != "x8" {
			t.Fatalf("fired early at %q", k)
		}
	}
	if len(h.keyTrail) != 0 {
		t.Fatal("the stuttered run should have fired and reset")
	}
}

func TestTrailNeverFiresOnPlainNavigation(t *testing.T) {
	h := &Home{}
	for _, k := range []string{"j", "j", "k", "k", "enter", "space", "j", "k", "j", "k", "a", "d"} {
		if h.noteTrail(k) {
			t.Fatalf("fired on %q", k)
		}
	}
	if len(h.keyTrail) != trailLen {
		t.Fatalf("trail should stay bounded at %d, got %d", trailLen, len(h.keyTrail))
	}
}

// runningFrost gives a booted, sized Home with a run in progress.
func runningFrost(t *testing.T) *Home {
	t.Helper()
	h := newPersistTestHome(t)
	h.booted = true
	h.Update(tea.WindowSizeMsg{Width: 80, Height: 24}) //nolint:errcheck // sizing only
	h.frost = frost.New(frost.NewGrid(80, 24), 1)
	return h
}

func TestOnlyARealResizeEndsTheRun(t *testing.T) {
	h := runningFrost(t)
	// Bubble Tea reports the size on every SIGWINCH, changed or not.
	h.Update(tea.WindowSizeMsg{Width: 80, Height: 24}) //nolint:errcheck // the run is the assertion
	if h.frost == nil {
		t.Fatal("a same-size WindowSizeMsg ended the run")
	}
	h.Update(tea.WindowSizeMsg{Width: 80, Height: 23}) //nolint:errcheck // the run is the assertion
	if h.frost != nil {
		t.Fatal("a real resize should end the run")
	}
}

func TestQuitKeyEndsTheRunInOnePress(t *testing.T) {
	h := runningFrost(t)
	h.handleKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}) //nolint:errcheck // quitting is the assertion
	if h.frost != nil {
		t.Fatal("ctrl+c left the run active")
	}
	if !h.quitting {
		t.Fatal("ctrl+c should quit fleet in one press, not just leave the run")
	}
}

func TestHiddenPaletteRowSurfacesOnlyOnASearch(t *testing.T) {
	d := NewCommandPaletteDialog()
	items := []PaletteItem{
		{Kind: PaletteKindCommand, ID: "plain", Name: "Plain Command"},
		{Kind: PaletteKindCommand, ID: "shy", Name: "Zzz", Haystack: "Zzz needle words", Hidden: true},
	}
	d.Show(items, []string{"shy"}) // even a recent pick stays hidden
	ids := func() []string {
		var out []string
		for _, it := range d.filtered {
			out = append(out, it.ID)
		}
		return out
	}
	if got := ids(); len(got) != 1 || got[0] != "plain" {
		t.Fatalf("unfiltered list = %v, want only the plain command", got)
	}
	d.filterInput.SetValue("needle")
	d.rebuildFiltered()
	if got := ids(); len(got) != 1 || got[0] != "shy" {
		t.Fatalf("searching a hidden row's keyword gave %v, want the hidden row", got)
	}
}

func TestGateShowsTheHintAndOpensOnTheRightAnswer(t *testing.T) {
	d := newGateDialog()
	d.unlock = func(a string) bool { return a == "yes" }
	d.SetSize(80, 24)
	d.Show()

	type_ := func(s string) {
		for _, r := range s {
			d.Update(tea.KeyPressMsg{Code: r, Text: string(r)}) //nolint:errcheck // the dialog state is the assertion
		}
	}
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	// The box wraps a long line, so look for the hint's opening words.
	opening := strings.Join(strings.Fields(frost.Gate().Hint)[:2], " ")

	// The hint is the other way in, so it shows before any answer is given.
	if v := d.View(); !strings.Contains(v, opening) || !strings.Contains(v, frost.Gate().Label) || !strings.Contains(v, frost.Gate().Prompt) {
		t.Fatal("the prompt should show the label, the hint and the ask up front")
	}

	type_("no")
	_, cmd := d.Update(enter)
	if cmd != nil || !d.IsVisible() || d.note != frost.Gate().Wrong || d.input.Value() != "" {
		t.Fatalf("a wrong answer should stay open, say %q and clear the field: note=%q value=%q",
			frost.Gate().Wrong, d.note, d.input.Value())
	}
	if v := d.View(); !strings.Contains(v, frost.Gate().Wrong) || !strings.Contains(v, opening) {
		t.Fatal("the view should show the wrong line and still the hint")
	}

	type_("yes")
	_, cmd = d.Update(enter)
	if d.IsVisible() || cmd == nil {
		t.Fatal("the right answer should close the prompt and hand back a command")
	}
	if _, ok := cmd().(gateOpenMsg); !ok {
		t.Fatal("the command should announce the unlock")
	}

	d.Show()
	if d.note != "" || d.input.Value() != "" {
		t.Fatal("reopening should start fresh")
	}
	d.Update(tea.KeyPressMsg{Code: tea.KeyEscape}) //nolint:errcheck // the dialog state is the assertion
	if d.IsVisible() {
		t.Fatal("esc should close the prompt")
	}
}

func TestGateIsReachableFromThePalette(t *testing.T) {
	h := newPersistTestHome(t)
	h.booted = true
	h.Update(tea.WindowSizeMsg{Width: 80, Height: 24}) //nolint:errcheck // sizing only
	found := false
	for _, it := range h.buildPaletteItems() {
		if it.ID == "frost_gate" {
			found = it.Hidden && it.Name == frost.Gate().Label
		}
	}
	if !found {
		t.Fatal("the palette should carry a hidden row for the gate, named from the pack")
	}
	// Through the real list, not a hand-built one: a later pass over the
	// commands must not reset the search text the row relies on.
	h.commandPalette.Show(h.buildPaletteItems(), nil)
	h.commandPalette.filterInput.SetValue(strings.Fields(frost.Gate().Keywords)[0])
	h.commandPalette.rebuildFiltered()
	hit := false
	for _, it := range h.commandPalette.filtered {
		hit = hit || it.ID == "frost_gate"
	}
	if !hit {
		t.Fatal("searching the first keyword did not surface the row")
	}
	h.commandPalette.Hide()
	h.dispatchCommand("frost_gate") //nolint:errcheck // the dialog is the assertion
	if !h.gate.IsVisible() || !h.modalOpen() {
		t.Fatal("dispatching the row should open the prompt as a modal")
	}
	if !strings.Contains(h.renderBody(), frost.Gate().Label) {
		t.Fatal("the body should render the prompt while it is open")
	}
	// The unlock message starts a run, the same one the key sequence starts.
	h.gate.Hide()
	h.Update(gateOpenMsg{}) //nolint:errcheck // the run is the assertion
	if h.frost == nil {
		t.Fatal("the unlock should start a run")
	}
}
