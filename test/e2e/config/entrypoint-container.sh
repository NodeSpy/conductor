#!/usr/bin/env bash
# The container-backend daemon (group ZC): non-root, its agents in
# containers on the stack's own Docker engine (the dind sidecar). Like the
# sandbox daemon, a throwaway key signs what conductor pushes. Hermetic.
set -euo pipefail
CONFIG="${1:?usage: entrypoint-container.sh <config.yaml>}"
D=/home/conductor/cdata
mkdir -p "$D/keys"
[ -f "$D/keys/signing" ] || ssh-keygen -q -t ed25519 -N "" -C e2e-throwaway -f "$D/keys/signing"
echo "conductor@users.noreply.forge.test $(cat "$D/keys/signing.pub")" > "$D/keys/allowed_signers"
git config --global user.name "Conductor User"
git config --global user.email "conductor@users.noreply.forge.test"
git config --global init.defaultBranch main
exec conductor run --config "$CONFIG"
