#!/usr/bin/env bash
# fleet installer (requires gh CLI)
# Usage: curl -fsSL https://raw.githubusercontent.com/brizzai/fleet/master/install.sh | bash
#    or: gh repo clone brizzai/fleet /tmp/fleet && bash /tmp/fleet/install.sh

set -euo pipefail

REPO="brizzai/fleet"
INSTALL_DIR="${HOME}/.local/bin"
VERSION=""

# Parse args
while [[ $# -gt 0 ]]; do
    case $1 in
        --version) VERSION="$2"; shift 2 ;;
        --dir) INSTALL_DIR="$2"; shift 2 ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

# Require gh CLI
if ! command -v gh &>/dev/null; then
    echo "Error: gh CLI is required. Install it: brew install gh"
    exit 1
fi

if ! gh auth status &>/dev/null; then
    echo "Error: gh CLI is not authenticated. Run: gh auth login"
    exit 1
fi

# Detect OS (macOS or Linux)
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
    darwin|linux) ;;
    *) echo "Error: fleet supports macOS and Linux only (got: $OS)"; exit 1 ;;
esac

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) echo "Error: Unsupported architecture: $ARCH"; exit 1 ;;
esac

echo "fleet installer"
echo "Platform: ${OS}/${ARCH}"

# Resolve version
if [[ -z "$VERSION" ]]; then
    VERSION=$(gh release view --repo "$REPO" --json tagName --jq '.tagName' 2>/dev/null || true)
    if [[ -z "$VERSION" ]]; then
        echo "Error: Could not determine latest version. Check repo access."
        exit 1
    fi
fi

VERSION_NUM="${VERSION#v}"
echo "Version: ${VERSION}"

# Download
ARCHIVE_NAME="fleet_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

echo "Downloading ${ARCHIVE_NAME}..."
gh release download "$VERSION" --repo "$REPO" --pattern "$ARCHIVE_NAME" --dir "$TMP_DIR"

# Extract and install
tar -xzf "$TMP_DIR/$ARCHIVE_NAME" -C "$TMP_DIR"
mkdir -p "$INSTALL_DIR"
mv "$TMP_DIR/fleet" "$INSTALL_DIR/fleet"
chmod +x "$INSTALL_DIR/fleet"

# Check PATH
if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
    echo ""
    echo "Note: $INSTALL_DIR is not in your PATH."
    echo "Add to your shell config:"
    echo "  export PATH=\"\$HOME/.local/bin:\$PATH\""
fi

# Check dependencies
echo ""
if command -v tmux &>/dev/null; then
    echo "$(tmux -V 2>/dev/null) [OK]"
else
    echo "Warning: tmux is not installed (required)"
    echo "  brew install tmux"
fi

if command -v git &>/dev/null; then
    echo "git $(git --version 2>/dev/null | cut -d' ' -f3) [OK]"
else
    echo "Warning: git is not installed (required)"
fi

echo ""
echo "Installed fleet ${VERSION} to ${INSTALL_DIR}/fleet"
echo ""
echo "Get started:"
echo "  fleet           # Launch TUI"
echo "  fleet --version # Check version"
