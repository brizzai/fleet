package review

import "testing"

func TestDirsCarryTheirFullPath(t *testing.T) {
	nodes := BuildTree([]TreeFile{
		{Path: "internal/session/session.go"},
		{Path: "internal/session/storage.go"},
		{Path: "internal/ui/app.go"},
		{Path: "pkg/shared/models/conv.go"},
	})
	want := map[string]string{
		"internal/":          "internal",
		"session/":           "internal/session",
		"ui/":                "internal/ui",
		"pkg/shared/models/": "pkg/shared/models",
	}
	for _, n := range nodes {
		if !n.IsDir {
			continue
		}
		w, ok := want[n.Label]
		if !ok {
			t.Errorf("unexpected dir row %q", n.Label)
			continue
		}
		if n.Path != w {
			t.Errorf("dir %q has Path %q, want %q", n.Label, n.Path, w)
		}
		delete(want, n.Label)
	}
	for lbl := range want {
		t.Errorf("missing dir row %q", lbl)
	}
}
