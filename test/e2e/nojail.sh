#!/usr/bin/env bash
# "Hosts that cannot jail plugins" e2e — see README.md "Hosts that cannot
# jail plugins" for the feature this proves and how to add a row.
#
# The plugin jail (internal/plugin) needs unprivileged user namespaces. Some
# hosts can never build one — a root daemon (a user namespace maps root to
# root, no boundary) or ANY host where unprivileged user namespaces are
# blocked, notably a DEFAULT Docker container (default seccomp blocks
# unshare(CLONE_NEWUSER)). internal/plugin/hostjail.go probes this at boot;
# internal/plugin/asbefore.go is what a plugin does about it: one installed
# BEFORE the jail keeps running UNJAILED, as before; one installed SINCE is
# refused, with a fix in the error.
#
# This harness proves both container SHAPES docker gives you by default (no
# --privileged, no --cap-add, no --security-opt — the opposite of group Z's
# conductor-sandbox, which runs privileged specifically so it CAN jail):
#
#   root     conductor runs as root (euid 0)
#   nonroot  conductor runs as uid:gid 1000:1000 — the default seccomp
#            profile still blocks unprivileged userns for it
#
# ...crossed with three install-state ROWS (config/nojail/entrypoint.sh seeds
# installed.yaml for each; see there for the exact shape of each record):
#
#   prejail-v1      a version 1 installed.yaml (pre-jail conductor's own
#                   format) — github + js must both keep working.
#   rewritten-v089  a version 2 file whose records still carry legacy: true
#                   (what v0.88/v0.89 left behind when they could not
#                   describe a pre-jail plugin themselves) — both must still
#                   work.
#   fresh           a version 2 record with no legacy/pre_jail at all — a
#                   genuine post-jail install. Only js is fresh in this row
#                   (github keeps its pre-jail record): js must be REFUSED,
#                   github must keep working regardless.
#
# ...and one more row, smee-migration (run_smee_row): a config in the retired
# smee_url shape (config/nojail/smee.yaml) on such a host. Boot migrates it,
# installing the smee plugin fresh (from a local release repo); that install
# takes over from the pre-jail github plugin and from conductor's own relay,
# so it runs unjailed as they did (with the warning) instead of being
# refused, and a delivery on each local fake smee channel reaches its trigger
# (gh.new_comment through gh-smee, hooks.alarm through hooks-smee).
#
# Add a row: a new case arm in config/nojail/entrypoint.sh, then add its name
# and expected js outcome to the ROWS/JS_EXPECT table below — every assertion
# (boot warning, connector registration, webhook→js→record completion) runs
# against it automatically, in both shapes.
#
# Each row+shape is its OWN standalone container (`docker run`, not compose —
# this harness calls no mock service: the github connector's own webhook
# listener is driven directly, exactly like conductor-conn's `gh:` instance in
# connectors.e2e.yaml, just without the trust: full that config needs for the
# OLD root-is-refused-outright behavior). A verb call against mock-github
# would need the compose network and is a documented bonus — see README.md.
set -uo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$DIR"
# shellcheck source=lib/assert.sh
source "$DIR/lib/assert.sh"

# Same per-checkout project/tag scheme as run.sh, so a `make e2e` (or a prior
# `nojail.sh`) run in this checkout is reused as a build-cache hit instead of
# a second image build.
PROJECT="${PROJECT:-pc-e2e-$(basename "$(cd "$DIR/../.." && pwd)" | tr 'A-Z' 'a-z' | tr -c 'a-z0-9_\n' '-')}"
export E2E_IMAGE_TAG="$PROJECT"
IMAGE="conductor-e2e:$PROJECT"
KEEP="${KEEP:-0}"
COMPOSE=(docker compose -f "$DIR/docker-compose.yml" -p "$PROJECT")

# NJ_SHAPES / NJ_ROWS narrow a local run (space-separated); both default to
# everything.
SHAPES="${NJ_SHAPES:-root nonroot}"
ROWS="${NJ_ROWS:-prejail-v1 rewritten-v089 fresh smee-migration}"
declare -A JS_EXPECT=( [prejail-v1]=works [rewritten-v089]=works [fresh]=refused )

WORK="$(mktemp -d)"
cleanup() {
  # Logs are only useful once a failure's been reported — leave them on a
  # red run so the matrix's "see $log" notes still resolve; a clean run has
  # nothing worth keeping.
  [ "$FAIL" -eq 0 ] 2>/dev/null && rm -rf "$WORK"
}
trap cleanup EXIT

banner() { printf '\n\033[1m== %s ==\033[0m\n' "$1"; }

banner "build (project $PROJECT, reusing the e2e image if already built)"
"${COMPOSE[@]}" build conductor || { echo "image build failed"; exit 1; }

# Same throwaway-RSA-key dance as run.sh's setup() (shared path: a prior
# `make e2e`/`nojail.sh` run in this checkout already made one). Built via the
# image itself, so no host openssl dependency; always re-chmod in case an
# older run left it 0600 and root-owned (unreadable to the nonroot shape).
PEM="$DIR/config/github-app.pem"
if [ ! -f "$PEM" ]; then
  docker run --rm -v "$DIR/config:/c" "$IMAGE" sh -c 'openssl genrsa -out /c/github-app.pem 2048' >/dev/null 2>&1
fi
docker run --rm -v "$DIR/config:/c" "$IMAGE" chmod 644 /c/github-app.pem >/dev/null 2>&1

# post_webhook <container> — sign fixtures/new_comment.json exactly like
# run.sh's post_webhook_to and POST it at the row's own github webhook
# listener (0.0.0.0:8787, nojail.yaml).
post_webhook() {
  docker exec "$1" bash -c '
    set -e
    f=/fixtures/new_comment.json
    sig=$(openssl dgst -sha256 -hmac e2e-webhook-secret "$f" | sed "s/^.*= //")
    curl -s -o /dev/null -w "%{http_code}" -X POST http://localhost:8787/webhook \
      -H "X-GitHub-Event: issue_comment" \
      -H "X-GitHub-Delivery: $(head -c16 /dev/urandom | od -An -tx1 | tr -d " \n")" \
      -H "X-Hub-Signature-256: sha256=$sig" \
      -H "Content-Type: application/json" \
      --data-binary @"$f"
  '
}

wait_for() { # <timeout_s> <cmd...>
  local timeout="$1"; shift
  local deadline=$((SECONDS + timeout))
  while [ $SECONDS -lt $deadline ]; do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}

# run_row <shape> <row> — launches one container, asserts everything, tears
# it down. shape is "root" or "nonroot"; row is a key of JS_EXPECT.
run_row() {
  local shape="$1" row="$2"
  local name="nojail-$shape-$row"
  local scen="$shape/$row"
  local log="$WORK/$name.log"
  local user_args=()
  local want_reason
  if [ "$shape" = nonroot ]; then
    user_args=(--user 1000:1000)
    want_reason="container that does not allow user namespaces"
  else
    want_reason="runs as root, and a user namespace maps root to root"
  fi

  docker rm -f "$name" >/dev/null 2>&1 || true
  docker run -d --name "$name" "${user_args[@]}" \
    -e "ROW=$row" -e HOME=/home/conductor \
    -v "$DIR/config/nojail/nojail.yaml:/etc/conductor/nojail.yaml:ro" \
    -v "$DIR/config/nojail/entrypoint.sh:/etc/conductor/nojail-entrypoint.sh:ro" \
    -v "$PEM:/etc/conductor/github-app.pem:ro" \
    -v "$DIR/fixtures:/fixtures:ro" \
    "$IMAGE" bash /etc/conductor/nojail-entrypoint.sh /etc/conductor/nojail.yaml \
    >/dev/null || { bad "$scen container started" NJ "$scen-up" "docker run failed"; return; }

  if ! wait_for 30 docker exec "$name" test -S /home/conductor/data/control.sock; then
    bad "$scen daemon reached readiness" NJ "$scen-up" "no control socket after 30s"
    docker logs "$name" > "$log" 2>&1
    echo "    log: $log"
    [ "$KEEP" = 1 ] || docker rm -f "$name" >/dev/null 2>&1
    return
  fi
  ok "$scen daemon booted" NJ "$scen-up"

  docker logs "$name" > "$log" 2>&1

  # The boot warning: logged once, by hostJailBootWarning, with THIS shape's
  # own reason.
  if grep -q 'plugins: WARNING: this host cannot jail plugins:' "$log" && grep -q "$want_reason" "$log"; then
    ok "$scen boot warning names this shape's reason" NJ "$scen-warn"
  else
    bad "$scen boot warning" NJ "$scen-warn" "no 'cannot jail plugins' warning naming '$want_reason' — see $log"
  fi

  # github is pre-jail in EVERY row (it never has a fresh record here) — it
  # must register its connector type and verbs, unjailed, in every row and
  # both shapes.
  if grep -q 'plugin github: registered connector type "github" ([1-9][0-9]* verb(s)); permissions: NOT jailed' "$log"; then
    ok "$scen github registered its verbs, unjailed" NJ "$scen-gh"
  else
    bad "$scen github connector registration" NJ "$scen-gh" "no 'registered connector type \"github\" (N verb(s)); permissions: NOT jailed' — see $log"
  fi

  # js: works (pre-jail rows) or refused (the fresh row).
  if [ "${JS_EXPECT[$row]}" = works ]; then
    if grep -q 'plugin js: registered code-step engine "js"' "$log"; then
      ok "$scen js engine registered, unjailed" NJ "$scen-js"
    else
      bad "$scen js engine registration" NJ "$scen-js" "no 'registered code-step engine \"js\"' — see $log"
    fi
  else
    if grep -q 'plugin js: FAILED TO START' "$log" \
      && grep -q 'cannot jail it on this host' "$log" \
      && grep -q 'installed after the plugin jail' "$log" \
      && grep -q 'or set trust: full on its entry to run it unjailed' "$log"; then
      ok "$scen fresh js is refused with the fix text" NJ "$scen-js"
    else
      bad "$scen fresh js refusal" NJ "$scen-js" "no 'cannot jail it on this host ... installed after the plugin jail ... trust: full' refusal — see $log"
    fi
  fi

  # The end-to-end path: a signed GitHub-shaped webhook fires gh.new_comment,
  # whose use: js step runs (or doesn't) and a final use: cli (builtin, never
  # jailed) step records its output.
  local code
  code="$(post_webhook "$name")"
  if [ "${JS_EXPECT[$row]}" = works ]; then
    if wait_for 20 docker exec "$name" grep -qF 'NOJAIL-JS please address this in the PR' /home/conductor/data/nojail-result.txt; then
      ok "$scen webhook -> js step -> recorded output" NJ "$scen-e2e"
    else
      bad "$scen webhook/js/record" NJ "$scen-e2e" "webhook POST returned $code but no/wrong nojail-result.txt — see $log"
    fi
  else
    # The daemon must still be alive and the webhook accepted (github itself
    # works in this row) even though the js step inside the triggered
    # workflow fails.
    if [ "$code" = 202 ] && wait_for 15 docker exec "$name" grep -q '"event":"workflow_failed"' /home/conductor/data/audit.jsonl; then
      ok "$scen webhook accepted; the js step failed in place, nothing else" NJ "$scen-e2e"
    else
      bad "$scen webhook on a fresh-js row" NJ "$scen-e2e" "expected a 202 + workflow_failed (js refused) — code=$code, see $log"
    fi
  fi

  if [ "$KEEP" = 1 ]; then
    echo "    KEEP=1 — leaving $name up; log: $log"
  else
    docker rm -f "$name" >/dev/null 2>&1
  fi
}

# post_smee <container> <channel> <github|plain> — POST a delivery to the
# container's mocksmee channel, as the sender (a GitHub App, an alarm) does.
post_smee() {
  docker exec "$1" bash -c '
    set -e
    f=/fixtures/new_comment.json
    if [ "$2" = github ]; then
      sig=$(openssl dgst -sha256 -hmac e2e-webhook-secret "$f" | sed "s/^.*= //")
      curl -s -o /dev/null -w "%{http_code}" -X POST "http://127.0.0.1:3300/$1" \
        -H "X-GitHub-Event: issue_comment" \
        -H "X-GitHub-Delivery: $(head -c16 /dev/urandom | od -An -tx1 | tr -d " \n")" \
        -H "X-Hub-Signature-256: sha256=$sig" \
        -H "Content-Type: application/json" \
        --data-binary @"$f"
    else
      curl -s -o /dev/null -w "%{http_code}" -X POST "http://127.0.0.1:3300/$1" \
        -H "Content-Type: application/json" --data-binary "{\"alarm\":\"nojail\"}"
    fi
  ' _ "$2" "$3"
}

# run_smee_row <shape> — the smee-migration row (see the header).
run_smee_row() {
  local shape="$1" row=smee-migration
  local name="nojail-$shape-$row"
  local scen="$shape/$row"
  local log="$WORK/$name.log"
  local user_args=()
  [ "$shape" = nonroot ] && user_args=(--user 1000:1000)

  docker rm -f "$name" >/dev/null 2>&1 || true
  docker run -d --name "$name" "${user_args[@]}" \
    -e "ROW=$row" -e HOME=/home/conductor \
    -v "$DIR/config/nojail/smee.yaml:/etc/conductor/smee.yaml:ro" \
    -v "$DIR/config/nojail/entrypoint.sh:/etc/conductor/nojail-entrypoint.sh:ro" \
    -v "$PEM:/etc/conductor/github-app.pem:ro" \
    -v "$DIR/fixtures:/fixtures:ro" \
    "$IMAGE" bash /etc/conductor/nojail-entrypoint.sh /etc/conductor/smee.yaml \
    >/dev/null || { bad "$scen container started" NJ "$scen-up" "docker run failed"; return; }

  # The migration installs smee before the daemon is ready: allow for it.
  if ! wait_for 90 docker exec "$name" test -S /home/conductor/data/control.sock; then
    bad "$scen daemon reached readiness" NJ "$scen-up" "no control socket after 90s"
    docker logs "$name" > "$log" 2>&1
    echo "    log: $log"
    [ "$KEEP" = 1 ] || docker rm -f "$name" >/dev/null 2>&1
    return
  fi
  ok "$scen daemon booted" NJ "$scen-up"

  local cfg
  cfg="$(docker exec "$name" cat /home/conductor/cfg/conductor.yaml)"
  if docker exec "$name" test -f /home/conductor/cfg/conductor.yaml.pre-smee \
    && ! grep -v '^ *#' <<<"$cfg" | grep -q 'smee_url:' \
    && grep -q 'expose: gh-smee' <<<"$cfg" && grep -q 'expose: hooks-smee' <<<"$cfg"; then
    ok "$scen config migrated (backup beside it)" NJ "$scen-migrate"
  else
    bad "$scen config migration" NJ "$scen-migrate" "smee_url not rewritten into gh-smee/hooks-smee, or no backup: $cfg"
  fi

  # Both channels subscribed: the smee connectors are up.
  if wait_for 60 docker exec "$name" grep -q 'subscriber joined "nojail-gh"' /home/conductor/data/mocksmee.log \
    && wait_for 30 docker exec "$name" grep -q 'subscriber joined "nojail-hooks"' /home/conductor/data/mocksmee.log; then
    ok "$scen both smee channels subscribed" NJ "$scen-subs"
  else
    bad "$scen smee subscriptions" NJ "$scen-subs" "a channel has no subscriber — see $log"
  fi
  sleep 1
  docker logs "$name" > "$log" 2>&1

  if grep -q 'plugin smee: installed connectors/smee/v1.1.0' "$log" \
    && grep -q 'config migrate: smee takes over from github, which ran before the plugin jail' "$log"; then
    ok "$scen the migration installed smee fresh; it takes over github's pre-jail treatment" NJ "$scen-install"
  else
    bad "$scen fresh smee install" NJ "$scen-install" "no 'installed connectors/smee/v1.1.0' + 'smee takes over from github' — see $log"
  fi
  if grep -q 'plugin github: registered connector type "github" ([1-9][0-9]* verb(s)); permissions: NOT jailed' "$log" \
    && grep -qE 'plugin smee[^:]*: WARNING: running UNJAILED, as before the plugin jail' "$log" \
    && ! grep -q 'plugin smee.*cannot jail it on this host' "$log" \
    && ! grep -q 'plugin smee: NOT granted' "$log"; then
    ok "$scen github and gh-smee run unjailed, with the warning; smee is not refused" NJ "$scen-unjailed"
  else
    bad "$scen unjailed github + smee" NJ "$scen-unjailed" "github not NOT-jailed, smee not running as before, or smee refused — see $log"
  fi

  post_smee "$name" nojail-gh github >/dev/null
  if wait_for 30 docker exec "$name" grep -qF 'NOJAIL-SMEE-GH' /home/conductor/data/gh-result.txt; then
    ok "$scen smee channel -> gh-smee -> github listener -> gh.new_comment" NJ "$scen-gh"
  else
    docker logs "$name" > "$log" 2>&1
    bad "$scen github delivery" NJ "$scen-gh" "no gh-result.txt — see $log"
  fi
  post_smee "$name" nojail-hooks plain >/dev/null
  if wait_for 30 docker exec "$name" grep -qF 'NOJAIL-SMEE-HOOKS' /home/conductor/data/hooks-result.txt; then
    ok "$scen smee channel -> hooks-smee -> webhook listener -> hooks.alarm" NJ "$scen-hooks"
  else
    docker logs "$name" > "$log" 2>&1
    bad "$scen webhook delivery" NJ "$scen-hooks" "no hooks-result.txt — see $log"
  fi

  if [ "$KEEP" = 1 ]; then
    echo "    KEEP=1 — leaving $name up; log: $log"
  else
    docker rm -f "$name" >/dev/null 2>&1
  fi
}

banner "hosts that cannot jail plugins: $SHAPES x $ROWS"
for shape in $SHAPES; do
  for row in $ROWS; do
    if [ "$row" = smee-migration ]; then
      run_smee_row "$shape"
    else
      run_row "$shape" "$row"
    fi
  done
done

print_matrix
if [ "$FAIL" -ne 0 ]; then
  echo "per-container boot logs kept at: $WORK"
fi
[ "$FAIL" -eq 0 ]
