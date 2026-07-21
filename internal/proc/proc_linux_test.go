package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseStat(t *testing.T) {
	cases := []struct {
		name string
		line string
		want procStat
		err  bool
	}{
		{
			name: "plain comm",
			line: "1234 (sleep) S 100 200 300 34816 456 4194304 0 0 0 0",
			want: procStat{ppid: 100, tpgid: 456},
		},
		{
			name: "comm with spaces and parens", // e.g. a process named "a) b (c"
			line: "42 (a) b (c) R 7 8 9 34816 11 0 0",
			want: procStat{ppid: 7, tpgid: 11},
		},
		{
			name: "no tty",
			line: "99 (daemon) S 1 99 99 0 -1 4194304 0",
			want: procStat{ppid: 1, tpgid: -1},
		},
		{name: "no paren", line: "garbage", err: true},
		{name: "truncated", line: "5 (x) S 1", err: true},
	}
	for _, c := range cases {
		got, err := parseStat(c.line)
		if c.err {
			if err == nil {
				t.Errorf("%s: expected error, got %+v", c.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: parseStat = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestPathUnder(t *testing.T) {
	dir := "/code/wt"
	prefix := dir + string(filepath.Separator)
	cases := map[string]bool{
		"/code/wt":               true,
		"/code/wt/file":          true,
		"/code/wt/a/b (deleted)": true, // held-open unlinked file still counts
		"/code/wt-sibling/file":  false,
		"/code":                  false,
		"/elsewhere":             false,
		"/code/wt2":              false,
	}
	for p, want := range cases {
		if got := pathUnder(p, dir, prefix); got != want {
			t.Errorf("pathUnder(%q) = %v, want %v", p, got, want)
		}
	}
}

// TestFindHoldersLive exercises the real /proc walk: a child process with its
// cwd inside a temp worktree must be reported as a holder, and Kill must
// clear it.
func TestFindHoldersLive(t *testing.T) {
	dir := t.TempDir()

	cmd := exec.Command("sleep", "60")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	holders, err := FindHolders(dir, nil)
	if err != nil {
		t.Fatalf("FindHolders: %v", err)
	}
	found := false
	for _, h := range holders {
		if h.PID == cmd.Process.Pid {
			found = true
			if filepath.Base(h.Command) != "sleep" {
				t.Errorf("holder command = %q, want sleep", h.Command)
			}
		}
	}
	if !found {
		t.Fatalf("FindHolders(%s) = %+v; child sleep pid %d (cwd inside) not reported",
			dir, holders, cmd.Process.Pid)
	}

	if err := Kill([]int{cmd.Process.Pid}, 2*time.Second); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	// Reap the child so the pid leaves the table (a zombie still answers
	// signal 0), then confirm liveness is gone.
	_, _ = cmd.Process.Wait()
	if Alive(cmd.Process.Pid) {
		t.Fatalf("pid %d still alive after Kill", cmd.Process.Pid)
	}
}

// TestFindHoldersOpenFD covers the fd path: a child holding an open file
// under the dir (cwd elsewhere) is still a holder.
func TestFindHoldersOpenFD(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "dev.log")

	// tail -f keeps an fd open on the file while its cwd stays outside dir.
	if err := os.WriteFile(logPath, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("tail", "-f", logPath)
	cmd.Dir = os.TempDir()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start tail: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// tail may need a moment to open the file.
	deadline := time.Now().Add(2 * time.Second)
	for {
		holders, err := FindHolders(dir, nil)
		if err != nil {
			t.Fatalf("FindHolders: %v", err)
		}
		for _, h := range holders {
			if h.PID == cmd.Process.Pid {
				return // fd hit found
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("tail pid %d holding open fd under %s not reported (got %+v)",
				cmd.Process.Pid, dir, holders)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestCommandNameSelf sanity-checks cmdline/comm reading against our own pid.
func TestCommandNameSelf(t *testing.T) {
	name := commandName(os.Getpid())
	if name == "" {
		t.Fatal("commandName(self) is empty")
	}
	// The test binary's argv[0] ends in .test.
	if got := filepath.Base(name); !strings.HasSuffix(got, ".test") {
		t.Fatalf("commandName(self) = %q, want a *.test binary", name)
	}
}

// TestFindHoldersSparesLoginShell: a login shell announces itself as "-bash"
// in argv[0]; the /proc walk reads argv[0] (unlike lsof's comm), so the dash
// variant must still match the never-kill set. Regression test for the pane
// shell of a tmux session being reported as killable.
func TestFindHoldersSparesLoginShell(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("bash", "-c", "sleep 60")
	cmd.Args[0] = "-bash" // spawn with the login-shell argv[0] convention
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatalf("start -bash: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	holders, err := FindHolders(dir, nil)
	if err != nil {
		t.Fatalf("FindHolders: %v", err)
	}
	for _, h := range holders {
		if h.PID == cmd.Process.Pid {
			t.Fatalf("login shell -bash reported as killable holder: %+v", h)
		}
	}
}

// TestFindHoldersExcludesNeverKill: a shell holding the dir must be spared.
func TestFindHoldersExcludesNeverKill(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("sh", "-c", "sleep 60")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sh: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	holders, err := FindHolders(dir, nil)
	if err != nil {
		t.Fatalf("FindHolders: %v", err)
	}
	for _, h := range holders {
		if h.PID == cmd.Process.Pid {
			t.Fatalf("sh (never-kill) reported as holder: %+v", h)
		}
	}
}
