#!/usr/bin/env bash
# Conductor container entrypoint: establish the acts-as-the-user git identity
# (conductor reads `git config user.name/email` for commit attribution), then exec
# the daemon with the config passed as $1. Hermetic — no secrets, no network egress.
set -euo pipefail

CONFIG="${1:?usage: entrypoint.sh <config.yaml>}"

git config --global user.name "Conductor User"
git config --global user.email "conductor@users.noreply.forge.test"
# The fake paseo runs git inside freshly-cloned worktrees owned by this user; avoid
# git's "dubious ownership" guard tripping across the /data volume boundary.
git config --global --add safe.directory '*'
# Allow pushing to a checked-out branch on the bare forge without complaint.
git config --global init.defaultBranch main

mkdir -p /data /data/fakepaseo /home/conductor

exec conductor run --config "$CONFIG"
