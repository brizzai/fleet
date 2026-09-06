package review

import "strings"

// CommentKind is what a comment IS, not how loud it is.
//
// The four are GitHub review conventions people already type by hand — "nit:",
// "question:" — so the tag ships in the body and reads correctly on the web too.
// Making it a keystroke is the whole feature; the color is a side effect.
type CommentKind int

const (
	CommentIssue CommentKind = iota
	CommentNit
	CommentQuestion
	CommentSuggestion
)

// CommentKinds is the cycle order for Tab. Issue leads because a review that
// only ever files nits is not the one worth making the fastest.
var CommentKinds = []CommentKind{CommentIssue, CommentNit, CommentQuestion, CommentSuggestion}

func (k CommentKind) String() string {
	switch k {
	case CommentNit:
		return "NIT"
	case CommentQuestion:
		return "QUESTION"
	case CommentSuggestion:
		return "SUGGESTION"
	}
	return "ISSUE"
}

// Prefix is what leads the comment body on GitHub. Lowercase and colon-suffixed
// because that is the convention already in use; an ISSUE shouts and adds
// nothing, so it carries no prefix at all.
func (k CommentKind) Prefix() string {
	switch k {
	case CommentNit:
		return "nit: "
	case CommentQuestion:
		return "question: "
	case CommentSuggestion:
		return "suggestion: "
	}
	return ""
}

// Next returns the kind Tab moves to, wrapping.
func (k CommentKind) Next() CommentKind {
	return CommentKind((int(k) + 1) % len(CommentKinds))
}

// Prev returns the kind Shift+Tab moves to.
func (k CommentKind) Prev() CommentKind {
	return CommentKind((int(k) + len(CommentKinds) - 1) % len(CommentKinds))
}

// Comment is one review note anchored to a line of the new side.
//
// Anchored to New and never to Old, because that is what GitHub accepts: a
// review comment positions against the head of the pull request.
type Comment struct {
	ID     int
	File   string
	Line   int
	Kind   CommentKind
	Body   string
	Author string // empty means you; set for a comment an agent wrote
	Sent   bool   // already submitted to GitHub
}

// Payload is the body as it will appear on GitHub.
func (c Comment) Payload() string { return c.Kind.Prefix() + c.Body }

// wrapBody breaks a comment across width columns at word boundaries.
//
// Falls back to a hard cut for a run with no spaces in it — a pasted URL or a
// stack frame — because a word longer than the box would otherwise overflow the
// panel and shear every row below it.
func wrapBody(s string, width int) []string {
	if width < 8 {
		width = 8
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, w := range strings.Fields(para) {
			for len([]rune(w)) > width {
				if line != "" {
					out = append(out, line)
					line = ""
				}
				r := []rune(w)
				out = append(out, string(r[:width]))
				w = string(r[width:])
			}
			switch {
			case line == "":
				line = w
			case len([]rune(line))+1+len([]rune(w)) <= width:
				line += " " + w
			default:
				out = append(out, line)
				line = w
			}
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}
