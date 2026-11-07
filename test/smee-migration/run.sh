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
# but never the bundled webhook connector's, conductor's own port — so the
# migration makes that one's smee connector `trust: full` + `isolate: true`,
# the trust the relay had inside conductor. Each row says which deliveries
# must arrive and which must be refused, and logged. The rows that run the new
# conductor must also have rewritten the config (backup beside it), and a
# second boot must leave it alone. Where no smee release it could be granted
# expose_local from is available, the github rewrite must be held back with a
# warning, and the github connector must keep delivering on smee_url.
#
# Hermetic: every daemon gets its own HOME, state and config dirs under WORK;
# the channel, the github API and the sink are local fakes on loopback; the
# plugins are recorded in each state dir's install state from local builds
# (seed/), and the smee releases a row's migration may install or update to
# come from a git repo of its own (forge), so nothing is fetched from the
# network. Run it in a PID namespace so nothing it
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
  "$@" >>"$WORK/$name.log" 2>&1 &
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

# A fake release repo per row: what `use: smee` resolves and fetches, over
# plain git like the real one (refs/dist/<tag>/<platform>, the binary and
# checksums.txt). Each row's HOME rewrites the official source's URL to it.
SMEE_SOURCE=$("$BIN/seed" -source-url smee) || { echo "seed -source-url failed"; exit 2; }
PLATFORM="$(go env GOOS)_$(go env GOARCH)"
ASSET="conductor-smee_$PLATFORM"
# forge <dir> <tag=binary>…: publish each binary as connectors/smee/<tag>.
forge() {
  local d=$1; shift
  local repo="$d/forge.git"
  git init -q --bare "$repo"
  for rel in "$@"; do
    local tag=${rel%%=*} bin=${rel#*=} blob sums tree commit
    blob=$(git --git-dir="$repo" hash-object -w "$bin")
    sums=$(printf '%s  %s\n' "$(sha256sum "$bin" | cut -d' ' -f1)" "$ASSET" | git --git-dir="$repo" hash-object -w --stdin)
    tree=$(printf '100755 blob %s\t%s\n100644 blob %s\tchecksums.txt\n' "$blob" "$ASSET" "$sums" | git --git-dir="$repo" mktree)
    commit=$(GIT_AUTHOR_NAME=harness GIT_AUTHOR_EMAIL=harness@localhost GIT_COMMITTER_NAME=harness GIT_COMMITTER_EMAIL=harness@localhost \
      git --git-dir="$repo" commit-tree "$tree" -m "connectors/smee/$tag")
    git --git-dir="$repo" update-ref "refs/dist/connectors/smee/$tag/$PLATFORM" "$commit"
  done
  mkdir -p "$d/home"
  git config --file "$d/home/.gitconfig" "url.file://$repo.insteadOf" "$SMEE_SOURCE"
}

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

# HOST is the host the next daemons boot on: "" (this one, which can jail
# plugins), root (the daemon runs as root — a default Docker container), or
# userns-blocked (non-root, user namespaces blocked — a container whose
# seccomp denies them). On the last two the plugin jail can't be built.
HOST=""
boot() { # <dir> <conductor>
  local d=$1 name; name="$(basename "$d")/daemon"
  mkdir -p "$d/home" "$d/run" "$d/tmp"
  chmod 700 "$d/run"
  # shellcheck disable=SC2046
  local cmd=($(daemon_env "$d") "$2" run --config "$d/cfg/conductor.yaml" --state-dir "$d/state")
  case "$HOST" in
    "") start_bg "$name" "${cmd[@]}" ;;
    root) start_bg "$name" unshare --user --map-root-user -- "${cmd[@]}" ;;
    userns-blocked)
      # bwrap forks: the daemon records its own pid (stop_bg kills that).
      "$BWRAP" --unshare-user --disable-userns --die-with-parent --dev-bind / / -- \
        sh -c 'echo $$ >"$0"; exec "$@"' "$WORK/$name.pid" "${cmd[@]}" >>"$WORK/$name.log" 2>&1 &
      wait_for 10 test -s "$WORK/$name.pid" ;;
  esac
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

# asbefore <name> <plugin>: on a host that cannot jail plugins, the plugin
# ran unjailed as before the jail, and the log says so.
asbefore() {
  if grep -E "plugin [^ ]*$2[^ ]*: WARNING: running UNJAILED, as before the plugin jail — this host cannot jail plugins" "$WORK/$1/daemon.log" >/dev/null; then
    pass "$1: the $2 plugin runs unjailed as before the jail, with the warning"
  else
    fail "$1: the $2 plugin did not run as before the jail"
  fi
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

# row <name> <conductor> <github> <smee installed, or -> <smee releases: tag=binary,… or -> <migrates: yes|hooks-only|no> <gh: yes|refused> <hooks: yes|refused> [github channel subscribed: yes|no]
#
# hooks-only: the github rewrite is held back (no smee release it could be
# granted expose_local from): github keeps its own smee_url client, with a
# warning, and the bundled webhook connector is migrated anyway.
row() {
  local name=$1 cond=$2 gh=$3 smee=$4 releases=$5 migrates=$6 ghx=$7 hooksx=$8 subs=${9:-yes}
  local d="$WORK/$name"
  say "$name"
  mkdir -p "$d/state"
  write_config "$d" "$name"
  local orig; orig=$(cat "$d/cfg/conductor.yaml")
  local seeds=("github=$gh")
  [ "$smee" = - ] || seeds+=("smee=$smee")
  local pre=()
  [ -n "$HOST" ] && pre=(-pre-jail) # a box that upgraded: its plugins predate the jail
  "$BIN/seed" -state "$d/state" "${pre[@]}" "${seeds[@]}" >"$d/seed.log" 2>&1 || { fail "$name: seed ($(tail -1 "$d/seed.log"))"; return; }
  if [ "$releases" != - ]; then
    # shellcheck disable=SC2086
    forge "$d" ${releases//,/ }
  fi
  boot "$d" "$cond"

  if [ "$subs" = yes ]; then
    if wait_for 60 subscribed "$name-gh"; then pass "$name: the github channel has a subscriber"; else fail "$name: nobody subscribed to the github channel"; fi
  fi
  if wait_for 60 subscribed "$name-hooks"; then pass "$name: the webhook channel has a subscriber"; else fail "$name: nobody subscribed to the webhook channel"; fi
  sleep 1 # the listener the relay posts to is up by the time the relay subscribed; give it a beat anyway
  if [ "$cond" = "$CONDUCTOR_NEW" ] && [ -n "$HOST" ]; then
    asbefore "$name" github
    asbefore "$name" smee # gh-smee: the smee the migration installed inherits github's treatment
  elif [ "$cond" = "$CONDUCTOR_NEW" ]; then
    jailed "$name" github
    [ "$migrates" = yes ] && jailed "$name" smee # (hooks-only: the one smee connector is the unjailed hooks-smee)
  fi

  post_github "$name" 1
  expect "$name" gh.new_comment "$name-gh" "$name" gh "$ghx" "$NO_CAP"
  post_hooks "$name" 1
  expect "$name" hooks.alarm "$name-hooks" "$name" hooks "$hooksx" "$OWN_PORT"

  local now; now=$(cat "$d/cfg/conductor.yaml")
  if [ "$migrates" = hooks-only ]; then
    if grep -q "smee_url: http://127.0.0.1:$P_SMEE/$name-gh" <<<"$now" && ! grep -q "gh-smee" <<<"$now"; then pass "$name: the github connector is held back on smee_url"; else fail "$name: the github connector was rewritten"; echo "$now"; fi
    if ! grep -v '^ *#' <<<"$now" | grep -q "smee_url: \${HOOKS_SMEE_URL}" && awk '/^  hooks-smee:/{h=1;next} /^  [^ ]/{h=0} h' <<<"$now" | grep -q "trust: full"; then
      pass "$name: the webhook connector was migrated (trust: full)"
    else
      fail "$name: the webhook connector was not migrated"; echo "$now"
    fi
    if grep -q "config migrate: warning: the rewrites that wire smee are held back: no smee release declaring expose_local" "$d/daemon.log"; then pass "$name: the hold-back is a warning naming why"; else fail "$name: no hold-back warning"; fi
    if [ "$(cat "$d/cfg/conductor.yaml.pre-smee" 2>/dev/null)" = "$orig" ]; then pass "$name: backup holds the original"; else fail "$name: no faithful backup"; fi
  elif [ "$migrates" = yes ]; then
    if grep -v '^ *#' <<<"$now" | grep -q "smee_url"; then fail "$name: smee_url still in the config"; else pass "$name: the config was rewritten"; fi
    if grep -q "use: smee" <<<"$now" && grep -q "channel: \${HOOKS_SMEE_URL}" <<<"$now" && grep -q "secret: \${GH_WEBHOOK_SECRET}" <<<"$now"; then
      pass "$name: smee connectors on the same channels, secrets still references"
    else
      fail "$name: rewrite content"; echo "$now"
    fi
    # The bundled webhook connector's smee connector is unjailed and isolated,
    # with the reason; github's is not.
    if awk '/^  hooks-smee:/{h=1;next} /^  [^ ]/{h=0} h' <<<"$now" | grep -q "trust: full" &&
      awk '/^  hooks-smee:/{h=1;next} /^  [^ ]/{h=0} h' <<<"$now" | grep -q "isolate: true" &&
      awk '/^  hooks-smee:/{h=1;next} /^  [^ ]/{h=0} h' <<<"$now" | grep -q "# trust/isolate: " &&
      ! awk '/^  gh-smee:/{h=1;next} /^  [^ ]/{h=0} h' <<<"$now" | grep -q "trust"; then
      pass "$name: the webhook connector's smee connector is trust: full + isolate: true, with the reason; github's stays jailed"
    else
      fail "$name: trust on the generated smee connectors"; echo "$now"
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
# runs github and gh-smee jailed: smee delivers to github's listener (the
# listener its expose: wiring names, granted through expose_local). The
# migration made hooks-smee trust: full + isolate: true, so the bundled
# webhook connector keeps receiving too.
row old-cond.old-gh   "$CONDUCTOR_OLD" "$GITHUB_OLD" "$SMEE_OLD" - no  yes yes
row old-cond.new-gh   "$CONDUCTOR_OLD" "$GITHUB_NEW" "$SMEE_OLD" - no  yes yes
row new-cond.old-gh   "$CONDUCTOR_NEW" "$GITHUB_OLD" "$SMEE_NEW" - yes yes yes
row new-cond.new-gh   "$CONDUCTOR_NEW" "$GITHUB_NEW" "$SMEE_NEW" - yes yes yes

# smee is not installed, and its latest release predates expose_local: the
# migration installs it for hooks-smee (unjailed, so it delivers), but holds
# the github rewrite back — a jailed gh-smee could not reach github's
# listener. github keeps its own smee_url client, and says it is deprecated.
row new-cond.fresh-smee-released "$CONDUCTOR_NEW" "$GITHUB_NEW" - "v1.1.0=$SMEE_OLD" hooks-only yes yes
# An smee from before expose_local is installed, and a release declaring it
# is out: the migration updates to it and approves what the update asks for
# (expose_local, and its channel's host), since that is exactly what it wires.
row new-cond.old-smee-installed "$CONDUCTOR_NEW" "$GITHUB_NEW" "$SMEE_OLD" "v1.1.0=$SMEE_OLD,v1.2.0=$SMEE_NEW" yes yes yes
if grep -q "config migrate: approved smee connectors/smee/v1.2.0 (replacing connectors/smee/v0.0.0-harness), the update the rewrite needs, which also may: .*expose" "$WORK/new-cond.old-smee-installed/daemon.log"; then
  pass "new-cond.old-smee-installed: the migration's approval is logged, naming what it grants"
else
  fail "new-cond.old-smee-installed: no logged approval"
fi
# An smee from before expose_local is installed, and nothing newer is out:
# held back like the fresh install.
row new-cond.old-smee-no-release "$CONDUCTOR_NEW" "$GITHUB_NEW" "$SMEE_OLD" "v1.1.0=$SMEE_OLD" hooks-only yes yes

# Hosts that cannot jail plugins, upgraded from a conductor without the jail
# (github recorded as installed before it), smee not installed and its
# released build without expose_local. The migration installs smee and it
# inherits github's treatment there — unjailed, as before the jail, with the
# warning — instead of being refused as a plugin installed since; so the
# whole config migrates and both channels keep delivering.
BWRAP=$(command -v bwrap || true)
HOST=root
row cantjail-root.fresh-smee "$CONDUCTOR_NEW" "$GITHUB_NEW" - "v1.1.0=$SMEE_OLD" yes yes yes
if [ -n "$BWRAP" ]; then
  HOST=userns-blocked
  row cantjail-userns-blocked.fresh-smee "$CONDUCTOR_NEW" "$GITHUB_NEW" - "v1.1.0=$SMEE_OLD" yes yes yes
else
  fail "cantjail-userns-blocked: needs bwrap (--disable-userns) to block user namespaces"
fi
for r in cantjail-root.fresh-smee cantjail-userns-blocked.fresh-smee; do
  if grep -q "this host cannot jail plugins" "$WORK/$r/daemon.log" 2>/dev/null; then pass "$r: the boot warns this host cannot jail plugins"; else fail "$r: no host-jail warning"; fi
done
HOST=""

# The migrated bundled-webhook relay with its trust taken off by hand: jailed,
# it is refused conductor's own port, and the log names the fix.
say "new-cond.webhook-jailed-by-hand"
d="$WORK/new-cond.webhook-jailed-by-hand"
mkdir -p "$d/state"
cp -r "$WORK/new-cond.new-gh/cfg" "$d/cfg"
rm -f "$d/cfg/conductor.yaml.pre-smee"
sed -i "s#/new-cond.new-gh-gh#/byhand-gh#; s#/new-cond.new-gh/#/byhand/#g; s#new-cond.new-gh-hooks#byhand-hooks#" "$d/cfg/conductor.yaml" "$d/cfg/conductor.env"
sed -i '/^    # trust\/isolate: /d; /^    trust: full$/d; /^    isolate: true$/d' "$d/cfg/conductor.yaml"
"$BIN/seed" -state "$d/state" "github=$GITHUB_NEW" "smee=$SMEE_NEW" >"$d/seed.log" 2>&1
boot "$d" "$CONDUCTOR_NEW"
if wait_for 60 subscribed byhand-hooks && wait_for 30 subscribed byhand-gh; then pass "webhook-jailed-by-hand: both channels have a subscriber"; else fail "webhook-jailed-by-hand: no subscriber"; fi
sleep 1
post_github byhand 1
if wait_for 30 fired byhand gh; then pass "webhook-jailed-by-hand: gh.new_comment fires"; else fail "webhook-jailed-by-hand: gh.new_comment never fired"; fi
post_hooks byhand 1
sleep 8
if fired byhand hooks; then fail "webhook-jailed-by-hand: hooks.alarm fired through a jailed relay"; else pass "webhook-jailed-by-hand: the jail kept the relay from conductor's own port"; fi
if grep -q -- "$OWN_PORT" "$d/daemon.log"; then pass "webhook-jailed-by-hand: the refusal is logged"; else fail "webhook-jailed-by-hand: no log line naming the refusal"; fi
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
