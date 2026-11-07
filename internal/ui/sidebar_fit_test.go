package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
)

func TestSidebarRowsEndInEllipsisWhenTooWide(t *testing.T) {
	long := "a-very-long-branch-name-that-cannot-possibly-fit-in-thirty-columns"
	info := &git.RepoInfo{Branch: "feature/" + long}

	for _, width := range []int{30, 60, 120} {
		for _, selected := range []bool{false, true} {
			row := fitSidebarRow(renderCheckoutHeader(SidebarItem{IsCheckoutHeader: true}, info, width, selected), width)
			if w := ansi.StringWidth(row); w > width {
				t.Errorf("width %d selected %v: branch row is %d wide", width, selected, w)
			}
			if plain := ansi.Strip(row); !strings.Contains(plain, long) && !strings.Contains(plain, "…") {
				t.Errorf("width %d: cut branch without an ellipsis: %q", width, plain)
			}
		}
	}

	// A wide sidebar now shows the whole branch, not the old 22-character cut.
	if plain := ansi.Strip(renderCheckoutHeader(SidebarItem{IsCheckoutHeader: true}, info, 120, false)); !strings.Contains(plain, long) {
		t.Errorf("120 columns still cut the branch: %q", plain)
	}

	s := session.NewSession(strings.Repeat("long session title ", 10), "/tmp/fit")
	row := fitSidebarRow(renderSessionItem(SidebarItem{Session: s}, 30, true, -1), 30)
	if w := ansi.StringWidth(row); w > 30 {
		t.Errorf("session row is %d wide, want <= 30", w)
	}
}
