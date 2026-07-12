package chrome

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"

	"github.com/brizzai/fleet/internal/debuglog"
	"github.com/brizzai/fleet/internal/hooks"
)

const (
	nmhName       = "com.brizzai.fleet.tabcontrol"
	legacyNMHName = "com.brizzcode.tabcontrol"
)

// nmhManifest is the Native Messaging Host manifest format.
type nmhManifest struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Path           string   `json:"path"`
	Type           string   `json:"type"`
	AllowedOrigins []string `json:"allowed_origins"`
}

// nmhManifestDirs returns the NativeMessagingHosts directories for Chrome-family
// browsers on this OS. Stable Google Chrome is always included so the manifest is
// ready before Chrome's first launch; the other variants (Chromium and the
// beta/unstable channels) are included only when that browser's config dir
// already exists, so we don't create stray dirs for browsers that aren't there.
func nmhManifestDirs() []string {
	home, _ := os.UserHomeDir()

	type candidate struct {
		base string // browser config dir; "" means always install
		nmh  string // NativeMessagingHosts dir
	}
	var cands []candidate
	if runtime.GOOS == "darwin" {
		appSup := filepath.Join(home, "Library", "Application Support")
		cands = []candidate{
			{nmh: filepath.Join(appSup, "Google", "Chrome", "NativeMessagingHosts")},
			{base: filepath.Join(appSup, "Chromium"), nmh: filepath.Join(appSup, "Chromium", "NativeMessagingHosts")},
		}
	} else {
		cfg := filepath.Join(home, ".config")
		cands = []candidate{
			{nmh: filepath.Join(cfg, "google-chrome", "NativeMessagingHosts")},
			{base: filepath.Join(cfg, "chromium"), nmh: filepath.Join(cfg, "chromium", "NativeMessagingHosts")},
			{base: filepath.Join(cfg, "google-chrome-beta"), nmh: filepath.Join(cfg, "google-chrome-beta", "NativeMessagingHosts")},
			{base: filepath.Join(cfg, "google-chrome-unstable"), nmh: filepath.Join(cfg, "google-chrome-unstable", "NativeMessagingHosts")},
		}
	}

	dirs := make([]string, 0, len(cands))
	for _, c := range cands {
		if c.base != "" {
			if info, err := os.Stat(c.base); err != nil || !info.IsDir() {
				continue
			}
		}
		dirs = append(dirs, c.nmh)
	}
	return dirs
}

// InstallNativeMessagingHost writes the NMH manifest JSON into every applicable
// Chrome-family NativeMessagingHosts dir so Chrome/Chromium can find the host.
// Returns true if any manifest was written or updated.
//
// The binary path comes from hooks.FleetBinaryPath, which resolves symlinks and
// — critically — refuses to hand back a `go run` temp path. The manifest outlives
// this process, so a temp path would leave Chrome unable to spawn the host.
func InstallNativeMessagingHost() bool {
	log := debuglog.Logger

	// The native host command is the binary itself with "chrome-host" subcommand.
	hostPath := hooks.FleetBinaryPath()

	manifest := nmhManifest{
		Name:        nmhName,
		Description: "fleet Chrome tab control",
		Path:        hostPath,
		Type:        "stdio",
		AllowedOrigins: []string{
			// Extension ID derived from the key in chrome-extension/manifest.json.
			"chrome-extension://haphpcoecelhofejcklinnlbfijgdnih/",
		},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		log.Warn("chrome: cannot marshal NMH manifest", "err", err)
		return false
	}

	changed := false
	for _, dir := range nmhManifestDirs() {
		manifestPath := filepath.Join(dir, nmhName+".json")

		// Remove legacy NMH manifest if present so Chrome doesn't keep two competing entries.
		legacyPath := filepath.Join(dir, legacyNMHName+".json")
		if _, err := os.Stat(legacyPath); err == nil {
			if err := os.Remove(legacyPath); err != nil {
				log.Warn("chrome: cannot remove legacy NMH manifest", "path", legacyPath, "err", err)
			} else {
				log.Info("chrome: removed legacy NMH manifest", "path", legacyPath)
			}
		}

		// Skip if the manifest already exists with the correct path.
		if existing, err := os.ReadFile(manifestPath); err == nil {
			var m nmhManifest
			if json.Unmarshal(existing, &m) == nil && m.Path == hostPath {
				continue
			}
		}

		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Warn("chrome: cannot create NMH dir", "err", err, "dir", dir)
			continue
		}
		if err := os.WriteFile(manifestPath, data, 0644); err != nil {
			log.Warn("chrome: cannot write NMH manifest", "err", err, "path", manifestPath)
			continue
		}

		log.Info("chrome: installed NMH manifest", "path", manifestPath)
		changed = true
	}
	return changed
}
