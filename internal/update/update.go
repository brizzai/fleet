package update

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	repo             = "brizzai/fleet"
	apiURL           = "https://api.github.com/repos/" + repo + "/releases/latest"
	checkCacheFile   = "last_update_check"
	checkIntervalSec = 3600 // 1 hour
)

type releaseInfo struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"` // API URL for downloading
}

// configDir returns ~/.config/fleet/.
func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fleet")
}

// ShouldCheck returns true if enough time has passed since the last check.
func ShouldCheck() bool {
	path := filepath.Join(configDir(), checkCacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return true
	}
	return time.Now().Unix()-ts >= checkIntervalSec
}

// saveCheckTime writes the current timestamp to the cache file.
func saveCheckTime() {
	path := filepath.Join(configDir(), checkCacheFile)
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0600)
}

// ghToken returns a GitHub token from env or gh CLI.
func ghToken() string {
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token
	}
	// Try gh CLI auth token.
	out, err := exec.Command("gh", "auth", "token").Output()
	if err == nil {
		if t := strings.TrimSpace(string(out)); t != "" {
			return t
		}
	}
	return ""
}

// checkLatestRelease fetches the latest release info from GitHub.
func checkLatestRelease() (*releaseInfo, error) {
	client := &http.Client{Timeout: 3 * time.Second}

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := ghToken(); token != "" {
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

	var release releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}
	return &release, nil
}

// CheckLatest returns the latest release tag if it's newer than currentVersion.
// Returns "" if already up to date or on error.
func CheckLatest(currentVersion string) (string, error) {
	release, err := checkLatestRelease()
	if err != nil {
		return "", err
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	current := strings.TrimPrefix(currentVersion, "v")

	if latest == current {
		return "", nil
	}

	return release.TagName, nil
}

// Update checks for a newer version and replaces the current binary.
// Returns the new version tag, or "" if already up to date.
func Update(currentVersion string) (string, error) {
	release, err := checkLatestRelease()
	if err != nil {
		return "", err
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	current := strings.TrimPrefix(currentVersion, "v")
	if latest == current {
		saveCheckTime()
		return "", nil
	}

	archiveName := fmt.Sprintf("fleet_%s_%s_%s.tar.gz", latest, runtime.GOOS, runtime.GOARCH)
	assetURL := findAssetURL(release.Assets, archiveName)
	if assetURL == "" {
		return "", fmt.Errorf("asset %s not found in release", archiveName)
	}

	binaryData, err := downloadAsset(assetURL)
	if err != nil {
		return "", err
	}

	if err := replaceBinary(binaryData); err != nil {
		return "", err
	}

	saveCheckTime()
	return release.TagName, nil
}

// findAssetURL returns the API URL for the named asset, or "" if not found.
func findAssetURL(assets []releaseAsset, name string) string {
	for _, a := range assets {
		if a.Name == name {
			return a.URL
		}
	}
	return ""
}

// downloadAsset downloads a release asset and extracts the binary from the tar.gz archive.
func downloadAsset(assetURL string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", assetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	if token := ghToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download returned %d", resp.StatusCode)
	}

	data, err := extractBinary(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("extract failed: %w", err)
	}
	return data, nil
}

// replaceBinary atomically replaces the current executable with new binary data.
func replaceBinary(binaryData []byte) error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return err
	}

	dir := filepath.Dir(exePath)
	tmp, err := os.CreateTemp(dir, "fleet-update-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(binaryData); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	tmp.Close()

	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return err
	}

	if err := os.Rename(tmpPath, exePath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// extractBinary reads a tar.gz stream and returns the fleet binary contents.
func extractBinary(r io.Reader) ([]byte, error) {
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
