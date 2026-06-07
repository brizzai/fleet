package proc

import (
	"reflect"
	"testing"
)

func TestParseHolders(t *testing.T) {
	dir := "/Users/me/code/repo-wt"
	// Mirrors real `lsof -Fpcn` output: a process block is p<pid>, then c<cmd>,
	// then one n<path> per open file (cwd included, fd field omitted by -Fpcn).
	out := "" +
		"p100\ncprocess-compose\nn" + dir + "\nn" + dir + "/.brz-env.log\n" +
		"p200\ncair\nn" + dir + "/pkg/shared\nn" + dir + "/pkg/shared/app.go\n" +
		"p300\ncnode\nn" + dir + "/apps/dashboard\n" +
		"p400\ncCode Helper (Plugin)\nn" + dir + "/pkg\n" + // editor helper: spared
		"p500\ncgopls\nn" + dir + "/pkg/shared\n" + // language server: spared
		"p600\nczsh\nn" + dir + "\n" + // shell: spared
		"p700\ncnode\nn/Users/me/other/elsewhere\n" + // outside dir: ignored
		"p999\nccode\nn" + dir + "/x\n" // matches extraExclude editor: spared

	got := parseHolders(out, dir, 12345, map[string]bool{"code": true})

	want := []Holder{
		{PID: 100, Command: "process-compose"},
		{PID: 200, Command: "air"},
		{PID: 300, Command: "node"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseHolders = %+v, want %+v", got, want)
	}
}

func TestParseHoldersSkipsSelf(t *testing.T) {
	dir := "/tmp/wt"
	out := "p4242\ncprocess-compose\nn" + dir + "/log\n"
	if got := parseHolders(out, dir, 4242, nil); len(got) != 0 {
		t.Fatalf("expected own pid to be skipped, got %+v", got)
	}
}

func TestParseHoldersPrefixBoundary(t *testing.T) {
	dir := "/code/wt"
	// "/code/wt-sibling" must NOT match the "/code/wt" prefix.
	out := "p1\ncnode\nn/code/wt-sibling/file\n" +
		"p2\ncnode\nn/code/wt/file\n"
	got := parseHolders(out, dir, 0, nil)
	want := []Holder{{PID: 2, Command: "node"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseHolders = %+v, want %+v", got, want)
	}
}

func TestExcluded(t *testing.T) {
	cases := []struct {
		cmd   string
		extra map[string]bool
		want  bool
	}{
		{"process-compose", nil, false},
		{"air", nil, false},
		{"node", nil, false},
		{"gopls", nil, true},
		{"terraform-ls", nil, true},
		{"zsh", nil, true},
		{"Code Helper (Plugin)", nil, true},
		{"code", nil, true},
		{"", nil, true},
		{"mycustomeditor", map[string]bool{"mycustomeditor": true}, true},
		{"/usr/local/bin/code --wait", nil, true},
	}
	for _, c := range cases {
		if got := excluded(c.cmd, c.extra); got != c.want {
			t.Errorf("excluded(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestNormalizeCmd(t *testing.T) {
	cases := map[string]string{
		"code":                       "code",
		"/usr/local/bin/code --wait": "code",
		"Cursor":                     "cursor",
		"  vim  ":                    "vim",
		"":                           "",
	}
	for in, want := range cases {
		if got := normalizeCmd(in); got != want {
			t.Errorf("normalizeCmd(%q) = %q, want %q", in, got, want)
		}
	}
}
