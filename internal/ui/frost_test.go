package ui

import (
	"hash/fnv"
	"testing"
)

// trailOf is the detector's own hash of a key run, so a test can install a
// run of its choosing.
func trailOf(keys ...string) uint64 {
	f := fnv.New64a()
	for _, k := range keys {
		_, _ = f.Write([]byte(k))
		_, _ = f.Write([]byte{0})
	}
	return f.Sum64()
}

func TestTrailFiresOnlyOnTheFullRun(t *testing.T) {
	run := []string{"x1", "x2", "x3", "x4", "x5", "x6", "x7", "x8"}
	old := trailKey
	trailKey = trailOf(run...)
	defer func() { trailKey = old }()

	h := &Home{}
	for i, k := range run {
		if fire := h.noteTrail(k); fire != (i == len(run)-1) {
			t.Fatalf("key %d %q: fire=%v", i, k, fire)
		}
	}
	if len(h.keyTrail) != 0 {
		t.Fatalf("detector should reset after firing, got %v", h.keyTrail)
	}
	// A stray key mid-run breaks it; repeating the run from the top completes.
	for _, k := range append([]string{"x1", "x2", "j"}, run...) {
		if h.noteTrail(k) && k != "x8" {
			t.Fatalf("fired early at %q", k)
		}
	}
	// A stutter on the first key still completes: the last eight are the run.
	h.keyTrail = nil
	for _, k := range append([]string{"x1"}, run...) {
		if h.noteTrail(k) && k != "x8" {
			t.Fatalf("fired early at %q", k)
		}
	}
	if len(h.keyTrail) != 0 {
		t.Fatal("the stuttered run should have fired and reset")
	}
}

func TestTrailNeverFiresOnPlainNavigation(t *testing.T) {
	h := &Home{}
	for _, k := range []string{"j", "j", "k", "k", "enter", "space", "j", "k", "j", "k", "a", "d"} {
		if h.noteTrail(k) {
			t.Fatalf("fired on %q", k)
		}
	}
	if len(h.keyTrail) != trailLen {
		t.Fatalf("trail should stay bounded at %d, got %d", trailLen, len(h.keyTrail))
	}
}
