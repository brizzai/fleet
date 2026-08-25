package ticketing

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/jira"
	"github.com/brizzai/fleet/internal/linear"
	"github.com/brizzai/fleet/internal/ticket"
)

// remoteImageLink matches a markdown image whose target is still a URL.
var remoteImageLink = regexp.MustCompile(`!\[[^\]]*\]\((https?://[^)\s]+)\)`)

// knownImageExt is the set an agent's file-read tool dispatches on.
var knownImageExt = map[string]struct{}{
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {}, ".bmp": {}, ".svg": {},
}

// TestMaterializeEndToEnd exercises a real tracker API against a real issue.
//
// Opt-in per provider, because each needs a credential and network:
//
//	LINEAR_API_KEY=lin_api_… FLEET_LINEAR_E2E=BRZ-1515 \
//	  go test ./internal/ticketing/ -run EndToEnd -v
//
//	JIRA_SITE=acme.atlassian.net JIRA_EMAIL=you@example.com JIRA_API_TOKEN=ATATT… \
//	  FLEET_JIRA_E2E=BRZ-1515 go test ./internal/ticketing/ -run EndToEnd -v
//
// One test for both, because the properties it checks are the contract every
// provider owes internal/ticket rather than anything tracker-specific — and a
// second copy is how the two would drift.
//
// It never mutates the tracker — MoveState stays false, so nothing is written to
// the issue. What it proves is the part that is easy to get subtly wrong and
// impossible to catch in a unit test: that an authenticated image download
// actually succeeds, that the bytes land with a usable extension, and that every
// surviving link in ticket.md is one the agent can open.
func TestMaterializeEndToEnd(t *testing.T) {
	cases := []struct {
		name     string
		provider ticket.Provider
		idEnv    string
		needs    []string
	}{
		{"linear", linear.New(), "FLEET_LINEAR_E2E", []string{linear.APIKeyEnvVar}},
		{"jira", jira.New(), "FLEET_JIRA_E2E", []string{jira.SiteEnvVar, jira.EmailEnvVar, jira.TokenEnvVar}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := os.Getenv(c.idEnv)
			if id == "" {
				t.Skipf("set %s=<ISSUE-ID> to run", c.idEnv)
			}
			for _, v := range c.needs {
				if os.Getenv(v) == "" {
					t.Skipf("set %s to run", v)
				}
			}
			c.provider.Warm(context.Background())

			wt := t.TempDir()
			res, err := ticket.Materialize(context.Background(), c.provider, ticket.Opts{
				WorktreePath: wt,
				Identifier:   id,
				MoveState:    false, // never mutate a real issue from a test
			})
			if err != nil {
				t.Fatalf("Materialize: %v", err)
			}
			t.Logf("identifier=%s images=%d dropped=%d", res.Identifier, res.Images, res.ImagesDropped)

			body, err := os.ReadFile(filepath.Join(res.Dir, "ticket.md"))
			if err != nil {
				t.Fatalf("ticket.md not written: %v", err)
			}
			md := string(body)
			if !strings.Contains(md, "ticket: "+res.Identifier) {
				t.Error("ticket.md is missing its front matter")
			}

			// No image link may still point at the tracker. Those URLs are 401
			// to the agent, which is the exact failure this whole path exists
			// to remove.
			for _, m := range remoteImageLink.FindAllStringSubmatch(md, -1) {
				t.Errorf("ticket.md kept a remote image link: %s", m[1])
			}
			// And no placeholder may survive: every one is either rewritten to
			// a real file or replaced by a note saying it wasn't downloaded.
			if strings.Contains(md, ticket.ImagePlaceholder) {
				t.Error("ticket.md still carries an unresolved image placeholder")
			}

			if res.Images == 0 {
				return
			}
			entries, err := os.ReadDir(filepath.Join(res.Dir, "images"))
			if err != nil {
				t.Fatalf("images/ missing though %d were reported: %v", res.Images, err)
			}
			for _, e := range entries {
				ext := strings.ToLower(filepath.Ext(e.Name()))
				if _, known := knownImageExt[ext]; !known {
					t.Errorf("%s has no usable extension — an agent's file-read tool dispatches on it", e.Name())
				}
				info, err := e.Info()
				if err != nil || info.Size() == 0 {
					t.Errorf("%s is empty", e.Name())
				}
			}
		})
	}
}
