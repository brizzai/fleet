// brizz-code-shim is a deprecation wrapper for users still on the legacy
// `brizz-code` binary. It is packaged inside `brizz-code_*.tar.gz` archives
// on the renamed `brizzai/fleet` releases so that v1.x auto-updates land
// here instead of failing.
//
// Behavior on invocation:
//   - Print a deprecation warning (rate-limited to once per 24h).
//   - If `fleet` is on PATH, exec it with the same args.
//   - Else if the shim lives under a Homebrew prefix, print brew install
//     instructions (we don't want to drop a non-brew-managed binary into
//     a brew-managed directory).
//   - Else download the latest fleet release, verify its checksum, and
//     install it next to the shim, then exec it.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

const (
	fleetRepo     = "brizzai/fleet"
	apiLatest     = "https://api.github.com/repos/" + fleetRepo + "/releases/latest"
	warnStateName = "brizz-code-deprecation-warned"
	warnInterval  = 24 * time.Hour
)

func main() {
	printDeprecationWarning()

	if path, err := exec.LookPath("fleet"); err == nil {
		execFleet(path)
	}

	exePath, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}

	if isBrewPath(exePath) {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Install fleet via Homebrew:")
		fmt.Fprintln(os.Stderr, "  brew install brizzai/tap/fleet")
		if exePath != "" {
			fmt.Fprintf(os.Stderr, "  rm -f %q\n", exePath)
		}
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "Installing fleet…")
	installed, err := installFleetNextTo(exePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Auto-install failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Install fleet manually:")
		fmt.Fprintln(os.Stderr, "  brew install brizzai/tap/fleet")
		fmt.Fprintln(os.Stderr, "  # or")
		fmt.Fprintln(os.Stderr, "  curl -fsSL https://raw.githubusercontent.com/brizzai/fleet/master/install.sh | bash")
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Installed fleet to %s\n", installed)
	execFleet(installed)
}

func execFleet(path string) {
	args := append([]string{path}, os.Args[1:]...)
	if err := syscall.Exec(path, args, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to exec fleet: %v\n", err)
		os.Exit(1)
	}
}

func printDeprecationWarning() {
	if !shouldWarn() {
		return
	}
	fmt.Fprintf(os.Stderr, "⚠  brizz-code has been renamed to fleet (shim %s).\n", version)
	fmt.Fprintln(os.Stderr, "   Switch to the `fleet` command — this wrapper will be removed in a future release.")
	fmt.Fprintln(os.Stderr, "")
	recordWarning()
}

func warnStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "fleet", warnStateName)
}

func shouldWarn() bool {
	path := warnStatePath()
	if path == "" {
		return true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		return true
	}
	return time.Since(ts) >= warnInterval
}

func recordWarning() {
	path := warnStatePath()
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, []byte(time.Now().Format(time.RFC3339)), 0600)
}

func isBrewPath(p string) bool {
	if p == "" {
		return false
	}
	staticPrefixes := []string{
		"/opt/homebrew/",
		"/usr/local/Homebrew/",
		"/usr/local/Cellar/",
		"/opt/homebrew/Cellar/",
		"/home/linuxbrew/",
	}
	for _, pre := range staticPrefixes {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	if out, err := exec.Command("brew", "--prefix").Output(); err == nil {
		prefix := strings.TrimSpace(string(out))
		if prefix != "" && strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

type releaseInfo struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func installFleetNextTo(brizzExe string) (string, error) {
	if brizzExe == "" {
		return "", fmt.Errorf("could not determine install location")
	}
	targetDir := filepath.Dir(brizzExe)

	rel, err := fetchLatestRelease()
	if err != nil {
		return "", fmt.Errorf("fetch release: %w", err)
	}
	ver := strings.TrimPrefix(rel.TagName, "v")
	archiveName := fmt.Sprintf("fleet_%s_darwin_%s.tar.gz", ver, runtime.GOARCH)

	var archiveURL, checksumsURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case archiveName:
			archiveURL = a.URL
		case "checksums.txt":
			checksumsURL = a.URL
		}
	}
	if archiveURL == "" {
		return "", fmt.Errorf("asset %s not found in release %s", archiveName, rel.TagName)
	}

	archiveData, err := httpGet(archiveURL)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", archiveName, err)
	}

	if checksumsURL != "" {
		if sums, err := httpGet(checksumsURL); err == nil {
			if want := lookupChecksum(string(sums), archiveName); want != "" {
				sum := sha256.Sum256(archiveData)
				got := hex.EncodeToString(sum[:])
				if got != want {
					return "", fmt.Errorf("checksum mismatch for %s: want %s got %s", archiveName, want, got)
				}
			}
		}
	}

	bin, err := extractFleetBinary(bytes.NewReader(archiveData))
	if err != nil {
		return "", fmt.Errorf("extract: %w", err)
	}

	targetPath := filepath.Join(targetDir, "fleet")
	tmp, err := os.CreateTemp(targetDir, "fleet-install-*")
	if err != nil {
		return "", fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", err
	}
	tmp.Close()
	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	return targetPath, nil
}

func fetchLatestRelease() (*releaseInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", apiLatest, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}
	var rel releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func httpGet(url string) ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func lookupChecksum(text, name string) string {
	for line := range strings.SplitSeq(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			return fields[0]
		}
	}
	return ""
}

func extractFleetBinary(r io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(hdr.Name) == "fleet" && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("fleet binary not found in archive")
}
