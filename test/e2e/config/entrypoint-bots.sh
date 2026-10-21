#!/usr/bin/env bash
# The bots daemon (groups Z and L): its config is copied somewhere writable,
# because a pack is vendored next to the config (`conductor init`); an
# operator key is minted so the e2e can sign an approval.
set -euo pipefail

SRC="${1:?usage: entrypoint-bots.sh <config.yaml>}"

git config --global user.name "Conductor User"
git config --global user.email "conductor@users.noreply.forge.test"
git config --global --add safe.directory '*'
git config --global init.defaultBranch main

mkdir -p /data /data/fakepaseo /data/cfg /home/conductor
if [ ! -f /data/ops ]; then
  ssh-keygen -q -t ed25519 -N "" -C ops@e2e -f /data/ops
fi
sed "s|@OPS_PUB@|$(cat /data/ops.pub)|" "$SRC" > /data/cfg/conductor.yaml
rm -rf /data/cfg/packs && cp -r /fixtures/packs /data/cfg/packs
conductor init --config /data/cfg/conductor.yaml
exec conductor run --config /data/cfg/conductor.yaml
