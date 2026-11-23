package selfreview

import "testing"

func TestCacheKey(t *testing.T) {
	base := CacheKeyInput{Model: "sonnet", TreeSHA: "t", BaseSHA: "b", Prompts: []string{"p1", "p2"},
		Ticket: "k", ReviewRules: "r", Round: 1, PrevRunID: ""}
	k := CacheKey(base)
	if len(k) != 64 || CacheKey(base) != k {
		t.Fatalf("key %q is not a stable sha256 hex", k)
	}
	mut := []func(*CacheKeyInput){
		func(c *CacheKeyInput) { c.Model = "opus" },
		func(c *CacheKeyInput) { c.TreeSHA = "t2" },
		func(c *CacheKeyInput) { c.BaseSHA = "b2" },
		func(c *CacheKeyInput) { c.Prompts = []string{"p1"} },
		func(c *CacheKeyInput) { c.Prompts = []string{"p1p2"} }, // boundary shift
		func(c *CacheKeyInput) { c.Prompts = []string{"p1", "p", "2"} },
		func(c *CacheKeyInput) { c.Ticket = "k2" },
		func(c *CacheKeyInput) { c.ReviewRules = "r2" },
		func(c *CacheKeyInput) { c.Round = 2 },
		func(c *CacheKeyInput) { c.PrevRunID = "run1" },
	}
	seen := map[string]int{k: -1}
	for i, m := range mut {
		c := base
		c.Prompts = append([]string(nil), base.Prompts...)
		m(&c)
		got := CacheKey(c)
		if j, dup := seen[got]; dup {
			t.Errorf("mutation %d collides with %d", i, j)
		}
		seen[got] = i
	}
}
