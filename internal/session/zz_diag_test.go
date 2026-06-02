package session

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Throwaway diagnostic — runs the REAL detectStatus/normalizeForHash on captured
// stuck-window panes to see why the pane-running fallback doesn't fire. DELETE AFTER.
func TestDiagStuckGrabs(t *testing.T) {
	dir := os.Getenv("DIAG_DIR")
	if dir == "" {
		t.Skip("set DIAG_DIR")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "grab_*.ansi.txt"))
	sort.Strings(files)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	prevHash := ""
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		clean := StripANSI(string(raw))
		st := detectStatus(clean, log)
		h := hashContent(normalizeForHash(string(raw)))
		changed := ""
		if prevHash != "" && h != prevHash {
			changed = "  <== normalized HASH CHANGED"
		}
		prevHash = h
		if st == "" {
			st = "(empty)"
		}
		fmt.Printf("%-16s detectStatus=%-9s normHash=%s%s\n", filepath.Base(f), st, h[:10], changed)
	}
}
