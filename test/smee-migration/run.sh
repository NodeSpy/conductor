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
#   GITHUB_DP       the github plugin declaring its listener's default path
#                   (optional: the explicit-listen rows without a path)
#   CONDUCTOR_V090  a jailing conductor from before expose_local (optional:
#                   the default-path github build loads and serves there)
#   CONDUCTOR_PREV  the conductor release before this tree (optional: the
#                   rows proving it unaffected, and the failing-before row)
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
# publish <dir> <plugin> <tag> <binary>: release the binary as
# connectors/<plugin>/<tag> in the row's repo, and point the official
# source's URL at that repo.
publish() {
  local d=$1 plugin=$2 tag=$3 bin=$4 repo="$1/forge.git" asset="conductor-$2_$PLATFORM" blob sums tree commit
  [ -d "$repo" ] || git init -q --bare "$repo"
  blob=$(git --git-dir="$repo" hash-object -w "$bin")
  sums=$(printf '%s  %s\n' "$(sha256sum "$bin" | cut -d' ' -f1)" "$asset" | git --git-dir="$repo" hash-object -w --stdin)
  tree=$(printf '100755 blob %s\t%s\n100644 blob %s\tchecksums.txt\n' "$blob" "$asset" "$sums" | git --git-dir="$repo" mktree)
  commit=$(GIT_AUTHOR_NAME=harness GIT_AUTHOR_EMAIL=harness@localhost GIT_COMMITTER_NAME=harness GIT_COMMITTER_EMAIL=harness@localhost \
    git --git-dir="$repo" commit-tree "$tree" -m "connectors/$plugin/$tag")
  git --git-dir="$repo" update-ref "refs/dist/connectors/$plugin/$tag/$PLATFORM" "$commit"
  mkdir -p "$d/home"
  git config --file "$d/home/.gitconfig" "url.file://$repo.insteadOf" "$SMEE_SOURCE"
}
# forge <dir> <tag=binary>…: publish each binary as connectors/smee/<tag>.
forge() {
  local d=$1; shift
  for rel in "$@"; do publish "$d" smee "${rel%%=*}" "${rel#*=}"; done
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
    # No listen or path written: conductor gives the listeners their address.
    if grep -qE '^ +(listen|path): ' <<<"$now"; then fail "$name: the rewrite wrote a listen or path"; echo "$now"; else pass "$name: the rewrite wrote no listen and no path"; fi
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
# config the new one already rewrote. The rewrite wrote no listen, which only
# a conductor that picks the address itself can serve: an older one disables
# the github connector, naming the missing field (no crash, the rest runs),
# until the backup is restored or a listen is added. A config an earlier
# release rewrote, with listen and path written in, keeps delivering under
# the older conductor through the smee connector (listeners predate the move).
say "rollback-after-migration"
d="$WORK/rollback"
mkdir -p "$d/state"
cp -r "$WORK/new-cond.new-gh/cfg" "$d/cfg"
sed -i "s#/new-cond.new-gh-gh#/rollback-gh#; s#/new-cond.new-gh/#/rollback/#g; s#new-cond.new-gh-hooks#rollback-hooks#" "$d/cfg/conductor.yaml" "$d/cfg/conductor.env"
"$BIN/seed" -state "$d/state" "github=$GITHUB_NEW" "smee=$SMEE_NEW" >"$d/seed.log" 2>&1
boot "$d" "$CONDUCTOR_OLD"
if wait_for 60 grep -q 'connector "gh" disabled.*webhook.expose is set with no webhook.listen' "$d/daemon.log"; then pass "rollback: the older conductor disables github, naming the missing listen"; else fail "rollback: no disabled-github line"; fi
if wait_for 30 grep -q 'connector "hooks" disabled' "$d/daemon.log"; then pass "rollback: the older conductor disables the webhook connector too (no listen)"; else fail "rollback: no disabled-hooks line"; fi
sleep 3
if [ -d "/proc/$(cat "$WORK/rollback/daemon.pid")" ]; then pass "rollback: the daemon keeps running"; else fail "rollback: the daemon exited"; fi
stop "$d"
# The same, with the listen and path an earlier release wrote.
d="$WORK/rollback-earlier"
mkdir -p "$d/state"
cp -r "$WORK/new-cond.new-gh/cfg" "$d/cfg"
sed -i "s#/new-cond.new-gh-gh#/rollback-earlier-gh#; s#/new-cond.new-gh/#/rollback-earlier/#g; s#new-cond.new-gh-hooks#rollback-earlier-hooks#" "$d/cfg/conductor.yaml" "$d/cfg/conductor.env"
P_EARLIER=$(freeport)
sed -i "s#^      expose: gh-smee#      listen: 127.0.0.1:$P_EARLIER\n      path: /webhook\n      expose: gh-smee#" "$d/cfg/conductor.yaml"
P_EARLIER_H=$(freeport)
sed -i "s#^    expose: hooks-smee#    listen: 127.0.0.1:$P_EARLIER_H\n    expose: hooks-smee#" "$d/cfg/conductor.yaml"
"$BIN/seed" -state "$d/state" "github=$GITHUB_NEW" "smee=$SMEE_NEW" >"$d/seed.log" 2>&1
boot "$d" "$CONDUCTOR_OLD"
if wait_for 60 subscribed rollback-earlier-gh; then pass "rollback-earlier: the smee connector subscribed under the older conductor"; else fail "rollback-earlier: no subscriber"; fi
sleep 1
post_github rollback-earlier 1
if wait_for 30 fired rollback-earlier gh; then pass "rollback-earlier: gh.new_comment fires under the older conductor"; else fail "rollback-earlier: gh.new_comment never fired"; fi
stop "$d"

# A listener's listen and path are optional next to expose. Configs in the
# current shape (no smee_url, nothing to migrate):
#
#   expose-only   no listen, no path: conductor picks the address, the relay
#                 delivers through it (github and smee jailed)
#   listen        an explicit listen, no path: the relay delivers there, to
#                 the plugin's declared default path, AND a direct POST to
#                 the listen address arrives too
#   listen-path   an explicit listen and path: both arrive, on any github
#
# modern_row <name> <conductor> <github> <shape> <gh: yes|disabled> [direct: yes]
write_modern() { # <dir> <row> <shape> <gh port> <hooks port>
  local d=$1 row=$2 shape=$3 pg=$4 ph=$5 ghl="" hl=""
  case $shape in
    listen) ghl="      listen: 127.0.0.1:$pg"; hl="    listen: 127.0.0.1:$ph" ;;
    listen-path) ghl="      listen: 127.0.0.1:$pg
      path: /webhook"; hl="    listen: 127.0.0.1:$ph" ;;
  esac
  mkdir -p "$d/cfg"
  printf 'GH_WEBHOOK_SECRET=%s\n' "$SECRET" >"$d/cfg/conductor.env"
  cat >"$d/cfg/conductor.yaml" <<YAML
# harness row $row — current shape, $shape
update: { auto: false }
connectors:
  gh:
    use: github
    api_base: http://127.0.0.1:$P_GH
    token: harness-static-token
    webhook:
$ghl
      expose: gh-smee
      secret: \${GH_WEBHOOK_SECRET}
    sweep: { enabled: false }
    me: { logins: [harness-user] }
  gh-smee:
    use: smee
    channel: http://127.0.0.1:$P_SMEE/$row-gh
  hooks:
    use: webhook
$hl
    expose: hooks-smee
    allow_private: true
    sources:
      alarm: { path: /alarm }
  hooks-smee:
    use: smee
    channel: http://127.0.0.1:$P_SMEE/$row-hooks
    trust: full
    isolate: true
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
  sed -i '/^$/d' "$d/cfg/conductor.yaml"
}
post_github_direct() { # <row> <n> <url>
  local body sig
  body=$(printf '{"action":"created","installation":{"id":42},"repository":{"full_name":"harness/svc","name":"svc","default_branch":"main","owner":{"login":"harness"}},"issue":{"number":%d,"html_url":"https://forge.test/harness/svc/pull/%d","user":{"login":"harness-user"},"pull_request":{"url":"https://forge.test/harness/svc/pull/%d"}},"comment":{"id":%d,"user":{"login":"a-reviewer"},"body":"direct %s %d"}}' "$2" "$2" "$2" "$((7000 + $2))" "$1" "$2")
  sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$SECRET" | sed 's/^.* //')
  curl -sf -X POST "$3" -H "Content-Type: application/json" \
    -H "X-GitHub-Event: issue_comment" -H "X-GitHub-Delivery: direct-$1-$2-$RANDOM" \
    -H "X-Hub-Signature-256: sha256=$sig" --data-binary "$body" -o /dev/null
}
# arrives <name> <row> <which> <what>: one more <which> fired since the last
# call for this row/which.
arrives() {
  local name=$1 row=$2 which=$3 what=$4 before=${5:-0}
  if wait_for 30 bash -c "[ \$(curl -sf http://127.0.0.1:$P_SINK/_captured | grep -o '\"/harness/$row/$which\"' | wc -l) -gt $before ]"; then
    pass "$name: $what"
  else
    fail "$name: $what — never arrived"
  fi
}
modern_row() {
  local name=$1 cond=$2 gh=$3 shape=$4 ghx=$5 d="$WORK/$1" pg ph b
  say "$name"
  pg=$(freeport) ph=$(freeport)
  mkdir -p "$d/state"
  write_modern "$d" "$name" "$shape" "$pg" "$ph"
  local orig; orig=$(cat "$d/cfg/conductor.yaml")
  "$BIN/seed" -state "$d/state" "github=$gh" "smee=$SMEE_NEW" >"$d/seed.log" 2>&1 || { fail "$name: seed ($(tail -1 "$d/seed.log"))"; return; }
  boot "$d" "$cond"
  # The bundled webhook connector's relay path came with the move: a
  # conductor before it serves only github's side of these rows.
  local hooks=yes
  [ "$cond" = "$CONDUCTOR_OLD" ] && hooks=no
  if [ "$ghx" = disabled ]; then
    if wait_for 30 grep -q 'connector "gh" disabled.*webhook.expose is set with no webhook.listen' "$d/daemon.log"; then pass "$name: github is disabled for the missing listen"; else fail "$name: github not disabled"; fi
    if grep -q 'connector "hooks" disabled' "$d/daemon.log"; then pass "$name: the webhook connector is disabled for the missing listen"; else fail "$name: the webhook connector not disabled"; fi
    post_github "$name" 1
    sleep 8
    if fired "$name" gh; then fail "$name: gh.new_comment fired"; else pass "$name: no github delivery"; fi
    stop "$d"
    return
  fi
  # direct: a jailed conductor before expose_local (v0.88 – v0.90) never lets
  # a jailed smee reach a listener; only the direct POST is checked there.
  [ "$ghx" = direct ] && hooks=no
  [ "$hooks" = yes ] && { if wait_for 60 subscribed "$name-hooks"; then pass "$name: the webhook relay subscribed"; else fail "$name: nobody subscribed to the webhook channel"; fi; }
  if wait_for 60 subscribed "$name-gh"; then pass "$name: the github relay subscribed"; else fail "$name: nobody subscribed to the github channel"; fi
  if [ "$cond" = "$CONDUCTOR_NEW" ]; then jailed "$name" github; jailed "$name" smee; fi
  sleep 1
  if [ "$ghx" != direct ]; then
    post_github "$name" 1
    arrives "$name" "$name" gh "a smee delivery fired gh.new_comment"
  fi
  if [ "$hooks" = yes ]; then
    post_hooks "$name" 1
    arrives "$name" "$name" hooks "a smee delivery fired hooks.alarm"
  fi
  if [ "$shape" != expose-only ]; then
    b=$(count_fired "$name" gh)
    post_github_direct "$name" 2 "http://127.0.0.1:$pg/webhook"
    arrives "$name" "$name" gh "a direct POST to the explicit listen address fired gh.new_comment" "$b"
    if [ "$hooks" = yes ]; then
      b=$(count_fired "$name" hooks)
      curl -sf -X POST "http://127.0.0.1:$ph/alarm" -H "Content-Type: application/json" --data-binary '{"alarm":"direct"}' -o /dev/null
      arrives "$name" "$name" hooks "a direct POST to the webhook connector's listen address fired hooks.alarm" "$b"
    fi
  fi
  if [ "$(cat "$d/cfg/conductor.yaml")" = "$orig" ]; then pass "$name: the config is untouched"; else fail "$name: the config changed"; fi
  stop "$d"
}
modern_row now.expose-only.old-gh   "$CONDUCTOR_NEW" "$GITHUB_OLD" expose-only yes
modern_row now.expose-only.new-gh   "$CONDUCTOR_NEW" "$GITHUB_NEW" expose-only yes
modern_row now.listen-path.old-gh   "$CONDUCTOR_NEW" "$GITHUB_OLD" listen-path yes
if [ -n "${GITHUB_DP:-}" ]; then
  modern_row now.expose-only.dp-gh  "$CONDUCTOR_NEW" "$GITHUB_DP" expose-only yes
  modern_row now.listen.dp-gh       "$CONDUCTOR_NEW" "$GITHUB_DP" listen yes
  # Older conductors, the default-path github build: unaffected (the SDK
  # withholds default_path from them), on a config they can serve.
  modern_row old-cond.listen-path.dp-gh "$CONDUCTOR_OLD" "$GITHUB_DP" listen-path yes
  [ -n "${CONDUCTOR_PREV:-}" ] && modern_row prev-cond.listen-path.dp-gh "$CONDUCTOR_PREV" "$GITHUB_DP" listen-path yes
  [ -n "${CONDUCTOR_V090:-}" ] && modern_row v090-cond.listen-path.dp-gh "$CONDUCTOR_V090" "$GITHUB_DP" listen-path direct
fi
if [ -n "${CONDUCTOR_PREV:-}" ]; then
  # Before this change: expose with no listen disables the listener.
  modern_row prev-cond.expose-only.new-gh "$CONDUCTOR_PREV" "$GITHUB_NEW" expose-only disabled
fi

# The fleet over time: a box on an older conductor that installs its plugins
# itself (from its own release repo, so its install records are exactly what
# that conductor writes), through each step of the release sequence:
#
#   S0  today: the released plugins
#   S1  github v2.12.0 + smee v1.2.0 released; the box takes its plugin
#       updates (`plugin update`, what its auto-update does) on its own
#       conductor
#   S2  this conductor released; the box updates to it: the migration runs
#   S3  (informational) the box rolled back to its old conductor
#
# Deliveries on both channels must fire at S0, S1 and S2, except github's at
# S0 and S1 on a conductor the plugin SDK withholds config: entries from
# (v0.88, v0.89: no capabilities.references): the harness channel is on
# loopback, which github reaches only through its config:webhook.smee_url
# egress, while a real smee.io channel is declared literally. Runs when
# OLD_CONDUCTORS names them ("<label>=<binary> …"), with GITHUB_REL and
# SMEE_REL the released plugin builds.
joins() { grep -c "subscriber joined \"$1\"" "$WORK/mocksmee.log"; }
FLEET=() N=100
fleet_step() { # <name> <step> <conductor> <yes|hooks|info>: which deliveries must fire
  local name=$1 step=$2 cond=$3 must=$4 d="$WORK/$1" jg jh which res before
  jg=$(joins "$name-gh"); jh=$(joins "$name-hooks")
  echo "=== $step" >>"$d/daemon.log"
  boot "$d" "$cond"
  wait_for 60 bash -c "[ \$(grep -c 'subscriber joined \"$name-gh\"' '$WORK/mocksmee.log') -gt $jg ]"
  wait_for 30 bash -c "[ \$(grep -c 'subscriber joined \"$name-hooks\"' '$WORK/mocksmee.log') -gt $jh ]"
  sleep 1
  for which in gh hooks; do
    before=$(count_fired "$name" "$which")
    N=$((N + 1))
    if [ "$which" = gh ]; then post_github "$name" "$N"; else post_hooks "$name" "$N"; fi
    if wait_for 30 bash -c "[ \$(curl -sf http://127.0.0.1:$P_SINK/_captured | grep -o '\"/harness/$name/$which\"' | wc -l) -gt $before ]"; then res=fires; else res="does not fire"; fi
    FLEET+=("$name | $step | $which | $res")
    if [ "$must" = yes ] || [ "$must" = "$which" ]; then
      if [ "$res" = fires ]; then pass "$name $step: $which fires"; else fail "$name $step: $which does not fire"; fi
    fi
  done
  stop "$d"
}
fleet() { # <label> <old conductor>
  local name="fleet.$1" old=$2 d="$WORK/fleet.$1"
  say "$name"
  mkdir -p "$d/state"
  write_config "$d" "$name"
  publish "$d" github v2.11.2 "$GITHUB_REL"
  publish "$d" smee v1.1.1 "$SMEE_REL"
  local early=yes
  case $1 in v0.88* | v0.89*) early=hooks ;; esac
  fleet_step "$name" "S0 $1, released plugins" "$old" $early
  publish "$d" github v2.12.0 "$GITHUB_NEW"
  publish "$d" smee v1.2.0 "$SMEE_NEW"
  # shellcheck disable=SC2046
  $(daemon_env "$d") "$old" plugin update --config "$d/cfg/conductor.yaml" --state-dir "$d/state" >"$d/plugin-update.log" 2>&1
  grep -E "plugin (github|smee)" "$d/plugin-update.log" | sed "s/^/   [$name S1 plugin update] /" | cut -c1-260
  fleet_step "$name" "S1 $1, plugins updated" "$old" $early
  fleet_step "$name" "S2 this conductor" "$CONDUCTOR_NEW" yes
  if grep -q "expose: gh-smee" "$d/cfg/conductor.yaml" && grep -q "expose: hooks-smee" "$d/cfg/conductor.yaml"; then
    pass "$name S2: the config was migrated"
  else
    fail "$name S2: the config was not migrated"
  fi
  grep -hE "config migrate: (approved|recorded|updated|warning)|could not record" "$d/daemon.log" | sed "s/^/   [$name S2] /" | cut -c1-260
  fleet_step "$name" "S3 rolled back to $1" "$old" info
}
if [ -n "${OLD_CONDUCTORS:-}" ]; then
  for v in GITHUB_REL SMEE_REL; do [ -x "${!v:-}" ] || { fail "fleet: $v must name an executable"; }; done
  for oc in $OLD_CONDUCTORS; do fleet "${oc%%=*}" "${oc#*=}"; done
  say "fleet"
  printf '%s\n' "${FLEET[@]}"
fi

say "result"
echo "$PASSED passed, $FAILED failed"
for f in "${FAILS[@]}"; do echo "  - $f"; done
echo "logs: $WORK"
[ "$FAILED" -eq 0 ]
