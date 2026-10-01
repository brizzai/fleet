package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestInjectCopilotHooks(t *testing.T) {
	dir := t.TempDir()
	changed, err := InjectCopilotHooks(dir)
	if err != nil || !changed {
		t.Fatalf("first inject: changed=%v err=%v", changed, err)
	}
	if !CopilotHooksInstalled(dir) {
		t.Fatal("hooks not reported installed after inject")
	}
	if changed, _ := InjectCopilotHooks(dir); changed {
		t.Fatal("second inject rewrote an unchanged file")
	}

	data, err := os.ReadFile(filepath.Join(dir, "hooks", copilotHooksFile))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version int `json:"version"`
		Hooks   map[string][]struct {
			Type, Bash string
			TimeoutSec int
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != 1 {
		t.Errorf("version = %d, want 1 (Copilot rejects the file without it)", doc.Version)
	}
	for _, ev := range copilotHookEvents {
		entries := doc.Hooks[ev]
		if len(entries) != 1 {
			t.Fatalf("%s: %d entries, want 1", ev, len(entries))
		}
		e := entries[0]
		if e.Type != "command" || !strings.HasPrefix(e.Bash, copilotNonFleetGuard) ||
			!strings.HasSuffix(e.Bash, " --agent copilot --event "+ev) || !isFleetHook(e.Bash) {
			t.Errorf("%s: unexpected entry %+v", ev, e)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Error("inject must never touch config.json")
	}

	if removed, err := RemoveCopilotHooks(dir); !removed || err != nil {
		t.Fatalf("remove: removed=%v err=%v", removed, err)
	}
	if CopilotHooksInstalled(dir) {
		t.Fatal("still installed after remove")
	}
}

// A failing permissionRequest hook makes Copilot DENY the tool, so a moved
// fleet binary would block every Copilot session on the machine.
func TestCopilotHooksNeverSubscribePermissionRequest(t *testing.T) {
	for _, ev := range copilotHookEvents {
		if strings.EqualFold(ev, "permissionRequest") || strings.EqualFold(ev, "sessionStart") {
			t.Errorf("must not subscribe %s", ev)
		}
	}
}

func readTrusted(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		TrustedFolders []string `json:"trustedFolders"`
	}
	if err := json.Unmarshal(stripJSONComments(data), &root); err != nil {
		t.Fatalf("rewritten config.json no longer parses: %v\n%s", err, data)
	}
	return root.TrustedFolders
}

func TestEnsureCopilotDirTrust(t *testing.T) {
	dir := t.TempDir()
	fixture, err := os.ReadFile(filepath.Join("testdata", "copilot_config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	real, _ := filepath.EvalSymlinks(project)

	if err := EnsureCopilotDirTrust(dir, project); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCopilotDirTrust(dir, project); err != nil {
		t.Fatal(err)
	}
	got := readTrusted(t, cfg)
	if len(got) != 2 || got[0] != "/Users/dev/already-trusted" || got[1] != real {
		t.Fatalf("trustedFolders = %v, want the existing entry plus %q once (realpath)", got, real)
	}

	data, _ := os.ReadFile(cfg)
	s := string(data)
	if !strings.HasPrefix(s, "// User settings belong in settings.json.\n// This file is managed automatically.\n{") {
		t.Errorf("comment header not kept:\n%s", s)
	}
	if !strings.Contains(s, `"host": "https://github.com"`) {
		t.Errorf("a // inside a string was treated as a comment:\n%s", s)
	}
	if fi, _ := os.Stat(cfg); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 kept", fi.Mode().Perm())
	}
}

func TestEnsureCopilotDirTrustCreatesConfig(t *testing.T) {
	dir := t.TempDir()
	project := t.TempDir()
	if err := EnsureCopilotDirTrust(dir, project); err != nil {
		t.Fatal(err)
	}
	if got := readTrusted(t, filepath.Join(dir, "config.json")); len(got) != 1 {
		t.Fatalf("trustedFolders = %v", got)
	}
}

func TestEnsureCopilotDirTrustRefusesUnparseable(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	bad := []byte("// header\n{ \"trustedFolders\": [ \n")
	if err := os.WriteFile(cfg, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCopilotDirTrust(dir, t.TempDir()); err == nil {
		t.Fatal("expected an error for an unparseable config.json")
	}
	if got, _ := os.ReadFile(cfg); string(got) != string(bad) {
		t.Fatal("an unparseable config.json was rewritten")
	}
}

func TestStripJSONComments(t *testing.T) {
	in := "// head\n{\"a\": \"http://x\", /* c */ \"b\": \"q\\\"//\" // tail\n}"
	var v map[string]string
	if err := json.Unmarshal(stripJSONComments([]byte(in)), &v); err != nil {
		t.Fatal(err)
	}
	if v["a"] != "http://x" || v["b"] != `q"//` {
		t.Fatalf("got %v", v)
	}
}

// The guard must end the hook without starting fleet when the session isn't
// fleet's, and hand over to fleet (same stdin) when it is.
func TestCopilotNonFleetGuardRunsInBash(t *testing.T) {
	cmd := copilotNonFleetGuard + "cat"
	run := func(instance string) string {
		t.Helper()
		c := exec.Command("bash", "-c", cmd)
		c.Env = append(os.Environ(), "FLEET_INSTANCE_ID="+instance)
		c.Stdin = strings.NewReader("payload")
		out, err := c.Output()
		if err != nil {
			t.Fatalf("instance=%q: %v", instance, err)
		}
		return string(out)
	}
	if got := run(""); got != "" {
		t.Errorf("non-fleet session reached the command: %q", got)
	}
	if got := run("inst1"); got != "payload" {
		t.Errorf("fleet session: got %q, want the payload passed through", got)
	}
}

func TestEnsureCopilotDirTrustKeepsSymlinkedConfig(t *testing.T) {
	dir, dotfiles := t.TempDir(), t.TempDir()
	target := filepath.Join(dotfiles, "copilot.json")
	if err := os.WriteFile(target, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCopilotDirTrust(dir, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config.json symlink was replaced by a regular file")
	}
	if got := readTrusted(t, target); len(got) != 1 {
		t.Fatalf("link target trustedFolders = %v", got)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644 kept (not the 0600 default)", fi.Mode().Perm())
	}
}

// A hooks file naming a deleted fleet build (a removed worktree's build/) is
// rewritten to the running binary.
func TestRepairCopilotHooksHealsADeletedCommand(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(t.TempDir(), "deleted-worktree", "build", "fleet")
	writeCopilotHooksFile(t, dir, gone)

	repaired, err := RepairCopilotHooks(dir)
	if err != nil || !repaired {
		t.Fatalf("RepairCopilotHooks = %v, %v; want a repair", repaired, err)
	}
	if !CopilotHooksInstalled(dir) {
		t.Error("hooks file does not name the current hook command after repair")
	}
}

// A path that still resolves is left alone, whoever wrote it — rewriting it on
// a timer would make two fleet builds fight over the file.
func TestRepairCopilotHooksLeavesAResolvablePathAlone(t *testing.T) {
	dir := t.TempDir()
	sibling := filepath.Join(t.TempDir(), "fleet")
	if err := os.WriteFile(sibling, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeCopilotHooksFile(t, dir, sibling)
	before, _ := os.ReadFile(path)

	if repaired, err := RepairCopilotHooks(dir); err != nil || repaired {
		t.Fatalf("RepairCopilotHooks = %v, %v; want no repair", repaired, err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("rewrote a hooks file whose binary still exists")
	}
}

// No hooks file means no Copilot install to repair; nothing may be created.
func TestRepairCopilotHooksNeverInstalls(t *testing.T) {
	dir := t.TempDir()
	if repaired, err := RepairCopilotHooks(dir); err != nil || repaired {
		t.Fatalf("RepairCopilotHooks = %v, %v; want no-op", repaired, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hooks")); !os.IsNotExist(err) {
		t.Error("created the hooks dir for a user without Copilot hooks")
	}
}

func writeCopilotHooksFile(t *testing.T, dir, bin string) string {
	t.Helper()
	doc := copilotHooksDoc{Version: 1, Hooks: map[string][]copilotHookEntry{
		"agentStop": {{Type: "command", Bash: copilotNonFleetGuard + shellQuote(bin) + " hook-handler " + fleetHookArg + " --agent copilot --event agentStop", TimeoutSec: 10}},
	}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "hooks", copilotHooksFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Seeding trust must not rewrite Copilot's other values into escaped forms.
func TestEnsureCopilotDirTrustKeepsValuesVerbatim(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte("// header\n{\n  \"note\": \"a & b <c>\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCopilotDirTrust(dir, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cfg)
	if !strings.Contains(string(got), `"a & b <c>"`) {
		t.Fatalf("value was rewritten:\n%s", got)
	}
	if !strings.HasPrefix(string(got), "// header\n") || !strings.HasSuffix(string(got), "}\n") {
		t.Fatalf("header or trailing newline lost:\n%q", got)
	}
}

// Launches seed trust on their own goroutines; concurrent seeds must neither
// lose an entry nor wipe the user's config.
func TestConcurrentTrustSeedsKeepEveryEntry(t *testing.T) {
	codexDir, copilotDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte("model = \"keep-me\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 8)
	for i := range paths {
		paths[i] = t.TempDir()
	}
	var wg sync.WaitGroup
	for _, p := range paths {
		wg.Add(2)
		go func() { defer wg.Done(); _ = EnsureCodexDirTrust(codexDir, p) }()
		go func() { defer wg.Done(); _ = EnsureCopilotDirTrust(copilotDir, p) }()
	}
	wg.Wait()
	codex, _ := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	copilot, _ := os.ReadFile(filepath.Join(copilotDir, "config.json"))
	if !strings.Contains(string(codex), `model = "keep-me"`) {
		t.Fatal("concurrent Codex seeds wiped the user's config.toml")
	}
	for _, p := range paths {
		real, _ := filepath.EvalSymlinks(p)
		if !strings.Contains(string(codex), p) && !strings.Contains(string(codex), real) {
			t.Errorf("codex lost trust for %s", real)
		}
		if !strings.Contains(string(copilot), real) {
			t.Errorf("copilot lost trust for %s", real)
		}
	}
}

// A symlinked config.toml (dotfiles) is written through, not replaced.
func TestCodexTrustWritesThroughSymlink(t *testing.T) {
	dir, dots := t.TempDir(), t.TempDir()
	target := filepath.Join(dots, "config.toml")
	if err := os.WriteFile(target, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "config.toml")); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCodexDirTrust(dir, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(filepath.Join(dir, "config.toml")); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config.toml symlink was replaced by a regular file")
	}
	if data, _ := os.ReadFile(target); !strings.Contains(string(data), "trust_level") {
		t.Fatal("trust was not written to the symlink target")
	}
}

// A config.toml symlink whose target doesn't exist yet stays a link, and the
// trust lands in the target — as on master.
func TestCodexTrustKeepsADanglingSymlink(t *testing.T) {
	dir, dots := t.TempDir(), t.TempDir()
	target := filepath.Join(dots, "config.toml") // not created
	if err := os.Symlink(target, filepath.Join(dir, "config.toml")); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCodexDirTrust(dir, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(filepath.Join(dir, "config.toml")); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("dangling config.toml symlink was replaced by a regular file")
	}
	if data, _ := os.ReadFile(target); !strings.Contains(string(data), "trust_level") {
		t.Fatal("trust was not written to the symlink target")
	}
}

// Two fleet processes seeding at once (parallel `fleet wt --agent copilot`)
// must not lose entries: trustMu is per-process, so this needs Copilot's flock.
// The test binary re-runs itself as the two writers.
func TestCopilotTrustAcrossProcesses(t *testing.T) {
	if dir := os.Getenv("FLEET_TRUST_CHILD_DIR"); dir != "" {
		for i := range 40 {
			if err := EnsureCopilotDirTrust(dir, fmt.Sprintf("/nonexistent/%s/%d", os.Getenv("FLEET_TRUST_CHILD_ID"), i)); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	dir := t.TempDir()
	var cmds []*exec.Cmd
	for _, id := range []string{"a", "b"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCopilotTrustAcrossProcesses$")
		cmd.Env = append(os.Environ(), "FLEET_TRUST_CHILD_DIR="+dir, "FLEET_TRUST_CHILD_ID="+id)
		cmds = append(cmds, cmd)
	}
	for _, c := range cmds {
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("child: %v", err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		TrustedFolders []string `json:"trustedFolders"`
	}
	if err := json.Unmarshal(stripJSONComments(data), &cfg); err != nil {
		t.Fatal(err)
	}
	if got := len(cfg.TrustedFolders); got != 80 {
		t.Fatalf("trustedFolders has %d entries after two processes added 40 each, want 80", got)
	}
}

// A stuck holder of Copilot's trust lock (a hung Copilot, a stale network
// lock) must not hang session creation — Copilot's or, via a shared mutex,
// Codex's. The seed waits briefly, then writes unlocked.
func TestStuckCopilotTrustLockDoesNotBlockSeeds(t *testing.T) {
	old := copilotTrustLockWait
	copilotTrustLockWait = 200 * time.Millisecond
	defer func() { copilotTrustLockWait = old }()

	copilotDir, codexDir := t.TempDir(), t.TempDir()
	holder, err := os.OpenFile(filepath.Join(copilotDir, "config.json.trusted-folders.lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	copilotDone := make(chan error, 1)
	go func() { copilotDone <- EnsureCopilotDirTrust(copilotDir, "/nonexistent/stuck") }()

	start := time.Now()
	if err := EnsureCodexDirTrust(codexDir, "/nonexistent/codex"); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("a Codex seed waited on Copilot's trust lock")
	}
	select {
	case err := <-copilotDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Copilot seed hung on a stuck trust lock")
	}
	if data, _ := os.ReadFile(filepath.Join(copilotDir, "config.json")); !strings.Contains(string(data), "/nonexistent/stuck") {
		t.Fatal("trust not written after giving up on the lock")
	}
}

// A config dir where the lock can't be created (read-only) is not an error
// when the folder is already trusted.
func TestCopilotTrustWithoutLockableDirStillWorks(t *testing.T) {
	dir := t.TempDir()
	proj := t.TempDir()
	if err := EnsureCopilotDirTrust(dir, proj); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, "config.json.trusted-folders.lock"))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if err := EnsureCopilotDirTrust(dir, proj); err != nil {
		t.Fatalf("already-trusted folder in a read-only dir errored: %v", err)
	}
}
