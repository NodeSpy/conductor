#!/usr/bin/env bash
# The smee move, end to end and across versions: real daemons, real github
# and smee plugin builds, a smee-compatible fake channel, and a trigger that
# must fire for every delivery.
#
# A config in the retired shape — github `webhook.smee_url`, and the bundled
# webhook connector's `smee_url` — boots on each conductor × plugin
# combination a fleet passes through while conductor and plugins auto-update
# independently. A signed issue_comment posted to the github channel must
# fire a `gh.new_comment` trigger, and a delivery posted to the webhook
# channel a `hooks.<source>` trigger. The new conductor runs the plugins in
# their jail: there, the smee relay reaches github's listener (expose_local)
# but never the bundled webhook connector's, conductor's own port, unless
# its smee connector is `trust: full` — each row says which deliveries must
# arrive and which must be refused, and logged. The rows that run the new
# conductor must also have rewritten the config (backup beside it), and a
# second boot must leave it alone.
#
# Hermetic: every daemon gets its own HOME, state and config dirs under WORK;
# the channel, the github API and the sink are local fakes on loopback; the
# plugins are recorded in each state dir's install state from local builds
# (seed/), so nothing is fetched. Run it in a PID namespace so nothing it
# starts can outlive it:
#
#   unshare --user --map-current-user --pid --fork --kill-child --mount --mount-proc \
#     bash test/smee-migration/run.sh
#
# Inputs (binaries; each defaults to a build from this tree where it can):
#   CONDUCTOR_NEW   this tree's conductor (built when unset)
#   CONDUCTOR_OLD   a conductor from before the move (required)
#   GITHUB_OLD      the github plugin before its smee_url deprecation (required)
#   GITHUB_NEW      the github plugin with smee_url deprecated (required)
#   SMEE_OLD        the released smee plugin (required)
#   SMEE_NEW        the smee plugin from the same change (required)
#   WORK            scratch dir (default: a fresh dir under $TMPDIR)
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="${WORK:-$(mktemp -d "${TMPDIR:-/var/tmp}/smee-migration.XXXXXX")}"
BIN="$WORK/bin"
mkdir -p "$BIN"

say() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
pass() { printf '\033[32mPASS\033[0m %s\n' "$*"; PASSED=$((PASSED + 1)); }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$*"; FAILED=$((FAILED + 1)); FAILS+=("$*"); }
PASSED=0 FAILED=0 FAILS=()

for v in CONDUCTOR_OLD GITHUB_OLD GITHUB_NEW SMEE_OLD SMEE_NEW; do
  [ -x "${!v:-}" ] || { echo "$v must name an executable"; exit 2; }
done

say "build"
(
  cd "$ROOT"
  [ -n "${CONDUCTOR_NEW:-}" ] || go build -o "$BIN/conductor-new" ./cmd/conductor
  go build -o "$BIN/seed" ./test/smee-migration/seed
  go build -o "$BIN/mocksmee" ./test/e2e/services/mocksmee
  go build -o "$BIN/mockgithub" ./test/e2e/services/mockgithub
  go build -o "$BIN/sinkcatcher" ./test/e2e/services/sinkcatcher
) || { echo "build failed"; exit 2; }
CONDUCTOR_NEW="${CONDUCTOR_NEW:-$BIN/conductor-new}"

freeport() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }

# start_bg <name> <cmd…>: start in the background, pid recorded in a file.
start_bg() {
  local name=$1; shift
  "$@" >"$WORK/$name.log" 2>&1 &
  echo $! >"$WORK/$name.pid"
}
# stop_bg <name>: kill the pid recorded for name, once it is checked to still
# be the command started under that name.
stop_bg() {
  local f="$WORK/$1.pid" pid
  [ -f "$f" ] || return 0
  pid=$(cat "$f")
  if [ -r "/proc/$pid/cmdline" ] && tr '\0' ' ' <"/proc/$pid/cmdline" | grep -q -- "$2"; then
    kill "$pid" 2>/dev/null
    for _ in $(seq 50); do [ -d "/proc/$pid" ] || break; sleep 0.1; done
    kill -9 "$pid" 2>/dev/null
  fi
  rm -f "$f"
}
wait_for() { # <seconds> <cmd…>
  local n=$(( $1 * 10 )); shift
  for _ in $(seq "$n"); do "$@" && return 0; sleep 0.1; done
  return 1
}

P_SMEE=$(freeport) P_GH=$(freeport) P_SINK=$(freeport)
say "fakes: channel :$P_SMEE, github API :$P_GH, sink :$P_SINK"
start_bg mocksmee env LISTEN="127.0.0.1:$P_SMEE" SMEE_BODY=object "$BIN/mocksmee"
start_bg mockgithub env LISTEN="127.0.0.1:$P_GH" ME=harness-user "$BIN/mockgithub"
start_bg sinkcatcher env LISTEN="127.0.0.1:$P_SINK" "$BIN/sinkcatcher"
trap 'for d in "$WORK"/*/daemon.pid; do [ -f "$d" ] && stop_bg "$(basename "$(dirname "$d")")/daemon" conductor; done; stop_bg mocksmee mocksmee; stop_bg mockgithub mockgithub; stop_bg sinkcatcher sinkcatcher' EXIT
for p in $P_SMEE $P_GH $P_SINK; do
  wait_for 10 curl -sf "http://127.0.0.1:$p/_health" -o /dev/null || { echo "a fake did not come up"; exit 2; }
done

SECRET=harness-webhook-secret

# write_config <dir> <row>: the RETIRED shape, both connectors on their own
# channel of the fake.
write_config() {
  local d=$1 row=$2
  mkdir -p "$d/cfg"
  cat >"$d/cfg/conductor.env" <<ENV
GH_WEBHOOK_SECRET=$SECRET
HOOKS_SMEE_URL=http://127.0.0.1:$P_SMEE/$row-hooks
ENV
  cat >"$d/cfg/conductor.yaml" <<YAML
# harness row $row — written in the retired smee shape
update: { auto: false }
connectors:
  gh:
    use: github
    api_base: http://127.0.0.1:$P_GH
    token: harness-static-token
    webhook:
      smee_url: http://127.0.0.1:$P_SMEE/$row-gh   # the channel the App posts to
      secret: \${GH_WEBHOOK_SECRET}
    sweep: { enabled: false }
    me: { logins: [harness-user] }
  hooks:
    use: webhook
    smee_url: \${HOOKS_SMEE_URL}
    allow_private: true
    sources:
      alarm: { path: /alarm }
triggers:
  - on: gh.new_comment
    name: gh-comment
    filter: { repo: ["harness/svc"] }
    steps:
      - id: tell
        uses: hooks.post
        options: { url: "http://127.0.0.1:$P_SINK/harness/$row/gh", body: "fired" }
  - on: hooks.alarm
    name: hooks-alarm
    steps:
      - id: tell
        uses: hooks.post
        options: { url: "http://127.0.0.1:$P_SINK/harness/$row/hooks", body: "fired" }
YAML
}

daemon_env() { # <dir>
  echo env -i HOME="$1/home" PATH=/usr/local/bin:/usr/bin:/bin \
    XDG_STATE_HOME="$1/home/.local/state" XDG_CONFIG_HOME="$1/home/.config" \
    XDG_RUNTIME_DIR="$1/run" TMPDIR="$1/tmp"
}

boot() { # <dir> <conductor>
  local d=$1
  mkdir -p "$d/home" "$d/run" "$d/tmp"
  chmod 700 "$d/run"
  # shellcheck disable=SC2046
  start_bg "$(basename "$d")/daemon" $(daemon_env "$d") "$2" run --config "$d/cfg/conductor.yaml" --state-dir "$d/state"
}
stop() { stop_bg "$(basename "$1")/daemon" "conductor"; }

subscribed() { # <channel>: the fake has a live subscriber on it
  grep -q "subscriber joined \"$1\"" "$WORK/mocksmee.log"
}
fired() { # <row> <which>
  curl -sf "http://127.0.0.1:$P_SINK/_captured" | grep -q "\"/harness/$1/$2\""
}
count_fired() { # <row> <which>
  curl -sf "http://127.0.0.1:$P_SINK/_captured" | grep -o "\"/harness/$1/$2\"" | wc -l
}

post_github() { # <row> <n>
  local body
  body=$(printf '{"action":"created","installation":{"id":42},"repository":{"full_name":"harness/svc","name":"svc","default_branch":"main","owner":{"login":"harness"}},"issue":{"number":%d,"html_url":"https://forge.test/harness/svc/pull/%d","user":{"login":"harness-user"},"pull_request":{"url":"https://forge.test/harness/svc/pull/%d"}},"comment":{"id":%d,"user":{"login":"a-reviewer"},"body":"comment %s %d"}}' "$2" "$2" "$2" "$((9000 + $2))" "$1" "$2")
  local sig
  sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$SECRET" | sed 's/^.* //')
  curl -sf -X POST "http://127.0.0.1:$P_SMEE/$1-gh" -H "Content-Type: application/json" \
    -H "X-GitHub-Event: issue_comment" -H "X-GitHub-Delivery: $1-$2-$RANDOM" \
    -H "X-Hub-Signature-256: sha256=$sig" --data-binary "$body" -o /dev/null
}
post_hooks() { # <row> <n>
  curl -sf -X POST "http://127.0.0.1:$P_SMEE/$1-hooks" -H "Content-Type: application/json" \
    --data-binary "{\"alarm\":\"$1\",\"n\":$2}" -o /dev/null
}

# expect <name> <what> <channel> <row> <which> <yes|refused> <refusal log>:
# a delivery on the channel fires its trigger (yes), or, refused by the jail,
# never arrives and the daemon log names the refusal.
expect() {
  local name=$1 what=$2 row=$4 which=$5 mode=$6 why=$7
  if [ "$mode" = yes ]; then
    if wait_for 30 fired "$row" "$which"; then pass "$name: a delivery on the channel fired $what"; else fail "$name: $what never fired"; fi
    return
  fi
  sleep 8
  if fired "$row" "$which"; then fail "$name: $what fired, but its relay should have been refused"; else pass "$name: the jail kept the relay from delivering $what"; fi
  if grep -q -- "$why" "$WORK/$name/daemon.log"; then pass "$name: the refusal is logged"; else fail "$name: no log line naming the refusal ($why)"; fi
}

# jailed <name> <plugin>: the new conductor ran the plugin in its jail.
jailed() {
  if grep -E "plugin [^ ]*$2[^ ]*: registered connector type .*permissions: jailed to" "$WORK/$1/daemon.log" >/dev/null; then
    pass "$1: the $2 plugin runs jailed"
  else
    fail "$1: the $2 plugin is not jailed"
  fi
}

OWN_PORT="its listener 127.0.0.1:[0-9]* is conductor's own port"
NO_CAP="does not declare expose_local"

# row <name> <conductor> <github> <smee> <migrates: yes|no> <gh: yes|refused> <hooks: yes|refused>
row() {
  local name=$1 cond=$2 gh=$3 smee=$4 migrates=$5 ghx=$6 hooksx=$7
  local d="$WORK/$name"
  say "$name"
  mkdir -p "$d/state"
  write_config "$d" "$name"
  local orig; orig=$(cat "$d/cfg/conductor.yaml")
  "$BIN/seed" -state "$d/state" "github=$gh" "smee=$smee" >"$d/seed.log" 2>&1 || { fail "$name: seed ($(tail -1 "$d/seed.log"))"; return; }
  boot "$d" "$cond"

  if wait_for 60 subscribed "$name-gh"; then pass "$name: the github channel has a subscriber"; else fail "$name: nobody subscribed to the github channel"; fi
  if wait_for 30 subscribed "$name-hooks"; then pass "$name: the webhook channel has a subscriber"; else fail "$name: nobody subscribed to the webhook channel"; fi
  sleep 1 # the listener the relay posts to is up by the time the relay subscribed; give it a beat anyway
  if [ "$cond" = "$CONDUCTOR_NEW" ]; then jailed "$name" github; jailed "$name" smee; fi

  post_github "$name" 1
  expect "$name" gh.new_comment "$name-gh" "$name" gh "$ghx" "$NO_CAP"
  post_hooks "$name" 1
  expect "$name" hooks.alarm "$name-hooks" "$name" hooks "$hooksx" "$OWN_PORT"

  local now; now=$(cat "$d/cfg/conductor.yaml")
  if [ "$migrates" = yes ]; then
    if grep -q "smee_url" <<<"$now"; then fail "$name: smee_url still in the config"; else pass "$name: the config was rewritten"; fi
    if grep -q "use: smee" <<<"$now" && grep -q "channel: \${HOOKS_SMEE_URL}" <<<"$now" && grep -q "secret: \${GH_WEBHOOK_SECRET}" <<<"$now"; then
      pass "$name: smee connectors on the same channels, secrets still references"
    else
      fail "$name: rewrite content"; echo "$now"
    fi
    if [ "$(cat "$d/cfg/conductor.yaml.pre-smee" 2>/dev/null)" = "$orig" ]; then pass "$name: backup holds the original"; else fail "$name: no faithful backup"; fi
    grep -q "config migrate: rewrote" "$WORK/$name/daemon.log" && pass "$name: the rewrite is logged" || fail "$name: the rewrite is not logged"
    # Second boot: idempotent, and still delivering.
    stop "$d"
    boot "$d" "$cond"
    sleep 2
    if [ "$(cat "$d/cfg/conductor.yaml")" = "$now" ]; then pass "$name: a second boot leaves the config alone"; else fail "$name: the second boot rewrote again"; fi
    if [ "$ghx" = yes ]; then
      local before; before=$(count_fired "$name" gh)
      wait_for 60 bash -c "[ \$(grep -c 'subscriber joined \"$name-gh\"' '$WORK/mocksmee.log') -ge 2 ]"
      sleep 1
      post_github "$name" 2
      if wait_for 30 bash -c "[ \$(curl -sf http://127.0.0.1:$P_SINK/_captured | grep -o '\"/harness/$name/gh\"' | wc -l) -gt $before ]"; then
        pass "$name: still delivering after the second boot"
      else
        fail "$name: no delivery after the second boot"
      fi
    fi
  else
    if [ "$now" = "$orig" ]; then pass "$name: the config is untouched"; else fail "$name: the config changed"; fi
  fi
  if grep -q "webhook.smee_url is deprecated" "$d/daemon.log"; then echo "   (the github plugin logged its smee_url deprecation)"; fi
  stop "$d"
}

# The fleet, one row per combination it passes through. The new conductor
# runs every plugin jailed: smee may deliver to github's listener (the
# listener its expose: wiring names, granted through expose_local), never to
# the bundled webhook connector's, which is conductor's own port.
row old-cond.old-gh   "$CONDUCTOR_OLD" "$GITHUB_OLD" "$SMEE_OLD" no  yes yes
row old-cond.new-gh   "$CONDUCTOR_OLD" "$GITHUB_NEW" "$SMEE_OLD" no  yes yes
row new-cond.old-gh   "$CONDUCTOR_NEW" "$GITHUB_OLD" "$SMEE_NEW" yes yes refused
row new-cond.new-gh   "$CONDUCTOR_NEW" "$GITHUB_NEW" "$SMEE_NEW" yes yes refused
# An smee build from before expose_local, jailed: it cannot reach the
# listener, and the log says what to do.
row new-cond.old-smee "$CONDUCTOR_NEW" "$GITHUB_NEW" "$SMEE_OLD" yes refused refused

# The operator's opt-out for the bundled webhook connector: trust: full on
# its smee connector (the old relay ran inside conductor, unjailed, too).
say "new-cond.webhook-trust-full"
d="$WORK/new-cond.webhook-trust-full"
mkdir -p "$d/state"
cp -r "$WORK/new-cond.new-gh/cfg" "$d/cfg"
rm -f "$d/cfg/conductor.yaml.pre-smee"
sed -i "s#/new-cond.new-gh-gh#/trustfull-gh#; s#/new-cond.new-gh/#/trustfull/#g; s#new-cond.new-gh-hooks#trustfull-hooks#" "$d/cfg/conductor.yaml" "$d/cfg/conductor.env"
sed -i 's#^  hooks-smee:$#  hooks-smee:\n    trust: full#' "$d/cfg/conductor.yaml"
"$BIN/seed" -state "$d/state" "github=$GITHUB_NEW" "smee=$SMEE_NEW" >"$d/seed.log" 2>&1
boot "$d" "$CONDUCTOR_NEW"
if wait_for 60 subscribed trustfull-hooks && wait_for 30 subscribed trustfull-gh; then pass "webhook-trust-full: both channels have a subscriber"; else fail "webhook-trust-full: no subscriber"; fi
sleep 1
post_hooks trustfull 1
if wait_for 30 fired trustfull hooks; then pass "webhook-trust-full: hooks.alarm fires through the relay path"; else fail "webhook-trust-full: hooks.alarm never fired"; fi
post_github trustfull 1
if wait_for 30 fired trustfull gh; then pass "webhook-trust-full: gh.new_comment still fires"; else fail "webhook-trust-full: gh.new_comment never fired"; fi
stop "$d"

# Rollback: the previous conductor (doUpdate's <exe>.prev) started on a
# config the new one already rewrote. github keeps working through the smee
# connector (listeners predate the move); the bundled webhook connector of an
# older conductor has no relay path, so its deliveries wait for the backup to
# be restored (or the new conductor to come back).
say "rollback-after-migration"
d="$WORK/rollback"
mkdir -p "$d/state"
cp -r "$WORK/new-cond.new-gh/cfg" "$d/cfg"
sed -i "s#/new-cond.new-gh-gh#/rollback-gh#; s#/new-cond.new-gh/#/rollback/#g; s#new-cond.new-gh-hooks#rollback-hooks#" "$d/cfg/conductor.yaml" "$d/cfg/conductor.env"
"$BIN/seed" -state "$d/state" "github=$GITHUB_NEW" "smee=$SMEE_NEW" >"$d/seed.log" 2>&1
boot "$d" "$CONDUCTOR_OLD"
if wait_for 60 subscribed rollback-gh; then pass "rollback: the smee connector subscribed under the older conductor"; else fail "rollback: no subscriber"; fi
sleep 1
post_github rollback 1
if wait_for 30 fired rollback gh; then pass "rollback: gh.new_comment fires under the older conductor"; else fail "rollback: gh.new_comment never fired"; fi
stop "$d"

say "result"
echo "$PASSED passed, $FAILED failed"
for f in "${FAILS[@]}"; do echo "  - $f"; done
echo "logs: $WORK"
[ "$FAILED" -eq 0 ]
