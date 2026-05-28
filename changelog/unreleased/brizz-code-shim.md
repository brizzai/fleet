---
type: added
---
Compatibility shim for users still on the legacy `brizz-code` binary. Released as `brizz-code_<version>_darwin_<arch>.tar.gz` so v1.x auto-updates land on a small wrapper that prints a deprecation warning (rate-limited to once/day), then either execs `fleet` if installed, falls back to auto-installing the latest fleet release next to itself (verifying via `checksums.txt`), or — if running from a Homebrew prefix — points the user at `brew install brizzai/tap/fleet`.
