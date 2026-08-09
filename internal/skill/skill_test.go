package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points every skill root at a temp dir, so a test never reads or
// writes the developer's real ~/.claude or ~/.agents.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("OPENCODE_CONFIG_DIR", filepath.Join(home, ".config", "opencode"))
	return home
}

func agentsNamed(t *testing.T, names ...string) []Agent {
	t.Helper()
	var out []Agent
	for _, n := range names {
		a, ok := Find(n)
		if !ok {
			t.Fatalf("unknown agent %q", n)
		}
		out = append(out, a)
	}
	return out
}

func resultFor(results []Result, agent string) Result {
	for _, r := range results {
		if r.Agent == agent {
			return r
		}
	}
	return Result{}
}

// The frontmatter is the contract every agent reads. Only `name` and
// `description` are accepted by all four targets (Claude Code's packaging
// validator rejects extra keys outright), and the name must equal the directory
// fleet writes into or the skill silently never loads.
func TestFrontmatterIsPortable(t *testing.T) {
	text := string(Content)
	if !strings.HasPrefix(text, "---\n") {
		t.Fatal("SKILL.md must open with YAML frontmatter")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		t.Fatal("SKILL.md frontmatter is unterminated")
	}
	front := text[4 : 4+end]

	keys := map[string]string{}
	for line := range strings.SplitSeq(front, "\n") {
		if line == "" || strings.HasPrefix(line, " ") {
			continue // continuation of a folded value
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("unparseable frontmatter line: %q", line)
		}
		keys[k] = strings.TrimSpace(v)
	}

	for k := range keys {
		if k != "name" && k != "description" {
			t.Errorf("frontmatter key %q is not accepted by every target agent; keep it to name + description", k)
		}
	}
	if keys["name"] != Name {
		t.Errorf("frontmatter name = %q, want %q (must match the install directory)", keys["name"], Name)
	}
	if keys["description"] == "" {
		t.Error("frontmatter description is empty; it is the only thing agents see before loading the skill")
	}
	if len(keys["description"]) > 1024 {
		t.Errorf("description is %d chars, over the 1024 limit", len(keys["description"]))
	}
}

// The skill's whole purpose is teaching the CLI commands, so a command being
// renamed without the skill following is a silent regression.
func TestSkillCoversTheCLISurface(t *testing.T) {
	text := string(Content)
	for _, want := range []string{"fleet wt", "fleet send", "fleet list", "-p -", "-force"} {
		if !strings.Contains(text, want) {
			t.Errorf("SKILL.md never mentions %q", want)
		}
	}
}

func TestInstallWritesOncePerSharedRoot(t *testing.T) {
	isolate(t)

	// codex, cursor and opencode all read ~/.agents/skills, so they resolve to
	// one file rather than three racing writes to the same path.
	results := Install(agentsNamed(t, "claude", "codex", "cursor", "opencode"))

	claudePath := resultFor(results, "claude").Path
	agentsPath := resultFor(results, "codex").Path
	if claudePath == agentsPath {
		t.Fatalf("claude and codex must not share a path, got %q", claudePath)
	}
	for _, name := range []string{"cursor", "opencode"} {
		if got := resultFor(results, name).Path; got != agentsPath {
			t.Errorf("%s path = %q, want the shared %q", name, got, agentsPath)
		}
	}
	for _, r := range results {
		if r.Outcome != Written {
			t.Errorf("%s outcome = %q, want written", r.Agent, r.Outcome)
		}
	}

	for _, path := range []string{claudePath, agentsPath} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if string(got) != string(Content) {
			t.Errorf("%s content does not match the embedded skill", path)
		}
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	isolate(t)
	sel := agentsNamed(t, "claude")

	if got := resultFor(Install(sel), "claude").Outcome; got != Written {
		t.Fatalf("first install outcome = %q, want written", got)
	}
	if got := resultFor(Install(sel), "claude").Outcome; got != Unchanged {
		t.Errorf("second install outcome = %q, want unchanged", got)
	}
}

func TestUnselectedAgentsAreSkippedNotWritten(t *testing.T) {
	isolate(t)

	results := Install(agentsNamed(t, "claude"))
	codex := resultFor(results, "codex")
	if codex.Outcome != Skipped {
		t.Errorf("codex outcome = %q, want skipped", codex.Outcome)
	}
	if _, err := os.Stat(codex.Path); err == nil {
		t.Errorf("codex skill was written at %s despite not being selected", codex.Path)
	}
}

func TestStatusReportsOutdatedAndAbsent(t *testing.T) {
	isolate(t)

	if got := resultFor(Status(), "claude").Outcome; got != Absent {
		t.Errorf("outcome before install = %q, want absent", got)
	}

	Install(agentsNamed(t, "claude"))
	if got := resultFor(Status(), "claude").Outcome; got != Installed {
		t.Errorf("outcome after install = %q, want installed", got)
	}

	// A copy from an older fleet must read as outdated, not as installed —
	// that's the whole signal telling the user to reinstall.
	path := resultFor(Status(), "claude").Path
	if err := os.WriteFile(path, []byte("---\nname: fleet\n---\nstale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resultFor(Status(), "claude").Outcome; got != Outdated {
		t.Errorf("outcome after tampering = %q, want outdated", got)
	}
}

// Status covers every agent regardless of detection: a skill left behind by an
// agent the user has since uninstalled is precisely what `status` should find.
func TestStatusIgnoresDetection(t *testing.T) {
	isolate(t)
	Install(agentsNamed(t, "codex"))

	if got := len(Status()); got != len(Agents()) {
		t.Fatalf("Status returned %d results, want one per agent (%d)", got, len(Agents()))
	}
	if got := resultFor(Status(), "codex").Outcome; got != Installed {
		t.Errorf("codex status = %q, want installed", got)
	}
}

func TestUninstallRemovesFileAndDir(t *testing.T) {
	isolate(t)
	sel := agentsNamed(t, "claude")
	path := resultFor(Install(sel), "claude").Path

	if got := resultFor(Uninstall(sel), "claude").Outcome; got != Removed {
		t.Errorf("uninstall outcome = %q, want removed", got)
	}
	if _, err := os.Stat(path); err == nil {
		t.Errorf("%s still exists after uninstall", path)
	}
	if _, err := os.Stat(filepath.Dir(path)); err == nil {
		t.Errorf("empty skill dir %s was left behind", filepath.Dir(path))
	}
	if got := resultFor(Uninstall(sel), "claude").Outcome; got != Absent {
		t.Errorf("second uninstall outcome = %q, want absent", got)
	}
}

// Uninstall must leave a directory the user put other files in, rather than
// deleting work fleet did not create.
func TestUninstallKeepsNonEmptyDir(t *testing.T) {
	isolate(t)
	sel := agentsNamed(t, "claude")
	path := resultFor(Install(sel), "claude").Path
	dir := filepath.Dir(path)
	keep := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	Uninstall(sel)
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("uninstall removed a file fleet did not write: %v", err)
	}
}

func TestAnyInstalled(t *testing.T) {
	isolate(t)
	if AnyInstalled() {
		t.Fatal("AnyInstalled = true on a clean home")
	}
	Install(agentsNamed(t, "opencode"))
	if !AnyInstalled() {
		t.Error("AnyInstalled = false after installing for opencode")
	}
}

// Detection must not depend on the binary alone: Cursor installs its `cursor`
// shell command only when asked, so the config dir is the signal that catches
// those users.
func TestDetectedFindsAgentByConfigDir(t *testing.T) {
	home := isolate(t)
	t.Setenv("PATH", t.TempDir()) // no agent binaries reachable

	if len(Detected()) != 0 {
		t.Fatalf("Detected = %v with no binaries and no config dirs", Detected())
	}
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	found := Detected()
	if len(found) != 1 || found[0].Name != "cursor" {
		t.Errorf("Detected = %v, want just cursor", found)
	}
}
