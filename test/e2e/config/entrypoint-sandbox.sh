#!/usr/bin/env bash
# The agent-sandbox daemon (group Z): runs NON-root — the sandbox refuses to
# build as root, where a user namespace is no privilege boundary. It sets the
# acts-as-the-user git identity and a THROWAWAY ssh signing key conductor signs
# the agents' pushed commits with (the key never enters the sandbox), and the
# allowed_signers file the harness verifies those signatures against.
# Hermetic, no secrets.
set -euo pipefail
CONFIG="${1:?usage: entrypoint-sandbox.sh <config.yaml>}"
D=/home/conductor/data
mkdir -p "$D/keys"
ssh-keygen -q -t ed25519 -N "" -C e2e-throwaway -f "$D/keys/signing"
echo "conductor@users.noreply.forge.test $(cat "$D/keys/signing.pub")" > "$D/keys/allowed_signers"
git config --global user.name "Conductor User"
git config --global user.email "conductor@users.noreply.forge.test"
git config --global init.defaultBranch main
exec conductor run --config "$CONFIG"
