package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/session"
)

func TestParseSendArgs(t *testing.T) {
	t.Run("selector and message", func(t *testing.T) {
		o, err := parseSendArgs([]string{"fix-242", "run the tests"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if o.selector != "fix-242" || o.message != "run the tests" {
			t.Errorf("got %+v", o)
		}
	})

	t.Run("unquoted message words are joined", func(t *testing.T) {
		o, err := parseSendArgs([]string{"fix-242", "run", "the", "tests"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if o.message != "run the tests" {
			t.Errorf("message = %q", o.message)
		}
	})

	t.Run("force before selector", func(t *testing.T) {
		o, err := parseSendArgs([]string{"-force", "fix-242", "yes"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !o.force || o.selector != "fix-242" || o.message != "yes" {
			t.Errorf("got %+v", o)
		}
	})

	// A message is free-form prose that may legitimately start with a dash, so
	// everything after the selector is text — the flag parser never sees it.
	t.Run("dashes in the message are message text", func(t *testing.T) {
		o, err := parseSendArgs([]string{"fix-242", "--force", "is not a flag here"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if o.force {
			t.Error("a flag after the selector should not be parsed as a flag")
		}
		if o.message != "--force is not a flag here" {
			t.Errorf("message = %q", o.message)
		}
	})

	t.Run("stdin marker survives parsing", func(t *testing.T) {
		o, err := parseSendArgs([]string{"fix-242", "-"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if o.message != "-" {
			t.Errorf("message = %q, want -", o.message)
		}
	})

	t.Run("missing pieces", func(t *testing.T) {
		for _, args := range [][]string{nil, {"fix-242"}, {"fix-242", "   "}} {
			if _, err := parseSendArgs(args); !errors.Is(err, errMissingSendArgs) {
				t.Errorf("args %v: err = %v, want errMissingSendArgs", args, err)
			}
		}
	})
}

// noBranch stands in for the git lookup in tests that shouldn't need it.
func noBranch(string) string { return "" }

func testRows() []*session.SessionRow {
	return []*session.SessionRow{
		{ID: "aaaa1111-1700000001", Title: "fix login", ProjectPath: "/repo/fix-login", WorkspaceName: "fix-login"},
		{ID: "bbbb2222-1700000002", Title: "fix logout", ProjectPath: "/repo/fix-logout", WorkspaceName: "fix-logout"},
		{ID: "cccc3333-1700000003", Title: "Docs", ProjectPath: "/repo/docs", WorkspaceName: "docs"},
	}
}

func TestResolveSession(t *testing.T) {
	rows := testRows()

	t.Run("exact id", func(t *testing.T) {
		got, err := resolveSession(rows, nil, "bbbb2222-1700000002", noBranch)
		if err != nil || got.Title != "fix logout" {
			t.Fatalf("got %v, err %v", got, err)
		}
	})

	t.Run("id prefix", func(t *testing.T) {
		got, err := resolveSession(rows, nil, "cccc", noBranch)
		if err != nil || got.Title != "Docs" {
			t.Fatalf("got %v, err %v", got, err)
		}
	})

	t.Run("exact title is case-insensitive", func(t *testing.T) {
		got, err := resolveSession(rows, nil, "docs", noBranch)
		if err != nil || got.ID != "cccc3333-1700000003" {
			t.Fatalf("got %v, err %v", got, err)
		}
	})

	t.Run("workspace name", func(t *testing.T) {
		got, err := resolveSession(rows, nil, "fix-logout", noBranch)
		if err != nil || got.Title != "fix logout" {
			t.Fatalf("got %v, err %v", got, err)
		}
	})

	t.Run("branch", func(t *testing.T) {
		branchOf := func(path string) string {
			if path == "/repo/docs" {
				return "feature/rewrite-docs"
			}
			return "main"
		}
		got, err := resolveSession(rows, nil, "feature/rewrite-docs", branchOf)
		if err != nil || got.Title != "Docs" {
			t.Fatalf("got %v, err %v", got, err)
		}
	})

	t.Run("title substring", func(t *testing.T) {
		got, err := resolveSession(rows, nil, "logout", noBranch)
		if err != nil || got.Title != "fix logout" {
			t.Fatalf("got %v, err %v", got, err)
		}
	})

	// The whole point of the selector is to name one session. A tier matching
	// several must report them, not guess — and must not fall through to a
	// vaguer tier that happens to match exactly one.
	t.Run("ambiguous matches are listed, not guessed", func(t *testing.T) {
		_, err := resolveSession(rows, nil, "fix", noBranch)
		if err == nil {
			t.Fatal("expected an ambiguity error")
		}
		for _, want := range []string{"fix login", "fix logout", "aaaa1111", "bbbb2222"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("ambiguity error should name %q, got:\n%s", want, err)
			}
		}
	})

	// An exact title must win over a substring hit on another session, or
	// naming a session exactly could still be called ambiguous.
	t.Run("exact title beats substring", func(t *testing.T) {
		rows := []*session.SessionRow{
			{ID: "aaaa1111-1700000001", Title: "api"},
			{ID: "bbbb2222-1700000002", Title: "api rewrite"},
		}
		got, err := resolveSession(rows, nil, "api", noBranch)
		if err != nil || got.ID != "aaaa1111-1700000001" {
			t.Fatalf("got %v, err %v", got, err)
		}
	})

	t.Run("no match", func(t *testing.T) {
		if _, err := resolveSession(rows, nil, "nope", noBranch); err == nil {
			t.Fatal("expected a no-match error")
		}
	})

	t.Run("no sessions at all", func(t *testing.T) {
		if _, err := resolveSession(nil, nil, "anything", noBranch); err == nil {
			t.Fatal("expected an error when there are no sessions")
		}
	})
}

func TestResolveSessionSlots(t *testing.T) {
	rows := testRows()
	slots := map[int]string{3: "bbbb2222-1700000002"}

	t.Run("bound slot", func(t *testing.T) {
		got, err := resolveSession(rows, slots, "@3", noBranch)
		if err != nil || got.Title != "fix logout" {
			t.Fatalf("got %v, err %v", got, err)
		}
	})

	t.Run("unbound slot", func(t *testing.T) {
		if _, err := resolveSession(rows, slots, "@4", noBranch); err == nil {
			t.Fatal("expected an error for an unbound slot")
		}
	})

	t.Run("not a slot number", func(t *testing.T) {
		for _, sel := range []string{"@", "@x", "@11", "@-1"} {
			if _, err := resolveSession(rows, slots, sel, noBranch); err == nil {
				t.Errorf("selector %q: expected an error", sel)
			}
		}
	})

	// A slot selector must never fall back to matching titles: silently sending
	// to a session that merely looks like "@3" is worse than refusing.
	t.Run("stale binding does not fall through", func(t *testing.T) {
		_, err := resolveSession(rows, map[int]string{3: "gone-1700000009"}, "@3", noBranch)
		if err == nil || !strings.Contains(err.Error(), "no longer exists") {
			t.Fatalf("err = %v, want a stale-binding error", err)
		}
	})
}

func TestReadStdinArg(t *testing.T) {
	t.Run("passes plain text through", func(t *testing.T) {
		got, err := readStdinArg("hello", strings.NewReader("ignored"), "message")
		if err != nil || got != "hello" {
			t.Fatalf("got %q, err %v", got, err)
		}
	})

	t.Run("reads stdin on -", func(t *testing.T) {
		got, err := readStdinArg("-", strings.NewReader("line one\nline two\n"), "message")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "line one\nline two" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("empty stdin is an error", func(t *testing.T) {
		_, err := readStdinArg("-", strings.NewReader("  \n "), "message")
		if err == nil {
			t.Fatal("expected an error for empty stdin")
		}
	})
}

func TestSummarizeText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"short stays whole", "fix it", "fix it"},
		{"long is cut", strings.Repeat("a", 15), strings.Repeat("a", 10) + "…"},
		{"multiline keeps the first line", "first line\nsecond line", "first line…"},
		{"exact length is not marked", strings.Repeat("a", 10), strings.Repeat("a", 10)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := summarizeText(c.in, 10); got != c.want {
				t.Errorf("summarizeText(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	// Prompts routinely carry pasted terminal output; a byte-based cut would
	// both truncate early and could split one of these 3-byte runes.
	t.Run("counts runes, not bytes", func(t *testing.T) {
		got := summarizeText(strings.Repeat("│", 8), 10)
		if got != strings.Repeat("│", 8) {
			t.Errorf("got %q, want the string untouched", got)
		}
	})
}
