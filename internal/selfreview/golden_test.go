package selfreview

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fixture is a realistic run modelled on a Scanbox SC collector change: a
// schedule move, a product call about closures, a 403 split, a cursor bug, a
// migration, a dependency bump, a lockfile, a test and a generated fixture.
type fixture struct {
	Round       int               `json:"round"`
	Title       string            `json:"title"`
	Prompts     []string          `json:"prompts"`
	Claims      string            `json:"claims"`
	CommitLog   string            `json:"commit_log"`
	CI          CIInfo            `json:"ci"`
	ReviewMD    string            `json:"review_md"`
	RuleSkip    map[string]string `json:"rule_skip"`
	ExtraFiles  []FileInput       `json:"extra_files"`
	TreeSHA     string            `json:"tree_sha"`
	Answers     []Answer          `json:"answers"`
	alwaysHuman []string
}

func loadFixture(t *testing.T, name string) (fixture, Input) {
	t.Helper()
	dir := filepath.Join("testdata", name)
	var fx fixture
	readJSON(t, filepath.Join(dir, "input.json"), &fx)
	patch, err := os.ReadFile(filepath.Join(dir, "diff.patch"))
	if err != nil {
		t.Fatal(err)
	}
	rules, alwaysHuman := ParseReviewRules(fx.ReviewMD)
	fx.alwaysHuman = alwaysHuman

	files := append(splitGitDiff(string(patch)), fx.ExtraFiles...)
	for i := range files {
		if r, ok := fx.RuleSkip[files[i].Path]; ok {
			files[i].RuleSkip = r
		}
		files[i].HighRisk = HighRiskPath(files[i].Path, alwaysHuman)
	}
	return fx, Input{
		Round:       fx.Round,
		Title:       fx.Title,
		Prompts:     fx.Prompts,
		Claims:      fx.Claims,
		Files:       files,
		CommitLog:   fx.CommitLog,
		CI:          fx.CI,
		ReviewRules: rules,
	}
}

// splitGitDiff is the test's stand-in for internal/git's DiffFiles.
func splitGitDiff(s string) []FileInput {
	var out []FileInput
	for _, chunk := range strings.Split(s, "diff --git ")[1:] {
		var f FileInput
		f.Status = "M"
		lines := strings.Split(chunk, "\n")
		hunkAt := -1
		for i, l := range lines {
			switch {
			case strings.HasPrefix(l, "new file mode"):
				f.Status = "A"
			case strings.HasPrefix(l, "deleted file mode"):
				f.Status = "D"
			case strings.HasPrefix(l, "+++ b/"):
				f.Path = strings.TrimPrefix(l, "+++ b/")
			case strings.HasPrefix(l, "--- a/") && f.Path == "":
				f.Path = strings.TrimPrefix(l, "--- a/")
			case strings.HasPrefix(l, "@@"):
				if hunkAt < 0 {
					hunkAt = i
				}
			}
			if hunkAt >= 0 && i > hunkAt-1 && len(l) > 0 {
				if l[0] == '+' {
					f.Additions++
				} else if l[0] == '-' {
					f.Deletions++
				}
			}
		}
		if hunkAt >= 0 {
			f.Patch = strings.Join(lines[hunkAt:], "\n")
		}
		out = append(out, f)
	}
	return out
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func golden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from golden (run go test -update to accept):\n--- got ---\n%s", path, got)
	}
}

func TestGoldenScanbox(t *testing.T) {
	fx, in := loadFixture(t, "scanbox")
	dir := filepath.Join("testdata", "scanbox")

	instruction, stdin := BuildPrompt(in)
	golden(t, filepath.Join(dir, "instruction.golden.txt"), instruction)
	golden(t, filepath.Join(dir, "stdin.golden.txt"), stdin)

	raw, err := os.ReadFile(filepath.Join(dir, "model_output.json"))
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	warnings := Enforce(run, in)
	enforced, _ := json.MarshalIndent(run, "", "  ")
	golden(t, filepath.Join(dir, "enforced.golden.json"), string(enforced)+"\n")
	golden(t, filepath.Join(dir, "warnings.golden.txt"), strings.Join(warnings, "\n")+"\n")
	golden(t, filepath.Join(dir, "message.golden.txt"), ComposeMessage(run, fx.Answers, fx.Round, fx.TreeSHA)+"\n")
	golden(t, filepath.Join(dir, "round.golden.md"), ComposeRoundMarkdown(run, fx.Answers, fx.Round, fx.TreeSHA))

	// The properties the goldens encode, stated so a careless -update can't
	// quietly accept a regression.
	// Eight stops, one past MaxStops: all kept, none hidden.
	if len(run.Stops) != MaxStops+1 || run.MoreStops != 0 {
		t.Errorf("stops = %d (+%d more), want %d (+0 more)", len(run.Stops), run.MoreStops, MaxStops+1)
	}
	if !contains(run.NotCovered, "go.mod") {
		t.Errorf("go.mod (high-risk, skipped by guide) not in Not covered: %v", run.NotCovered)
	}
	for _, s := range run.Skipped {
		if s.Path == "go.sum" && (s.By != "rule" || s.Reason != "lockfile") {
			t.Errorf("go.sum skip = %+v, want by rule / lockfile", s)
		}
		if s.Path == "go.mod" {
			t.Error("high-risk go.mod stayed in skipped")
		}
	}
	if run.TotalLines != run.StopLines+run.SkippedLines+run.UncoveredLines {
		t.Errorf("line counts do not add up: %d != %d+%d+%d", run.TotalLines, run.StopLines, run.SkippedLines, run.UncoveredLines)
	}
	msg := ComposeMessage(run, fx.Answers, fx.Round, fx.TreeSHA)
	for _, s := range run.Stops {
		if s.Why != "" && strings.Contains(msg, s.Why) {
			t.Errorf("message carries model prose (why of %s)", s.ID)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
