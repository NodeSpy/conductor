#!/usr/bin/env bash
# Entrypoint for the auto-migration daemon (group L1): seed a LEGACY config
# into a writable location (the mounted config dir is read-only), then boot.
# The daemon's own auto-migration transforms it in place — the harness asserts
# the backup, the new schema, and that the migrated behavior still works.
set -euo pipefail

git config --global user.name "Conductor User"
git config --global user.email "conductor@users.noreply.forge.test"
git config --global --add safe.directory '*'
git config --global init.defaultBranch main

mkdir -p /data /data/fakepaseo /data/config /home/conductor
if [ ! -f /data/config/config.yaml ]; then
  cp /etc/conductor/legacy-migrate.yaml /data/config/config.yaml
fi

exec conductor run --config /data/config/config.yaml
