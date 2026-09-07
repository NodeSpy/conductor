#!/usr/bin/env bash
# Entrypoint for the connectors-model daemon (group K): the standard git
# identity setup PLUS a loopback sshd — the `hosts: selfbox` target is this
# container's own 127.0.0.1, which proves the remote-execution path (system
# ssh, key auth, known_hosts) hermetically without a second box.
set -euo pipefail

CONFIG="${1:?usage: entrypoint-conn.sh <config.yaml>}"

git config --global user.name "Conductor User"
git config --global user.email "conductor@users.noreply.forge.test"
git config --global --add safe.directory '*'
git config --global init.defaultBranch main

mkdir -p /data /data/fakepaseo /home/conductor /root/.ssh
chmod 700 /root/.ssh

# Host keys + a client keypair authorized for root, then sshd + known_hosts.
ssh-keygen -A >/dev/null
if [ ! -f /root/.ssh/id_ed25519 ]; then
  ssh-keygen -t ed25519 -N "" -f /root/.ssh/id_ed25519 >/dev/null
fi
cat /root/.ssh/id_ed25519.pub >> /root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys
echo "PermitRootLogin prohibit-password" >> /etc/ssh/sshd_config
/usr/sbin/sshd
for i in $(seq 1 20); do
  if ssh-keyscan -T 2 127.0.0.1 > /root/.ssh/known_hosts 2>/dev/null && [ -s /root/.ssh/known_hosts ]; then
    break
  fi
  sleep 0.5
done

exec conductor run --config "$CONFIG"
