#!/usr/bin/env bash
# Install a released conductor binary for this OS/arch, over plain git (the
# same release refs `conductor update` reads: refs/dist/<tag>/<os>_<arch>).
# It lives in the repo and is run straight from it:
#
#   curl -fsSL https://raw.githubusercontent.com/NodeSpy/conductor/main/scripts/install-release.sh | bash
#
# Optional: pin a version, e.g. `... | bash -s -- v0.6.4`. CONDUCTOR_GIT
# points at another git URL (a mirror, a private fork — your own git
# credentials are used).
set -euo pipefail

REPO="${CONDUCTOR_REPO:-NodeSpy/conductor}"
GIT_URL="${CONDUCTOR_GIT:-https://github.com/$REPO}"
BIN_DIR="${CONDUCTOR_BIN_DIR:-$HOME/.local/bin}"
RAW="https://raw.githubusercontent.com/$REPO/main"
NAME="conductor"

command -v git >/dev/null || { echo "error: git is required" >&2; exit 1; }
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  i386 | i686) arch=386 ;;
  *) echo "error: unsupported arch $(uname -m)" >&2; exit 1 ;;
esac
plat="${os}_${arch}"
asset="${NAME}_${plat}"

tag="${1:-}"
if [ -z "$tag" ]; then
  # The newest stable vX.Y.Z published for this platform.
  tag="$(git ls-remote --refs "$GIT_URL" "refs/dist/v*/$plat" \
    | sed -n "s#.*refs/dist/\(v[0-9][0-9.]*\)/$plat\$#\1#p" | sort -V | tail -n1)"
  [ -n "$tag" ] || { echo "error: no release published for $plat at $GIT_URL" >&2; exit 1; }
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
git -C "$work" init -q
git -C "$work" fetch -q --depth 1 --no-tags "$GIT_URL" "refs/dist/$tag/$plat"
git -C "$work" cat-file blob "FETCH_HEAD:$asset" >"$work/$asset"
git -C "$work" cat-file blob "FETCH_HEAD:checksums.txt" >"$work/checksums.txt"
( cd "$work" && grep "  $asset\$" checksums.txt | sha256sum -c - ) >/dev/null \
  || { echo "error: checksum mismatch for $asset $tag" >&2; exit 1; }

mkdir -p "$BIN_DIR"
dest="$BIN_DIR/$NAME"
echo "==> installing $asset $tag -> $dest"
install -m 0755 "$work/$asset" "$dest"

"$dest" version || true
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "note: add $BIN_DIR to your PATH" ;;
esac

# Seed a valid starter config + secrets if missing; drop the full example as reference.
CFG_DIR="${CONDUCTOR_CFG_DIR:-$HOME/.config/$NAME}"
mkdir -p "$CFG_DIR" "$HOME/.local/state/$NAME"
# The default split layout: each section imports from its conf.d/ folder.
mkdir -p "$CFG_DIR"/conf.d/{connectors,runtimes,hosts,agents,workflows,triggers}
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
