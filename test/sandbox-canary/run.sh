#!/usr/bin/env bash
# The agent-sandbox canary (docs/design/agent-sandbox.md §9): conductor's
# built-in sandbox — Linux namespaces + pivot_root, macOS Seatbelt — around a
# fake agent, through the real path: a throwaway conductor daemon (temp HOME)
# answers `conductor sandbox exec`'s sandbox-policy, and the probe runs under
# the policy it returns. No model, no secrets.
#
#   - the operator's secrets and conductor's state are absent;
#   - the workspace and the identity home are writable, and persist;
#   - the conductor socket is reachable (`conductor call` reaches the daemon);
#   - the network is cut but for the allowlist (example.org yes, anything else no);
#   - no credential is in the agent's environment;
#   - with the daemon down, the launch is refused (exit 125) — never unsandboxed.
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
# A SHORT root: unix socket paths are capped at 104 bytes on macOS.
W="$(mktemp -d /tmp/csc.XXXXXX)"
W="$(cd "$W" && pwd -P)"
DAEMON_PID=""
cleanup() { [ -n "$DAEMON_PID" ] && kill "$DAEMON_PID" 2>/dev/null; rm -rf "$W"; }
trap cleanup EXIT
fail=0
check() { # check <name> <expected> <actual>
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want '$2', got '$3'"; fail=1; fi
}

mkdir -p "$W/bin" "$W/home/.ssh" "$W/home/.aws" "$W/st" "$W/ws" "$W/cfg"
echo "DECOY-ssh" > "$W/home/.ssh/id_ed25519"
echo "DECOY-aws" > "$W/home/.aws/credentials"
echo "ws" > "$W/ws/README.md"
( cd "$ROOT" && go build -o "$W/bin/conductor" ./cmd/conductor ) || { echo "build failed"; exit 1; }

cat > "$W/cfg/config.yaml" <<EOF
connectors:
  timer:
    use: cron
    schedules:
      never: { cron: "0 4 1 1 *" }
store:
  state_file: $W/st/state.json
  audit_log: $W/st/audit.jsonl
sandbox:
  egress: ["example.org"]
EOF
echo "DECOY-state" > "$W/st/secret.json"

run_env() { env -i HOME="$W/home" PATH="$W/bin:/usr/bin:/bin:/usr/sbin:/sbin" LANG=C "$@"; }

env -i HOME="$W/home" PATH="$W/bin:/usr/bin:/bin:/usr/sbin:/sbin" LANG=C \
  conductor run --config "$W/cfg/config.yaml" > "$W/daemon.log" 2>&1 &
DAEMON_PID=$!
for _ in $(seq 1 60); do [ -S "$W/st/control.sock" ] && break; sleep 0.5; done
if ! grep -q "agent sandbox: on" "$W/daemon.log"; then
  echo "FAIL the daemon did not turn the agent sandbox on"; cat "$W/daemon.log"; exit 1
fi
if grep -q "cannot be built on this box" "$W/daemon.log"; then
  echo "FAIL the sandbox cannot be built here:"; grep "cannot be built" "$W/daemon.log"; exit 1
fi

cat > "$W/ws/probe.sh" <<'PROBE'
#!/bin/sh
r() { if eval "$2" >/dev/null 2>&1; then echo "$1=yes"; else echo "$1=no"; fi; }
r secret_ssh "cat $CANARY_W/home/.ssh/id_ed25519"
r secret_aws "cat $CANARY_W/home/.aws/credentials"
r state "cat $CANARY_W/st/secret.json"
r ws_write "echo w > ./out.txt"
r home_write "echo h > \$HOME/kept.txt"
r allowed_egress "curl -sS -m 15 -o /dev/null https://example.org"
r other_egress "curl -sS -m 8 -o /dev/null https://cloudflare.com"
r direct_net "curl -sS -m 8 --noproxy '*' -o /dev/null https://example.org"
out=$(conductor call step.done 2>&1); case "$out" in *"skill.verbs"*|*released*) echo socket=yes ;; *) echo "socket=no ($out)" ;; esac
if env | grep -qE '^(GH_TOKEN|GITHUB_TOKEN|SSH_AUTH_SOCK|AWS_SECRET_ACCESS_KEY|CLAUDE_CODE_MESSAGING_TOKEN)='; then echo env_clean=no; else echo env_clean=yes; fi
echo "home=$HOME"
PROBE
cd "$W/ws"
out="$(run_env CANARY_W="$W" GH_TOKEN=ghp_leak SSH_AUTH_SOCK=/tmp/x AWS_SECRET_ACCESS_KEY=leak CLAUDE_CODE_MESSAGING_TOKEN=leak \
  conductor sandbox exec --config "$W/cfg/config.yaml" -- /bin/sh ./probe.sh 2>&1)"
rc=$?
echo "== $(uname -s): conductor sandbox exec probe (exit $rc) =="
echo "$out"
v() { printf '%s\n' "$out" | sed -n "s/^$1=//p" | head -1 | cut -d' ' -f1; }
check "sandbox exec ran the agent" 0 "$rc"
check "operator ssh key absent" no "$(v secret_ssh)"
check "operator cloud credentials absent" no "$(v secret_aws)"
check "conductor state absent" no "$(v state)"
check "workspace writable" yes "$(v ws_write)"
check "identity home writable" yes "$(v home_write)"
check "allowlisted egress reaches" yes "$(v allowed_egress)"
check "other egress refused" no "$(v other_egress)"
check "no network around the proxy" no "$(v direct_net)"
check "conductor socket reachable" yes "$(v socket)"
check "no credential in env" yes "$(v env_clean)"
home="$(v home)"
case "$home" in "$W"/st/agent-state/*) echo "ok   HOME is the identity's tool state" ;; *) echo "FAIL HOME=$home"; fail=1 ;; esac
check "workspace write persisted" w "$(cat "$W/ws/out.txt" 2>/dev/null)"
check "identity home write persisted" h "$(cat "$home/kept.txt" 2>/dev/null)"

kill "$DAEMON_PID"; wait "$DAEMON_PID" 2>/dev/null; DAEMON_PID=""
for _ in $(seq 1 20); do [ -S "$W/st/control.sock" ] || break; sleep 0.5; done
rm -f "$W/ws/ran.txt"
run_env conductor sandbox exec --config "$W/cfg/config.yaml" -- /bin/sh -c 'echo ran > ran.txt' > "$W/refused.log" 2>&1
refused_rc=$?
check "daemon down: refused with 125" 125 "$refused_rc"
check "daemon down: the agent never ran" no "$( [ -e "$W/ws/ran.txt" ] && echo yes || echo no)"
grep -q "never launched unsandboxed" "$W/refused.log" && echo "ok   refusal says why" || { echo "FAIL refusal message: $(cat "$W/refused.log")"; fail=1; }

echo "== agent-sandbox canary: $([ $fail = 0 ] && echo PASS || echo FAIL) =="
exit $fail
