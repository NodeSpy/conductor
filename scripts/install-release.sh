#!/usr/bin/env bash
# Install a released paseo-conductor binary for this OS/arch.
#
# The repo is private, so this uses the authenticated `gh` CLI to fetch release
# assets. One-liner:
#   gh api repos/NodeSpy/paseo-conductor/contents/scripts/install-release.sh \
#     -H "Accept: application/vnd.github.raw" | bash
#
# Optional: pass a tag to pin a version, e.g. `... | bash -s v0.2.0`.
set -euo pipefail

REPO="${PASEO_CONDUCTOR_REPO:-NodeSpy/paseo-conductor}"
BIN_DIR="${PASEO_CONDUCTOR_BIN_DIR:-$HOME/.local/bin}"

command -v gh >/dev/null 2>&1 || {
  echo "error: this installer needs the GitHub CLI (gh), authenticated (gh auth login)." >&2
  echo "       https://cli.github.com" >&2
  exit 1
}

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  i386 | i686) arch=386 ;;
  *) echo "error: unsupported arch $(uname -m)" >&2; exit 1 ;;
esac
asset="paseo-conductor_${os}_${arch}"

tag="${1:-$(gh release view --repo "$REPO" --json tagName --jq .tagName)}"
[ -n "$tag" ] || { echo "error: no release found in $REPO" >&2; exit 1; }

mkdir -p "$BIN_DIR"
dest="$BIN_DIR/paseo-conductor"
echo "==> installing $asset ($tag) -> $dest"
gh release download "$tag" --repo "$REPO" --pattern "$asset" --output "$dest" --clobber
chmod +x "$dest"

"$dest" version || true
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "note: add $BIN_DIR to your PATH" ;;
esac
