package ui

import (
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
)

var folderAreas = []config.Area{
	{Name: "personal", Paths: []string{"/tmp/fo/P"}, Default: true},
	{Name: "work", Paths: []string{"/tmp/fo/W"}},
}

// folderHome has: Youtube/{lastro,historia} + a loose repo in personal, and a
// "Youtube" folder in work too (same name, different area).
func folderHome(t *testing.T, areas []config.Area, current string) (*Home, map[string]*session.Session) {
	t.Helper()
	paths := map[string]string{
		"lastro":   "/tmp/fo/P/Youtube/lastro",
		"historia": "/tmp/fo/P/Youtube/historia",
		"ideias":   "/tmp/fo/P/ideias",
		"wyt":      "/tmp/fo/W/Youtube/canal",
	}
	byName := map[string]*session.Session{}
	var ss []*session.Session
	for _, n := range []string{"lastro", "historia", "ideias", "wyt"} {
		s := session.NewSession(n, paths[n])
		byName[n] = s
		ss = append(ss, s)
	}
	h := areaTestHome(t, areas, current, ss...)
	gi := map[string]*git.RepoInfo{}
	for _, s := range ss {
		gi[s.ProjectPath] = &git.RepoInfo{OriginKey: "local:" + s.Title}
	}
	h.gitInfoCache.Store(&gi)
	return h, byName
}

// shape renders flatItems as "H:<folder>", "O:<origin>@depth", "S:<title>@depth".
func shape(items []SidebarItem) string {
	var out []string
	for _, it := range items {
		d := strings.Repeat(">", it.Depth)
		switch {
		case it.IsSpacer:
			out = append(out, "_")
		case it.IsFolderHeader:
			out = append(out, "F:"+it.FolderLabel)
		case it.IsOriginHeader:
			out = append(out, d+"O:"+it.OriginLabel)
		case it.Session != nil:
			out = append(out, d+"S:"+it.Session.Title)
		}
	}
	return strings.Join(out, " ")
}

func TestFolderGroupsInFlatItems(t *testing.T) {
	tests := []struct {
		name     string
		areas    []config.Area
		current  string
		collapse []string
		want     string
	}{
		{
			name: "no areas: no groups", areas: nil,
			want: "O:historia S:historia _ O:ideias S:ideias _ O:lastro S:lastro _ O:wyt S:wyt",
		},
		{
			name: "all view groups per area, loose repo interleaved by order", areas: folderAreas,
			want: "F:Youtube >O:historia >S:historia >O:lastro >S:lastro _ O:ideias S:ideias _ F:Youtube >O:wyt >S:wyt",
		},
		{
			name: "area view", areas: folderAreas, current: "work",
			want: "F:Youtube >O:wyt >S:wyt",
		},
		{
			name: "collapsed folder keeps only its header", areas: folderAreas, current: "personal",
			collapse: []string{FolderExpandKey("personal", "Youtube")},
			want:     "F:Youtube _ O:ideias S:ideias",
		},
		{
			name: "same folder name in another area is a separate group", areas: folderAreas,
			collapse: []string{FolderExpandKey("work", "Youtube")},
			want:     "F:Youtube >O:historia >S:historia >O:lastro >S:lastro _ O:ideias S:ideias _ F:Youtube",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := folderHome(t, tc.areas, tc.current)
			for _, k := range tc.collapse {
				h.repoExpanded[k] = false
			}
			h.rebuildFlatItems()
			// Collapsed checkouts are not part of the shape; drop checkout rows.
			if got := shape(h.flatItems); got != tc.want {
				t.Errorf("shape =\n %s\nwant\n %s", got, tc.want)
			}
		})
	}
}

func TestFolderHeaderAggregates(t *testing.T) {
	h, by := folderHome(t, folderAreas, "personal")
	by["lastro"].SetStatus(session.StatusWaiting)
	by["historia"].SetStatus(session.StatusRunning)
	h.rebuildFlatItems()
	hdr := h.flatItems[0]
	if !hdr.IsFolderHeader || !hdr.Expanded || hdr.SessionCount != 2 {
		t.Fatalf("header = %+v", hdr)
	}
	if hdr.StatusCounts[session.StatusWaiting] != 1 || hdr.StatusCounts[session.StatusRunning] != 1 {
		t.Errorf("status counts = %v", hdr.StatusCounts)
	}
	if k := h.expandKeyFor(hdr); k != "folder:personal/Youtube" {
		t.Errorf("expand key = %q", k)
	}
}

func TestFolderNavigation(t *testing.T) {
	h, by := folderHome(t, folderAreas, "personal")
	by["lastro"].SetStatus(session.StatusWaiting)
	by["ideias"].SetStatus(session.StatusWaiting)

	// Ctrl+Shift+↓ stops at the folder header and at every origin header.
	h.rebuildFlatItems()
	pos := FirstSelectableItem(h.flatItems)
	var stops []string
	for range 3 {
		pos = NextOriginItem(h.flatItems, pos, 1)
		it := h.flatItems[pos]
		if it.IsFolderHeader {
			stops = append(stops, "F:"+it.FolderLabel)
		} else if it.IsOriginHeader {
			stops = append(stops, "O:"+it.OriginLabel)
		}
	}
	if got := strings.Join(stops, " "); got != "O:historia O:lastro O:ideias" {
		t.Errorf("stops = %s", got)
	}
	// From the first origin inside the folder, ↑ stops on the folder header.
	if up := NextOriginItem(h.flatItems, 1, -1); !h.flatItems[up].IsFolderHeader {
		t.Errorf("ctrl+shift+up landed on %+v, want the folder header", h.flatItems[up])
	}

	// Space skips the waiting session inside a collapsed folder.
	h.repoExpanded[FolderExpandKey("personal", "Youtube")] = false
	h.rebuildFlatItems()
	h.cursor = 0
	h.jumpToNextAttentionSession()
	if it := h.flatItems[h.cursor]; it.Session == nil || it.Session.Title != "ideias" {
		t.Errorf("jump entered a collapsed folder: %+v", it)
	}
}

func TestGroupFoldersNilIsIdentity(t *testing.T) {
	items := []SidebarItem{{IsOriginHeader: true, OriginLabel: "a"}}
	if got := GroupFolders(items, nil, nil); len(got) != 1 || got[0].Depth != 0 {
		t.Errorf("changed: %+v", got)
	}
}
