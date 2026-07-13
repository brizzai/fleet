// Package editor resolves the user's configured editor into a launched process.
//
// An editor reaches a Mac two ways: a CLI launcher on PATH (`code`, `goland`)
// and an application bundle. JetBrains Toolbox does not install the CLI
// launcher unless asked, and VS Code's is opt-in too, so a GoLand user commonly
// has the app but nothing named `goland` on PATH. Launch falls back to `open -a`
// for exactly that case, and Available only offers editors this machine can
// actually launch.
package editor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// knownEditor pairs an editor command with the prefix of its macOS app bundle
// name. Bundles are prefix-matched because editions vary the suffix ("PyCharm
// Community Edition.app", "IntelliJ IDEA Ultimate.app"). A terminal editor has
// no bundle and so carries an empty app.
type knownEditor struct {
	cmd string
	app string
}

// knownEditors is the single source of truth for which commands are editors.
// internal/proc seeds its never-kill set from Commands(), so an editor added
// here is automatically spared when fleet clears processes off a worktree —
// previously the two lists were hand-synced and had already drifted.
var knownEditors = []knownEditor{
	{"code", "Visual Studio Code"},
	{"cursor", "Cursor"},
	{"windsurf", "Windsurf"},
	{"zed", "Zed"},
	{"subl", "Sublime Text"},
	{"idea", "IntelliJ IDEA"},
	{"goland", "GoLand"},
	{"pycharm", "PyCharm"},
	{"webstorm", "WebStorm"},
	{"phpstorm", "PhpStorm"},
	{"rubymine", "RubyMine"},
	{"clion", "CLion"},
	{"rider", "Rider"},
	{"datagrip", "DataGrip"},
	{"rustrover", "RustRover"},
	{"vim", ""},
	{"nvim", ""},
	{"nano", ""},
	{"emacs", ""},
}

// defaultEditor mirrors config.GetEditor's final fallback, so Available is never
// empty even on a machine where nothing was detected (the settings cycler
// indexes into the list it gets back).
const defaultEditor = "code"

// Commands returns every known editor command, installed or not. It is the
// process-name set internal/proc must never kill; it touches no filesystem, so
// importing it stays cheap.
func Commands() []string {
	out := make([]string, 0, len(knownEditors))
	for _, k := range knownEditors {
		out = append(out, k.cmd)
	}
	return out
}

var (
	scanOnce sync.Once
	bundles  []string // full paths of installed app bundles
)

// installedBundles lists the app bundles in the standard macOS locations. It
// scans once per process: installing an IDE mid-session is not worth a rescan
// on every settings frame.
func installedBundles() []string {
	scanOnce.Do(func() {
		dirs := []string{"/Applications"}
		if home, err := os.UserHomeDir(); err == nil {
			dirs = append(dirs,
				filepath.Join(home, "Applications"),
				// JetBrains Toolbox's default install location.
				filepath.Join(home, "Applications", "JetBrains Toolbox"),
			)
		}
		for _, dir := range dirs {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".app") {
					bundles = append(bundles, filepath.Join(dir, e.Name()))
				}
			}
		}
	})
	return bundles
}

// appFor returns the full path of the app bundle backing cmd, if one is
// installed. The path (not the bare name) is what reaches `open -a`, so the
// bundle we launch is the one the scan actually found rather than whatever
// LaunchServices resolves the name to.
func appFor(cmd string) (string, bool) { return appForIn(cmd, installedBundles()) }

// appForIn is the pure core of appFor: match cmd's bundle prefix against the
// installed bundle paths. Split out so it can be unit-tested without scanning
// /Applications.
func appForIn(cmd string, bundlePaths []string) (string, bool) {
	prefix := ""
	for _, k := range knownEditors {
		if k.cmd == cmd {
			prefix = k.app
			break
		}
	}
	if prefix == "" {
		return "", false
	}
	// Exact bundle name wins. Prefix matching is what lets an edition resolve at
	// all ("PyCharm Community Edition.app" -> pycharm), but on its own it hands
	// the win to whichever bundle ReadDir lists first — and ReadDir sorts by
	// name, where the space in "Visual Studio Code - Insiders.app" (0x20) sorts
	// ahead of the dot in "Visual Studio Code.app" (0x2e). A user with both would
	// get Insiders. So take the exact match if there is one, and only then fall
	// back to a prefix.
	for _, p := range bundlePaths {
		if bundleName(p) == prefix {
			return p, true
		}
	}
	for _, p := range bundlePaths {
		if strings.HasPrefix(bundleName(p), prefix) {
			return p, true
		}
	}
	return "", false
}

// bundleName is an app bundle path's display name: "/Applications/GoLand.app"
// -> "GoLand".
func bundleName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".app")
}

// Available returns the known editors this machine can launch — CLI launcher on
// PATH, or app bundle installed — so the settings cycler never offers a choice
// that would fail. Never empty.
func Available() []string {
	var out []string
	for _, k := range knownEditors {
		if _, err := exec.LookPath(k.cmd); err == nil {
			out = append(out, k.cmd)
			continue
		}
		if _, ok := appFor(k.cmd); ok {
			out = append(out, k.cmd)
		}
	}
	if len(out) == 0 {
		return []string{defaultEditor}
	}
	return out
}

// Launch opens path in the editor named by spec, which may carry flags
// ("code -n").
func Launch(spec, path string) error {
	cmd, wait, err := command(spec, path)
	if err != nil {
		return err
	}
	if !wait {
		return cmd.Start()
	}
	// `open` exits as soon as LaunchServices takes the request, so waiting on it
	// costs nothing and is the only way its failure is visible: Start alone
	// always succeeds (/usr/bin/open exists), swallowing an unresolvable bundle
	// and leaving the child unreaped.
	out, err := cmd.CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// command builds the launch command and reports whether it must be waited on.
// An editor process has to outlive fleet, so it is only started; `open` is a
// short-lived courier, so it is waited on. Split from Launch so the routing can
// be unit-tested without launching anything.
func command(spec, path string) (cmd *exec.Cmd, wait bool, err error) {
	parts := strings.Fields(spec)
	if len(parts) == 0 {
		return nil, false, errors.New("no editor configured")
	}
	if _, err := exec.LookPath(parts[0]); err == nil {
		return exec.Command(parts[0], append(parts[1:], path)...), false, nil
	}
	app, installed := appFor(parts[0])
	switch {
	case installed && len(parts) == 1:
		return exec.Command("open", "-a", app, path), true, nil
	case installed:
		// Flags are a CLI-only contract: `open -a`'s --args is ignored outright
		// when the app is already running, so passing them through would drop
		// them unpredictably. Name the real cause rather than claiming the app
		// is missing — it is installed, sitting right there at app.
		return nil, false, fmt.Errorf("%s is installed, but flags need its CLI launcher on PATH", parts[0])
	}
	return nil, false, fmt.Errorf("%s not found: no command on PATH and no matching app installed", parts[0])
}
