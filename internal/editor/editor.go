// Package editor resolves the user's configured editor into a runnable command.
//
// An editor reaches a Mac two ways: a CLI launcher on PATH (`code`, `goland`)
// and an application bundle. JetBrains Toolbox does not install the CLI
// launcher unless asked, and VS Code's is opt-in too, so a GoLand user commonly
// has the app but nothing named `goland` on PATH. Command falls back to
// `open -a` for exactly that case, and Available only offers editors this
// machine can actually launch.
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

var knownEditors = []knownEditor{
	{"code", "Visual Studio Code"},
	{"cursor", "Cursor"},
	{"windsurf", "Windsurf"},
	{"zed", "Zed"},
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

var (
	scanOnce sync.Once
	bundles  []string // installed app bundle names, sans the .app extension
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
				if name, ok := strings.CutSuffix(e.Name(), ".app"); ok {
					bundles = append(bundles, name)
				}
			}
		}
	})
	return bundles
}

// appFor returns the installed app bundle backing cmd, if there is one.
func appFor(cmd string) (string, bool) { return appForIn(cmd, installedBundles()) }

// appForIn is the pure core of appFor: match cmd's bundle prefix against the
// installed bundle names. Split out so it can be unit-tested without scanning
// /Applications.
func appForIn(cmd string, bundles []string) (string, bool) {
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
	for _, b := range bundles {
		if strings.HasPrefix(b, prefix) {
			return b, true
		}
	}
	return "", false
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

// Command builds the command that opens path in the editor named by spec, which
// may carry flags ("code -n"). Flags are a CLI-only contract, so a spec that
// carries them and has no launcher on PATH is an error rather than an `open -a`
// that silently drops them.
func Command(spec, path string) (*exec.Cmd, error) {
	parts := strings.Fields(spec)
	if len(parts) == 0 {
		return nil, errors.New("no editor configured")
	}
	if _, err := exec.LookPath(parts[0]); err == nil {
		return exec.Command(parts[0], append(parts[1:], path)...), nil
	}
	if app, ok := appFor(parts[0]); ok && len(parts) == 1 {
		return exec.Command("open", "-a", app, path), nil
	}
	return nil, fmt.Errorf("%s not found: no command on PATH and no matching app installed", parts[0])
}
