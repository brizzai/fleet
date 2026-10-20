package github

import (
	"path"
	"strings"
)

// DemoteReason says why a file was folded out of the main list. Empty means
// the file is shown.
//
// A reason rather than a bool because hiding is only safe if the list can say
// what it hid and why: "23 test files hidden" is auditable, "11 of 34 shown"
// is a number you have to trust.
type DemoteReason string

const (
	DemoteNone      DemoteReason = ""
	DemoteLockfile  DemoteReason = "lockfile"
	DemoteGenerated DemoteReason = "generated"
	DemoteVendored  DemoteReason = "vendored"
	DemoteSnapshot  DemoteReason = "snapshot"
	DemoteTest      DemoteReason = "tests"
)

// lockfiles are matched on exact basename. A prefix or suffix rule would catch
// files that merely look like one — `package.json` is not `package-lock.json`,
// and hiding the former would hide a real dependency change.
var lockfiles = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"bun.lockb": true, "go.sum": true, "Cargo.lock": true, "poetry.lock": true,
	"Gemfile.lock": true, "composer.lock": true, "uv.lock": true,
	"Pipfile.lock": true, "flake.lock": true, "pubspec.lock": true,
}

// vendorDirs are path segments whose contents are nobody's review.
var vendorDirs = map[string]bool{
	"vendor": true, "node_modules": true, "third_party": true,
	"Pods": true, ".yarn": true, "Godeps": true,
}

// Classify says whether a file should be folded out of the main list, and why.
//
// Deterministic and local — no model, no network, no latency. Every rule is one
// a reviewer would apply themselves in the first two seconds of opening a PR,
// which is exactly why it is safe to apply for them.
func Classify(f PRFile) DemoteReason {
	base := path.Base(f.Path)

	if lockfiles[base] {
		return DemoteLockfile
	}

	for seg := range strings.SplitSeq(path.Dir(f.Path), "/") {
		if vendorDirs[seg] {
			return DemoteVendored
		}
	}

	// Generated code, by the conventions that actually appear in repos.
	switch {
	case strings.HasSuffix(base, ".pb.go"),
		strings.HasSuffix(base, "_pb2.py"),
		strings.HasSuffix(base, "_pb2.pyi"),
		strings.HasSuffix(base, ".pb.cc"),
		strings.HasSuffix(base, ".pb.h"),
		strings.HasSuffix(base, "_generated.go"),
		strings.HasSuffix(base, ".gen.go"),
		strings.HasSuffix(base, ".g.dart"),
		strings.HasSuffix(base, ".freezed.dart"),
		strings.HasSuffix(base, ".designer.cs"),
		strings.Contains(f.Path, "/generated/"),
		strings.Contains(f.Path, "/__generated__/"),
		base == "schema.graphql" && strings.Contains(f.Path, "generated"):
		return DemoteGenerated
	}

	if strings.Contains(f.Path, "__snapshots__/") || strings.HasSuffix(base, ".snap") {
		return DemoteSnapshot
	}

	if isTestFile(f.Path) {
		return DemoteTest
	}

	return DemoteNone
}

// isTestFile covers the conventions of the languages this actually runs
// against — Go, Python, JS/TS.
//
// Tests are demoted, never hidden outright, and they sit last in the fold for
// a reason: a test file IS review-worthy, more so than a lockfile, but on a
// PR that is 23 test files out of 34 it is not where you start. The fold makes
// the shape of the change legible; one key brings them back.
func isTestFile(p string) bool {
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasSuffix(base, "_test.py"),
		strings.HasPrefix(base, "test_"),
		strings.HasSuffix(base, ".test.ts"), strings.HasSuffix(base, ".test.tsx"),
		strings.HasSuffix(base, ".test.js"), strings.HasSuffix(base, ".test.jsx"),
		strings.HasSuffix(base, ".spec.ts"), strings.HasSuffix(base, ".spec.tsx"),
		strings.HasSuffix(base, ".spec.js"), strings.HasSuffix(base, ".spec.jsx"):
		return true
	}
	for seg := range strings.SplitSeq(path.Dir(p), "/") {
		if seg == "__tests__" || seg == "testdata" {
			return true
		}
	}
	return false
}

// Split separates the files worth opening with from the ones folded away,
// preserving the incoming order in both.
func Split(files []PRFile) (shown []PRFile, folded map[DemoteReason][]PRFile) {
	folded = make(map[DemoteReason][]PRFile)
	for _, f := range files {
		if r := Classify(f); r != DemoteNone {
			folded[r] = append(folded[r], f)
			continue
		}
		shown = append(shown, f)
	}
	return shown, folded
}
