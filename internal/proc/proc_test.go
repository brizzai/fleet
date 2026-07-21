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
		{"-bash", nil, true}, // login shell: argv[0] carries a leading dash
		{"-zsh", nil, true},
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
		"-bash":                      "bash", // login-shell dash stripped
		"":                           "",
	}
	for in, want := range cases {
		if got := normalizeCmd(in); got != want {
			t.Errorf("normalizeCmd(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseForeground(t *testing.T) {
	// Mirrors `ps -axo pid=,ppid=,tpgid=,command=`: leading-padded pid, ppid,
	// tpgid (the tty's foreground process group), then the full argv (which may
	// contain spaces). A shell's row carries its tpgid; the child whose pid ==
	// that tpgid is the foreground command. want = the pane-shell pids.
	out := "" +
		"  100     1     0 /sbin/launchd\n" + // unrelated root process (no tty)
		"  200   100   350 -zsh\n" + // shell A: foreground group is 350
		"  350   200   350 npm run dev --port 3000\n" + // A's fg leader (pid == tpgid)
		"  360   350   350 node /repo/server.js\n" + // grandchild in the group: not the leader
		"  400   999   510 -zsh\n" + // shell B: foreground group is 510
		"  510   400   510 tail -f app.log\n" // B's fg leader
	want := map[int]bool{200: true, 400: true}
	got := parseForeground(out, want)
	expect := map[int]string{
		200: "npm run dev --port 3000",
		400: "tail -f app.log",
	}
	if !reflect.DeepEqual(got, expect) {
		t.Fatalf("parseForeground = %+v, want %+v", got, expect)
	}
}

func TestParseForegroundExcludesBackgroundJob(t *testing.T) {
	// `some-server &` (lower pid, its own group) then `npm run dev` (foreground):
	// tpgid points at the foreground group, so the background job never wins even
	// though it has the lower pid.
	out := "" +
		"  300   100   650 -zsh\n" + // shell: foreground group is 650
		"  500   300   500 some-server\n" + // backgrounded: its own group, not foreground
		"  650   300   650 npm run dev\n" // foreground leader (pid == tpgid)
	got := parseForeground(out, map[int]bool{300: true})
	if got[300] != "npm run dev" {
		t.Fatalf("parseForeground bg job = %q, want %q", got[300], "npm run dev")
	}
}

func TestParseForegroundPipelinePicksLeader(t *testing.T) {
	// A pipeline `grep foo | sort -u` shares one foreground group led by the first
	// stage; tpgid resolves to that leader.
	out := "" +
		"  300   100   650 -zsh\n" +
		"  650   300   650 grep foo\n" + // group leader (pid == tpgid)
		"  700   300   650 sort -u\n" // same group, not the leader
	got := parseForeground(out, map[int]bool{300: true})
	if got[300] != "grep foo" {
		t.Fatalf("parseForeground pipeline = %q, want %q", got[300], "grep foo")
	}
}

func TestParseForegroundIdleShellHasNoChild(t *testing.T) {
	// At a prompt the shell is its own foreground group (tpgid == its pid), so the
	// group leader is the shell — not a child — and is excluded.
	out := "  200     1   200 -zsh\n"
	if got := parseForeground(out, map[int]bool{200: true}); len(got) != 0 {
		t.Fatalf("idle shell should have no foreground command, got %+v", got)
	}
}
