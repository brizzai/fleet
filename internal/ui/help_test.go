package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// renderedBoxHeight measures the height of the dialog box inside the centering
// padding produced by lipgloss.Place.
func renderedBoxHeight(view string) int {
	lines := strings.Split(view, "\n")
	first, last := -1, -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) != "" {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	if first == -1 {
		return 0
	}
	return last - first + 1
}

// The help sheet must never exceed the terminal: short panes used to clip the
// top and bottom rows (issue #141).
func TestHelpOverlayNeverOverflows(t *testing.T) {
	sizes := []struct{ w, h int }{
		{120, 45}, {120, 30}, {100, 20}, {80, 24}, {200, 20}, {60, 15},
	}
	for _, s := range sizes {
		ho := NewHelpOverlay()
		ho.SetSize(s.w, s.h)
		ho.Show()
		view := ho.View()
		if bh := renderedBoxHeight(view); bh > s.h {
			t.Errorf("%dx%d: box height %d exceeds terminal height %d", s.w, s.h, bh, s.h)
		}
		for _, ln := range strings.Split(view, "\n") {
			if w := lipgloss.Width(ln); w > s.w {
				t.Errorf("%dx%d: line width %d exceeds terminal width %d", s.w, s.h, w, s.w)
				break
			}
		}
	}
}

// On a short terminal where the sheet must scroll, every binding has to be
// reachable across the scroll range.
func TestHelpOverlayAllBindingsReachable(t *testing.T) {
	ho := NewHelpOverlay()
	ho.SetSize(100, 20)
	ho.Show()
	lay := ho.layout()
	if lay.maxScroll == 0 {
		t.Fatalf("expected the sheet to scroll at 100x20, got maxScroll=0")
	}
	seen := map[string]bool{}
	for s := 0; s <= lay.maxScroll; s++ {
		ho.scroll = s
		v := ho.View()
		for _, r := range lay.rows {
			if r.binding() && strings.Contains(v, r.Desc) {
				seen[r.Desc] = true
			}
		}
	}
	for _, r := range lay.rows {
		if r.binding() && !seen[r.Desc] {
			t.Errorf("binding %q (%s) is never visible while scrolling", r.Desc, r.Key)
		}
	}
}

// The sheet holds 51 bindings and most of them are off-screen below ~160
// columns, so typing has to filter rather than close.
func TestHelpTypingFiltersInsteadOfClosing(t *testing.T) {
	ho := NewHelpOverlay()
	ho.SetSize(100, 30)
	ho.Show()

	for _, r := range "snooze" {
		ho, _ = ho.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if !ho.IsVisible() {
		t.Fatal("typing closed the help sheet")
	}
	rows := ho.helpRows()
	if len(rows) == 0 {
		t.Fatal(`"snooze" matched nothing`)
	}
	for _, r := range rows {
		if r.Header {
			t.Errorf("a filtered result carries structure rows: %+v", r)
		}
	}
	if rows[0].Key != "z" {
		t.Errorf("best match for %q is %q (%s), want the z binding", "snooze", rows[0].Key, rows[0].Desc)
	}
}

// The haystack is key + description, so both spellings of a lookup work.
func TestHelpFilterMatchesKeyAndDesc(t *testing.T) {
	cases := []struct{ query, wantKey string }{
		{"snooze", "z"},      // found by description
		{"ctrl+t", "Ctrl+T"}, // found by key label
	}
	for _, tc := range cases {
		ho := NewHelpOverlay()
		ho.SetSize(100, 30)
		ho.Show()
		ho.filter.SetValue(tc.query)
		rows := ho.helpRows()
		found := false
		for _, r := range rows {
			if r.Key == tc.wantKey {
				found = true
			}
		}
		if !found {
			t.Errorf("query %q never matched the %q binding (%d rows)", tc.query, tc.wantKey, len(rows))
		}
	}
}

// Headers are dropped while filtering, so the section folds onto the row. That
// is load-bearing: Enter means two different things in two sections, and a
// filtered list without the tag shows the same key contradicting itself.
func TestHelpFilteredRowsCarryTheirSection(t *testing.T) {
	ho := NewHelpOverlay()
	ho.SetSize(100, 30)
	ho.Show()
	ho.filter.SetValue("Enter")

	sections := map[string]bool{}
	for _, r := range ho.helpRows() {
		if r.Key != "Enter" {
			continue
		}
		if r.Tag == "" {
			t.Errorf("filtered row %q carries no section tag", r.Desc)
		}
		sections[r.Section] = true
	}
	if len(sections) < 2 {
		t.Fatalf("expected Enter to match in several sections, got %v", sections)
	}
}

// esc must not discard the filter and the sheet in one press.
func TestHelpEscClearsFilterThenCloses(t *testing.T) {
	ho := NewHelpOverlay()
	ho.SetSize(100, 30)
	ho.Show()
	ho.filter.SetValue("fork")

	ho, _ = ho.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !ho.IsVisible() {
		t.Fatal("the first esc closed the sheet instead of clearing the filter")
	}
	if ho.filter.Value() != "" {
		t.Fatalf("the filter still reads %q after esc", ho.filter.Value())
	}
	ho, _ = ho.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if ho.IsVisible() {
		t.Error("the second esc did not close the sheet")
	}
}

// The whole point of the column rework: a column is sized to what lands in it,
// not to the widest binding in the table.
func TestHelpColumnsAreSizedToTheirOwnContents(t *testing.T) {
	ho := NewHelpOverlay()
	ho.SetSize(200, 50)
	ho.Show()
	lay := ho.layout()
	if len(lay.chunks) < 2 {
		t.Fatalf("expected several columns at 200x50, got %d", len(lay.chunks))
	}
	for i, ch := range lay.chunks {
		want := 0
		for _, r := range ch.rows {
			if r.binding() {
				want = max(want, ch.keyW+2+runeLen(r.Desc))
			} else if r.Header {
				want = max(want, runeLen(r.Desc))
			}
		}
		if ch.w != want {
			t.Errorf("column %d is %d wide, its contents need %d", i, ch.w, want)
		}
	}
	// A uniform width across every column means the per-column sizing regressed
	// back to one global measurement.
	uniform := true
	for _, ch := range lay.chunks[1:] {
		if ch.w != lay.chunks[0].w {
			uniform = false
		}
	}
	if uniform {
		t.Error("every column came out the same width — per-column sizing is not doing anything")
	}
}

// A section with no label renders as an empty header row, which reads as a
// layout bug rather than as a missing string.
func TestHelpSectionsAreAllLabelled(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range HelpOverlayBindings() {
		if seen[e.Section] {
			continue
		}
		seen[e.Section] = true
		if _, ok := helpSections[e.Section]; !ok {
			t.Errorf("section %q has bindings but no label in helpSections", e.Section)
		}
	}
	for id := range helpSections {
		if !seen[id] {
			t.Errorf("helpSections labels %q, which no binding uses", id)
		}
	}
}

// The whole sheet fits, unscrolled, in two columns at 120 columns — and it does
// so with about 7 columns of slack (a 105-wide grid against a 112 budget). That
// margin is the entire reason the sheet is readable at this size, and it is set
// by the *longest description in the table*: one binding whose Desc runs ~7
// characters past the current longest collapses the layout back to one column
// and ~30 rows of scrolling, silently. Nothing else in the suite would notice.
func TestHelpSheetHoldsTwoColumnsAt120(t *testing.T) {
	// 41 and not the 40 this was written at. The sheet was exactly full there —
	// 59 rows, nothing to spare — so the review feature's `c` and `C` did not
	// make it fat, they were simply the first bindings added after it reached
	// capacity. Raised by the one row they cost rather than kept at a number
	// that would refuse every future keybinding; the sheet is height-bound, so
	// a wider terminal does not help and three columns do not fit at 120.
	ho := NewHelpOverlay()
	ho.SetSize(120, 41)
	ho.Show()
	lay := ho.layout()
	if got := len(lay.chunks); got != 2 {
		t.Errorf("120x41 renders %d columns, want 2 — a description probably grew", got)
	}
	if lay.maxScroll != 0 {
		t.Errorf("120x41 hides %d rows, want the whole sheet visible", lay.maxScroll)
	}
}

// Letters type and arrows scroll: the sharpest behaviour change in the sheet,
// since j/k used to scroll it.
func TestHelpArrowsScrollAndLettersType(t *testing.T) {
	ho := NewHelpOverlay()
	ho.SetSize(80, 24)
	ho.Show()
	if ho.layout().maxScroll == 0 {
		t.Fatal("80x24 has to scroll or this test proves nothing")
	}

	ho, _ = ho.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if ho.scroll != 0 {
		t.Errorf("j moved the scroll offset to %d — it is a filter character now", ho.scroll)
	}
	if ho.filter.Value() != "j" {
		t.Errorf("j never reached the filter (it holds %q)", ho.filter.Value())
	}

	ho.filter.SetValue("")
	ho, _ = ho.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if ho.scroll != 1 {
		t.Errorf("down moved the offset to %d, want 1", ho.scroll)
	}
	if ho.filter.Value() != "" {
		t.Errorf("down was typed into the filter (%q)", ho.filter.Value())
	}
}

// fuzzy reports byte offsets, every bound in helpRows counts runes, and
// `Shift+↑/↓` carries two 3-byte arrows — so an unconverted index highlighted
// `p hea` for the query "group", four characters off, and clipped the tail.
func TestHelpFilterHighlightsSurviveMultiByteKeys(t *testing.T) {
	for _, tc := range []struct{ query, key, want string }{
		{"group", "Shift+↑/↓", "group"},
		{"sidebar", "`", "sidebar"},
	} {
		ho := NewHelpOverlay()
		ho.SetSize(200, 50)
		ho.Show()
		ho.filter.SetValue(tc.query)

		var found bool
		for _, r := range ho.helpRows() {
			if r.Key != tc.key {
				continue
			}
			found = true
			desc := []rune(r.Desc)
			var lit string
			for _, i := range r.DescIdx {
				if i < 0 || i >= len(desc) {
					t.Fatalf("%q on %q: index %d out of range for %q", tc.query, tc.key, i, r.Desc)
				}
				lit += string(desc[i])
			}
			if lit != tc.want {
				t.Errorf("%q on %q: highlighted %q in %q, want %q", tc.query, tc.key, lit, r.Desc, tc.want)
			}
		}
		if !found {
			t.Fatalf("%q matched no row keyed %q", tc.query, tc.key)
		}
	}
}

// The at-rest overflow test can't cover this: a filtered row carries a section
// tag worth up to 17 more columns ("  TERMINAL DRAWER"), which no resting row
// ever pays for.
func TestHelpFilteredSheetNeverOverflows(t *testing.T) {
	sizes := []struct{ w, h int }{
		{120, 45}, {120, 30}, {100, 20}, {80, 24}, {200, 20}, {60, 15},
	}
	// "e" is the widest result set (it matches nearly everything); "enter" and
	// "drawer" pull in the longest tag.
	for _, q := range []string{"e", "enter", "drawer", "ctrl"} {
		for _, s := range sizes {
			ho := NewHelpOverlay()
			ho.SetSize(s.w, s.h)
			ho.Show()
			ho.filter.SetValue(q)
			// Measure the layout, not View(): its trailing
			// MaxWidth/MaxHeight/Place clamp every line to the terminal, so
			// asserting on the rendered string asserts the clamp and passes
			// while the layout is silently being cut.
			lay := ho.layout()
			if w := chunksWidth(lay.chunks, lay.gutter) + DialogStyle.GetHorizontalFrameSize(); w > s.w {
				t.Errorf("%q at %dx%d: box width %d exceeds %d", q, s.w, s.h, w, s.w)
			}
			if bh := renderedBoxHeight(ho.View()); bh > s.h {
				t.Errorf("%q at %dx%d: box height %d exceeds %d", q, s.w, s.h, bh, s.h)
			}
		}
	}
}

// A handful of matches must not fan out into a column per row. The column count
// is chosen by height first for exactly this reason.
func TestHelpFilteredResultsStayInOneColumn(t *testing.T) {
	for _, q := range []string{"fork", "enter", "ctrl"} {
		ho := NewHelpOverlay()
		ho.SetSize(200, 50)
		ho.Show()
		ho.filter.SetValue(q)
		lay := ho.layout()
		if len(lay.chunks) != 1 {
			t.Errorf("%q: %d matches rendered across %d columns, want 1",
				q, len(lay.rows), len(lay.chunks))
		}
	}
}
