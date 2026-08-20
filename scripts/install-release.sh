#!/usr/bin/env bash
# Install a released paseo-conductor binary for this OS/arch.
#
# The repo is private, so this uses the authenticated `gh` CLI to fetch release
# assets. This script is mirrored to a public gist so it can be curl'd:
#
#   curl -fsSL https://gist.githubusercontent.com/danielcbaldwin/3504ed91ac1014b3f073f44916e443a7/raw/install-release.sh | bash
#
# Optional: pin a version, e.g. `... | bash -s -- v0.2.1`.
# NOTE: after editing this file, re-publish the gist: scripts/publish-installer-gist.sh
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

# Seed a valid starter config + secrets if missing; drop the full example as reference.
CFG_DIR="${PASEO_CONDUCTOR_CFG_DIR:-$HOME/.config/paseo-conductor}"
mkdir -p "$CFG_DIR" "$HOME/.local/state/paseo-conductor"
gh api "repos/$REPO/contents/config.example.yaml" -H "Accept: application/vnd.github.raw" \
  >"$CFG_DIR/config.example.yaml" 2>/dev/null || true
if [ ! -f "$CFG_DIR/config.yaml" ]; then
  gh api "repos/$REPO/contents/config.starter.yaml" -H "Accept: application/vnd.github.raw" >"$CFG_DIR/config.yaml" 2>/dev/null \
    && echo "==> wrote starter config to $CFG_DIR/config.yaml (github integration is disabled until you configure it)" || true
fi
if [ ! -f "$CFG_DIR/conductor.env" ]; then
  printf '%s\n' '# Secrets referenced by config.yaml via ${...}. Keep private (chmod 600).' \
    'GH_WEBHOOK_SECRET=' 'GH_SMEE_URL=https://smee.io/CHANGE_ME' >"$CFG_DIR/conductor.env"
  chmod 600 "$CFG_DIR/conductor.env"
  echo "==> wrote $CFG_DIR/conductor.env (fill in the secrets)"
fi

echo "Edit $CFG_DIR/config.yaml + conductor.env, then optionally install the service:"
# Offer to install the background service (systemd/launchd), prompting first.
gh api "repos/$REPO/contents/scripts/service.sh" -H "Accept: application/vnd.github.raw" 2>/dev/null \
  | bash -s -- "$dest" "$CFG_DIR" || echo "(run the installer's service step later: scripts/service.sh)"
