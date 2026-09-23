package ui

import (
	"path/filepath"
	"strings"
)

// editorName is the editor as editor_opened reports it: the base name of the
// configured command's first word, so neither a path nor flags are sent.
func editorName(spec string) string {
	fields := strings.Fields(spec)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}
