package ticket

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoTicketSubprocess is the point of the move off the CLI, pinned.
//
// An earlier version of the Linear package shelled out to `linear`, which cost
// three version-skew bugs in one session: a release compiled without network
// access to uploads.linear.app that failed every image download and still
// exited 0, an `auth login` command that did not exist in the installed
// version, and an error message naming a `configure` command that never existed
// at all. None of that can come back while this holds.
//
// It scans every ticket package, not just its own directory. The single-package
// version passed vacuously the moment a second provider arrived: internal/jira
// could have shelled out to `acli` or `curl` with nothing complaining. Jira in
// particular has no comparable CLI, which is precisely why the shell-out
// approach was a Linear-only trick rather than an architecture.
//
// The guard is an allowlist rather than a ban, because these packages
// legitimately run three OS helpers — two keychains and a browser opener. An
// allowlist fails on anything new, which is the property that matters: adding a
// subprocess here should require saying so out loud.
func TestNoTicketSubprocess(t *testing.T) {
	allowed := map[string]string{
		"security":    "macOS keychain",
		"secret-tool": "libsecret keychain",
		"open":        "browser, macOS",
		"xdg-open":    "browser, Linux",
	}

	// Relative to this file, which is internal/ticket. A missing directory
	// fails rather than skipping: a renamed package must not silently drop out
	// of the scan.
	dirs := []string{".", "../linear", "../jira", "../ticketing"}

	fset := token.NewFileSet()
	scanned := 0

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v — if this package moved, update the list above", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			scanned++
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "exec" {
					return true
				}
				if sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext" && sel.Sel.Name != "LookPath" {
					return true
				}
				// The binary is the first string-literal argument, after the
				// ctx that CommandContext takes.
				for _, arg := range call.Args {
					lit, ok := arg.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					bin := strings.Trim(lit.Value, `"`)
					if _, allow := allowed[bin]; !allow {
						t.Errorf("%s runs %q — the data path is HTTP now, on purpose. "+
							"If this is a genuinely new OS helper, add it to the allowlist above.", path, bin)
					}
					return false
				}
				return false
			})
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no files — this guard is vacuous")
	}
	// Every listed package must contribute, or a typo in the list above would
	// silently narrow the scan back to one directory.
	if scanned < len(dirs) {
		t.Fatalf("scanned only %d files across %d packages — the scan is not reaching them all", scanned, len(dirs))
	}
}
