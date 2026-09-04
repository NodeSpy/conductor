#!/usr/bin/env bash
# Build conductor from source, seed config, and optionally install the
# per-user background service (systemd on Linux, launchd on macOS).
set -euo pipefail

BIN_DIR="${HOME}/.local/bin"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

CFG_DIR="${HOME}/.config/conductor"
STATE_DIR="${HOME}/.local/state/conductor"
BIN_NAME="conductor"
BIN="${BIN_DIR}/${BIN_NAME}"

mkdir -p "$BIN_DIR" "$CFG_DIR" "$STATE_DIR"
# The default split layout: each section imports from its conf.d/ folder.
mkdir -p "$CFG_DIR"/conf.d/{connectors,runtimes,hosts,agents,workflows,triggers}

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
