package linear

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMaterializeEndToEnd exercises the real Linear API against a real issue.
//
// Opt-in, because it needs a credential and network:
//
//	LINEAR_API_KEY=lin_api_… FLEET_LINEAR_E2E=BRZ-1515 go test ./internal/linear/ -run EndToEnd -v
//
// It never mutates Linear — MoveState stays false, so nothing is written to the
// issue. What it proves is the part that is easy to get subtly wrong and
// impossible to catch in a unit test: that an authenticated image download
// actually succeeds, that the bytes land with a usable extension, and that every
// surviving link in ticket.md is one the agent can open.
func TestMaterializeEndToEnd(t *testing.T) {
	id := os.Getenv("FLEET_LINEAR_E2E")
	if id == "" {
		t.Skip("set FLEET_LINEAR_E2E=<ISSUE-ID> to run")
	}
	if os.Getenv(APIKeyEnvVar) == "" {
		t.Skipf("set %s to run", APIKeyEnvVar)
	}

	wt := t.TempDir()
	res, err := Materialize(context.Background(), Opts{
		WorktreePath: wt,
		Identifier:   id,
		MoveState:    false, // never mutate a real issue from a test
	})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	t.Logf("identifier=%s images=%d dropped=%d", res.Identifier, res.Images, res.ImagesDropped)

	body, err := os.ReadFile(filepath.Join(res.Dir, ticketFile))
	if err != nil {
		t.Fatalf("ticket.md not written: %v", err)
	}
	if !strings.Contains(string(body), "ticket: "+res.Identifier) {
		t.Error("ticket.md is missing its front matter")
	}

	// No link may still point at Linear. An uploads.linear.app URL is 401 to
	// the agent, which is the exact failure this whole path exists to remove.
	if strings.Contains(string(body), uploadsHost) {
		t.Error("ticket.md still carries an uploads.linear.app URL — the agent cannot open those")
	}

	for _, ref := range findImages(body) {
		t.Errorf("ticket.md kept a remote image link: %s", ref.target)
	}

	if res.Images == 0 {
		return
	}
	entries, err := os.ReadDir(filepath.Join(res.Dir, imagesDir))
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
}
