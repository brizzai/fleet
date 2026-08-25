package ticket

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// TestSeedPromptTellsAgentNotToStart is the requirement the user stated
// directly: the agent must read and understand, not begin working.
func TestSeedPromptTellsAgentNotToStart(t *testing.T) {
	p := SeedPrompt("Linear", Result{
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
	p := SeedPrompt("Linear", Result{
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
	p := SeedPrompt("Linear", Result{
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
	if ext, ok := detectExt(png); !ok || ext != ".png" {
		t.Errorf("extensionless PNG: got (%q, %v), want (.png, true)", ext, ok)
	}
	if ext, ok := detectExt(gif); !ok || ext != ".gif" {
		t.Errorf("gif: got (%q, %v)", ext, ok)
	}
	if _, ok := detectExt(html); ok {
		t.Error("an HTML 401 body must be rejected, not saved beside real screenshots")
	}

	// A filename cannot vouch for bytes. This is the whole reason detectExt
	// stopped taking one: for Jira the alt IS the filename, so a name-first
	// rule meant the sniff never ran and an HTML interstitial served for
	// screenshot.png landed on disk as a .png.
	if _, ok := detectExt([]byte("<!DOCTYPE html><html>nope</html>")); ok {
		t.Error("HTML must be rejected however convincingly it is named")
	}
	if got := http.DetectContentType(png); !strings.HasPrefix(got, "image/png") {
		t.Fatalf("sniffer precondition failed: %s", got)
	}
}
