#!/usr/bin/env bash
# Build conductor from source, seed config, and optionally install the
# per-user background service (systemd on Linux, launchd on macOS).
set -euo pipefail

BIN_DIR="${HOME}/.local/bin"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Resolve the config file exactly as the binary does (internal/confdir):
# $CONDUCTOR_CONFIG (a file, or a directory holding config.yaml), else
# $XDG_CONFIG_HOME/conductor/config.yaml (absolute paths only), else
# ~/.config/conductor/config.yaml.
resolve_cfg_file() {
  local v="${CONDUCTOR_CONFIG:-}"
  if [ -n "$v" ]; then
    case "$v" in
      */) echo "${v%/}/config.yaml" ;;
      *) if [ -d "$v" ]; then echo "$v/config.yaml"; else echo "$v"; fi ;;
    esac
  elif [ -n "${XDG_CONFIG_HOME:-}" ] && [ "${XDG_CONFIG_HOME#/}" != "$XDG_CONFIG_HOME" ]; then
    echo "$XDG_CONFIG_HOME/conductor/config.yaml"
  else
    echo "$HOME/.config/conductor/config.yaml"
  fi
}
CFG_FILE="$(resolve_cfg_file)"
CFG_DIR="$(dirname "$CFG_FILE")"
STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/conductor"
BIN_NAME="conductor"
BIN="${BIN_DIR}/${BIN_NAME}"

mkdir -p "$BIN_DIR" "$CFG_DIR" "$STATE_DIR"
# The default split layout: each section imports from its conf.d/ folder.
mkdir -p "$CFG_DIR"/conf.d/{connectors,runtimes,hosts,agents,workflows,triggers}

echo "==> building $BIN_NAME"
(cd "$here" && CGO_ENABLED=0 go build -o "$BIN" ./cmd/conductor)

install -m 0644 "$here/config.example.yaml" "$CFG_DIR/config.example.yaml"
if [ ! -f "$CFG_FILE" ]; then
  echo "==> writing starter config to $CFG_FILE (github integration disabled until configured)"
  install -m 0644 "$here/config.starter.yaml" "$CFG_FILE"
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
  - $CFG_FILE   (app_id, repos, rules)
  - $CFG_DIR/conductor.env (secrets)
  - drop your GitHub App private key at the path in config
EOF

# Offer to install the background service (prompts first). A moved config is
# exported so the unit pins it (the service doesn't inherit this shell).
if [ "$CFG_FILE" != "$HOME/.config/conductor/config.yaml" ]; then
  export CONDUCTOR_CONFIG="$CFG_FILE"
fi
bash "$here/scripts/service.sh" "$BIN" "$CFG_DIR"
