#!/usr/bin/env bash
# Build paseo-conductor from source, seed config, and optionally install the
# per-user background service (systemd on Linux, launchd on macOS).
set -euo pipefail

BIN_DIR="${HOME}/.local/bin"
CFG_DIR="${HOME}/.config/paseo-conductor"
STATE_DIR="${HOME}/.local/state/paseo-conductor"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

mkdir -p "$BIN_DIR" "$CFG_DIR" "$STATE_DIR"

echo "==> building paseo-conductor"
(cd "$here" && CGO_ENABLED=0 go build -o "$BIN_DIR/paseo-conductor" ./cmd/paseo-conductor)

if [ ! -f "$CFG_DIR/config.yaml" ]; then
  echo "==> writing starter config to $CFG_DIR/config.yaml"
  install -m 0644 "$here/config.example.yaml" "$CFG_DIR/config.yaml"
fi

if [ ! -f "$CFG_DIR/conductor.env" ]; then
  cat >"$CFG_DIR/conductor.env" <<'EOF'
# Secrets referenced by config.yaml via ${...}. Keep this file private (chmod 600).
EDN_WEBHOOK_SECRET=
EDN_SMEE_URL=https://smee.io/CHANGE_ME
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
bash "$here/scripts/service.sh" "$BIN_DIR/paseo-conductor" "$CFG_DIR"
