#!/usr/bin/env bash
# The paseo canary (docs/design/agent-sandbox.md §9): the provider-entry +
# `conductor sandbox exec` path against whatever paseo is on PATH — the
# scheduled job installs the LATEST published one. A throwaway conductor
# daemon and a throwaway paseo daemon (temp HOME, own port, relay off); no
# model, no secrets: the agent is the e2e's fake ACP agent (fakeacp), which
# makes one commit in its session's workspace.
#
#   - `conductor paseo setup` merges its entries into paseo's config and
#     `paseo reload` picks them up (the CLI and config shape conductor needs);
#   - a session started with a conductor-owned provider entry runs through
#     `conductor sandbox exec` (the daemon answers sandbox-policy for the
#     session's directory, PASEO_AGENT_CWD) and does its work there;
#   - with the conductor daemon down the session never starts.
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
command -v paseo >/dev/null || { echo "paseo is not on PATH"; exit 1; }
W="$(mktemp -d /tmp/csp.XXXXXX)"
W="$(cd "$W" && pwd -P)"
PORT="${PASEO_CANARY_PORT:-7945}"
DPID=""
cleanup() {
  [ -n "$DPID" ] && kill "$DPID" 2>/dev/null
  pe paseo daemon stop >/dev/null 2>&1
  rm -rf "$W"
}
trap cleanup EXIT
fail=0
check() { if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want '$2', got '$3'"; fail=1; fi; }

mkdir -p "$W/bin" "$W/home/.paseo" "$W/st" "$W/cfg" "$W/ws"
( cd "$ROOT" && go build -o "$W/bin/conductor" ./cmd/conductor && go build -o "$W/bin/fakeacp" ./test/e2e/services/fakeacp ) \
  || { echo "build failed"; exit 1; }
PASEO_DIR="$(dirname "$(command -v paseo)")"
NODE_DIR="$(dirname "$(command -v node)")"
pe() { env -i HOME="$W/home" PATH="$W/bin:$PASEO_DIR:$NODE_DIR:/usr/bin:/bin:/usr/sbin:/sbin" LANG=C "$@"; }
echo "paseo $(pe paseo --version 2>&1 | head -1)"

cat > "$W/cfg/config.yaml" <<EOF
connectors:
  timer:
    use: cron
    schedules:
      never: { cron: "0 4 1 1 *" }
store:
  state_file: $W/st/state.json
  audit_log: $W/st/audit.jsonl
EOF
env -i HOME="$W/home" PATH="$W/bin:/usr/bin:/bin" LANG=C conductor run --config "$W/cfg/config.yaml" > "$W/conductor.log" 2>&1 &
DPID=$!
for _ in $(seq 1 60); do [ -S "$W/st/control.sock" ] && break; sleep 0.5; done

cat > "$W/home/.paseo/config.json" <<EOF
{ "version": 1,
  "daemon": { "listen": "127.0.0.1:$PORT", "relay": { "enabled": false }, "mcp": { "injectIntoAgents": true } },
  "agents": { "providers": {} } }
EOF
( cd "$W" && pe paseo daemon start --timeout 120 ) || { echo "FAIL paseo daemon did not start"; exit 1; }

# The real merge + reload against this paseo.
out="$(cd "$W" && pe conductor paseo setup --config "$W/cfg/config.yaml" --providers claude,codex 2>&1)"
echo "$out"
case "$out" in *"added     claude-sandboxed"*"added     codex-sandboxed"*"paseo reloaded"*) echo "ok   paseo setup merged and reloaded" ;;
  *) echo "FAIL paseo setup"; fail=1 ;; esac
again="$(cd "$W" && pe conductor paseo setup --config "$W/cfg/config.yaml" --providers claude,codex 2>&1)"
case "$again" in *"added=0 updated=0 unchanged=2"*) echo "ok   paseo setup is idempotent" ;; *) echo "FAIL rerun: $again"; fail=1 ;; esac

# A conductor-owned entry around the fake ACP agent: the same mechanism
# (extends + command), no model needed.
python3 - "$W" <<'PY'
import json, sys
w = sys.argv[1]
p = f"{w}/home/.paseo/config.json"
c = json.load(open(p))
c["agents"]["providers"]["fake-sandboxed"] = {
    "extends": "acp", "label": "Fake (sandboxed)", "x-conductor": True,
    "command": [f"{w}/bin/conductor", "sandbox", "exec", "--config", f"{w}/cfg/config.yaml", "--", f"{w}/bin/fakeacp"]}
json.dump(c, open(p, "w"), indent=2)
PY
pe paseo reload >/dev/null 2>&1 || { echo "FAIL paseo reload"; fail=1; }

git -C "$W/ws" init -q -b main && echo ws > "$W/ws/README.md"
run() { (cd "$W/ws" && pe timeout 180 paseo run --provider fake-sandboxed --cwd "$W/ws" --title "$1" --json "canary task" 2>&1); }
res="$(run canary-1)"
echo "$res" | head -20
check "the sandboxed fake agent did its work in its workspace" yes "$( [ -s "$W/ws/CONDUCTOR_FIX.md" ] && echo yes || echo no)"
if grep -q "sandbox-policy: .* in $W/ws " "$W/conductor.log"; then echo "ok   launched through conductor sandbox exec (sandbox-policy for the session's directory)";
else echo "FAIL no sandbox-policy for $W/ws in the conductor log"; fail=1; fi

kill "$DPID"; wait "$DPID" 2>/dev/null; DPID=""
for _ in $(seq 1 20); do [ -S "$W/st/control.sock" ] || break; sleep 0.5; done
# A fresh paseo: no agent process left from the first session (an ACP
# provider's process can serve a later session), so the next one must launch.
( cd "$W" && pe paseo daemon stop >/dev/null 2>&1; pe paseo daemon start --timeout 120 >/dev/null 2>&1 )
rm -f "$W/ws/CONDUCTOR_FIX.md"
res="$(run canary-down)"
case "$res" in *"refusing to launch"*|*'"status": "error"'*|*AGENT_CREATE_FAILED*) echo "ok   conductor down: the session is refused" ;; *) echo "FAIL conductor down: $res"; fail=1 ;; esac
check "conductor down: the agent never ran" no "$( [ -e "$W/ws/CONDUCTOR_FIX.md" ] && echo yes || echo no)"

echo "== paseo canary: $([ $fail = 0 ] && echo PASS || echo FAIL) =="
exit $fail
