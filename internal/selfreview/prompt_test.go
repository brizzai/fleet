package selfreview

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func bigPatch(n int) string {
	return "@@ -1,0 +1,1 @@\n+" + strings.Repeat("x", n) + "\n"
}

func TestBuildPromptDiffBudget(t *testing.T) {
	in := Input{Round: 1, Files: []FileInput{
		{Path: "huge.go", Additions: 1, Patch: bigPatch(DiffBudgetChars - 100)},
		{Path: "auth/login.go", Additions: 1, HighRisk: true, Patch: bigPatch(500)},
		{Path: "small.go", Additions: 1, Patch: bigPatch(10)},
		{Path: "go.sum", Additions: 1, RuleSkip: "lockfile", Patch: bigPatch(10)},
	}}
	_, stdin := BuildPrompt(in)
	diff := stdin[strings.Index(stdin, "=== DIFF ===")-1:]
	if !strings.Contains(diff, "--- file: auth/login.go") {
		t.Error("a high-risk file lost its patch to the budget")
	}
	if strings.Contains(diff, "--- file: huge.go") {
		t.Error("a file over the remaining budget was sent whole")
	}
	if !strings.Contains(stdin, "huge.go +1 -0 [diff omitted: over budget; Read it]") {
		t.Error("an over-budget file is not listed as stats")
	}
	if !strings.Contains(diff, "--- file: small.go") {
		t.Error("a small file after an over-budget one was dropped")
	}
	if strings.Contains(diff, "--- file: go.sum") {
		t.Error("a rule-skipped file sent its patch")
	}
	if len(diff) > DiffBudgetChars+2_000 {
		t.Errorf("diff section is %d chars, over budget", len(diff))
	}
}

func TestBuildPromptInputBudgets(t *testing.T) {
	var prompts []string
	for i := 0; i < 10; i++ {
		prompts = append(prompts, strings.Repeat("p", PromptMaxChars*2))
	}
	in := Input{Round: 1, Prompts: prompts, Claims: strings.Repeat("c", 10_000),
		ReviewRules: strings.Repeat("r", 10_000), CommitLog: strings.Repeat("l", 10_000)}
	_, stdin := BuildPrompt(in)
	if n := strings.Count(stdin, "[prompt "); n != MaxPrompts {
		t.Errorf("%d prompts sent, want %d", n, MaxPrompts)
	}
	if !strings.Contains(stdin, "[prompt 1]") || !strings.Contains(stdin, "[prompt 10]") {
		t.Error("want the first prompt and the latest ones")
	}
	for _, c := range []struct {
		ch  string
		max int
	}{{"p", PromptMaxChars}, {"c", ClaimsMaxChars}, {"r", RulesMaxChars}, {"l", CommitLogMaxChars}} {
		run := strings.Repeat(c.ch, c.max)
		if strings.Contains(stdin, run) {
			t.Errorf("%q block exceeds its %d-char budget", c.ch, c.max)
		}
	}
	if !strings.Contains(stdin, "=== CLAIMS (the agent's own summary; not evidence) ===") {
		t.Error("claims are not labelled as claims")
	}
}

// argv is world-readable; nothing but fleet's own text may reach it.
func TestInstructionCarriesNoInput(t *testing.T) {
	secret := "SECRET-diff-content-42"
	in := Input{Round: 2, Title: secret, Prompts: []string{secret}, Claims: secret, Ticket: secret,
		ReviewRules: secret, CommitLog: secret, Files: []FileInput{{Path: secret, Patch: "+" + secret}},
		Previous: &Run{Verdict: secret}}
	instruction, stdin := BuildPrompt(in)
	if strings.Contains(instruction, secret) {
		t.Fatal("input leaked into the argv instruction")
	}
	if !strings.Contains(stdin, secret) {
		t.Fatal("input missing from stdin")
	}
}

func TestInstructionShapeByRound(t *testing.T) {
	r1, _ := BuildPrompt(Input{Round: 1})
	r2, _ := BuildPrompt(Input{Round: 2})
	r3, _ := BuildPrompt(Input{Round: 3})
	if strings.Contains(r1, `"prev"`) || strings.Contains(r1, "17. Round") {
		t.Error("round 1 carries the round-2 rule")
	}
	if !strings.Contains(r2, `"prev"`) || !strings.Contains(r2, "Never re-ask") {
		t.Error("round 2 lacks rule 11")
	}
	if !strings.Contains(r3, "final round") {
		t.Error("round 3 does not say it is the last")
	}
	if !strings.Contains(r1, "at most 18 words — count them") || !strings.Contains(r1, `Starts with "Ready"`) {
		t.Error("the verdict rule is missing")
	}
	if !strings.Contains(r1, `ONE yes/no question, at most 15 words`) || !strings.Contains(r1, `"yes" means keep the code as it is`) {
		t.Error("the decide rule is missing")
	}
	if !strings.Contains(r2, "Every stop still has its why") {
		t.Error("round 2 does not keep the why")
	}
	if !strings.Contains(r1, `"Covered by tests" is never a reason to skip a logic change`) ||
		!strings.Contains(r1, `"kind":"mechanical|docs|fixture|config|generated"`) {
		t.Error("the skip-kind rule or schema is missing")
	}
	if !strings.Contains(r1, `"recommend":{"answer":"keep|change"`) || !strings.Contains(r1, "comparable code in this repo") ||
		!strings.Contains(r1, `"chapters":[`) || !strings.Contains(r1, `kind "scope" first`) {
		t.Error("the recommend, elsewhere, scope or chapters rule is missing")
	}
	if !strings.Contains(r1, `never "a passing limit"`) {
		t.Error("the plain-words rule is missing")
	}
	// Short and strict: the instruction is rules, not an essay. v2 added
	// the walkthrough and the recommendation, which is most of its length.
	if n := len(strings.Fields(r2)); n > 860 {
		t.Errorf("instruction is %d words; keep it short", n)
	}
}

func TestBuildPromptStripsEscapes(t *testing.T) {
	_, stdin := BuildPrompt(Input{Claims: "done \x1b]0;pwned\x07 \x1b[2J ok", Files: []FileInput{{Path: "a\x1b[31m.go"}}})
	if strings.ContainsRune(stdin, 0x1b) {
		t.Error("escape sequence reached stdin")
	}
	if !utf8.ValidString(stdin) {
		t.Error("stdin is not valid UTF-8")
	}
}
