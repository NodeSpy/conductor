#!/usr/bin/env bash
# Install paseo-conductor as a systemd --user service.
set -euo pipefail

BIN_DIR="${HOME}/.local/bin"
CFG_DIR="${HOME}/.config/paseo-conductor"
UNIT_DIR="${HOME}/.config/systemd/user"
STATE_DIR="${HOME}/.local/state/paseo-conductor"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

mkdir -p "$BIN_DIR" "$CFG_DIR" "$UNIT_DIR" "$STATE_DIR"

echo "==> building paseo-conductor"
( cd "$here" && CGO_ENABLED=0 go build -o "$BIN_DIR/paseo-conductor" ./cmd/paseo-conductor )

echo "==> installing systemd unit"
install -m 0644 "$here/systemd/paseo-conductor.service" "$UNIT_DIR/paseo-conductor.service"

if [ ! -f "$CFG_DIR/config.yaml" ]; then
  echo "==> writing starter config to $CFG_DIR/config.yaml"
  install -m 0644 "$here/config.example.yaml" "$CFG_DIR/config.yaml"
fi

if [ ! -f "$CFG_DIR/conductor.env" ]; then
  cat > "$CFG_DIR/conductor.env" <<'EOF'
# Secrets referenced by config.yaml via ${...}. Keep this file private (chmod 600).
EDN_WEBHOOK_SECRET=
EDN_SMEE_URL=https://smee.io/CHANGE_ME
EOF
  chmod 600 "$CFG_DIR/conductor.env"
  echo "==> wrote $CFG_DIR/conductor.env (fill in the secrets)"
fi

cat <<EOF

Next steps:
  1. Edit $CFG_DIR/config.yaml (app_id, repos, rules) and $CFG_DIR/conductor.env (secrets).
  2. Drop your GitHub App private key at the path in config (e.g. $CFG_DIR/ednition-app.pem).
  3. Validate:   paseo-conductor validate
  4. Enable:     systemctl --user daemon-reload && systemctl --user enable --now paseo-conductor
  5. Keep alive: loginctl enable-linger "$USER"
  6. Logs:       journalctl --user -u paseo-conductor -f
EOF
