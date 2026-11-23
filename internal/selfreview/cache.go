package selfreview

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// CacheKeyInput is everything a run's answer depends on. Two runs with the
// same key would ask the same model the same question about the same tree.
//
// Claims and CI are deliberately absent: they move without the code moving
// (CI finishes, the agent re-summarises), and regenerating on every CI tick
// would throw away a review the human may already be half-way through.
type CacheKeyInput struct {
	Model       string
	TreeSHA     string
	BaseSHA     string
	Prompts     []string // intent
	Ticket      string   // intent
	ReviewRules string   // REVIEW.md as read (before ParseReviewRules)
	Round       int
	PrevRunID   string
}

// CacheKey is sha256(PromptVersion, model, treeSHA, baseSHA, hash(intent),
// hash(REVIEW.md), round, prevRunID), hex-encoded.
//
// Every field is length-prefixed, so no two different inputs can collide by
// shifting a boundary ("ab"+"c" vs "a"+"bc").
func CacheKey(k CacheKeyInput) string {
	intent := sha256.New()
	writeField(intent, strconv.Itoa(len(k.Prompts)))
	for _, p := range k.Prompts {
		writeField(intent, p)
	}
	writeField(intent, k.Ticket)
	rules := sha256.Sum256([]byte(k.ReviewRules))

	h := sha256.New()
	for _, f := range []string{
		PromptVersion,
		k.Model,
		k.TreeSHA,
		k.BaseSHA,
		hex.EncodeToString(intent.Sum(nil)),
		hex.EncodeToString(rules[:]),
		strconv.Itoa(k.Round),
		k.PrevRunID,
	} {
		writeField(h, f)
	}
	return hex.EncodeToString(h.Sum(nil))
}

type writer interface{ Write([]byte) (int, error) }

func writeField(w writer, s string) {
	_, _ = w.Write([]byte(strconv.Itoa(len(s)) + ":"))
	_, _ = w.Write([]byte(s))
}
