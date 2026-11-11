package area

import (
	"slices"
	"testing"

	"github.com/brizzai/fleet/internal/claudeaccount"
	"github.com/brizzai/fleet/internal/config"
)

const home = "/home/u"

func twoAreas() []config.Area {
	return []config.Area{
		{Name: "personal", Paths: []string{"~/Projects/Personal", "~/Projects/Study"}, Account: "me@x.com", Default: true},
		{Name: "work", Paths: []string{"~/Projects/Work", "/srv/work"}, Account: "me@corp.com"},
	}
}

func TestOf(t *testing.T) {
	nested := []config.Area{
		{Name: "outer", Paths: []string{"~/p"}},
		{Name: "inner", Paths: []string{"~/p/deep"}},
	}
	noDefault := []config.Area{{Name: "work", Paths: []string{"~/w"}}}

	tests := []struct {
		name  string
		areas []config.Area
		path  string
		main  string
		want  string
	}{
		{"no areas configured", nil, "/home/u/x", "", All},
		{"tilde expansion", twoAreas(), "/home/u/Projects/Work/api", "", "work"},
		{"exact prefix", twoAreas(), "/home/u/Projects/Work", "", "work"},
		{"absolute path", twoAreas(), "/srv/work/x", "", "work"},
		{"directory boundary", twoAreas(), "/home/u/Projects/Work2/api", "", "personal"},
		{"prefix shorter than dir name", []config.Area{{Name: "w", Paths: []string{"~/Projects/Trab"}}, {Name: "d", Paths: []string{"~"}, Default: true}}, "/home/u/Projects/Trabalho/x", "", "d"},
		{"longest prefix wins", nested, "/home/u/p/deep/x", "", "inner"},
		{"shorter prefix still matches", nested, "/home/u/p/other", "", "outer"},
		{"trailing slash and dots cleaned", twoAreas(), "/home/u/Projects/Work/../Work/api/", "", "work"},
		{"unmatched goes to default", twoAreas(), "/tmp/scratch", "", "personal"},
		{"unmatched without default is unowned", noDefault, "/tmp/scratch", "", All},
		{"worktree outside folder follows main repo", twoAreas(), "/tmp/wt/feature", "/home/u/Projects/Work/api", "work"},
		{"own path beats main repo", twoAreas(), "/home/u/Projects/Personal/wt", "/home/u/Projects/Work/api", "personal"},
		{"main repo unmatched falls to default", twoAreas(), "/tmp/wt", "/tmp/other", "personal"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New(tc.areas, home)
			var main func() string
			if tc.main != "" {
				main = func() string { return tc.main }
			}
			if got := m.Of(tc.path, main); got != tc.want {
				t.Errorf("Of(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestOfDoesNotAskForMainRepoWhenPathMatches(t *testing.T) {
	m := New(twoAreas(), home)
	m.Of("/home/u/Projects/Work/a", func() string {
		t.Error("mainRepo called although the path matched")
		return ""
	})
}

func TestNext(t *testing.T) {
	m := New(twoAreas(), home)
	tests := []struct{ cur, want string }{
		{All, "personal"},
		{"personal", "work"},
		{"work", All},
		{"gone", "personal"},
	}
	for _, tc := range tests {
		if got := m.Next(tc.cur); got != tc.want {
			t.Errorf("Next(%q) = %q, want %q", tc.cur, got, tc.want)
		}
	}
	if got := New(nil, home).Next(All); got != All {
		t.Errorf("Next with no areas = %q, want all", got)
	}
}

func TestVisible(t *testing.T) {
	tests := []struct {
		cur, of string
		want    bool
	}{
		{All, "work", true},
		{"work", "work", true},
		{"work", "personal", false},
		{"work", All, true},
	}
	for _, tc := range tests {
		if got := Visible(tc.cur, tc.of); got != tc.want {
			t.Errorf("Visible(%q,%q) = %v, want %v", tc.cur, tc.of, got, tc.want)
		}
	}
}

func TestResolveAndNames(t *testing.T) {
	m := New(append(twoAreas(), config.Area{Name: "  "}), home)
	if got := m.Names(); !slices.Equal(got, []string{"personal", "work"}) {
		t.Errorf("Names() = %v", got)
	}
	if n, ok := m.Resolve("WORK"); !ok || n != "work" {
		t.Errorf("Resolve(WORK) = %q, %v", n, ok)
	}
	if _, ok := m.Resolve("nope"); ok {
		t.Error("Resolve(nope) should fail")
	}
	if New(nil, home).Enabled() {
		t.Error("no areas must be disabled")
	}
}

func TestAccountOfArea(t *testing.T) {
	m := New(twoAreas(), home)
	if got := m.Account("/home/u/Projects/Work/x", nil); got != "me@corp.com" {
		t.Errorf("Account = %q", got)
	}
	if got := New(nil, home).Account("/x", nil); got != "" {
		t.Errorf("Account with no areas = %q", got)
	}
}

func TestPickAccount(t *testing.T) {
	accts := []claudeaccount.Account{{Email: "me@x.com"}, {Email: "me@corp.com"}}
	tests := []struct {
		name    string
		email   string
		accts   []claudeaccount.Account
		usage   map[string]claudeaccount.Usage
		allowed []string
		want    string
	}{
		{"area account used", "me@corp.com", accts, nil, nil, "me@corp.com"},
		{"allowed list includes it", "me@corp.com", accts, nil, []string{"me@corp.com"}, "me@corp.com"},
		{"not allowed falls to normal selection", "me@corp.com", accts, nil, []string{"me@x.com"}, ""},
		{"unknown account falls to normal selection", "ghost@x.com", accts, nil, nil, ""},
		{"logged out falls to normal selection", "me@corp.com", accts, map[string]claudeaccount.Usage{"me@corp.com": {LoggedOut: true}}, nil, ""},
		{"area without account", "", accts, nil, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := PickAccount(tc.email, tc.accts, tc.usage, tc.allowed)
			if got.Email != tc.want || ok != (tc.want != "") {
				t.Errorf("PickAccount = %q, %v; want %q", got.Email, ok, tc.want)
			}
		})
	}
}

func TestFolder(t *testing.T) {
	areas := []config.Area{
		{Name: "personal", Paths: []string{"~/P"}, Default: true},
		{Name: "work", Paths: []string{"~/W", "~/P/Work"}},
		{Name: "other", Paths: []string{"~/O"}},
	}
	tests := []struct {
		name       string
		areas      []config.Area
		path, main string
		wantArea   string
		wantFolder string
	}{
		{"two levels below the area", areas, "/home/u/P/Youtube/lastro", "", "personal", "Youtube"},
		{"deeper still: one grouping level only", areas, "/home/u/P/Youtube/a/b/c", "", "personal", "Youtube"},
		{"directly in the area folder", areas, "/home/u/P/ideias", "", "personal", ""},
		{"the area folder itself", areas, "/home/u/P", "", "personal", ""},
		{"no area matches", areas, "/tmp/x/y/z", "", "", ""},
		{"default area does not group unmatched paths", areas, "/srv/a/b", "", "", ""},
		{"no areas configured", nil, "/home/u/P/Youtube/lastro", "", "", ""},
		{"longest prefix is the base", areas, "/home/u/P/Work/api/svc", "", "work", "api"},
		{"longest prefix, directly inside", areas, "/home/u/P/Work/api", "", "work", ""},
		{"worktree outside the folders follows its main repo", areas, "/tmp/wt", "/home/u/W/clients/app", "work", "clients"},
		{"boundary: P2 is not P", areas, "/home/u/P2/a/b", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New(tc.areas, home)
			var main func() string
			if tc.main != "" {
				main = func() string { return tc.main }
			}
			a, f := m.Folder(tc.path, main)
			if a != tc.wantArea || f != tc.wantFolder {
				t.Errorf("Folder(%q) = (%q, %q), want (%q, %q)", tc.path, a, f, tc.wantArea, tc.wantFolder)
			}
		})
	}
}
