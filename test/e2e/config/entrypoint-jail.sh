#!/usr/bin/env bash
# The agent-jail daemon (group X): runs NON-root. It sets the acts-as-the-user
# git identity and a THROWAWAY ssh signing key (commit.gpgsign), so a jailed
# agent's commits must be signed through conductor's signing shim — the key
# never enters the jail — and verify on the forge. Hermetic, no secrets.
set -euo pipefail
CONFIG="${1:?usage: entrypoint-jail.sh <config.yaml>}"
D=/home/conductor/data
mkdir -p "$D" "$D/keys"
ssh-keygen -q -t ed25519 -N "" -C e2e-throwaway -f "$D/keys/signing"
echo "conductor@users.noreply.forge.test $(cat "$D/keys/signing.pub")" > "$D/keys/allowed_signers"
git config --global user.name "Conductor User"
git config --global user.email "conductor@users.noreply.forge.test"
git config --global init.defaultBranch main
git config --global gpg.format ssh
git config --global user.signingkey "$D/keys/signing.pub"
git config --global commit.gpgsign true
exec conductor run --config "$CONFIG"
