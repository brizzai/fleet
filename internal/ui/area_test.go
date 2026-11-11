package ui

import (
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
)

func areaTestHome(t *testing.T, areas []config.Area, current string, sessions ...*session.Session) *Home {
	t.Helper()
	h := &Home{
		cfg:          &config.Config{Areas: areas},
		sessions:     sessions,
		repoExpanded: map[string]bool{},
		pinnedRepos:  map[string]bool{},
		toasts:       NewToastStack(),
	}
	h.initAreas()
	h.areas.current = current
	gi := map[string]*git.RepoInfo{}
	for _, s := range sessions {
		gi[s.ProjectPath] = &git.RepoInfo{OriginKey: "local:" + s.Title}
	}
	h.gitInfoCache.Store(&gi)
	return h
}

var testAreas = []config.Area{
	{Name: "personal", Paths: []string{"/tmp/ar/personal"}, Default: true},
	{Name: "work", Paths: []string{"/tmp/ar/work"}},
}

func sessionIDs(items []SidebarItem) []string {
	var ids []string
	for _, it := range items {
		if it.Session != nil {
			ids = append(ids, it.Session.Title)
		}
	}
	return ids
}

func TestAreaFiltersSidebar(t *testing.T) {
	p := session.NewSession("p", "/tmp/ar/personal/a")
	w := session.NewSession("w", "/tmp/ar/work/b")
	w2 := session.NewSession("w2", "/tmp/ar/work2/c") // boundary: not work, falls to default
	pinned := "/tmp/ar/work/empty"

	tests := []struct {
		name    string
		areas   []config.Area
		current string
		want    []string
		pinned  bool
	}{
		{"no areas configured shows everything", nil, "", []string{"p", "w", "w2"}, true},
		{"all view shows everything", testAreas, "", []string{"p", "w", "w2"}, true},
		{"work only", testAreas, "work", []string{"w"}, true},
		{"personal gets default-owned paths", testAreas, "personal", []string{"p", "w2"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := areaTestHome(t, tc.areas, tc.current, p, w, w2)
			h.pinnedRepos[pinned] = true
			h.rebuildFlatItems()
			got := sessionIDs(h.flatItems)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") && !sameSet(got, tc.want) {
				t.Errorf("sessions = %v, want %v", got, tc.want)
			}
			hasPinned := false
			for _, it := range h.flatItems {
				if it.IsCheckoutHeader && it.RepoPath == pinned {
					hasPinned = true
				}
			}
			if hasPinned != tc.pinned {
				t.Errorf("pinned empty repo visible = %v, want %v", hasPinned, tc.pinned)
			}
		})
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}

func TestCycleAreaKeyOrderAndCursor(t *testing.T) {
	p := session.NewSession("p", "/tmp/ar/personal/a")
	w := session.NewSession("w", "/tmp/ar/work/b")
	h := areaTestHome(t, testAreas, "", p, w)
	h.rebuildFlatItems()
	h.cursor = 0
	for i, it := range h.flatItems {
		if it.Session != nil && it.Session.Title == "w" {
			h.cursor = i
		}
	}

	var seen []string
	for range 4 {
		h.cycleArea()
		seen = append(seen, h.areaLabel())
		if h.cursor < 0 || h.cursor >= len(h.flatItems) || h.flatItems[h.cursor].IsSpacer {
			t.Fatalf("cursor %d invalid after switching to %s", h.cursor, h.areaLabel())
		}
	}
	if got := strings.Join(seen, ","); got != "personal,work,all,personal" {
		t.Errorf("cycle = %s, want personal,work,all,personal", got)
	}
	// On "work" the selected session "w" stays selected; on "personal" it is
	// gone and the cursor lands on the first visible session.
	h.areas.current = "work"
	h.rebuildFlatItems()
	h.cursor = 0
	h.cycleArea() // work → all
	h.cycleArea() // all → personal
	if it := h.flatItems[h.cursor]; it.Session == nil || it.Session.Title != "p" {
		t.Errorf("cursor not on first visible session of personal: %+v", it)
	}
}

func TestCycleAreaNoopWithoutAreas(t *testing.T) {
	h := areaTestHome(t, nil, "", session.NewSession("p", "/tmp/ar/personal/a"))
	h.rebuildFlatItems()
	before := len(h.flatItems)
	h.cycleArea()
	if h.areas.current != "" || len(h.flatItems) != before {
		t.Error("cycleArea changed state with no areas configured")
	}
	if h.sidebarTitle() != "Sessions" {
		t.Errorf("title = %q, want plain Sessions", h.sidebarTitle())
	}
}

func TestSpaceJumpStaysInArea(t *testing.T) {
	p := session.NewSession("p", "/tmp/ar/personal/a")
	p.SetStatus(session.StatusWaiting)
	w := session.NewSession("w", "/tmp/ar/work/b")
	w.SetStatus(session.StatusWaiting)
	h := areaTestHome(t, testAreas, "work", p, w)
	h.rebuildFlatItems()
	h.cursor = FirstSelectableItem(h.flatItems)
	h.jumpToNextAttentionSession()
	if it := h.flatItems[h.cursor]; it.Session == nil || it.Session.Title != "w" {
		t.Errorf("jump left the area: %+v", it)
	}
}

func TestOtherAreasSummary(t *testing.T) {
	p1 := session.NewSession("p1", "/tmp/ar/personal/a")
	p1.SetStatus(session.StatusWaiting)
	p2 := session.NewSession("p2", "/tmp/ar/personal/b")
	p2.SetStatus(session.StatusFinished)
	p3 := session.NewSession("p3", "/tmp/ar/personal/c")
	p3.SetStatus(session.StatusRunning)
	w := session.NewSession("w", "/tmp/ar/work/b")
	w.SetStatus(session.StatusWaiting)

	h := areaTestHome(t, testAreas, "work", p1, p2, p3, w)
	if got := h.otherAreasSummary(); got != "personal ●2" {
		t.Errorf("summary = %q, want %q", got, "personal ●2")
	}
	if got := h.sidebarTitle(); !strings.Contains(got, "Sessions · work") || !strings.Contains(got, "personal ●2") {
		t.Errorf("title = %q", got)
	}
	h.areas.current = ""
	if got := h.otherAreasSummary(); got != "" {
		t.Errorf("summary in all view = %q, want empty", got)
	}
}

func TestEmptyAreaMessage(t *testing.T) {
	h := areaTestHome(t, testAreas, "work", session.NewSession("p", "/tmp/ar/personal/a"))
	h.rebuildFlatItems()
	if got := h.renderSidebarView(nil, 40, 10); !strings.Contains(got, "No sessions in area work") {
		t.Errorf("empty state = %q", got)
	}
}

func TestHiddenNewSessionToast(t *testing.T) {
	tests := []struct {
		name    string
		areas   []config.Area
		current string
		path    string
		want    string // "" = no toast
	}{
		{"other area visible", testAreas, "personal", "/tmp/ar/work/x", "Created in area work — press v to switch"},
		{"same area", testAreas, "work", "/tmp/ar/work/x", ""},
		{"all view", testAreas, "", "/tmp/ar/work/x", ""},
		{"no areas", nil, "", "/tmp/ar/work/x", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := areaTestHome(t, tc.areas, tc.current)
			h.noteHiddenNewSession(session.NewSession("n", tc.path))
			if h.infoMsg != tc.want {
				t.Errorf("toast = %q, want %q", h.infoMsg, tc.want)
			}
		})
	}
}
