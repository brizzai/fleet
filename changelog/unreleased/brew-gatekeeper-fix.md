---
type: fixed
---
`brew install brizzai/tap/fleet` no longer trips macOS Gatekeeper on first launch — the cask now strips the `com.apple.quarantine` attribute via a postflight hook. Also fixed the legacy `brizz-code` shim's brew-path message to point at `brew uninstall brizz-code` + `brew install brizzai/tap/fleet` instead of the incorrect `rm -f` line.
