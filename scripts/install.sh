#!/usr/bin/env bash
# Build conductor from source, seed config, and optionally install the
# per-user background service (systemd on Linux, launchd on macOS).
#
# Fleet-safe: an existing paseo-conductor install (config/state dir from
# before the rebrand) keeps being used in place; only a fresh install gets
# the new conductor-named paths.
set -euo pipefail

BIN_DIR="${HOME}/.local/bin"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [ -d "${HOME}/.config/conductor" ]; then
  CFG_DIR="${HOME}/.config/conductor"
elif [ -d "${HOME}/.config/paseo-conductor" ]; then
  CFG_DIR="${HOME}/.config/paseo-conductor"
else
  CFG_DIR="${HOME}/.config/conductor"
fi

if [ -d "${HOME}/.local/state/conductor" ]; then
  STATE_DIR="${HOME}/.local/state/conductor"
elif [ -d "${HOME}/.local/state/paseo-conductor" ]; then
  STATE_DIR="${HOME}/.local/state/paseo-conductor"
else
  STATE_DIR="${HOME}/.local/state/conductor"
fi

if [ -f "${BIN_DIR}/paseo-conductor" ] && [ ! -f "${BIN_DIR}/conductor" ]; then
  BIN_NAME="paseo-conductor"
else
  BIN_NAME="conductor"
fi
BIN="${BIN_DIR}/${BIN_NAME}"

mkdir -p "$BIN_DIR" "$CFG_DIR" "$STATE_DIR"

echo "==> building $BIN_NAME"
(cd "$here" && CGO_ENABLED=0 go build -o "$BIN" ./cmd/paseo-conductor)

install -m 0644 "$here/config.example.yaml" "$CFG_DIR/config.example.yaml"
if [ ! -f "$CFG_DIR/config.yaml" ]; then
  echo "==> writing starter config to $CFG_DIR/config.yaml (github integration disabled until configured)"
  install -m 0644 "$here/config.starter.yaml" "$CFG_DIR/config.yaml"
fi

if [ ! -f "$CFG_DIR/conductor.env" ]; then
  cat >"$CFG_DIR/conductor.env" <<'EOF'
# Secrets referenced by config.yaml via ${...}. Keep this file private (chmod 600).
GH_WEBHOOK_SECRET=
GH_SMEE_URL=https://smee.io/CHANGE_ME
EOF
  chmod 600 "$CFG_DIR/conductor.env"
  echo "==> wrote $CFG_DIR/conductor.env (fill in the secrets)"
fi

cat <<EOF

Before starting, edit:
  - $CFG_DIR/config.yaml   (app_id, repos, rules)
  - $CFG_DIR/conductor.env (secrets)
  - drop your GitHub App private key at the path in config
EOF

# Offer to install the background service (prompts first).
bash "$here/scripts/service.sh" "$BIN" "$CFG_DIR"
