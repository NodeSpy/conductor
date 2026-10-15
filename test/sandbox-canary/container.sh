#!/usr/bin/env bash
# The container canary: the agent sandbox's container backend end to end,
# against a real engine (ENGINE=docker | podman) and a throwaway conductor
# daemon (temp HOME and state; sandbox.backend: container, persistent). No
# model, no secrets: the agent image is IMAGE plus a static probe (cprobe)
# and the e2e's fake ACP agent on its PATH — an agent image carries its
# CLIs — and, when paseo is on PATH, the fake agent runs through a
# conductor-owned paseo provider entry.
#
#   - a session's container: no capabilities, no-new-privileges, seccomp,
#     non-root, no engine socket, no network but the allowlist proxy, the
#     operator's secret absent;
#   - an identity's container keeps what it installed in HOME across
#     sessions and a conductor restart (the same container);
#   - two identities see nothing of each other;
#   - `conductor sandbox archive` removes the identity's container and its
#     HOME volume, and nothing else;
#   - no engine on PATH: the session is refused (exit 125), never run;
#   - a paseo session started with a sandboxed provider entry runs in the
#     container.
#
# Every container and volume it makes carries this daemon's label, and its
# image a canary label; all are removed at the end. Nothing else on the
# engine is touched.
#
#   ENGINE=docker IMAGE=alpine:3 bash test/sandbox-canary/container.sh
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ENGINE="${ENGINE:-docker}"
IMAGE="${IMAGE:-alpine:3}"
ALLOWED="${ALLOWED_URL:-https://example.com/}"
DENIED="${DENIED_URL:-https://example.org/}"
command -v "$ENGINE" >/dev/null || { echo "$ENGINE is not on PATH"; exit 1; }
# Not under /tmp: the launcher temp-file rule (a container shows none)
# would see the decoy's path as one.
W="$(mktemp -d /var/tmp/csc.XXXXXX)"
W="$(cd "$W" && pwd -P)"
chmod 0755 "$W"
DPID=""
AGENT_IMAGE="conductor-canary-agent:$(basename "$W" | tr 'A-Z.' 'a-z-')"
PPORT="${PASEO_CANARY_PORT:-$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])')}"
ENGINE_DIR="$(dirname "$(command -v "$ENGINE")")"
EXTRA_PATH=""
command -v paseo >/dev/null && EXTRA_PATH="$(dirname "$(command -v paseo)"):$(dirname "$(command -v node)")"
# The engine client's own environment (a rootless podman's runtime dir).
CLIENT_ENV=()
[ -n "${XDG_RUNTIME_DIR:-}" ] && CLIENT_ENV+=("XDG_RUNTIME_DIR=$XDG_RUNTIME_DIR")
[ -n "${DOCKER_HOST:-}" ] && CLIENT_ENV+=("DOCKER_HOST=$DOCKER_HOST")
DPATH="$W/bin:$EXTRA_PATH:$ENGINE_DIR:/usr/bin:/bin:/usr/sbin:/sbin"
pe() { env -i HOME="$W/home" PATH="$DPATH" LANG=C "${CLIENT_ENV[@]}" "$@"; }
daemon_label() { echo "conductor.daemon=$(printf '%s' "$W/st/agent-state" | sha256sum | cut -c1-12)"; }
cleanup() {
  [ -n "$DPID" ] && kill "$DPID" 2>/dev/null
  [ -n "$EXTRA_PATH" ] && pe paseo daemon stop >/dev/null 2>&1
  # paseo's workers can outlive its daemon: this run's own (its HOME) go too.
  for p in $(pgrep -u "$(id -u)" . 2>/dev/null); do
    tr '\0' '\n' 2>/dev/null < "/proc/$p/environ" | grep -qx "HOME=$W/home" && kill "$p" 2>/dev/null
  done
  local l; l="$(daemon_label)"
  for c in $("$ENGINE" ps -aq --filter "label=$l" 2>/dev/null); do "$ENGINE" rm -f "$c" >/dev/null 2>&1; done
  for v in $("$ENGINE" volume ls -q --filter "label=$l" 2>/dev/null); do "$ENGINE" volume rm -f "$v" >/dev/null 2>&1; done
  "$ENGINE" rmi -f "$AGENT_IMAGE" >/dev/null 2>&1
  if [ -n "${CANARY_KEEP:-}" ]; then echo "kept $W"; return; fi
  rm -rf "$W" 2>/dev/null || { "$ENGINE" unshare rm -rf "$W" 2>/dev/null; rm -rf "$W" 2>/dev/null; }
}
trap cleanup EXIT
fail=0
check() { if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want '$2', got '$3'"; fail=1; fi; }
expect_ok() { # every probe line ok, and the named ones present
  local out="$1"; shift
  echo "$out" | sed 's/^/     | /'
  if echo "$out" | grep -q '^FAIL'; then fail=1; fi
  for w in "$@"; do echo "$out" | grep -q "^ok   $w" || { echo "FAIL missing: $w"; fail=1; }; done
}

mkdir -p "$W/bin" "$W/home/.ssh" "$W/home/.paseo" "$W/st" "$W/cfg" "$W/ws-a" "$W/ws-b" "$W/ws-p"
echo "DECOY-KEY" > "$W/home/.ssh/id_decoy" && chmod 600 "$W/home/.ssh/id_decoy"
( cd "$ROOT" && CGO_ENABLED=0 go build -o "$W/bin/conductor" ./cmd/conductor \
  && CGO_ENABLED=0 GOOS=linux go build -o "$W/bin/cprobe" ./test/sandbox-canary/cprobe \
  && CGO_ENABLED=0 GOOS=linux go build -o "$W/bin/fakeacp" ./test/e2e/services/fakeacp ) || { echo "build failed"; exit 1; }
"$ENGINE" image inspect "$IMAGE" >/dev/null 2>&1 || "$ENGINE" pull -q "$IMAGE" >/dev/null || { echo "pull $IMAGE failed"; exit 1; }
mkdir -p "$W/img" && cp "$W/bin/cprobe" "$W/bin/fakeacp" "$W/img/"
printf 'FROM %s\nCOPY cprobe fakeacp /usr/local/bin/\nLABEL conductor.canary=1\n' "$IMAGE" > "$W/img/Dockerfile"
"$ENGINE" build -q -t "$AGENT_IMAGE" "$W/img" >/dev/null || { echo "build the canary agent image failed"; exit 1; }
echo "engine $ENGINE ($("$ENGINE" --version 2>&1 | head -1)), agent image $AGENT_IMAGE (from $IMAGE)"

allowed_host="$(echo "$ALLOWED" | sed -E 's#^[a-z]+://([^/:]+).*#\1#')"
cat > "$W/cfg/config.yaml" <<EOF
connectors:
  timer:
    use: cron
    schedules:
      never: { cron: "0 4 1 1 *" }
runtimes:
  paseo: { use: paseo, home: $W/home/.paseo, default: true }
sandbox:
  backend: container
  egress: [$allowed_host]
  container:
    engine: $ENGINE
    image: $AGENT_IMAGE
    persistent: true
    read_only: true
    limits: { pids: 512, memory: 1g }
store:
  state_file: $W/st/state.json
  audit_log: $W/st/audit.jsonl
EOF
cat > "$W/home/.paseo/config.json" <<EOF
{ "version": 1,
  "daemon": { "listen": "127.0.0.1:$PPORT", "relay": { "enabled": false }, "mcp": { "injectIntoAgents": false } },
  "agents": { "providers": {} } }
EOF
start_conductor() {
  # Started directly (not through a function): $! is the daemon itself.
  env -i HOME="$W/home" PATH="$DPATH" LANG=C "${CLIENT_ENV[@]}" "$W/bin/conductor" run --config "$W/cfg/config.yaml" >> "$W/conductor.log" 2>&1 &
  DPID=$!
  for _ in $(seq 1 60); do [ -S "$W/st/control.sock" ] && break; sleep 0.5; done
}
stop_conductor() {
  kill "$DPID"; wait "$DPID" 2>/dev/null; DPID=""
  for _ in $(seq 1 20); do [ -S "$W/st/control.sock" ] || break; sleep 0.5; done
}
sx() { # sx WS ARGS… — a session in WS
  local ws="$1"; shift
  local rc=0 t0=$SECONDS
  (cd "$W/$ws" && pe timeout 400 conductor sandbox exec --socket "$W/st/control.sock" -- cprobe "$@" 2>>"$W/sx.err") || rc=$?
  echo "sx $ws $1: exit $rc in $((SECONDS - t0))s" >> "$W/sx.err"
}
start_conductor

# 1. The boundary, from inside the session's container.
out="$(sx ws-a boundary --decoy "$W/home/.ssh/id_decoy" --other-ws "$W/ws-b" --allowed "$ALLOWED" --denied "$DENIED")"
expect_ok "$out" "in an agent container" "HOME is the container's" "not root" "no capabilities" "no-new-privileges" \
  "seccomp filter" "the operator's secret is unreadable" "another identity's workspace is absent" "no engine socket at /var/run/docker.sock" \
  "no network of its own" "an allowlist proxy is configured" "allowlisted egress" "egress off the allowlist refused"
grep -q "egress to .* denied\|egress denied" "$W/conductor.log" "$W/sx.err" 2>/dev/null && echo "ok   the denial is logged"
name_a="$("$ENGINE" ps -a --filter "$(daemon_label | sed 's/^/label=/')" --filter "label=conductor.persistent=true" --format '{{.Names}}' | head -1)"
check "one persistent container for the identity" 1 "$("$ENGINE" ps -a --filter "label=$(daemon_label)" --format '{{.Names}}' | grep -c .)"
check "its HOME is a volume" 1 "$("$ENGINE" volume ls -q --filter "label=$(daemon_label)" | grep -c .)"
insp="$("$ENGINE" inspect "$name_a" --format '{{.HostConfig.Privileged}} {{.HostConfig.NetworkMode}} {{.HostConfig.ReadonlyRootfs}} {{.HostConfig.CapDrop}} {{.HostConfig.SecurityOpt}} {{.HostConfig.PidsLimit}} {{.Config.User}}' 2>&1)"
echo "     | inspect: $insp"
case "$insp" in "false none true "*ALL*no-new-privileges*" 512 $(id -u):$(id -g)") echo "ok   the engine shows the hardening (not privileged, network none, read-only root, caps dropped, nnp, pids)" ;; *) echo "FAIL engine inspect: $insp"; fail=1 ;; esac
mounts="$("$ENGINE" inspect "$name_a" --format '{{range .Mounts}}{{.Type}}:{{.Source}}:{{.Destination}} {{end}}' 2>&1)"
# Exactly: the HOME volume, the workspace, conductor's helper dir (read-only).
want_mounts="volume:/home/agent bind:$W/ws-a:$W/ws-a bind:$W/st/agent-state/.containers/bin:/run/conductor/bin"
got_mounts="$(echo "$mounts" | tr ' ' '\n' | sed -E 's#^volume:[^:]*:#volume:#' | grep . | sort | tr '\n' ' ' | sed 's/ $//')"
check "the container's mounts are the HOME volume, the workspace and the helper — nothing else" "$(echo "$want_mounts" | tr ' ' '\n' | sort | tr '\n' ' ' | sed 's/ $//')" "$got_mounts"

# 2. What the identity installs in HOME lasts: another session, then a
#    conductor restart (with the container stopped meanwhile).
expect_ok "$(sx ws-a install mytool)" "installed mytool into HOME"
expect_ok "$(sx ws-a run mytool)" "ran mytool from HOME"
stop_conductor
"$ENGINE" stop -t 2 "$name_a" >/dev/null
start_conductor
expect_ok "$(sx ws-a run mytool)" "ran mytool from HOME"
check "the restart reused the same container" "$name_a" "$("$ENGINE" ps --filter "label=$(daemon_label)" --filter label=conductor.persistent=true --format '{{.Names}}' | head -1)"

# 3. Another identity: its own container and HOME.
out="$(sx ws-b run mytool)"
echo "$out" | sed 's/^/     | /'
case "$out" in "FAIL ran mytool"*) echo "ok   another identity does not have the first one's tool" ;; *) echo "FAIL identity b ran identity a's tool"; fail=1 ;; esac
expect_ok "$(sx ws-b boundary --other-ws "$W/ws-a")" "another identity's workspace is absent"
check "two identities, two containers" 2 "$("$ENGINE" ps -a --filter "label=$(daemon_label)" --format '{{.Names}}' | grep -c .)"
pe conductor sandbox ls --socket "$W/st/control.sock" | sed 's/^/     | /'

# 4. Archive identity a: its container and volume go, b's stay.
arch="$(pe conductor sandbox archive "dir:$W/ws-a" --socket "$W/st/control.sock" 2>&1)"
echo "     | $arch"
check "archive removed a's container" 0 "$("$ENGINE" ps -a --filter "label=$(daemon_label)" --format '{{.Names}}' | grep -c -- "$name_a")"
check "b's container and volume remain" "1 1" "$("$ENGINE" ps -a --filter "label=$(daemon_label)" --format '{{.Names}}' | grep -c .) $("$ENGINE" volume ls -q --filter "label=$(daemon_label)" | grep -c .)"

# 5. A paseo session through a conductor-owned provider entry runs in the
#    container (the fake ACP agent, copied into the workspace).
if [ -n "$EXTRA_PATH" ]; then
  ( cd "$W" && pe paseo daemon start --timeout 120 > "$W/paseo-start.log" 2>&1 ) || { echo "FAIL paseo daemon did not start: $(tail -5 "$W/paseo-start.log")"; fail=1; }
  python3 - "$W" <<'PY'
import json, sys
w = sys.argv[1]
p = f"{w}/home/.paseo/config.json"
c = json.load(open(p))
c["agents"]["providers"]["fake-sandboxed"] = {
    "extends": "acp", "label": "Fake (sandboxed)", "x-conductor": True,
    "command": [f"{w}/bin/conductor", "sandbox", "exec", "--socket", f"{w}/st/control.sock", "--", "fakeacp"]}
json.dump(c, open(p, "w"), indent=2)
PY
  pe paseo reload >/dev/null 2>&1
  git -C "$W/ws-p" init -q -b main && echo ws > "$W/ws-p/README.md"
  res="$(cd "$W/ws-p" && pe timeout 300 paseo run --provider fake-sandboxed --cwd "$W/ws-p" --title canary-container --json "canary task" 2>&1)"
  echo "$res" | head -5 | sed 's/^/     | /'
  check "the paseo session did its work in its workspace" yes "$( [ -s "$W/ws-p/CONDUCTOR_FIX.md" ] && echo yes || echo no)"
  if grep -q "sandbox-policy: .* in $W/ws-p (identity dir:$W/ws-p, container conductor-" "$W/conductor.log"; then
    echo "ok   paseo's session ran in the identity's container (sandbox-policy answered with one)"
  else echo "FAIL no container sandbox-policy for $W/ws-p: $(grep "sandbox-policy" "$W/conductor.log" | tail -2)"; fail=1; fi
else
  echo "skip the paseo leg (no paseo on PATH)"
fi

# 6. No engine on the daemon's PATH: the session is refused, never run.
stop_conductor
DPATH="$W/bin:/nonexistent"
start_conductor
rm -f "$W/ws-b/ran"
code=0
(cd "$W/ws-b" && env -i HOME="$W/home" PATH="$W/bin:/usr/bin:/bin" "$W/bin/conductor" sandbox exec --socket "$W/st/control.sock" -- /bin/sh -c "touch $W/ws-b/ran" 2>"$W/noengine.err") || code=$?
check "no engine: refused with 125" 125 "$code"
check "no engine: the agent never ran" no "$( [ -e "$W/ws-b/ran" ] && echo yes || echo no)"
if grep -q "container backend is unavailable" "$W/noengine.err"; then echo "ok   the refusal names the missing engine"; else echo "FAIL refusal message: $(cat "$W/noengine.err")"; fail=1; fi
stop_conductor

echo "== container canary ($ENGINE): $([ $fail = 0 ] && echo PASS || echo FAIL) =="
[ $fail = 0 ] || { echo "--- conductor.log (tail)"; tail -40 "$W/conductor.log"; echo "--- sx.err"; tail -20 "$W/sx.err"; ls "$W/home/.paseo"; tail -20 "$W/home/.paseo/"*.log 2>/dev/null; }
exit $fail
