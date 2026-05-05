#!/bin/sh
set -e

REPO="farhan-helmy/sidegent-sandbox"
BINARY="sidegent"
INSTALL_DIR="/usr/local/bin"

# Detect OS
OS="$(uname -s)"
case "$OS" in
    Linux)  OS="linux" ;;
    Darwin) OS="darwin" ;;
    *)      echo "Error: Unsupported OS: $OS"; exit 1 ;;
esac

# Detect architecture
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64)  ARCH="amd64" ;;
    aarch64) ARCH="arm64" ;;
    arm64)   ARCH="arm64" ;;
    *)       echo "Error: Unsupported architecture: $ARCH"; exit 1 ;;
esac

# Get latest version
echo "Fetching latest version..."
VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)"
if [ -z "$VERSION" ]; then
    echo "Error: Could not determine latest version"
    exit 1
fi
echo "Latest version: $VERSION"

# Download
FILENAME="${BINARY}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${FILENAME}"

echo "Downloading ${FILENAME}..."
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

curl -fsSL "$URL" -o "${TMP_DIR}/${FILENAME}"

# Extract
tar -xzf "${TMP_DIR}/${FILENAME}" -C "$TMP_DIR"

# Install
if [ -w "$INSTALL_DIR" ]; then
    mv "${TMP_DIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
else
    echo "Installing to ${INSTALL_DIR} (requires sudo)..."
    sudo mv "${TMP_DIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
fi

chmod +x "${INSTALL_DIR}/${BINARY}"

echo ""
echo "sidegent ${VERSION} installed to ${INSTALL_DIR}/${BINARY}"
echo ""

# Check Docker
if ! command -v docker >/dev/null 2>&1; then
    echo "Warning: Docker is not installed. sidegent requires Docker to run."
    echo "Install Docker: https://docs.docker.com/get-docker/"
elif ! docker info >/dev/null 2>&1; then
    echo "Warning: Docker is not running. Start Docker before using sidegent."
else
    echo "Docker detected. You're ready to go!"
fi

echo ""
echo "Get started:"
echo "  sidegent run 'print(\"hello world\")'"
echo "  sidegent serve"
echo "  sidegent --help"
