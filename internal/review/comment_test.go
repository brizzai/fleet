package review

import "testing"

// A comment's kind persists as its NAME. CommentKinds is an iota, so if the
// name and the parse ever disagree — or if an ordinal were stored instead —
// adding a fifth kind in the middle would silently turn saved questions into
// nits with nothing on screen to show it happened.
func TestCommentKindSurvivesItsName(t *testing.T) {
	for _, k := range CommentKinds {
		if got := ParseCommentKind(k.String()); got != k {
			t.Errorf("ParseCommentKind(%q) = %v, want %v", k.String(), got, k)
		}
	}
	// An unrecognised name falls back to Issue, never to a random ordinal: a
	// row written by a future fleet must not read back as some other kind.
	if got := ParseCommentKind("PRAISE"); got != CommentIssue {
		t.Errorf("an unknown kind parsed as %v, want Issue", got)
	}
	if got := ParseCommentKind(""); got != CommentIssue {
		t.Errorf("an empty kind parsed as %v, want Issue", got)
	}
}
