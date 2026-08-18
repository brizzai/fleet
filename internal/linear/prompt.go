package linear

import (
	"fmt"
	"strings"
)

// maxPromptTitle caps the title in the first line so it can't crowd out the
// identifier when the preview pane and the auto-titler truncate.
const maxPromptTitle = 60

// seedPromptTemplate is the agent's first message for a materialized ticket.
//
// Line 1 does three jobs at once, which is why the identifier comes before the
// title: it is the agent's opening instruction, it is what the preview pane
// renders as the session's prompt strip (first line only), and it is the input
// to naming.GenerateTitle, which takes the first line and cuts it at ~50 runes.
// Put the title first and every ticket session gets a sidebar row that has been
// truncated before it names its ticket.
//
// The "don't start" instruction is stated twice, top and bottom, and this is
// deliberate rather than redundant: a first message that describes a task is
// overwhelmingly read as an instruction to perform it, and the whole point of
// this flow is that the human reads the agent's understanding before any code
// moves. TestSeedPromptTellsAgentNotToStart pins both.
const seedPromptTemplate = `Read Linear ticket %s — %s. Do not start work yet.

The ticket is materialized in this worktree at %s (git-excluded):
  ticket.md   the description, and any comments
%s
Read ticket.md%s. Then tell me in a few lines what is being asked, and anything
ambiguous, missing, or contradictory.

Do not edit files, run builds, or begin implementing until I tell you to.%s
`

// SeedPrompt renders the first message for a materialized ticket.
//
// The images clauses collapse to nothing when no image made it to disk. That is
// the honest-degradation rule in concrete form: a broken CLI plus a failed
// fallback must not produce a prompt that points at an images/ directory which
// does not exist.
func SeedPrompt(r Result) string {
	imagesBlock := ""
	imagesClause := ""
	if r.Images > 0 {
		noun := "screenshots"
		if r.Images == 1 {
			noun = "screenshot"
		}
		imagesBlock = fmt.Sprintf("  images/     the %d %s it references\n", r.Images, noun)
		imagesClause = " and open every file in images/"
	}

	urlBlock := ""
	if r.URL != "" {
		urlBlock = "\n" + r.URL
	}

	return fmt.Sprintf(seedPromptTemplate,
		r.Identifier,
		truncateWords(r.Title, maxPromptTitle),
		r.RelDir,
		imagesBlock,
		imagesClause,
		urlBlock,
	)
}

// truncateWords cuts on a word boundary so a truncated title never ends
// mid-word, matching how fleet's own naming heuristic truncates.
func truncateWords(s string, limit int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(untitled)"
	}
	if len([]rune(s)) <= limit {
		return s
	}
	r := []rune(s)[:limit]
	out := string(r)
	if i := strings.LastIndexByte(out, ' '); i > limit/2 {
		out = out[:i]
	}
	return strings.TrimRight(out, " ,.;:-") + "…"
}
