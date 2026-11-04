#!/usr/bin/env bash
# The plugin-jail canary (docs/wiki/Plugins.md § Security): official plugins
# from the plugins repo, run under the default jail with sandbox.plugins
# required, against local fakes — github (App auth from a key file its config
# points at, a dry-run sweep, a verb call, a webhook), sentry and pagerduty
# (webhooks to the address their config sets), slack (a verb through its
# network:, and refused outside it). See internal/plugin/official_jail_test.go.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
W="$(mktemp -d /tmp/cpj.XXXXXX)"
W="$(cd "$W" && pwd -P)"
GHFAKE_PID=""
cleanup() { [ -n "$GHFAKE_PID" ] && kill "$GHFAKE_PID" 2>/dev/null; rm -rf "$W"; }
trap cleanup EXIT

case "$(uname)" in
  Darwin) IP="$(ipconfig getifaddr en0 || ipconfig getifaddr en1)" ;;
  *) IP="$(ip -4 -o route get 1.1.1.1 | sed -n 's/.* src \([0-9.]*\).*/\1/p')" ;;
esac
[ -n "$IP" ] || { echo "no non-loopback address"; exit 1; }

git clone --quiet --filter=blob:none --depth 1 https://github.com/NodeSpy/conductor-plugins "$W/plugins"
mkdir -p "$W/bin" "$W/plugins/cmd/ghfaked" "$W/run"
sed '/^\/\/go:build ignore$/d' "$ROOT/test/sandbox-canary/ghfaked/main.go" > "$W/plugins/cmd/ghfaked/main.go"
(
  cd "$W/plugins"
  for c in github sentry pagerduty slack; do go build -o "$W/bin/$c" "./connectors/$c"; done
  go build -o "$W/ghfaked" ./cmd/ghfaked
)
"$W/ghfaked" -listen "$IP:0" -out "$W/run/sc.json" > "$W/run/ghfake.log" 2>&1 &
GHFAKE_PID=$!
for _ in $(seq 100); do [ -s "$W/run/sc.json" ] && break; sleep 0.1; done

cd "$ROOT"
CONDUCTOR_REQUIRE_SANDBOX=1 CONDUCTOR_OFFICIAL_PLUGINS="$W/bin" CONDUCTOR_OFFICIAL_HOST_IP="$IP" CONDUCTOR_GHFAKE="$W/run/sc.json" \
  go test -count=1 -v -run 'TestOfficial|TestPluginJailHolds' ./internal/plugin/
