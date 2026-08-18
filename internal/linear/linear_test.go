package linear

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// stringLiteralsIn returns every string literal inside the named function, so a
// guard can assert on the argv a subprocess is built from.
func stringLiteralsIn(t *testing.T, file, fn string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var out strings.Builder
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		d, ok := n.(*ast.FuncDecl)
		if !ok || d.Name.Name != fn {
			return true
		}
		found = true
		ast.Inspect(d.Body, func(m ast.Node) bool {
			if lit, ok := m.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				out.WriteString(lit.Value + " ")
			}
			return true
		})
		return false
	})
	if !found {
		t.Fatalf("%s not found in %s — renamed? this guard is now vacuous", fn, file)
	}
	return out.String()
}

// TestMarkdownFetchNeverUsesJSON is the guard for the bug that cost the user
// every screenshot on every ticket. The CLI returns from its --json branch
// BEFORE its image downloader runs, so a JSON fetch can never produce images —
// it is structural, survives CLI upgrades, and is invisible (exit code 0).
func TestMarkdownFetchNeverUsesJSON(t *testing.T) {
	md := stringLiteralsIn(t, "cli.go", "fetchMarkdown")
	if strings.Contains(md, `"--json"`) || strings.Contains(md, `"-j"`) {
		t.Error("fetchMarkdown must NOT pass --json: the CLI returns from the JSON branch " +
			"before downloadIssueImages runs, so the images never reach disk and the agent " +
			"is handed 401 uploads.linear.app URLs instead")
	}
	if !strings.Contains(md, `"--no-pager"`) {
		t.Error("fetchMarkdown must pass --no-pager")
	}

	// Converse arm, so this can't pass by fetchMarkdown quietly losing its argv.
	if meta := stringLiteralsIn(t, "cli.go", "Fetch"); !strings.Contains(meta, `"--json"`) {
		t.Error("Fetch (metadata) must pass --json")
	}
}

// TestStateWriteNeverUsesIssueStart pins the other CLI trap: `linear issue
// start` moves the state AND creates its own git branch, which would collide
// with the worktree fleet just made.
func TestStateWriteNeverUsesIssueStart(t *testing.T) {
	lits := stringLiteralsIn(t, "cli.go", "MoveToStarted")
	if strings.Contains(lits, `"start"`) {
		t.Error("MoveToStarted must use `issue update -s started`, never `issue start` — " +
			"the latter also creates a branch and would collide with fleet's worktree")
	}
	if !strings.Contains(lits, `"update"`) || !strings.Contains(lits, `"started"`) {
		t.Errorf("MoveToStarted should run `issue update <id> -s started`, got literals: %s", lits)
	}
}

// TestSeedPromptTellsAgentNotToStart is the requirement the user stated
// directly: the agent must read and understand, not begin working.
func TestSeedPromptTellsAgentNotToStart(t *testing.T) {
	p := SeedPrompt(Result{
		Ticket: Ticket{Identifier: "BRZ-3182", Title: "Filter bar renders cramped", URL: "https://linear.app/x/BRZ-3182"},
		RelDir: ".fleet/ticket/BRZ-3182",
		Images: 3,
	})

	for _, want := range []string{
		"Do not start work yet",
		"Do not edit files, run builds, or begin implementing",
		"BRZ-3182",
		".fleet/ticket/BRZ-3182",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("seeded prompt is missing %q.\nA first message that merely describes a task "+
				"reads as an instruction to perform it, and the agent will start editing before "+
				"the human has read its understanding.\ngot:\n%s", want, p)
		}
	}

	if regexp.MustCompile(`(?i)\b(implement|fix|build|start working on) (it|this|the)\b`).MatchString(p) {
		t.Errorf("seeded prompt reads as an instruction to begin work:\n%s", p)
	}
}

// TestSeedPromptFirstLineCarriesIdentifier pins the interaction with the two
// surfaces that only see line one: the preview pane's prompt strip and
// naming.GenerateTitle, which cuts at ~50 runes.
func TestSeedPromptFirstLineCarriesIdentifier(t *testing.T) {
	p := SeedPrompt(Result{
		Ticket: Ticket{
			Identifier: "BRZ-3182",
			Title:      "Filter bar renders cramped on narrow viewports in the intent drawer",
		},
		RelDir: ".fleet/ticket/BRZ-3182",
	})
	first := strings.SplitN(p, "\n", 2)[0]
	r := []rune(first)
	if len(r) > 50 {
		r = r[:50]
	}
	if !strings.Contains(string(r), "BRZ-3182") {
		t.Errorf("identifier must survive a 50-rune cut of line 1, else every ticket session "+
			"gets a sidebar row that doesn't name its ticket.\nfirst 50: %q", string(r))
	}
}

// TestSeedPromptOmitsImagesWhenNoneDownloaded is honest degradation made
// executable: never point the agent at a directory that does not exist.
func TestSeedPromptOmitsImagesWhenNoneDownloaded(t *testing.T) {
	p := SeedPrompt(Result{
		Ticket: Ticket{Identifier: "BRZ-1", Title: "x"},
		RelDir: ".fleet/ticket/BRZ-1",
		Images: 0,
	})
	if strings.Contains(p, "images/") {
		t.Errorf("prompt points at images/ when none were downloaded:\n%s", p)
	}
}

func TestIdentifierFromBranch(t *testing.T) {
	cases := []struct {
		branch, team, want string
	}{
		{"brz-3182-magic-fix", "BRZ", "BRZ-3182"},
		{"BRZ-3182-Remove-streamer", "BRZ", "BRZ-3182"},
		{"alice/brz-1594-conversation-items", "BRZ", "BRZ-1594"},
		{"brz-3182", "BRZ", "BRZ-3182"},
		{"BRZ-3182", "brz", "BRZ-3182"},

		// The whole point of the team gate. The CLI's own branch parser has no
		// gate and reads these as identifiers for teams that don't exist.
		{"fix-123-something", "BRZ", ""},
		{"release-2024-cleanup", "BRZ", ""},
		{"eng-42-other-team", "BRZ", ""},

		// Real non-ticket branches from the user's tree.
		{"kinshasa", "BRZ", ""},
		{"frosty-mahavira", "BRZ", ""},
		{"brzctl-gcp-project-default", "BRZ", ""},
		{"master", "BRZ", ""},

		{"brz-3182-x", "", ""},
		{"", "BRZ", ""},
	}
	for _, c := range cases {
		if got := IdentifierFromBranch(c.branch, c.team); got != c.want {
			t.Errorf("IdentifierFromBranch(%q, %q) = %q, want %q", c.branch, c.team, got, c.want)
		}
	}
}

func TestLooksLikeIdentifier(t *testing.T) {
	cases := []struct {
		text, team, want string
		ok               bool
	}{
		{"BRZ-3182", "BRZ", "BRZ-3182", true},
		{"brz-3182", "BRZ", "BRZ-3182", true},
		{" BRZ-3182 ", "BRZ", "BRZ-3182", true},

		// Prose must NOT look like an identifier — this is what keeps the
		// suggestion list from ever stealing Enter from someone naming a branch.
		{"drawer", "BRZ", "", false},
		{"brz-3182-fix", "BRZ", "", false},
		{"fix-123", "BRZ", "", false},
		{"", "BRZ", "", false},
	}
	for _, c := range cases {
		got, ok := LooksLikeIdentifier(c.text, c.team)
		if got != c.want || ok != c.ok {
			t.Errorf("LooksLikeIdentifier(%q, %q) = (%q, %v), want (%q, %v)",
				c.text, c.team, got, ok, c.want, c.ok)
		}
	}
}

func TestBranchNameFor(t *testing.T) {
	cases := []struct{ id, title, want string }{
		{"BRZ-3182", "Filter bar renders cramped", "brz-3182-filter-bar-renders-cramped"},
		{"BRZ-1", "", "brz-1"},
		{"BRZ-1", "!!! ???", "brz-1"},
		{"BRZ-1", "Fix the API/SDK mismatch", "brz-1-fix-the-api-sdk-mismatch"},
		{"BRZ-1", "  spaced   out  ", "brz-1-spaced-out"},
	}
	for _, c := range cases {
		if got := BranchNameFor(c.id, c.title); got != c.want {
			t.Errorf("BranchNameFor(%q, %q) = %q, want %q", c.id, c.title, got, c.want)
		}
	}

	// No derived name may ever be rejected by the dialog that shows it.
	long := BranchNameFor("BRZ-3182", strings.Repeat("very long title segment ", 20))
	if len(long) > len("brz-3182-")+maxBranchSlug {
		t.Errorf("derived branch not capped: %q", long)
	}
	for _, bad := range []string{"..", "//", "@{", " "} {
		if strings.Contains(long, bad) {
			t.Errorf("derived branch %q contains %q, which git rejects", long, bad)
		}
	}
	if strings.HasSuffix(long, "-") || strings.HasPrefix(long, "-") {
		t.Errorf("derived branch has a dangling dash: %q", long)
	}
}

func TestDetectExtRecoversExtension(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 512)...)
	gif := append([]byte("GIF89a"), make([]byte, 512)...)
	html := []byte(`<!DOCTYPE html><html><body>401 Unauthorized</body></html>`)

	// The real case: the CLI writes "Filter bar renders cramped (screenshot)"
	// with no extension, and an agent's read tool dispatches on extension.
	if ext, ok := detectExt("Filter bar renders cramped (screenshot)", png); !ok || ext != ".png" {
		t.Errorf("extensionless PNG: got (%q, %v), want (.png, true)", ext, ok)
	}
	if ext, ok := detectExt("x.gif", gif); !ok || ext != ".gif" {
		t.Errorf("gif: got (%q, %v)", ext, ok)
	}
	if _, ok := detectExt("whatever", html); ok {
		t.Error("an HTML 401 body must be rejected, not saved beside real screenshots")
	}
	if got := http.DetectContentType(png); !strings.HasPrefix(got, "image/png") {
		t.Fatalf("sniffer precondition failed: %s", got)
	}
}

func TestFindImagesClassifiesLocalAndRemote(t *testing.T) {
	md := []byte("text\n" +
		"![shot](/var/folders/x/linear-cli-images/abc/image)\n" +
		"![other](https://uploads.linear.app/a/b/c)\n")
	refs := findImages(md)
	if len(refs) != 2 {
		t.Fatalf("found %d images, want 2", len(refs))
	}
	if refs[0].remote {
		t.Error("an absolute local path must not be classified remote")
	}
	if !refs[1].remote {
		t.Error("an uploads.linear.app URL must be classified remote — that is the " +
			"signal that the CLI's downloader failed and fleet should fetch it")
	}
}

func TestTeamKeyReadsOnlyTeamID(t *testing.T) {
	dir := t.TempDir()
	toml := "# linear cli\nworkspace = \"brizz\"\nteam_id = \"BRZ\"\napi_key = \"lin_api_SECRET\"\n"
	if err := os.WriteFile(filepath.Join(dir, configFile), []byte(toml), 0644); err != nil {
		t.Fatal(err)
	}
	key, ok := TeamKey(dir)
	if !ok || key != "BRZ" {
		t.Fatalf("TeamKey = (%q, %v), want (BRZ, true)", key, ok)
	}

	// fleet must never carry a credential; the key never leaves the file.
	if _, ok := TeamKey(t.TempDir()); ok {
		t.Error("a repo with no .linear.toml must report not-connected")
	}
}

func TestExistingPromptIsTheReuseLedger(t *testing.T) {
	wt := t.TempDir()
	if _, ok := ExistingPrompt(wt); ok {
		t.Error("empty worktree should have no prompt")
	}
	dir := TicketDir(wt, "BRZ-3182")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, promptFile), []byte("seeded"), 0644); err != nil {
		t.Fatal(err)
	}
	got, ok := ExistingPrompt(wt)
	if !ok || got != "seeded" {
		t.Errorf("ExistingPrompt = (%q, %v), want (seeded, true)", got, ok)
	}
}

func TestNegativePinStopsRefetch(t *testing.T) {
	wt := t.TempDir()
	if NegativelyPinned(wt, "FIX-123") {
		t.Error("nothing pinned yet")
	}
	pinNoTicket(wt, "FIX-123")
	if !NegativelyPinned(wt, "FIX-123") {
		t.Error("a branch that resolved to no-such-issue must cost one subprocess ever, not one per session")
	}
	if NegativelyPinned(wt, "BRZ-1") {
		t.Error("the pin must be identifier-specific")
	}
}

// TestDecodeTicketListAgainstV2Payload pins the JSON shape `linear issue query
// --json` actually returns on CLI v2.5.0.
//
// It is NOT a bare array: v2.0.0 changed the output to preserve GraphQL
// connection shapes, so it arrives as {"nodes": [...], "pageInfo": {...}}. A
// decoder written against the obvious guess returns nothing, silently, and the
// suggestion list simply never appears.
//
// The fixture mirrors a real response's structure — all 17 v2 keys per node, a
// nested state object — with invented content, because this repo is public and
// a captured payload would publish a workspace's roadmap.
func TestDecodeTicketListAgainstV2Payload(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "query_v2.json"))
	if err != nil {
		t.Skipf("no captured payload: %v", err)
	}
	got := decodeTicketList(data)
	if len(got) == 0 {
		t.Fatal("decoded no tickets from a v2-shaped query payload")
	}
	for _, ti := range got {
		if ti.Identifier == "" || ti.Title == "" {
			t.Errorf("incomplete ticket decoded: %+v", ti)
		}
	}
	if got[0].StateName == "" {
		t.Error("state.name did not decode — the suggestion rows would show no state")
	}
	t.Logf("decoded %d tickets, first = %s %q (%s)", len(got), got[0].Identifier, got[0].Title, got[0].StateName)
}
