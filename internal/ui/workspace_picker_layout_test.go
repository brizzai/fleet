package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/brizzai/fleet/internal/workspace"
)

// TestFitCellMeasuresColumnsNotBytes pins design-system.md §7 for the worktree
// row, which previously cut with `name[:20]`. A worktree name is arbitrary user
// text: byte slicing cuts a multi-byte name at a fraction of the intended
// columns and can leave invalid UTF-8 behind for lipgloss to render.
func TestFitCellMeasuresColumnsNotBytes(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
	}{
		{"ascii fits", "feature-x", 20},
		{"ascii truncates", "a-very-long-worktree-name", 10},
		{"multi-byte truncates", "ünïcödé-wörktree-nàme", 10},
		{"exact fit", "exactly-10", 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fitCell(tc.in, tc.width)
			if w := lipgloss.Width(got); w != tc.width {
				t.Errorf("fitCell(%q, %d) is %d columns, want exactly %d — ragged columns are the bug",
					tc.in, tc.width, w, tc.width)
			}
			if !utf8.ValidString(got) {
				t.Errorf("fitCell(%q, %d) = %q: split a rune", tc.in, tc.width, got)
			}
		})
	}
}

// TestWorktreeColumnsFitTheBudget covers the sizing that replaced the fixed
// 20/16 split: derived from content when there is room, and degraded evenly —
// never starving one column to nothing — when there is not.
func TestWorktreeColumnsFitTheBudget(t *testing.T) {
	ws := func(names ...string) []workspace.WorkspaceInfo {
		out := make([]workspace.WorkspaceInfo, 0, len(names))
		for _, n := range names {
			out = append(out, workspace.WorkspaceInfo{Name: n, Branch: n + "-branch"})
		}
		return out
	}

	t.Run("room to spare uses natural widths", func(t *testing.T) {
		list := ws("short", "tiny")
		nameW, branchW := worktreeColumns(list, 80)
		if nameW != len("short") || branchW != len("short-branch") {
			t.Errorf("nameW=%d branchW=%d, want the widest actual values (5, 12) — no truncation when it fits",
				nameW, branchW)
		}
	})

	t.Run("over budget stays within it and keeps both readable", func(t *testing.T) {
		list := ws(strings.Repeat("n", 90), "s")
		const avail = 40
		nameW, branchW := worktreeColumns(list, avail)
		if nameW+branchW > avail {
			t.Errorf("nameW+branchW = %d, over the %d budget", nameW+branchW, avail)
		}
		if nameW <= 0 || branchW <= 0 {
			t.Errorf("nameW=%d branchW=%d: a column starved to nothing", nameW, branchW)
		}
		if branchW < 12 {
			t.Errorf("branchW=%d, want the floor (12) honoured so the narrower column stays readable", branchW)
		}
	})

	t.Run("budget too small for both floors still fits", func(t *testing.T) {
		list := ws(strings.Repeat("n", 50))
		const avail = 10
		nameW, branchW := worktreeColumns(list, avail)
		if nameW+branchW > avail {
			t.Errorf("nameW+branchW = %d, over the %d budget", nameW+branchW, avail)
		}
	})
}

// TestWorktreeDialogRowsNeverOverflow guards the box arithmetic. Lip Gloss v2's
// Style.Width is the TOTAL frame width, so a content column computed as "width
// less padding" is two cells too wide; the longest rows then wrap onto a second
// line inside the box — which a row with a session count did the moment rows
// started being sized against innerWidth.
//
// Asserted on the column budget, not the rendered output: the wrap adds a line
// rather than widening one, and lipgloss.Place pads the result to the terminal
// height, so nothing about View's dimensions moves when this breaks.
func TestWorktreeDialogRowsNeverOverflow(t *testing.T) {
	names := []string{
		"frosty-mahavira",
		"brizzai-BRZ-2644-Remove-legacy-paths",
		"brizzai-brz-3287-display-widths",
		"brizzai-brz-3241-rew",
	}
	var wss []workspace.WorkspaceInfo
	counts := map[string]int{}
	for i, n := range names {
		p := "/p/" + strings.Repeat("x", i)
		wss = append(wss, workspace.WorkspaceInfo{Name: n, Branch: n + "-branch", Path: p})
		counts[p] = 1 + i*6 // single- and double-digit counts both present
	}

	for _, w := range []int{40, 60, 80, 100, 120, 200} {
		d := NewWorktreeDialog()
		d.SetSize(w, 40)
		d.Show(wss, nil, nil, "/Users/y/code/brizzai", "origin/master", nil)
		d.sessionCounts = counts

		// Ground-truth innerWidth against Lip Gloss itself, rather than against
		// the same constant the production code uses — comparing the budget to
		// innerWidth alone is tautological, since both move together when the
		// arithmetic is wrong. A content line of exactly innerWidth columns must
		// come out of the box as one line, not two.
		probe := strings.Repeat("x", d.innerWidth())
		boxed := DialogStyle.Width(d.dialogWidth()).Render(probe)
		rows := 0
		for _, line := range strings.Split(boxed, "\n") {
			if strings.Contains(line, "x") {
				rows++
			}
		}
		if rows != 1 {
			t.Errorf("terminal width %d: a line of innerWidth (%d) columns wrapped onto %d rows inside the box",
				w, d.innerWidth(), rows)
		}

		nameW, branchW, countW := d.worktreeColumnWidths()
		row := len("\u25b8 ") + nameW + worktreeRowGap + branchW + worktreeRowGap + countW
		if row > d.innerWidth() {
			t.Errorf("terminal width %d: row budget %d exceeds content width %d — rows will wrap",
				w, row, d.innerWidth())
		}

		// And the rendered row must actually honour that budget.
		for i := range wss {
			for _, selected := range []bool{false, true} {
				line := d.renderWorktreeRow(&wss[i], selected, nameW, branchW, countW)
				if lw := lipgloss.Width(line); lw > d.innerWidth() {
					t.Errorf("terminal width %d: row %d (selected=%v) renders %d columns, content width is %d",
						w, i, selected, lw, d.innerWidth())
				}
			}
		}
	}
}
