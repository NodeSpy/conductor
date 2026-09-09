#!/usr/bin/env bash
# End-to-end proof for external connector plugins (#54). Hermetic, no network,
# no secrets: builds conductor + the reference acme-echo plugin, pins its
# SHA-256, and drives validate / plugin list / plugin show plus the negative
# verify-before-execute path. Prints a captured transcript for the PR.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cd "$ROOT"

say() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
fail() { printf '\033[31mFAIL: %s\033[0m\n' "$*"; exit 1; }

say "build conductor + example plugin"
go build -o "$WORK/conductor" ./cmd/conductor || fail "build conductor"
go build -o "$WORK/acme-echo" ./test/plugins/acme-echo || fail "build plugin"
SHA="$(sha256sum "$WORK/acme-echo" | cut -d' ' -f1)"
echo "plugin sha256: $SHA"

write_config() { # $1 = sha to pin
  cat > "$WORK/config.yaml" <<YAML
plugins:
  echo:
    source: ./acme-echo
    kind: connector
    provides: acme-echo
    version: 1.0.0
    sha256: $1
    allow_unsandboxed: true   # demo runs the plugin without an OS sandbox

# NOTE: production configs should grant an isolation: block instead of
# allow_unsandboxed — an external plugin with no isolation runs same-uid.

connectors:
  myecho:
    type: acme-echo
    token: demo-token-value
YAML
}

CFG="$WORK/config.yaml"
conductor() { "$WORK/conductor" "$@" --config "$CFG"; }

say "conductor validate (correct sha → green)"
write_config "$SHA"
conductor validate || fail "validate should pass with a correct pin"
echo "validate: OK"

say "conductor plugin list (bundled + external)"
conductor plugin list || fail "plugin list"

say "conductor plugin show acme-echo (Decl + disclosure)"
conductor plugin show acme-echo || fail "plugin show"

say "NEGATIVE: tampered sha → validate refuses to run the plugin"
write_config "0000000000000000000000000000000000000000000000000000000000000000"
neg="$(conductor validate 2>&1)"; rc=$?
echo "$neg"
if [ $rc -ne 0 ] && grep -qi "sha256 mismatch" <<<"$neg"; then
  echo "verify-before-execute: refused as expected"
else
  fail "tampered sha should have been refused"
fi

say "back-compat: the shipped config.example.yaml still validates"
# config.example.yaml references env-only secrets; supply dummy values (this is
# exactly what the Go example-config test does) so validation exercises the
# schema, not secret resolution.
env GH_WEBHOOK_SECRET=x GH_SMEE_URL=https://smee.example/x \
    SLACK_APP_TOKEN=x SLACK_BOT_TOKEN=x CW_SECRET=x \
    CONDUCTOR_INVOKE_TOKEN=x CONDUCTOR_INVOKE_HMAC=x \
    "$WORK/conductor" validate --config "$ROOT/config.example.yaml" || fail "config.example.yaml must stay green"
echo "config.example.yaml: OK"

printf '\n\033[32mALL PLUGIN E2E CHECKS PASSED\033[0m\n'
