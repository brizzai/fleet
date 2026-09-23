package review

import (
	"strings"
	"testing"
)

func classAt(h *Highlighter, line int, raw, want string) TokenClass {
	for _, t := range h.Line(line, raw) {
		if strings.TrimSpace(t.Text) == want {
			return t.Class
		}
	}
	return TokPlain
}

func TestHighlightClassifiesGo(t *testing.T) {
	src := "// a comment\nfunc reset() int {\n\tname = \"fleet\"\n\treturn 42\n}\n"
	lines := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	h := Highlight("session.go", src)

	// `func` is KeywordDeclaration in chroma's Go lexer. Reading that as a type
	// painted every declaration the type colour and left the keyword colour
	// unused, which is why classOf lists the type cases explicitly.
	if got := classAt(h, 1, lines[1], "func"); got != TokKeyword {
		t.Errorf("func classified %v, want keyword", got)
	}
	if got := classAt(h, 1, lines[1], "int"); got != TokType {
		t.Errorf("int classified %v, want type", got)
	}
	if got := classAt(h, 1, lines[1], "reset"); got != TokFunc {
		t.Errorf("reset classified %v, want func", got)
	}
	if got := classAt(h, 2, lines[2], `"fleet"`); got != TokString {
		t.Errorf("string literal classified %v, want string", got)
	}
	if got := classAt(h, 3, lines[3], "42"); got != TokNumber {
		t.Errorf("42 classified %v, want number", got)
	}
	if got := classAt(h, 0, lines[0], "// a comment"); got != TokComment {
		t.Errorf("comment classified %v, want comment", got)
	}
}

// A lexer is a state machine. Highlighting line by line reopens the quote on
// every row, so a multi-line string turns the whole rest of the file into one.
func TestHighlightKeepsStateAcrossLines(t *testing.T) {
	src := "x := `line one\nline two`\ny := 1\n"
	h := Highlight("f.go", src)
	if got := classAt(h, 1, "line two`", "line two`"); got != TokString {
		t.Errorf("the second half of a raw string classified %v, want string", got)
	}
	if got := classAt(h, 2, "y := 1", "1"); got != TokNumber {
		t.Error("the line after a closed raw string is still inside it")
	}
}

// A file chroma has no lexer for must answer every line as plain rather than
// erroring, so no caller has to branch on "is highlighting available".
func TestHighlightDegradesToPlain(t *testing.T) {
	h := Highlight("mystery.zzzz", "anything at all")
	toks := h.Line(0, "anything at all")
	if len(toks) != 1 || toks[0].Class != TokPlain || toks[0].Text != "anything at all" {
		t.Errorf("unknown file type gave %+v", toks)
	}
	// Out-of-range lines answer with the raw text, never a panic.
	if got := h.Line(999, "raw"); len(got) != 1 || got[0].Text != "raw" {
		t.Errorf("out-of-range line gave %+v", got)
	}
	var nilHL *Highlighter
	if got := nilHL.Line(0, "raw"); len(got) != 1 || got[0].Text != "raw" {
		t.Errorf("nil highlighter gave %+v", got)
	}
}
