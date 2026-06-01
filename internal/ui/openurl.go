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
	return exec.Command(bin, url).Start()
}
