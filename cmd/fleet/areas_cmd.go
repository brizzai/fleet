package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/brizzai/fleet/internal/area"
	"github.com/brizzai/fleet/internal/config"
	"github.com/brizzai/fleet/internal/git"
	"github.com/brizzai/fleet/internal/session"
)

// runAreas is `fleet areas`: the configured areas, how many sessions each holds,
// and where to edit them. Read-only.
func runAreas() {
	cfg := config.Load()
	var paths []string
	if storage, err := session.Open(session.DefaultDBPath()); err == nil {
		if rows, err := storage.LoadSessions(); err == nil {
			for _, r := range rows {
				paths = append(paths, r.ProjectPath)
			}
		}
		storage.Close()
	}
	home, _ := os.UserHomeDir()
	writeAreas(os.Stdout, cfg.Areas, paths, home, config.DefaultConfigPath(), git.GetMainWorktreePath)
}

// writeAreas renders the areas table. mainRepo resolves a worktree to its main
// checkout, as the sidebar does; nil skips that fallback.
func writeAreas(w io.Writer, areas []config.Area, sessionPaths []string, home, cfgPath string, mainRepo func(string) string) {
	m := area.New(areas, home)
	if !m.Enabled() {
		fmt.Fprintf(w, "No areas configured. Add them to %s:\n\n", cfgPath)
		fmt.Fprint(w, `  "areas": [
    {"name": "personal", "paths": ["~/Projects/Personal"], "account": "me@gmail.com", "default": true},
    {"name": "work", "paths": ["~/Projects/Work"], "account": "me@company.com"}
  ]
`)
		return
	}

	counts := map[string]int{}
	for _, p := range sessionPaths {
		var resolve func() string
		if mainRepo != nil {
			p := p
			resolve = func() string { return mainRepo(p) }
		}
		counts[m.Of(p, resolve)]++
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "AREA\tSESSIONS\tACCOUNT\tPATHS\t")
	for _, a := range areas {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		label := name
		if a.Default {
			label += " (default)"
		}
		acct := a.Account
		if acct == "" {
			acct = "-"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t\n", label, counts[name], acct, strings.Join(a.Paths, ", "))
	}
	tw.Flush()
	fmt.Fprintf(w, "\nOpen one with: fleet --area <name>   (v switches inside fleet)\nEdit areas in: %s (\"areas\")\n", cfgPath)
}
