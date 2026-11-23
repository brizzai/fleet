package review

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		path string
		want DemoteReason
	}{
		{"pkg/shared/conversation/processor.go", DemoteNone},
		{"pkg/shared/conversation/processor_test.go", DemoteTest},
		{"apps/dashboard/src/Chart.tsx", DemoteNone},
		{"apps/dashboard/src/Chart.test.tsx", DemoteTest},
		{"apps/analytics/test_clustering.py", DemoteTest},
		{"go.sum", DemoteLockfile},
		// package.json is a real dependency change and must never be folded
		// away with the lockfile beside it.
		{"package.json", DemoteNone},
		{"package-lock.json", DemoteLockfile},
		{"vendor/github.com/x/y/z.go", DemoteVendored},
		{"apps/web/node_modules/pkg/index.js", DemoteVendored},
		{"api/service.pb.go", DemoteGenerated},
		{"api/__generated__/types.ts", DemoteGenerated},
		{"src/__snapshots__/App.test.tsx.snap", DemoteSnapshot},
		{"internal/ui/testdata/fixture.json", DemoteTest},
	}
	for _, c := range cases {
		if got := Classify(File{Path: c.path}); got != c.want {
			t.Errorf("Classify(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestSplitPreservesOrder(t *testing.T) {
	in := []File{
		{Path: "a.go"}, {Path: "a_test.go"}, {Path: "b.go"}, {Path: "go.sum"},
	}
	shown, folded := Split(in)
	if len(shown) != 2 || shown[0].Path != "a.go" || shown[1].Path != "b.go" {
		t.Errorf("shown = %v", shown)
	}
	if len(folded[DemoteTest]) != 1 || len(folded[DemoteLockfile]) != 1 {
		t.Errorf("folded = %v", folded)
	}
}

// ClassifyPath and Classify must agree: the reviewer's input assembly folds by
// path, the view by File, and a file folded on one side and shown on the other
// would be "skipped by rule" in a list that still asks you about it.
func TestClassifyPathMatchesClassify(t *testing.T) {
	for _, p := range []string{"go.sum", "a_test.go", "vendor/x/y.go", "main.go", "api/x.pb.go"} {
		if ClassifyPath(p) != Classify(File{Path: p}) {
			t.Errorf("ClassifyPath(%q) disagrees with Classify", p)
		}
	}
}
