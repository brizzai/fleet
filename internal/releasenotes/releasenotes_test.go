package releasenotes

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestNormalizeVersion(t *testing.T) {
	cases := map[string]string{
		"v2.15.0":              "2.15.0",
		"2.15.0":               "2.15.0",
		"v2.15.0-3-gabc-dirty": "2.15.0",
		"2.15.0-next":          "2.15.0",
		"  v1.0.0 ":            "1.0.0",
		"dev":                  "dev",
	}
	for in, want := range cases {
		if got := NormalizeVersion(in); got != want {
			t.Errorf("NormalizeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2.15.0", "2.14.0", 1},
		{"2.14.0", "2.15.0", -1},
		{"2.15.0", "2.15.0", 0},
		{"2.15.1", "2.15.0", 1},
		{"2.9.0", "2.15.0", -1}, // numeric, not lexical
		{"3.0.0", "2.99.99", 1},
		{"2.15", "2.15.0", 0}, // missing segment == 0
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseBodyStripsInstallBoilerplate(t *testing.T) {
	body := "## fleet v2.15.0\n\n" +
		"### Install / Update\n\n" +
		"```bash\nbrew install brizzai/tap/fleet\n```\n\n" +
		"---\n\n" +
		"### Added\n\n" +
		"- Fleet can now auto-suspend idle sessions.\n" +
		"- A second added thing.\n\n" +
		"### Fixed\n\n" +
		"- Worktree name no longer snowballs.\n"

	got := parseBody(body)
	want := []Section{
		{Title: "Added", Bullets: []string{"Fleet can now auto-suspend idle sessions.", "A second added thing."}},
		{Title: "Fixed", Bullets: []string{"Worktree name no longer snowballs."}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseBody() = %#v\nwant %#v", got, want)
	}
}

func TestParseBodyNoDividerFallback(t *testing.T) {
	// No "---": parse the whole body. A bullet before any heading lands in an
	// untitled section.
	body := "- Just a bullet, no heading.\n### Added\n- Under a heading.\n"
	got := parseBody(body)
	want := []Section{
		{Bullets: []string{"Just a bullet, no heading."}},
		{Title: "Added", Bullets: []string{"Under a heading."}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseBody() = %#v\nwant %#v", got, want)
	}
}

func TestParseBodyJoinsWrappedBullet(t *testing.T) {
	body := "---\n### Added\n- First line\n  continuation line\n"
	got := parseBody(body)
	want := []Section{{Title: "Added", Bullets: []string{"First line continuation line"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseBody() = %#v\nwant %#v", got, want)
	}
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		days int
		want string
	}{
		{0, "today"},
		{1, "yesterday"},
		{5, "5 days ago"},
		{40, "1 month ago"},
		{70, "2 months ago"},
		{400, "1 year ago"},
		{800, "2 years ago"},
	}
	for _, c := range cases {
		then := now.AddDate(0, 0, -c.days)
		if got := ago(then, now); got != c.want {
			t.Errorf("ago(%d days) = %q, want %q", c.days, got, c.want)
		}
	}
	if got := ago(now.Add(24*time.Hour), now); got != "today" {
		t.Errorf("future date should read %q, got %q", "today", got)
	}
}

func TestNormalizeReleasesSortsAndDropsDrafts(t *testing.T) {
	fixture := `[
		{"tag_name":"v2.14.0","name":"v2.14.0","published_at":"2026-07-05T10:00:00Z","body":"---\n### Improved\n- Faster quit.","prerelease":false,"draft":false,"html_url":"https://x/2.14.0"},
		{"tag_name":"v2.15.0","name":"v2.15.0","published_at":"2026-07-06T10:00:00Z","body":"---\n### Added\n- Suspend idle.","prerelease":false,"draft":false,"html_url":"https://x/2.15.0"},
		{"tag_name":"v2.16.0-rc1","name":"v2.16.0-rc1","published_at":"2026-07-10T10:00:00Z","body":"draft body","prerelease":true,"draft":true,"html_url":"https://x/2.16.0"}
	]`
	var raw []ghRelease
	if err := json.Unmarshal([]byte(fixture), &raw); err != nil {
		t.Fatalf("fixture unmarshal: %v", err)
	}

	got := normalizeReleases(raw)
	if len(got) != 2 {
		t.Fatalf("expected 2 releases (draft dropped), got %d", len(got))
	}
	if got[0].Version != "2.15.0" || got[1].Version != "2.14.0" {
		t.Errorf("expected newest-first [2.15.0, 2.14.0], got [%s, %s]", got[0].Version, got[1].Version)
	}
	if got[0].Date != "2026-07-06" {
		t.Errorf("date = %q, want 2026-07-06", got[0].Date)
	}
	if len(got[0].Sections) != 1 || got[0].Sections[0].Title != "Added" {
		t.Errorf("unexpected sections for newest release: %#v", got[0].Sections)
	}
}
