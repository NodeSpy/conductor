#!/usr/bin/env bash
# Install (and, if the config is valid, start) paseo-conductor as a per-user
# background service.
#   Linux  -> systemd --user unit
#   macOS  -> launchd LaunchAgent
#
# Usage: service.sh <binary-path> <config-dir>
# Prompts first (reads /dev/tty). Set PASEO_CONDUCTOR_INSTALL_SERVICE=yes|no to
# skip the prompt. Non-interactive with no override => skip.
set -euo pipefail

BIN="${1:-$HOME/.local/bin/paseo-conductor}"
CFG="${2:-$HOME/.config/paseo-conductor}"

ask() {
  local ans="${PASEO_CONDUCTOR_INSTALL_SERVICE:-}"
  if [ -z "$ans" ]; then
    if [ -r /dev/tty ]; then
      printf 'Install the paseo-conductor background service now? [y/N] ' >/dev/tty
      read -r ans </dev/tty || ans=""
    else
      ans="no"
    fi
  fi
  case "$ans" in y | Y | yes | YES) return 0 ;; *) return 1 ;; esac
}

SYSTEMD_UNIT="$HOME/.config/systemd/user/paseo-conductor.service"
LAUNCHD_PLIST="$HOME/Library/LaunchAgents/sh.paseo-conductor.plist"
LAUNCHD_LOG="$HOME/Library/Logs/paseo-conductor.log"

write_unit() {
  case "$1" in
    linux)
      mkdir -p "$(dirname "$SYSTEMD_UNIT")"
      cat >"$SYSTEMD_UNIT" <<EOF
[Unit]
Description=paseo-conductor — event-driven agent orchestration for your Paseo daemon
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$BIN run
Restart=always
RestartSec=5
EnvironmentFile=-$CFG/conductor.env

[Install]
WantedBy=default.target
EOF
      systemctl --user daemon-reload
      ;;
    darwin)
      mkdir -p "$(dirname "$LAUNCHD_PLIST")" "$(dirname "$LAUNCHD_LOG")"
      cat >"$LAUNCHD_PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>sh.paseo-conductor</string>
    <key>ProgramArguments</key>
    <array>
        <string>$BIN</string>
        <string>run</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>StandardOutPath</key><string>$LAUNCHD_LOG</string>
    <key>StandardErrorPath</key><string>$LAUNCHD_LOG</string>
</dict>
</plist>
EOF
      ;;
  esac
}

start_service() {
  case "$1" in
    linux)
      systemctl --user enable --now paseo-conductor
      loginctl enable-linger "$USER" >/dev/null 2>&1 || true
      echo "==> systemd --user service installed and started."
      echo "    logs:   journalctl --user -u paseo-conductor -f"
      echo "    stop:   systemctl --user disable --now paseo-conductor"
      ;;
    darwin)
      launchctl unload "$LAUNCHD_PLIST" 2>/dev/null || true
      launchctl load -w "$LAUNCHD_PLIST"
      echo "==> launchd agent installed and started."
      echo "    logs:   tail -f $LAUNCHD_LOG"
      echo "    stop:   launchctl unload -w $LAUNCHD_PLIST"
      ;;
  esac
}

start_hint() {
  case "$1" in
    linux) echo "    systemctl --user enable --now paseo-conductor" ;;
    darwin) echo "    launchctl load -w $LAUNCHD_PLIST" ;;
  esac
}

if ! ask; then
  echo "Skipped service install. Start manually with: $BIN run"
  exit 0
fi

case "$(uname -s)" in
  Linux) OS=linux ;;
  Darwin) OS=darwin ;;
  *) echo "unsupported OS $(uname -s) — start manually: $BIN run" >&2; exit 1 ;;
esac

# Always install the unit/plist so it's ready…
write_unit "$OS"

# …but only start it if the config is valid — otherwise it would crash-loop.
if "$BIN" validate >/dev/null 2>&1; then
  start_service "$OS"
else
  echo
  echo "==> Service unit installed but NOT started — the config isn't ready yet:"
  "$BIN" validate 2>&1 | sed 's/^/      /' || true
  echo
  echo "    Edit $CFG/config.yaml and $CFG/conductor.env, then start it with:"
  start_hint "$OS"
fi
