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
# Group PAY: TEST personal details and the test card (fixed, for the leak
# grep), two fake shops (the card's merchant on :8091, another on :8092), and
# a throwaway operator key the harness signs purchase approvals with.
printf '%s' "E2E Jane Tester" > "$D/vault/p_name"
printf '%s' "e2e.jane.private@example.test" > "$D/vault/p_email"
printf '%s' "+1 555 0100 777" > "$D/vault/p_phone"
printf '%s' "1 Hidden Lane" > "$D/vault/p_street"
printf '%s' "Springfield" > "$D/vault/p_city"
printf '%s' "IL" > "$D/vault/p_region"
printf '%s' "62704" > "$D/vault/p_zip"
printf '%s' "US" > "$D/vault/p_country"
printf '%s' "4242424242424242" > "$D/vault/card_number"
printf '%s' "12/34" > "$D/vault/card_expiry"
printf '%s' "987" > "$D/vault/card_cvc"
chmod 600 "$D/vault/"*
ssh-keygen -q -t ed25519 -N "" -C ops@e2e -f "$D/keys/ops"
sed "s|ssh-ed25519 AAAA-E2E-OPS-KEY-PLACEHOLDER|$(cat "$D/keys/ops.pub")|" "$CONFIG" > "$D/sandbox.yaml"
testsite --listen 127.0.0.1:8090 --totp-seed JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP --shop 127.0.0.1:8091 --shop 127.0.0.1:8092 >"$D/testsite.log" 2>&1 &
exec conductor run --config "$D/sandbox.yaml"
