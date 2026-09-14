package review

import (
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// TokenClass is what a run of code means. Deliberately six classes and not
// chroma's two hundred: the reader has six colors to spend, and a mapping that
// distinguishes NameVariableInstance from NameVariableClass would render both
// identically while costing a switch arm.
type TokenClass int

const (
	TokPlain TokenClass = iota
	TokKeyword
	TokType
	TokString
	TokNumber
	TokComment
	TokFunc
)

// Token is a run of source text that renders in one color.
type Token struct {
	Text  string
	Class TokenClass
}

// Highlighter lexes a file's lines once and answers per line.
//
// Per FILE and not per line, because a lexer is a state machine: a multi-line
// string or block comment is only closed by a later line, and lexing each line
// alone reopens the quote on every row — the failure looks like the whole rest
// of the file turning into a string.
type Highlighter struct {
	lines [][]Token
}

// noHighlight is the answer for a file chroma has no lexer for. Every method
// works on it, so callers never branch on "is highlighting available".
var noHighlight = &Highlighter{}

// Line returns the tokens for a 0-based line, or the raw text as one plain
// token when there is nothing better to say.
func (h *Highlighter) Line(i int, raw string) []Token {
	if h == nil || i < 0 || i >= len(h.lines) {
		return []Token{{Text: raw, Class: TokPlain}}
	}
	return h.lines[i]
}

// lexerCache keeps resolved lexers by path extension. Building one is regex
// compilation and a review opens the same handful of languages over and over.
var lexerCache sync.Map // string -> chroma.Lexer (nil value means "no lexer")

// Highlight lexes source as one unit and indexes the result by line.
//
// path only picks the lexer; the bytes are what get lexed. A path chroma does
// not recognise returns a Highlighter that answers every line as plain, rather
// than an error the caller would have to handle at every render.
func Highlight(path, source string) *Highlighter {
	lx := lexerFor(path)
	if lx == nil || source == "" {
		return noHighlight
	}
	it, err := lx.Tokenise(nil, source)
	if err != nil {
		return noHighlight
	}

	h := &Highlighter{lines: [][]Token{{}}}
	for _, t := range it.Tokens() {
		class := classOf(t.Type)
		// A token's value can span newlines (a block comment, a raw string).
		// Splitting here is what keeps the line index honest.
		parts := strings.Split(t.Value, "\n")
		for i, p := range parts {
			if i > 0 {
				h.lines = append(h.lines, []Token{})
			}
			if p == "" {
				continue
			}
			cur := len(h.lines) - 1
			h.lines[cur] = append(h.lines[cur], Token{Text: p, Class: class})
		}
	}
	return h
}

func lexerFor(path string) chroma.Lexer {
	if v, ok := lexerCache.Load(path); ok {
		lx, _ := v.(chroma.Lexer)
		return lx
	}
	lx := lexers.Match(path)
	if lx != nil {
		// Coalesce merges adjacent same-type tokens, which turns a line of Go
		// into a handful of runs instead of one token per identifier — the
		// difference between a few styled spans per row and forty.
		lx = chroma.Coalesce(lx)
	}
	lexerCache.Store(path, lx)
	return lx
}

// classOf collapses chroma's token type tree onto the six colors we have.
//
// Ordered most specific first: KeywordType ("int", "string") is inside the
// Keyword category, and reading it as a keyword would paint every builtin type
// the keyword color and leave the type color unused.
func classOf(t chroma.TokenType) TokenClass {
	switch {
	case t.InCategory(chroma.Comment):
		return TokComment
	case t.InSubCategory(chroma.LiteralString):
		return TokString
	case t.InSubCategory(chroma.LiteralNumber):
		return TokNumber
	case t == chroma.KeywordType, t == chroma.NameBuiltin, t == chroma.NameClass,
		t == chroma.NameNamespace:
		// KeywordDeclaration is deliberately NOT here: Go's lexer tags `func`
		// with it, and reading it as a type painted every function declaration
		// the type color while the keyword color went unused.
		return TokType
	case t.InCategory(chroma.Keyword), t == chroma.OperatorWord:
		return TokKeyword
	case t == chroma.NameFunction, t == chroma.NameFunctionMagic,
		t == chroma.NameAttribute, t == chroma.NameDecorator:
		return TokFunc
	}
	return TokPlain
}
