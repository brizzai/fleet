package linear

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestNoLinearSubprocess is the point of the move off the CLI, pinned.
//
// An earlier version of this package shelled out to `linear`, which cost three
// version-skew bugs in one session: a release compiled without network access to
// uploads.linear.app that failed every image download and still exited 0, an
// `auth login` command that did not exist in the installed version, and an error
// message naming a `configure` command that never existed at all. None of that
// can come back while this holds.
//
// The guard is an allowlist rather than a ban, because the package legitimately
// runs three OS helpers — two keychains and a browser opener. An allowlist fails
// on anything new, which is the property that matters: adding a subprocess here
// should require saying so out loud.
func TestNoLinearSubprocess(t *testing.T) {
	allowed := map[string]string{
		"security":    "macOS keychain",
		"secret-tool": "libsecret keychain",
		"open":        "browser, macOS",
		"xdg-open":    "browser, Linux",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	scanned := 0

	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "exec" {
				return true
			}
			if sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext" && sel.Sel.Name != "LookPath" {
				return true
			}
			// The binary is the first string-literal argument, after the ctx
			// that CommandContext takes.
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				bin := strings.Trim(lit.Value, `"`)
				if _, allow := allowed[bin]; !allow {
					t.Errorf("%s runs %q — the data path is HTTP now, on purpose. "+
						"If this is a genuinely new OS helper, add it to the allowlist above.", name, bin)
				}
				return false
			}
			return false
		})
	}
	if scanned == 0 {
		t.Fatal("scanned no files — this guard is vacuous")
	}
}

// TestStartedStateResolvesByTypeAndPosition pins how the one mutation picks a
// state.
//
// Matching on TYPE rather than name is what makes this work on a team whose
// started state is called "In Dev" or "Doing". Position ordering matters just as
// much: a real team has several started states, and the lowest position is the
// one a human means by "I am starting this" — picking any other would move a
// fresh ticket straight to In Review.
func TestStartedStateResolvesByTypeAndPosition(t *testing.T) {
	issue := &issueFull{Team: &issueTeam{}}
	issue.Team.States.Nodes = []workflowState{
		{ID: "d", Name: "Done", Type: "completed", Position: 3},
		{ID: "r", Name: "In Review", Type: "started", Position: 1002},
		{ID: "p", Name: "In Dev", Type: "started", Position: 2},
		{ID: "b", Name: "Backlog", Type: "backlog", Position: 0},
	}
	got, ok := issue.startedState()
	if !ok || got.ID != "p" {
		t.Fatalf("startedState = (%+v, %v), want the lowest-position started state (In Dev)", got, ok)
	}

	// A team with no started state at all is a team fleet has nothing to say
	// about, not an error.
	issue.Team.States.Nodes = []workflowState{{ID: "b", Name: "Backlog", Type: "backlog"}}
	if _, ok := issue.startedState(); ok {
		t.Error("a team with no started state must report none")
	}
}

// TestGraphQLErrorClassification pins the shapes the live API actually returns.
//
// Captured from api.linear.app rather than guessed, because the obvious guess is
// wrong in the case that matters most: an unknown issue comes back as HTTP 200
// with an errors[] entry whose own extensions carry statusCode 400. Classifying
// on the HTTP status alone would report "no such issue" as a generic failure and
// break the negative pin that stops fleet re-asking on every session start.
func TestGraphQLErrorClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		errs   []gqlErrorEntry
		want   error
	}{
		{"ok", 200, nil, nil},
		{"unknown issue is a 200", 200,
			[]gqlErrorEntry{{Message: "Entity not found: Issue"}}, ErrNotFound},
		{"rejected credential", 401,
			[]gqlErrorEntry{{Message: "Authentication required, not authenticated"}}, ErrNotAuthenticated},
		{"auth error code without a 401", 200,
			[]gqlErrorEntry{{Message: "nope", Extensions: struct {
				Type string `json:"type"`
				Code string `json:"code"`
			}{Code: "AUTHENTICATION_ERROR"}}}, ErrNotAuthenticated},
		{"forbidden", 403, nil, ErrNotAuthenticated},
	}
	for _, c := range cases {
		got := classifyGraphQL(c.status, c.errs)
		if got != c.want {
			t.Errorf("%s: classifyGraphQL = %v, want %v", c.name, got, c.want)
		}
	}

	// An unrecognised error must still be an error, not a silent success.
	if err := classifyGraphQL(200, []gqlErrorEntry{{Message: "Query too complex"}}); err == nil {
		t.Error("an unclassified errors[] entry must not read as success")
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
	brz := []string{"BRZ"}
	cases := []struct {
		branch string
		teams  []string
		want   string
	}{
		{"brz-3182-magic-fix", brz, "BRZ-3182"},
		{"BRZ-3182-Remove-streamer", brz, "BRZ-3182"},
		{"alice/brz-1594-conversation-items", brz, "BRZ-1594"},
		{"brz-3182", brz, "BRZ-3182"},
		{"BRZ-3182", []string{"brz"}, "BRZ-3182"},

		// A repo may track more than one team — a workspace routinely has
		// several, and both must resolve.
		{"prd-7-spec", []string{"BRZ", "PRD"}, "PRD-7"},
		{"brz-9-x", []string{"BRZ", "PRD"}, "BRZ-9"},

		// The whole point of the team gate. An ungated parser reads these as
		// identifiers for teams that don't exist.
		{"fix-123-something", brz, ""},
		{"release-2024-cleanup", brz, ""},
		{"eng-42-other-team", brz, ""},

		// Real non-ticket branches from the user's tree.
		{"kinshasa", brz, ""},
		{"frosty-mahavira", brz, ""},
		{"brzctl-gcp-project-default", brz, ""},
		{"master", brz, ""},

		{"brz-3182-x", nil, ""},
		{"", brz, ""},
	}
	for _, c := range cases {
		if got := IdentifierFromBranch(c.branch, c.teams); got != c.want {
			t.Errorf("IdentifierFromBranch(%q, %v) = %q, want %q", c.branch, c.teams, got, c.want)
		}
	}
}

func TestLooksLikeIdentifier(t *testing.T) {
	brz := []string{"BRZ"}
	cases := []struct {
		text  string
		teams []string
		want  string
		ok    bool
	}{
		{"BRZ-3182", brz, "BRZ-3182", true},
		{"brz-3182", brz, "BRZ-3182", true},
		{" BRZ-3182 ", brz, "BRZ-3182", true},
		{"prd-7", []string{"BRZ", "PRD"}, "PRD-7", true},

		// Prose must NOT look like an identifier — this is what keeps the
		// suggestion list from ever stealing Enter from someone naming a branch.
		{"drawer", brz, "", false},
		{"brz-3182-fix", brz, "", false},
		{"fix-123", brz, "", false},
		{"", brz, "", false},
		{"BRZ-3182", nil, "", false},
	}
	for _, c := range cases {
		got, ok := LooksLikeIdentifier(c.text, c.teams)
		if got != c.want || ok != c.ok {
			t.Errorf("LooksLikeIdentifier(%q, %v) = (%q, %v), want (%q, %v)",
				c.text, c.teams, got, ok, c.want, c.ok)
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

// TestFindImagesTakesOnlyRemoteLinks pins what fleet is willing to go and fetch.
//
// Linear's markdown carries absolute uploads.linear.app URLs; anything else in a
// description — a relative path, a data URI, a link to someone's laptop — is not
// something fleet has any business reading off the filesystem and copying into a
// worktree.
func TestFindImagesTakesOnlyRemoteLinks(t *testing.T) {
	md := []byte("text\n" +
		"![shot](/etc/passwd)\n" +
		"![rel](../../secrets.png)\n" +
		"![inline](data:image/png;base64,AAAA)\n" +
		"![other](https://uploads.linear.app/a/b/c)\n")
	refs := findImages(md)
	if len(refs) != 1 {
		t.Fatalf("found %d images, want only the remote one: %+v", len(refs), refs)
	}
	if refs[0].target != "https://uploads.linear.app/a/b/c" {
		t.Errorf("kept the wrong link: %q", refs[0].target)
	}
}

func TestTeamKeysReadOnlyTeamID(t *testing.T) {
	dir := t.TempDir()
	toml := "# linear cli\nworkspace = \"brizz\"\nteam_id = \"BRZ\"\napi_key = \"lin_api_SECRET\"\n"
	if err := os.WriteFile(filepath.Join(dir, linearConfigFile), []byte(toml), 0644); err != nil {
		t.Fatal(err)
	}
	got := TeamKeys(dir)
	if len(got) != 1 || got[0] != "BRZ" {
		t.Fatalf("TeamKeys = %v, want [BRZ]", got)
	}

	// fleet resolves its own credential. An api_key sitting in another tool's
	// config is none of its business, and must never be adopted.
	if data, err := os.ReadFile(filepath.Join(dir, linearConfigFile)); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(data), "lin_api_SECRET") {
		t.Fatal("precondition: the fixture should contain an api_key to ignore")
	}

	// A repo naming no team is the resting state — nil, not an error, and it is
	// what keeps an unrelated repo silent for a connected user.
	if got := TeamKeys(t.TempDir()); got != nil {
		t.Errorf("a repo with no Linear config must report no teams, got %v", got)
	}

	// .fleet.json wins over .linear.toml: it is fleet's own config, and it is
	// the only form available to someone who never installed the other tool.
	if err := os.WriteFile(filepath.Join(dir, ".fleet.json"),
		[]byte(`{"linear":{"teams":["prd","inf"]}}`), 0644); err != nil {
		t.Fatal(err)
	}
	got = TeamKeys(dir)
	if len(got) != 2 || got[0] != "PRD" || got[1] != "INF" {
		t.Fatalf("TeamKeys = %v, want [PRD INF] upper-cased from .fleet.json", got)
	}
}

// TestCredentialResolutionOrder pins that the environment outranks the store.
//
// That order is what lets a stale or wrong stored credential be overridden
// without any UI, and it is the only path that works in CI, where there is no
// keychain to read.
func TestCredentialResolutionOrder(t *testing.T) {
	orig := getenv
	t.Cleanup(func() { getenv = orig; resetCredentialForTest() })

	getenv = func(k string) string {
		if k == APIKeyEnvVar {
			return "lin_api_fromEnv"
		}
		return ""
	}
	resetCredentialForTest()
	if !Available() {
		t.Fatal("an environment key must make Linear available without touching the keychain")
	}
	c, err := credential()
	if err != nil || c.Token != "lin_api_fromEnv" || c.Kind != credAPIKey {
		t.Fatalf("credential = (%+v, %v), want the environment key", c, err)
	}
	if got := ConnectedVia(); !strings.Contains(got, APIKeyEnvVar) {
		t.Errorf("ConnectedVia = %q, should name the environment so the dialog can say it is not disconnectable from here", got)
	}

	getenv = func(string) string { return "" }
	resetCredentialForTest()
	if _, err := credential(); err != ErrNotConnected {
		t.Errorf("with no environment key and nothing stored, credential must be ErrNotConnected, got %v", err)
	}
}

// TestAuthHeaderFormDiffersByKind pins the one place the two credential kinds
// diverge. A personal API key is sent raw; an OAuth token takes Bearer. Sending
// either in the other form reads as a rejected credential.
func TestAuthHeaderFormDiffersByKind(t *testing.T) {
	if got := (Credential{Kind: credAPIKey, Token: "k"}).authHeader(); got != "k" {
		t.Errorf("api key header = %q, want the raw token", got)
	}
	if got := (Credential{Kind: credOAuth, Token: "k"}).authHeader(); got != "Bearer k" {
		t.Errorf("oauth header = %q, want Bearer", got)
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

// resetCredentialForTest drops the cached credential so a test can re-resolve.
func resetCredentialForTest() {
	credState.mu.Lock()
	credState.cred = Credential{}
	credState.loaded = false
	credState.warmed.Store(false)
	credState.present.Store(false)
	credState.mu.Unlock()
}

// TestAssignedIssuesOrderPutsWorkInHand pins the ordering of the tickets tab.
//
// Ranking on state TYPE is what makes it work on a team that renamed its
// states. Position is what separates two states of the SAME type, and it is the
// difference between "In Progress" and "In Review" leading the list — a work
// queue wants the thing you are in the middle of, not the thing you already
// handed off.
func TestAssignedIssuesOrderPutsWorkInHand(t *testing.T) {
	mk := func(id, name, typ string, pos float64) issueLite {
		n := issueLite{Identifier: id, Title: id}
		n.State = &struct {
			Name     string  `json:"name"`
			Type     string  `json:"type"`
			Position float64 `json:"position"`
		}{Name: name, Type: typ, Position: pos}
		return n
	}
	nodes := []issueLite{
		mk("BRZ-4", "Backlog", "backlog", 0),
		mk("BRZ-3", "Todo", "unstarted", 1),
		mk("BRZ-2", "In Review", "started", 1002),
		mk("BRZ-1", "In Dev", "started", 2),
	}
	sort.SliceStable(nodes, func(a, b int) bool {
		ra, rb := stateTypeRank(nodes[a].stateType()), stateTypeRank(nodes[b].stateType())
		if ra != rb {
			return ra < rb
		}
		return nodes[a].statePosition() < nodes[b].statePosition()
	})

	var got []string
	for _, n := range nodes {
		got = append(got, n.Identifier)
	}
	want := []string{"BRZ-1", "BRZ-2", "BRZ-3", "BRZ-4"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v (started by position, then todo, then backlog)", got, want)
		}
	}

	// A payload with no state must sort last rather than lead.
	nodes = append(nodes, issueLite{Identifier: "BRZ-9"})
	sort.SliceStable(nodes, func(a, b int) bool {
		return stateTypeRank(nodes[a].stateType()) < stateTypeRank(nodes[b].stateType())
	})
	if nodes[len(nodes)-1].Identifier != "BRZ-9" {
		t.Errorf("a stateless issue should sort last, got order ending in %s", nodes[len(nodes)-1].Identifier)
	}
}
