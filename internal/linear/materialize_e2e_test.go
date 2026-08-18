package linear

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMaterializeEndToEnd exercises the real `linear` CLI against a real issue.
//
// Opt-in, because it needs an authenticated CLI and network:
//
//	FLEET_LINEAR_E2E=BRZ-1515 go test ./internal/linear/ -run EndToEnd -v
//
// It never mutates Linear — MoveState stays false, so nothing is written to the
// issue. What it proves is the part that is easy to get subtly wrong and
// impossible to catch in a unit test: that the markdown pass yields links we can
// resolve, that images land on disk with a usable extension, and that the
// fallback download works on a CLI whose own downloader is broken.
func TestMaterializeEndToEnd(t *testing.T) {
	id := os.Getenv("FLEET_LINEAR_E2E")
	if id == "" {
		t.Skip("set FLEET_LINEAR_E2E=<ISSUE-ID> to run")
	}
	if !Available() {
		t.Skip("linear CLI not installed")
	}
	repo := os.Getenv("FLEET_LINEAR_E2E_REPO")
	if repo == "" {
		t.Skip("set FLEET_LINEAR_E2E_REPO=<path to a repo with .linear.toml>")
	}

	wt := t.TempDir()
	res, err := Materialize(context.Background(), Opts{
		RepoDir:      repo,
		WorktreePath: wt,
		Identifier:   id,
		MoveState:    false, // never mutate a real issue from a test
	})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	t.Logf("identifier=%s images=%d dropped=%d fallback=%v", res.Identifier, res.Images, res.ImagesDropped, res.UsedFallback)

	body, err := os.ReadFile(filepath.Join(res.Dir, ticketFile))
	if err != nil {
		t.Fatalf("ticket.md not written: %v", err)
	}
	if !strings.Contains(string(body), "ticket: "+res.Identifier) {
		t.Error("ticket.md is missing its front matter")
	}

	// Every surviving link must be a relative images/ path — an absolute
	// $TMPDIR path is outside the project root (the agent's read tool prompts
	// or the file is purged) and a remote URL is 401.
	for _, ref := range findImages(body) {
		if ref.remote || filepath.IsAbs(ref.target) {
			t.Errorf("ticket.md still links %q; it must point inside images/", ref.target)
		}
	}

	if res.Images == 0 {
		t.Log("no images on this ticket (or none could be fetched) — pick a ticket with screenshots to exercise that path")
		return
	}

	entries, err := os.ReadDir(filepath.Join(res.Dir, imagesDir))
	if err != nil {
		t.Fatalf("images dir missing despite Images=%d: %v", res.Images, err)
	}
	if len(entries) != res.Images {
		t.Errorf("Images=%d but %d files on disk", res.Images, len(entries))
	}
	for _, e := range entries {
		p := filepath.Join(res.Dir, imagesDir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil || len(data) == 0 {
			t.Errorf("%s is unreadable or empty", e.Name())
			continue
		}
		if ext := filepath.Ext(e.Name()); ext == "" {
			t.Errorf("%s has no extension — an agent's read tool dispatches on it", e.Name())
		}
		if ct := http.DetectContentType(data); !strings.HasPrefix(ct, "image/") {
			t.Errorf("%s is %s, not an image", e.Name(), ct)
		}
	}
}
