package hooks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func readHooksJSON(t *testing.T, dir string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatalf("hooks.json is not JSON: %v\n%s", err, data)
	}
	return keys
}

func writeHooksJSON(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInjectAntigravityHooksCreatesFleetEntry(t *testing.T) {
	dir := t.TempDir()
	changed, err := InjectAntigravityHooks(dir)
	if err != nil || !changed {
		t.Fatalf("first inject: changed=%v err=%v", changed, err)
	}
	if changed, _ := InjectAntigravityHooks(dir); changed {
		t.Fatal("second inject rewrote an unchanged entry")
	}
	if got := AntigravityHookState(dir); got != "installed" {
		t.Errorf("state = %q, want installed", got)
	}

	var entry map[string]json.RawMessage
	if err := json.Unmarshal(readHooksJSON(t, dir)[antigravityHookKey], &entry); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []string{"PreInvocation", "PostToolUse", "Stop"} {
		if _, ok := entry[ev]; !ok {
			t.Errorf("missing %s", ev)
		}
	}
	// PreToolUse fails closed (a broken fleet would block every tool call), and
	// SessionStart is nulled by Antigravity's own /hooks manager.
	for _, ev := range []string{"PreToolUse", "SessionStart"} {
		if _, ok := entry[ev]; ok {
			t.Errorf("fleet must not register %s", ev)
		}
	}
	var decoded antigravityFleetHooks
	_ = json.Unmarshal(readHooksJSON(t, dir)[antigravityHookKey], &decoded)
	commands := map[string]string{
		"PreInvocation": decoded.PreInvocation[0].Command,
		"PostToolUse":   decoded.PostToolUse[0].Hooks[0].Command,
		"Stop":          decoded.Stop[0].Command,
	}
	for ev, cmd := range commands {
		// The guard's `exec` is what makes the handler's parent the agent
		// rather than a shell that forked it.
		if !strings.HasPrefix(cmd, nonFleetSessionGuard) || !strings.HasSuffix(cmd, "--agent antigravity --event "+ev) {
			t.Errorf("%s: command lacks the guard or its event: %s", ev, cmd)
		}
	}
}

// A dotfiles hooks.json can be a link to a file that doesn't exist yet: the
// install creates the target and leaves the link in place.
func TestInjectAntigravityHooksKeepsADanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	dotfiles := filepath.Join(t.TempDir(), "dotfiles")
	link := filepath.Join(dir, "hooks.json")
	if err := os.Symlink(filepath.Join(dotfiles, "hooks.json"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := InjectAntigravityHooks(dir); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("hooks.json symlink was replaced by a regular file")
	}
	if _, ok := readHooksJSON(t, dotfiles)[antigravityHookKey]; !ok {
		t.Fatal("the link's target holds no fleet entry")
	}
}

// hooks.json is shared with everything else the user installs. Fleet's write
// must leave every other key exactly as it was.
func TestInjectAntigravityHooksKeepsOtherHooks(t *testing.T) {
	dir := t.TempDir()
	user := `{"my-linter":{"PostToolUse":[{"matcher":"run_command","hooks":[{"type":"command","command":"./lint.sh \"a & b\" 2>&1"}]}]}}`
	writeHooksJSON(t, dir, user)
	if _, err := InjectAntigravityHooks(dir); err != nil {
		t.Fatal(err)
	}
	keys := readHooksJSON(t, dir)
	var want, got any
	_ = json.Unmarshal([]byte(user), &want)
	_ = json.Unmarshal(keys["my-linter"], &got)
	wantJSON, _ := json.Marshal(want.(map[string]any)["my-linter"])
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Errorf("user's hook changed:\n got %s\nwant %s", gotJSON, wantJSON)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "hooks.json")); !strings.Contains(string(raw), `2>&1`) {
		t.Errorf("user's hook was re-escaped on disk:\n%s", raw)
	}

	if removed, err := RemoveAntigravityHooks(dir); err != nil || !removed {
		t.Fatalf("remove: removed=%v err=%v", removed, err)
	}
	keys = readHooksJSON(t, dir)
	if _, ok := keys[antigravityHookKey]; ok {
		t.Error("fleet's entry survived remove")
	}
	if _, ok := keys["my-linter"]; !ok {
		t.Error("remove took the user's hook with it")
	}
}

// Antigravity's /hooks manager rewrites the file with every event spelled out
// (null when empty) and an "enabled" switch. That rewrite must not read as a
// stale entry, or fleet and Antigravity would rewrite the file back and forth.
func TestAntigravityRewriteOfFleetEntryIsNotStale(t *testing.T) {
	dir := t.TempDir()
	if _, err := InjectAntigravityHooks(dir); err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	_ = json.Unmarshal(readHooksJSON(t, dir)[antigravityHookKey], &entry)
	entry["SessionStart"], entry["PreToolUse"], entry["PostInvocation"] = nil, nil, nil
	rewritten, _ := json.MarshalIndent(map[string]any{antigravityHookKey: entry}, "", "    ")
	writeHooksJSON(t, dir, string(rewritten))

	if changed, err := InjectAntigravityHooks(dir); err != nil || changed {
		t.Errorf("inject after an Antigravity rewrite: changed=%v err=%v, want a no-op", changed, err)
	}
	if got := AntigravityHookState(dir); got != "installed" {
		t.Errorf("state = %q, want installed", got)
	}
}

// Editing any hook in /hooks writes back the copy that session loaded at
// startup, erasing an entry fleet installed afterwards. Repair puts it back.
func TestRepairAntigravityHooksRestoresAnErasedEntry(t *testing.T) {
	dir := t.TempDir()
	writeHooksJSON(t, dir, `{"my-linter":{"enabled":false,"Stop":null}}`)
	repaired, err := RepairAntigravityHooks(dir)
	if err != nil || !repaired {
		t.Fatalf("repair: repaired=%v err=%v", repaired, err)
	}
	keys := readHooksJSON(t, dir)
	if _, ok := keys[antigravityHookKey]; !ok {
		t.Fatal("fleet's entry was not restored")
	}
	if _, ok := keys["my-linter"]; !ok {
		t.Error("repair dropped the user's hook")
	}
	if again, _ := RepairAntigravityHooks(dir); again {
		t.Error("repair rewrote a current entry")
	}
}

// Switching fleet's hook off in /hooks is the user's decision. Neither a repair
// nor a reinstall may switch it back on.
func TestAntigravityHooksDisabledByTheUserStayDisabled(t *testing.T) {
	dir := t.TempDir()
	writeHooksJSON(t, dir, `{"`+antigravityHookKey+`":{"enabled":false,"Stop":[{"type":"command","command":"/gone/fleet hook-handler"}]}}`)
	if repaired, err := RepairAntigravityHooks(dir); err != nil || repaired {
		t.Errorf("repair of a disabled entry: repaired=%v err=%v, want a no-op", repaired, err)
	}
	if _, err := InjectAntigravityHooks(dir); err != nil {
		t.Fatal(err)
	}
	var entry antigravityFleetHooks
	_ = json.Unmarshal(readHooksJSON(t, dir)[antigravityHookKey], &entry)
	if entry.Enabled == nil || *entry.Enabled {
		t.Error("inject switched a user-disabled hook back on")
	}
	if got := AntigravityHookState(dir); got != "disabled" {
		t.Errorf("state = %q, want disabled", got)
	}
}

func TestRepairAntigravityHooksRepointsAMissingBinary(t *testing.T) {
	dir := t.TempDir()
	writeHooksJSON(t, dir, `{"`+antigravityHookKey+`":{"Stop":[{"type":"command","command":"`+
		strings.ReplaceAll(nonFleetSessionGuard, `"`, `\"`)+`'/gone/fleet' hook-handler --fleet-hook --agent antigravity --event Stop"}]}}`)
	if repaired, err := RepairAntigravityHooks(dir); err != nil || !repaired {
		t.Fatalf("repair: repaired=%v err=%v", repaired, err)
	}
	if strings.Contains(string(readHooksJSON(t, dir)[antigravityHookKey]), "/gone/fleet") {
		t.Error("repair kept the missing binary")
	}
}

// No ~/.gemini/config means no Antigravity here; repair must not create it.
func TestRepairAntigravityHooksNeverInstallsWithoutAntigravity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if repaired, err := RepairAntigravityHooks(dir); err != nil || repaired {
		t.Fatalf("repair = %v, %v; want a no-op", repaired, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("repair created Antigravity's config dir")
	}
}

// A file that doesn't parse is the user's half-finished edit; overwriting it
// would destroy their work.
func TestInjectAntigravityHooksRefusesAnUnparseableFile(t *testing.T) {
	dir := t.TempDir()
	broken := `{"my-linter": {`
	writeHooksJSON(t, dir, broken)
	if _, err := InjectAntigravityHooks(dir); err == nil {
		t.Fatal("inject accepted an unparseable hooks.json")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "hooks.json")); string(got) != broken {
		t.Errorf("unparseable file was rewritten: %q", got)
	}
}

// The schema rules are Antigravity's, not fleet's: a nested entry under a
// tool-less event makes it skip the whole file. Check the real loader agrees,
// when the binary is here to ask.
func TestAntigravityHooksPassTheRealValidator(t *testing.T) {
	agy, err := exec.LookPath("agy")
	if err != nil {
		t.Skip("agy not installed")
	}
	plugin := t.TempDir()
	if err := os.WriteFile(filepath.Join(plugin, "plugin.json"),
		[]byte(`{"name":"fleet-hooks-check","version":"0.0.1","description":"fleet test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InjectAntigravityHooks(plugin); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(agy, "plugin", "validate", plugin).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "hooks 1") {
		t.Fatalf("agy rejected fleet's hooks.json (err=%v):\n%s", err, out)
	}
}
