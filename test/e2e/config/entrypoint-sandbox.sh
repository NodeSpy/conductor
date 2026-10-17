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
# Group BR (conductor's browser): the local test site on loopback, with a
# TOTP step, and the test account in a file vault — a TEST account, nothing
# real. The seed and password are fixed so the harness can grep for leaks.
mkdir -p "$D/vault" && chmod 700 "$D/vault"
printf '%s' test-user > "$D/vault/username"
printf '%s' test-password-not-secret > "$D/vault/password"
printf '%s' "otpauth://totp/TestSite?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP" > "$D/vault/otp"
chmod 600 "$D/vault/"*
testsite --listen 127.0.0.1:8090 --totp-seed JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP >"$D/testsite.log" 2>&1 &
exec conductor run --config "$CONFIG"
