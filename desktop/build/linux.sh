#!/usr/bin/env bash
# Builds the Linux desktop client (AppImage + .deb) inside Docker, since the
# host doesn't have webkit2gtk-4.1 and we don't install system packages there.
#
# Usage: desktop/build/linux.sh [cargo tauri build args...]
#
# Caches and output all live on local disk (never ~/Projects, which is NFS):
#   $HOME/.cache/conductor-design/desktop-target               (CARGO_TARGET_DIR)
#   $HOME/.cache/conductor-design/desktop-registry              (cargo registry/git cache)
#   $HOME/.cache/conductor-design/desktop-app/node_modules       (desktop/app's npm install)
#   $HOME/.cache/conductor-design/desktop-out                   (AppImage/.deb output, default)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DESKTOP_DIR="$(cd "$HERE/.." && pwd)"
REPO_DIR="$(cd "$DESKTOP_DIR/.." && pwd)"

CACHE_ROOT="${CONDUCTOR_DESKTOP_CACHE:-$HOME/.cache/conductor-design}"
TARGET_DIR="$CACHE_ROOT/desktop-target"
REGISTRY_DIR="$CACHE_ROOT/desktop-registry"
APP_NODE_MODULES="$CACHE_ROOT/desktop-app/node_modules"
OUT_DIR="${CONDUCTOR_DESKTOP_OUT:-$CACHE_ROOT/desktop-out}"
IMAGE_TAG="conductor-desktop-linux-build"

mkdir -p "$TARGET_DIR" "$REGISTRY_DIR" "$APP_NODE_MODULES" "$OUT_DIR"

docker build -f "$HERE/linux.Dockerfile" -t "$IMAGE_TAG" "$HERE"

docker run --rm \
  -v "$REPO_DIR":/work:rw \
  -v "$TARGET_DIR":/cargo-target:rw \
  -v "$REGISTRY_DIR":/usr/local/cargo/registry:rw \
  -v "$APP_NODE_MODULES":/work/desktop/app/node_modules:rw \
  -v "$OUT_DIR":/out:rw \
  -e CARGO_TARGET_DIR=/cargo-target \
  -w /work/desktop/src-tauri \
  "$IMAGE_TAG" \
  bash -lc '
    set -euo pipefail
    npm --prefix /work/desktop/app install --no-audit --no-fund
    cargo tauri build "$@"
    shopt -s nullglob
    for f in /cargo-target/release/bundle/appimage/*.AppImage /cargo-target/release/bundle/deb/*.deb; do
      cp -v "$f" /out/
    done
  ' -- "$@"

echo "Output in $OUT_DIR:"
ls -la "$OUT_DIR"
