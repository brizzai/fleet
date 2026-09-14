package ui

import (
	"path/filepath"
	"strings"
)

// errorCategory is error_occurred's category: the error text before its first
// ':', kept only when it reads as fleet's own prose — several words of plain
// ASCII letters (apostrophes allowed), at most 40 characters. Anything else
// becomes "other". The text before the colon is fleet's only when fleet wrote
// the message; a wrapped os or git error, a path, or a repo, branch or account
// name would otherwise be sent verbatim — and those are made of exactly the
// digits, hyphens, dots, slashes and quotes this refuses.
func errorCategory(err error) string {
	c := strings.TrimSpace(strings.SplitN(err.Error(), ":", 2)[0])
	if len(c) > 40 || !strings.Contains(c, " ") {
		return "other"
	}
	for _, r := range c {
		letter := 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z'
		if !letter && r != ' ' && r != '\'' {
			return "other"
		}
	}
	return c
}

// editorName is the editor as editor_opened reports it: the base name of the
// configured command's first word, so neither a path nor flags are sent.
func editorName(spec string) string {
	fields := strings.Fields(spec)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}
