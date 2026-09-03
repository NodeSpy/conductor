#!/usr/bin/env bash
# Install a released conductor binary for this OS/arch. The repo is public, so this
# needs no auth — just curl. It lives in the repo and is run straight from it:
#
#   curl -fsSL https://raw.githubusercontent.com/NodeSpy/conductor/main/scripts/install-release.sh | bash
#
# Optional: pin a version, e.g. `... | bash -s -- v0.6.4`.
set -euo pipefail

REPO="${CONDUCTOR_REPO:-NodeSpy/conductor}"
BIN_DIR="${CONDUCTOR_BIN_DIR:-$HOME/.local/bin}"
RAW="https://raw.githubusercontent.com/$REPO/main"
NAME="conductor"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  i386 | i686) arch=386 ;;
  *) echo "error: unsupported arch $(uname -m)" >&2; exit 1 ;;
esac
asset="${NAME}_${os}_${arch}"

# GitHub's stable download URLs for public releases (no gh, no auth).
if [ "${1:-}" != "" ]; then
  url="https://github.com/$REPO/releases/download/$1/$asset"
else
  url="https://github.com/$REPO/releases/latest/download/$asset"
fi

mkdir -p "$BIN_DIR"
dest="$BIN_DIR/$NAME"
echo "==> installing $asset -> $dest"
curl -fsSL "$url" -o "$dest"
chmod +x "$dest"

"$dest" version || true
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "note: add $BIN_DIR to your PATH" ;;
esac

# Seed a valid starter config + secrets if missing; drop the full example as reference.
CFG_DIR="${CONDUCTOR_CFG_DIR:-$HOME/.config/$NAME}"
mkdir -p "$CFG_DIR" "$HOME/.local/state/$NAME"
curl -fsSL "$RAW/config.example.yaml" -o "$CFG_DIR/config.example.yaml" 2>/dev/null || true
if [ ! -f "$CFG_DIR/config.yaml" ]; then
  curl -fsSL "$RAW/config.starter.yaml" -o "$CFG_DIR/config.yaml" 2>/dev/null \
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
curl -fsSL "$RAW/scripts/service.sh" 2>/dev/null | bash -s -- "$dest" "$CFG_DIR" \
  || echo "(run the service step later: scripts/service.sh)"
