#!/usr/bin/env bash
# The paseo provider canary: the paseo on PATH (CI installs the latest
# published @getpaseo/cli), conductor's embedded plugin, and one relay
# round-trip — paseo creates an agent on the `conductor` provider, conductor
# launches a fake ACP agent (test/paseo-canary/fakeacp) in the workspace
# jail, and the agent's reply comes back through paseo. A red run means a
# paseo release broke the surface the plugin depends on
# (docs/wiki/Paseo-Provider.md, "What it depends on in paseo").
#
# Everything runs under one temp dir — HOME included — so neither paseo nor
# conductor touches the machine's own state (conductor's config dir is a
# sibling of HOME: the jail refuses a home that would show it). KEEP=1
# leaves it for a look.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
W="$(mktemp -d /tmp/pcanary.XXXXXX)"
PORT="${PASEO_CANARY_PORT:-6798}"
PIDS=()
cleanup() {
	for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
	wait 2>/dev/null || true
	if [ "${KEEP:-0}" = 1 ]; then echo "kept: $W"; else rm -rf "$W"; fi
}
trap cleanup EXIT
fail() { echo "CANARY FAIL: $*" >&2; echo "--- conductor log" >&2; tail -40 "$W/conductor.log" >&2 || true; echo "--- paseo warnings" >&2; grep -E '"level":(40|50|60)' "$W/paseo.log" | grep -o '"msg":"[^"]*"\|"message":"[^"]*"' | tail -10 >&2 || true; exit 1; }

PASEO="$(command -v paseo)" || fail "no paseo on PATH"
mkdir -p "$W/bin" "$W/cfg" "$W/home" "$W/state" "$W/ws" "$W/paseo"
(cd "$ROOT" && go build -o "$W/bin/conductor" ./cmd/conductor && go build -o "$W/bin/fakeacp" ./test/paseo-canary/fakeacp)
export HOME="$W/home" XDG_STATE_HOME="$W/state/xdg" XDG_CONFIG_HOME="$W/home/.config" PASEO_HOME="$W/paseo"
unset XDG_RUNTIME_DIR
touch "$HOME/canary-marker"
export CANARY_HOME_MARKER="$HOME/canary-marker"
echo "paseo $("$PASEO" --version 2>/dev/null | head -1)"

cat > "$W/cfg/config.yaml" <<YAML
connectors:
  hooks: { use: web, base_url: "http://127.0.0.1:18951", listen: "127.0.0.1:18951" }
runtimes:
  fake: { use: acp, agent: fake, command: ["$W/bin/fakeacp"], default: true }
paseo_provider: { runtime: fake }
update: { auto: false }
store: { state_file: "$W/state/state.json", audit_log: "$W/state/audit.jsonl" }
YAML
printf '{"version":1,"daemon":{"listen":"127.0.0.1:%s","relay":{"enabled":false}},"pluginsEnabled":true}\n' "$PORT" > "$PASEO_HOME/config.json"

"$W/bin/conductor" run --config "$W/cfg/config.yaml" > "$W/conductor.log" 2>&1 & PIDS+=($!)
"$PASEO" daemon run --home "$PASEO_HOME" > "$W/paseo.log" 2>&1 & PIDS+=($!)
for _ in $(seq 1 120); do
	[ -S "$W/state/paseo-provider.sock" ] && grep -q "Listening on" "$W/paseo.log" && break
	sleep 1
done
[ -S "$W/state/paseo-provider.sock" ] || fail "conductor's paseo provider socket never appeared"
grep -q "Listening on" "$W/paseo.log" || fail "the paseo daemon never listened"

"$W/bin/conductor" paseo install-plugin --config "$W/cfg/config.yaml" --paseo-home "$PASEO_HOME" --paseo-bin "$PASEO" || fail "install-plugin"
"$W/bin/conductor" paseo install-plugin --config "$W/cfg/config.yaml" --paseo-home "$PASEO_HOME" --paseo-bin "$PASEO" | grep -q unchanged || fail "install-plugin is not idempotent"
for _ in $(seq 1 60); do
	"$PASEO" provider ls --home "$PASEO_HOME" 2>/dev/null | grep -E '^conductor\s' | grep -qv loading && break
	sleep 1
done
"$PASEO" provider ls --home "$PASEO_HOME" 2>/dev/null | grep -E '^conductor\s' || fail "paseo lists no conductor provider"

(cd "$W/ws" && timeout 180 "$PASEO" run --home "$PASEO_HOME" --provider conductor --cwd "$W/ws" --title canary --wait-timeout 2m --json "say canary") > "$W/run.json" 2> "$W/run.err" || fail "paseo run: $(tail -3 "$W/run.err")"
AGENT="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["agentId"])' "$W/run.json")"
for _ in $(seq 1 60); do
	"$PASEO" logs "$AGENT" --home "$PASEO_HOME" > "$W/logs.txt" 2>&1 || true
	grep -q canary-ok "$W/logs.txt" && break
	sleep 1
done
grep -q canary-ok "$W/logs.txt" || fail "no canary-ok from the agent: $(tail -5 "$W/logs.txt")"
grep -q home-hidden "$W/logs.txt" || fail "the agent saw the real home: it did not run in the jail"
grep -q '"event":"paseo_provider".*"status":"launched"' "$W/state/audit.jsonl" || fail "no launch audited"
echo "CANARY OK: $(grep -o 'canary-ok[^"]*' "$W/logs.txt" | head -1)"
