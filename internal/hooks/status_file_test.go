package hooks

import (
	"path/filepath"
	"sync"
	"testing"
)

// Two hook handlers can write the same instance's status at once (Codex fires a
// PermissionRequest per concurrent tool call). They used to share one
// `<id>.json.tmp`: the loser's rename failed with ENOENT, and an interleaved
// write could be renamed into place as truncated JSON.
func TestWriteStatusFileConcurrent(t *testing.T) {
	dir := t.TempDir()
	const instanceID = "abc12345-1783869794"
	const writers = 16

	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = WriteStatusFile(dir, instanceID, &StatusFile{
				Status:     "waiting",
				Event:      "PermissionRequest",
				SessionID:  "019f56ec-ea41-7031-85d8-d27483ddc5a5",
				UserPrompt: "do this:",
			})
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d: %v", i, err)
		}
	}

	// Whichever writer landed last, the file must be complete and parseable.
	sf, err := ReadStatusFile(filepath.Join(dir, instanceID+".json"))
	if err != nil {
		t.Fatalf("status file unreadable after concurrent writes: %v", err)
	}
	if sf.Status != "waiting" || sf.Event != "PermissionRequest" {
		t.Errorf("got status=%q event=%q, want waiting/PermissionRequest", sf.Status, sf.Event)
	}
}
