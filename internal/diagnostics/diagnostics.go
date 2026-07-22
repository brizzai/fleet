package diagnostics

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"os/exec"
)

// Report holds collected diagnostic information.
type Report struct {
	Version       string
	GoVersion     string
	OS            string
	Arch          string
	MacOSVersion  string
	LinuxDistro   string // PRETTY_NAME from /etc/os-release (Linux only)
	KernelVersion string // uname -r (Linux only)
	TmuxVersion   string
	ClaudeVersion string
	CodexVersion  string
	GhVersion     string
	Config        string
	SessionCount  int
	RecentErrors  []string // pre-formatted from ErrorHistory
	RecentActions []string // pre-formatted from ActionLog
	RecentLogs    string   // last 100 lines of debug.log

	// Terminal environment (helps diagnose rendering/scrolling issues).
	TerminalEnv TerminalEnv
	TUIWidth    int // Bubble Tea reported width
	TUIHeight   int // Bubble Tea reported height
}

// TerminalEnv captures terminal-related environment and settings.
type TerminalEnv struct {
	TERM               string
	TermProgram        string // $TERM_PROGRAM (e.g. iTerm2, Apple_Terminal, tmux)
	TermProgramVersion string // $TERM_PROGRAM_VERSION
	ColorTerm          string // $COLORTERM (e.g. truecolor)
	Lang               string // $LANG
	LCAll              string // $LC_ALL
	InsideTmux         bool   // $TMUX is set (nested tmux)
	InsideSSH          bool   // $SSH_TTY or $SSH_CLIENT is set
	TmuxDefaultTerm    string // tmux show-option -gv default-terminal
	TmuxMouse          string // tmux show-option -gv mouse
	SttySize           string // rows cols from stty (space-separated)
}

// Collect gathers system diagnostics.
func Collect(version string, sessionCount int) *Report {
	r := &Report{
		Version:      version,
		GoVersion:    runtime.Version(),
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		SessionCount: sessionCount,
	}

	if runtime.GOOS == "linux" {
		r.LinuxDistro = OSReleasePrettyName()
		r.KernelVersion = runCmd("uname", "-r")
	} else {
		r.MacOSVersion = runCmd("sw_vers", "-productVersion")
	}
	r.TmuxVersion = runCmd("tmux", "-V")
	r.ClaudeVersion = runCmd("claude", "--version")
	r.CodexVersion = firstLine(runCmd("codex", "--version"))
	r.GhVersion = firstLine(runCmd("gh", "--version"))

	r.TerminalEnv = collectTerminalEnv()

	r.Config = readConfig()
	r.RecentLogs = readRecentLogs(100)

	return r
}

// collectTerminalEnv gathers terminal-related environment variables and tmux settings.
func collectTerminalEnv() TerminalEnv {
	env := TerminalEnv{
		TERM:               os.Getenv("TERM"),
		TermProgram:        os.Getenv("TERM_PROGRAM"),
		TermProgramVersion: os.Getenv("TERM_PROGRAM_VERSION"),
		ColorTerm:          os.Getenv("COLORTERM"),
		Lang:               os.Getenv("LANG"),
		LCAll:              os.Getenv("LC_ALL"),
		InsideTmux:         os.Getenv("TMUX") != "",
		InsideSSH:          os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CLIENT") != "",
	}

	// Get terminal size via stty.
	env.SttySize = runCmd("stty", "size")

	// Get tmux global settings relevant to rendering.
	env.TmuxDefaultTerm = runCmd("tmux", "show-option", "-gv", "default-terminal")
	env.TmuxMouse = runCmd("tmux", "show-option", "-gv", "mouse")

	return env
}

// OSSummary is the single source for the human-readable OS description —
// "macOS 15.1", "Ubuntu 24.04.4 LTS", or the bare GOOS as a fallback. Both
// the bug-report dialog and the markdown issue body render through this;
// they used to hand-roll separate switches over the same fields and drifted
// (the dialog shipped without the Linux case). Kernel version is deliberately
// not included — WSL kernel strings are long and would crowd the dialog's
// one-liner; the markdown body carries it on its own line.
func (r *Report) OSSummary() string {
	switch {
	case r.MacOSVersion != "":
		return "macOS " + r.MacOSVersion
	case r.LinuxDistro != "":
		return r.LinuxDistro
	default:
		return r.OS
	}
}

// FormatMarkdownWithDesc formats the report with a user-provided description.
func (r *Report) FormatMarkdownWithDesc(description string) string {
	return r.formatMarkdown(description)
}

// FormatMarkdown formats the report as a GitHub issue body.
func (r *Report) FormatMarkdown() string {
	return r.formatMarkdown("")
}

func (r *Report) formatMarkdown(description string) string {
	home, _ := os.UserHomeDir()
	sanitize := func(s string) string {
		if home != "" {
			return strings.ReplaceAll(s, home, "~")
		}
		return s
	}

	var b strings.Builder

	b.WriteString("## Bug Report\n\n")
	b.WriteString("### Description\n")
	if description != "" {
		b.WriteString(sanitize(description) + "\n\n")
	} else {
		b.WriteString("<!-- Please describe what happened -->\n\n")
	}

	// Recent Errors.
	if len(r.RecentErrors) > 0 {
		b.WriteString("### Recent Errors\n")
		b.WriteString("| Time | Error |\n|------|-------|\n")
		for _, e := range r.RecentErrors {
			b.WriteString("| " + sanitize(e) + " |\n")
		}
		b.WriteString("\n")
	}

	// Steps to Reproduce.
	if len(r.RecentActions) > 0 {
		b.WriteString("### Steps to Reproduce (last 20 actions)\n")
		b.WriteString("| Time | Action | Detail | Result |\n|------|--------|--------|--------|\n")
		for _, a := range r.RecentActions {
			b.WriteString("| " + sanitize(a) + " |\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(r.FormatEnvironmentMarkdown(true))

	return b.String()
}

// FormatEnvironmentMarkdown renders everything about the machine — versions,
// terminal environment, and optionally the debug log and config — with no
// description, errors, or action log. Split out of formatMarkdown so a report
// that supplies its own narrative (the wrong-status report, whose evidence is a
// signals table) can append the environment without a second "## Bug Report"
// heading and an empty description placeholder underneath it.
//
// includeLogs gates the debug log, which is the reporter's content: a
// wrong-status report offers to leave content out, and the global log tail must
// honor that choice rather than smuggling it back in.
func (r *Report) FormatEnvironmentMarkdown(includeLogs bool) string {
	home, _ := os.UserHomeDir()
	sanitize := func(s string) string {
		if home != "" {
			return strings.ReplaceAll(s, home, "~")
		}
		return s
	}

	var b strings.Builder

	// Diagnostics.
	b.WriteString("### Diagnostics\n")
	fmt.Fprintf(&b, "- **Version**: %s\n", r.Version)
	fmt.Fprintf(&b, "- **OS**: %s (%s)\n", r.OSSummary(), r.Arch)
	if r.KernelVersion != "" {
		fmt.Fprintf(&b, "- **Kernel**: %s\n", r.KernelVersion)
	}
	if r.TmuxVersion != "" {
		fmt.Fprintf(&b, "- **tmux**: %s\n", r.TmuxVersion)
	}
	if r.ClaudeVersion != "" {
		fmt.Fprintf(&b, "- **Claude CLI**: %s\n", sanitize(r.ClaudeVersion))
	}
	if r.CodexVersion != "" {
		fmt.Fprintf(&b, "- **Codex CLI**: %s\n", sanitize(r.CodexVersion))
	}
	if r.GhVersion != "" {
		fmt.Fprintf(&b, "- **gh CLI**: %s\n", r.GhVersion)
	}
	fmt.Fprintf(&b, "- **Sessions**: %d\n", r.SessionCount)
	b.WriteString("\n")

	// Terminal environment.
	te := r.TerminalEnv
	b.WriteString("### Terminal Environment\n")
	fmt.Fprintf(&b, "- **TERM**: `%s`\n", te.TERM)
	if te.TermProgram != "" {
		ver := te.TermProgram
		if te.TermProgramVersion != "" {
			ver += " " + te.TermProgramVersion
		}
		fmt.Fprintf(&b, "- **Terminal**: %s\n", ver)
	}
	if te.ColorTerm != "" {
		fmt.Fprintf(&b, "- **COLORTERM**: %s\n", te.ColorTerm)
	}
	if te.SttySize != "" {
		fmt.Fprintf(&b, "- **stty size**: %s\n", te.SttySize)
	}
	if r.TUIWidth > 0 || r.TUIHeight > 0 {
		fmt.Fprintf(&b, "- **TUI size**: %dx%d\n", r.TUIWidth, r.TUIHeight)
	}
	if te.Lang != "" {
		fmt.Fprintf(&b, "- **LANG**: %s\n", te.Lang)
	}
	if te.LCAll != "" {
		fmt.Fprintf(&b, "- **LC_ALL**: %s\n", te.LCAll)
	}
	if te.InsideTmux {
		b.WriteString("- **Nested tmux**: yes ($TMUX is set)\n")
	}
	if te.InsideSSH {
		b.WriteString("- **SSH session**: yes\n")
	}
	if te.TmuxDefaultTerm != "" {
		fmt.Fprintf(&b, "- **tmux default-terminal**: `%s`\n", te.TmuxDefaultTerm)
	}
	if te.TmuxMouse != "" {
		fmt.Fprintf(&b, "- **tmux mouse**: %s\n", te.TmuxMouse)
	}
	b.WriteString("\n")

	// Debug logs.
	if includeLogs && r.RecentLogs != "" {
		b.WriteString("<details><summary>Debug Log (last 100 lines)</summary>\n\n```\n")
		b.WriteString(sanitize(r.RecentLogs))
		b.WriteString("\n```\n</details>\n\n")
	}

	// Config.
	if r.Config != "" {
		b.WriteString("<details><summary>Config</summary>\n\n```json\n")
		b.WriteString(sanitize(r.Config))
		b.WriteString("\n```\n</details>\n")
	}

	return b.String()
}

func runCmd(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// OSReleasePrettyName returns PRETTY_NAME from /etc/os-release (os-release(5)),
// e.g. `Ubuntu 24.04.1 LTS`, or "" when unavailable. Exported because
// internal/analytics reports the same distro string.
//
// os-release(5): "/etc/os-release takes precedence over /usr/lib/os-release.
// Applications should check for the former, and exclusively use its data if
// it exists, and only fall back to /usr/lib/os-release if /etc/os-release
// does not exist." Most distros symlink the former to the latter, but some
// minimal/container images ship only /usr/lib/os-release.
func OSReleasePrettyName() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		data, err = os.ReadFile("/usr/lib/os-release")
		if err != nil {
			return ""
		}
	}
	return parseOSReleasePrettyName(string(data))
}

// parseOSReleasePrettyName is the pure parser behind OSReleasePrettyName.
// os-release(5) values are shell-compatible: unquoted, double- or
// single-quoted all occur in the wild (Alpine single-quotes).
func parseOSReleasePrettyName(data string) string {
	for line := range strings.SplitSeq(data, "\n") {
		v, ok := strings.CutPrefix(line, "PRETTY_NAME=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		return v
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func readConfig() string {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".config", "fleet", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func readRecentLogs(n int) string {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".config", "fleet", "debug.log")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
