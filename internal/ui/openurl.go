package ui

import (
	"os/exec"
	"runtime"
)

// openURL opens a URL in the user's default browser. macOS uses `open`,
// Linux uses `xdg-open`.
func openURL(url string) error {
	bin := "open"
	if runtime.GOOS == "linux" {
		bin = "xdg-open"
	}
	cmd := exec.Command(bin, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap the short-lived launcher so it doesn't linger as a zombie in the long-running TUI.
	go func() { _ = cmd.Wait() }()
	return nil
}
