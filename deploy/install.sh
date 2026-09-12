#!/bin/sh
# TunnelX Client Installer (Linux & macOS)
# Usage: curl -fsSL https://tl.codesky.tech/install.sh | bash

set -e

REPO="Sirtheprogrammer/tunnel"
BINARY_NAME="tunnelx"

# 1. Detect OS
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  linux*)  OS="linux" ;;
  darwin*) OS="darwin" ;;
  *) echo "Error: Unsupported operating system '$OS'. Windows users can run: irm https://tl.codesky.tech/install.ps1 | iex"; exit 1 ;;
esac

# 2. Detect Architecture
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "Error: Unsupported architecture '$ARCH'"; exit 1 ;;
esac

echo "=> Installing TunnelX CLI for ${OS}-${ARCH}..."

# 3. Locate Target Installation Directory
INSTALL_DIR="/usr/local/bin"
if [ ! -w "$INSTALL_DIR" ]; then
  if [ "$(id -u)" -ne 0 ]; then
    INSTALL_DIR="$HOME/.local/bin"
    mkdir -p "$INSTALL_DIR"
  fi
fi

# 4. Fetch Latest Release Version
# Resolve via the GitHub redirect (releases/latest -> releases/tag/<version>) instead
# of the api.github.com JSON endpoint, which is capped at 60 unauthenticated
# requests/hour per IP and gets exhausted easily (shared NAT/proxy, CI runners, etc.).
# When that happens this must NOT silently fall back to a hardcoded version, since a
# stale version will 404 once a newer release is published.
EFFECTIVE_URL="$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest" 2>/dev/null)" || EFFECTIVE_URL=""
VERSION="${EFFECTIVE_URL##*/}"

if [ -z "$VERSION" ]; then
  echo "Error: Could not determine the latest release version for ${REPO}." >&2
  echo "Check your network connection, or install manually from: https://github.com/${REPO}/releases" >&2
  exit 1
fi

DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${BINARY_NAME}-${VERSION}-${OS}-${ARCH}.tar.gz"

echo "=> Downloading from $DOWNLOAD_URL..."
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

if curl -fsSL "$DOWNLOAD_URL" -o "$TMP_DIR/${BINARY_NAME}.tar.gz"; then
  tar -xzf "$TMP_DIR/${BINARY_NAME}.tar.gz" -C "$TMP_DIR"
  
  # Search inside unpacked folder
  FOUND_BIN="$(find "$TMP_DIR" -type f -name "$BINARY_NAME" | head -n 1)"
  if [ -n "$FOUND_BIN" ]; then
    chmod +x "$FOUND_BIN"
    mv "$FOUND_BIN" "$INSTALL_DIR/$BINARY_NAME"
    echo "✓ Successfully installed $BINARY_NAME to $INSTALL_DIR/$BINARY_NAME"
    echo ""
    echo "Next steps:"
    echo "  1. tunnelx login <YOUR_TOKEN> --server tl.codesky.tech:7835"
    echo "  2. tunnelx http 3000"
  else
    echo "Error: Binary not found in downloaded archive"
    exit 1
  fi
else
  echo "=> Pre-compiled release not found. Installing via Go toolchain..."
  go install "tunnel/cmd/${BINARY_NAME}@latest"
  echo "✓ Installed via go install"
fi

