#!/bin/sh
# gw installer — downloads the prebuilt binary from GitHub releases.
#   curl -fsSL https://raw.githubusercontent.com/liu1700/gw/main/install.sh | sh
# Options via env:
#   GW_INSTALL_DIR   target directory (default: ~/.local/bin)
#   GW_VERSION       tag to install, e.g. v0.2.0 (default: latest release)
set -eu

REPO="liu1700/gw"
DIR="${GW_INSTALL_DIR:-$HOME/.local/bin}"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  darwin|linux) ;;
  *) echo "gw: unsupported OS: $OS (darwin and linux only for now)" >&2; exit 1 ;;
esac

ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "gw: unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

if [ -z "${GW_VERSION:-}" ]; then
  GW_VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep '"tag_name"' | head -1 | cut -d'"' -f4)
fi
[ -n "$GW_VERSION" ] || { echo "gw: could not determine latest release" >&2; exit 1; }

URL="https://github.com/$REPO/releases/download/$GW_VERSION/gw_${GW_VERSION#v}_${OS}_${ARCH}.tar.gz"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "gw: downloading $URL"
curl -fsSL "$URL" | tar -xz -C "$TMP" gw

mkdir -p "$DIR"
install -m 0755 "$TMP/gw" "$DIR/gw"
echo "gw: installed $("$DIR/gw" version) to $DIR/gw"

case ":$PATH:" in
  *":$DIR:"*) ;;
  *) echo "gw: note — $DIR is not in your PATH; add:  export PATH=\"$DIR:\$PATH\"" ;;
esac
