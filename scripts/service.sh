#!/usr/bin/env bash
# Install (and start) paseo-conductor as a per-user background service.
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
      printf 'Install and start the paseo-conductor background service now? [y/N] ' >/dev/tty
      read -r ans </dev/tty || ans=""
    else
      ans="no"
    fi
  fi
  case "$ans" in y | Y | yes | YES) return 0 ;; *) return 1 ;; esac
}

install_systemd() {
  local unit="$HOME/.config/systemd/user/paseo-conductor.service"
  mkdir -p "$(dirname "$unit")"
  cat >"$unit" <<EOF
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
  systemctl --user enable --now paseo-conductor
  loginctl enable-linger "$USER" >/dev/null 2>&1 || true
  echo "==> systemd --user service installed and started."
  echo "    logs:   journalctl --user -u paseo-conductor -f"
  echo "    stop:   systemctl --user disable --now paseo-conductor"
}

install_launchd() {
  local label="sh.paseo-conductor"
  local plist="$HOME/Library/LaunchAgents/$label.plist"
  local log="$HOME/Library/Logs/paseo-conductor.log"
  mkdir -p "$(dirname "$plist")" "$(dirname "$log")"
  cat >"$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>$label</string>
    <key>ProgramArguments</key>
    <array>
        <string>$BIN</string>
        <string>run</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>StandardOutPath</key><string>$log</string>
    <key>StandardErrorPath</key><string>$log</string>
</dict>
</plist>
EOF
  # Secrets come from $CFG/conductor.env, which the daemon loads itself
  # (launchd has no EnvironmentFile).
  launchctl unload "$plist" 2>/dev/null || true
  launchctl load -w "$plist"
  echo "==> launchd agent installed and started."
  echo "    logs:   tail -f $log"
  echo "    stop:   launchctl unload -w $plist"
}

if ! ask; then
  echo "Skipped service install. Start manually with: $BIN run"
  echo "(re-run scripts/service.sh anytime to install it.)"
  exit 0
fi

"$BIN" validate || {
  echo "warning: config did not validate — fix $CFG/config.yaml, then re-run this script." >&2
}

case "$(uname -s)" in
  Linux) install_systemd ;;
  Darwin) install_launchd ;;
  *) echo "unsupported OS $(uname -s) — start manually: $BIN run" >&2; exit 1 ;;
esac
