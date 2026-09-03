#!/usr/bin/env bash
# End-to-end runner for the pluggable-controller feature set (issue #10). Brings up
# the hermetic Docker stack and drives conductor through the milestone-feasible test
# groups, asserting behavior from the audit log, the forge, the mock GitHub API, and
# the sink-catcher. Prints a controller×workflow results matrix.
#
#   MODE=stub (default) — hermetic stubs, CI-safe, no secrets → `make e2e`
#   MODE=live           — real agents + keys (manual)         → `make e2e-live`
#
# Only groups whose milestones have merged are asserted; the rest are recorded as
# SKIP with the milestone that unlocks them. Set KEEP=1 to leave the stack up.
set -uo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$DIR"
# shellcheck source=lib/assert.sh
source "$DIR/lib/assert.sh"

MODE="${MODE:-stub}"
PROJECT="${PROJECT:-pc-e2e}"
KEEP="${KEEP:-0}"
COMPOSE=(docker compose -f "$DIR/docker-compose.yml")
# Live mode layers an override that swaps the stub controllers for real agents and
# mounts the operator's credentials (see docker-compose.live.yml + README).
if [ "$MODE" = "live" ] && [ -f "$DIR/docker-compose.live.yml" ]; then
  COMPOSE+=(-f "$DIR/docker-compose.live.yml")
fi
COMPOSE+=(-p "$PROJECT")

dc() { "${COMPOSE[@]}" "$@"; }
cexec() { dc exec -T "$@"; }
# curl from inside the network (the conductor container can reach every service).
netcurl() { dc exec -T conductor curl -s "$@"; }

# audit_match <container> <pat...> — any single audit line contains ALL patterns.
audit_match() {
  local c="$1"; shift
  local out; out="$(cexec "$c" cat /data/audit.jsonl 2>/dev/null)" || return 1
  local line all p
  while IFS= read -r line; do
    all=1
    for p in "$@"; do case "$line" in *"$p"*) ;; *) all=0 ;; esac; done
    [ "$all" = 1 ] && return 0
  done <<<"$out"
  return 1
}

# audit_count <container> <pat...> — number of audit lines containing ALL patterns.
audit_count() {
  local c="$1"; shift
  local out; out="$(cexec "$c" cat /data/audit.jsonl 2>/dev/null)"
  local line all p n=0
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    all=1
    for p in "$@"; do case "$line" in *"$p"*) ;; *) all=0 ;; esac; done
    [ "$all" = 1 ] && n=$((n + 1))
  done <<<"$out"
  echo "$n"
}

force() { # container kind repo#n config
  cexec "$1" paseo-conductor force "$2" "$3" --config "$4" 2>&1
}

# post_webhook <event> <fixture> — sign and POST a fixture to conductor's receiver.
post_webhook() {
  local event="$1" fixture="$2"
  cexec conductor bash -c '
    set -e
    f="/fixtures/'"$fixture"'"
    sig=$(openssl dgst -sha256 -hmac e2e-webhook-secret "$f" | sed "s/^.*= //")
    curl -s -o /dev/null -w "%{http_code}" -X POST http://localhost:8787/webhook \
      -H "X-GitHub-Event: '"$event"'" \
      -H "X-GitHub-Delivery: $(head -c16 /dev/urandom | od -An -tx1 | tr -d " \n")" \
      -H "X-Hub-Signature-256: sha256=$sig" \
      -H "Content-Type: application/json" \
      --data-binary @"$f"
  '
}

banner() { printf '\n\033[1m== %s ==\033[0m\n' "$1"; }

setup() {
  banner "build & up ($MODE mode, project $PROJECT)"
  dc down -v --remove-orphans >/dev/null 2>&1 || true
  dc build || { echo "build failed"; exit 1; }

  # Throwaway RSA key for the (mock) GitHub App — generated into the gitignored
  # config dir via the built image, so no host openssl dependency and no secret in
  # the repo. Conductor needs a parseable PEM even against the mock.
  if [ ! -f "$DIR/config/github-app.pem" ]; then
    docker run --rm -v "$DIR/config:/c" paseo-conductor-e2e:latest \
      openssl genrsa -out /c/github-app.pem 2048 >/dev/null 2>&1
  fi

  dc up -d || { echo "up failed"; dc logs; exit 1; }

  banner "wait for readiness"
  wait_for 60 cexec mock-github  curl -sf http://localhost:8080/_health  || fatal "mock-github not ready"
  wait_for 60 cexec sink-catcher curl -sf http://localhost:8080/_health  || fatal "sink-catcher not ready"
  wait_for 60 cexec forge git ls-remote git://localhost/acme/web.git      || fatal "forge not ready"
  for c in conductor conductor-ctrl conductor-fail; do
    wait_for 60 cexec "$c" test -S /data/control.sock || fatal "$c daemon not ready (control socket)"
  done
  echo "stack ready"
}

fatal() { echo "FATAL: $1"; dc logs --tail 60; teardown; exit 1; }

teardown() {
  if [ "$KEEP" = 1 ]; then
    echo "KEEP=1 — leaving stack up (project $PROJECT). Tear down: docker compose -p $PROJECT down -v"
    return
  fi
  banner "teardown"
  dc down -v --remove-orphans >/dev/null 2>&1 || true
}

# ---- test groups ------------------------------------------------------------

group_A_resolution() {
  banner "Group A — controller resolution & config"

  # A1: the MAIN daemon has NO controllers: block → paseo (backward compat).
  # Proven by the baseline force below (also underpins B/G/H/I).
  force conductor merge_conflict acme/web#1 /etc/conductor/conductor.yaml >/dev/null
  if wait_for 30 audit_match conductor '"repo":"acme/web"' '"event":"dispatch"' '"backend":"paseo"'; then
    ok "A1 no controllers: block → built-in paseo (backend=paseo)" A A1
  else
    bad "A1 no controllers: block → paseo" A A1 "no paseo dispatch for acme/web"
  fi

  # A2/A3/A4 on the second daemon with a controllers: block.
  force conductor-ctrl merge_conflict a2/web2#1 /etc/conductor/controllers.yaml >/dev/null
  force conductor-ctrl merge_conflict a3/web3#1 /etc/conductor/controllers.yaml >/dev/null
  force conductor-ctrl merge_conflict a4/web4#1 /etc/conductor/controllers.yaml >/dev/null

  if wait_for 30 audit_match conductor-ctrl '"repo":"a2/web2"' '"event":"dispatch"' '"backend":"paseo"'; then
    ok "A2 explicit controller: (type paseo) dispatches" A A2
  else
    bad "A2 explicit controller:" A A2 "no dispatch for a2/web2"
  fi

  if wait_for 30 audit_match conductor-ctrl '"repo":"a3/web3"' '"event":"dispatch"' '"backend":"paseo"'; then
    ok "A3 default:true controller dispatches (no explicit controller)" A A3
  else
    bad "A3 default:true controller" A A3 "no dispatch for a3/web3"
  fi

  if wait_for 30 audit_match conductor-ctrl '"repo":"a4/web4"' '"event":"escalate"'; then
    ok "A4 precedence: explicit non-runnable controller wins over default:true → escalate" A A4
  else
    bad "A4 precedence / escalate" A A4 "no escalate for a4/web4"
  fi

  # A5: validation rejects two defaults and an unknown transport; accepts one default.
  if ! cexec conductor paseo-conductor validate --config /etc/conductor/resolution/a5-two-defaults.yaml >/dev/null 2>&1; then
    ok "A5 validation rejects two default:true controllers" A A5a
  else
    bad "A5 two defaults rejected" A A5a "validate unexpectedly passed"
  fi
  if ! cexec conductor paseo-conductor validate --config /etc/conductor/resolution/a5-unknown-transport.yaml >/dev/null 2>&1; then
    ok "A5 validation rejects an unknown transport" A A5b
  else
    bad "A5 unknown transport rejected" A A5b "validate unexpectedly passed"
  fi
  if cexec conductor paseo-conductor validate --config /etc/conductor/resolution/a3-valid-default.yaml >/dev/null 2>&1; then
    ok "A5 validation accepts a single default:true controller" A A5c
  else
    bad "A5 single default accepted" A A5c "validate unexpectedly failed"
  fi
}

group_B_fixer() {
  banner "Group B — paseo controller runs a fixer (edit/commit/push as the user)"
  # Baseline force already dispatched acme/web#1 in group A; assert the fixer's work.
  if wait_for 45 forge_has_conductor_commit acme/web pr-1; then
    ok "B paseo fixer edited & pushed to the forge (new commit on pr-1)" B paseo
  else
    bad "B paseo fixer pushed a fix" B paseo "no conductor commit on acme/web pr-1"
  fi
  # Agent archived when done (reaper).
  if wait_for 30 fake_archived conductor; then
    ok "B agent archived when done (archive_when_done → reaper)" B archive
  else
    bad "B agent archived when done" B archive "no archive recorded in fake paseo"
  fi

  # Non-paseo controller rows require M3/M4 (registered ErrNotRunnable in M1).
  skip B "cli:claude-code fixer"  "M4/T4.3 cli controller not landed"
  skip B "cli:codex fixer"        "M4/T4.3 cli controller not landed"
  skip B "acp:gemini fixer"       "M3/T3.3 ACP transport not wired"
  skip B "acp:codex-adapter fixer" "M3/T3.3 ACP transport not wired"
  skip B "opencode-acp fixer"     "M3/T3.3 ACP transport not wired"
  skip B "opencode-native fixer"  "M4/T4.1 opencode native not landed"
  skip B "agent-deck fixer"       "M4/T4.2 agent-deck controller not landed"
}

forge_has_conductor_commit() { # repo branch
  local subj
  subj="$(cexec forge git --git-dir="/srv/git/$1.git" log "$2" -1 --format='%s' 2>/dev/null)"
  case "$subj" in conductor:*) return 0 ;; *) return 1 ;; esac
}

fake_archived() { # container
  cexec "$1" grep -q '"archived": true' /data/fakepaseo/state.json 2>/dev/null
}

group_I_identity() {
  banner "Group I — identity & isolation"
  # I1: the pushed commit is attributed to the user (git author env), not a bot.
  local author
  author="$(cexec forge git --git-dir=/srv/git/acme/web.git log pr-1 -1 --format='%an <%ae>' 2>/dev/null)"
  assert_contains "$author" "Conductor User <conductor@users.noreply.forge.test>" \
    I I1-commit "I1 commit attributed to the user, not the App bot ($author)"

  # I1: the acts-as-the-user WRITE carries the user write token (not the App token).
  local caps; caps="$(netcurl http://mock-github:8080/_captured)"
  assert_contains "$caps" "e2e-user-write-token" I I1-write \
    "I1 acts-as-the-user API write uses the user token"
  assert_not_contains "$caps" "fake-installation-token" I I1-write2 \
    "I1 the write is NOT made with the App installation token"

  # I2: audit + fake-agent activity reference only the local forge, never production.
  local ev; ev="$(cexec conductor cat /data/audit.jsonl /data/fakepaseo/events.log 2>/dev/null)"
  assert_not_contains "$ev" "api.github.com" I I2 "I2 no production api.github.com reference"
  assert_not_contains "$ev" "github.com"     I I2b "I2 no production github.com reference"
}

group_G_notify() {
  banner "Group G — notify sinks (M0)"
  netcurl -X POST http://sink-catcher:8080/_reset >/dev/null
  # A dispatch (notify.on includes 'dispatch') fans out to every configured sink.
  force conductor merge_conflict acme/api#1 /etc/conductor/conductor.yaml >/dev/null
  if wait_for 30 sinks_all_fired slack discord ntfy pushover notifiarr; then
    ok "G dispatch fired all sinks: slack, discord, ntfy, pushover, notifiarr" G dispatch
  else
    local got; got="$(netcurl http://sink-catcher:8080/_captured)"
    bad "G all sinks fired on dispatch" G dispatch "captured=$got"
  fi
}

sinks_all_fired() { # sink...
  local caps; caps="$(netcurl http://sink-catcher:8080/_captured)"
  local s
  for s in "$@"; do
    printf '%s' "$caps" | grep -qF "\"sink\":\"$s\"" || return 1
  done
  return 0
}

group_H_webhook() {
  banner "Group H — fixers via the webhook path"
  local before after code

  # H1: conflicting PR (pull_request → merge_conflict), mock serves mergeable_state=dirty.
  before="$(nonfailed_dispatch conductor '"repo":"acme/web"' '"kind":"merge_conflict"')"
  code="$(post_webhook pull_request merge_conflict.json)"
  if [ "$code" = "200" ] || [ "$code" = "202" ]; then
    ok "H1 webhook accepted (HTTP $code)" H H1-http
  else
    bad "H1 webhook accepted" H H1-http "unexpected HTTP $code"
  fi
  after_wait_dispatch conductor '"repo":"acme/web"' '"kind":"merge_conflict"' "$before" \
    H H1 "H1 merge_conflict dispatched from the webhook path"

  # H2a: failing check (check_run → failing_checks).
  before="$(nonfailed_dispatch conductor '"repo":"acme/api"' '"kind":"failing_checks"')"
  post_webhook check_run failing_checks.json >/dev/null
  after_wait_dispatch conductor '"repo":"acme/api"' '"kind":"failing_checks"' "$before" \
    H H2a "H2 failing_checks dispatched from the webhook path"

  # H2b: an ignore_checks-named check does NOT dispatch.
  before="$(audit_count conductor '"repo":"acme/api"' '"kind":"failing_checks"' '"event":"dispatch"')"
  post_webhook check_run failing_checks_ignored.json >/dev/null
  sleep 4
  after="$(audit_count conductor '"repo":"acme/api"' '"kind":"failing_checks"' '"event":"dispatch"')"
  if [ "$after" = "$before" ]; then
    ok "H2 ignore_checks suppresses a named check (no dispatch)" H H2b
  else
    bad "H2 ignore_checks suppresses a check" H H2b "dispatch count moved $before→$after"
  fi

  # H3: PR comment (issue_comment → new_comment).
  before="$(nonfailed_dispatch conductor '"repo":"acme/svc"' '"kind":"new_comment"')"
  post_webhook issue_comment new_comment.json >/dev/null
  after_wait_dispatch conductor '"repo":"acme/svc"' '"kind":"new_comment"' "$before" \
    H H3 "H3 new_comment dispatched from the webhook path"
}

# nonfailed_dispatch <container> <pat> <pat> — count of successful (non-failed)
# dispatch audit lines for the repo+kind (a queued/adopted dispatch still counts).
nonfailed_dispatch() {
  local total failed
  total="$(audit_count "$1" "$2" "$3" '"event":"dispatch"')"
  failed="$(audit_count "$1" "$2" "$3" '"event":"dispatch"' '"outcome":"failed"')"
  echo $((total - failed))
}

# after_wait_dispatch <container> <pat> <pat> <before> <group> <scen> <desc>
after_wait_dispatch() {
  local c="$1" p1="$2" p2="$3" before="$4" group="$5" scen="$6" desc="$7"
  local deadline=$((SECONDS + 30))
  while [ $SECONDS -lt $deadline ]; do
    local now; now="$(nonfailed_dispatch "$c" "$p1" "$p2")"
    if [ "$now" -gt "$before" ]; then ok "$desc" "$group" "$scen"; return; fi
    sleep 1
  done
  bad "$desc" "$group" "$scen" "successful dispatch count did not increase past $before"
}

group_J_failure() {
  banner "Group J — failure & escalation"

  # J1: an erroring/non-runnable controller escalates (the a4 stub from group A is a
  # controller conductor cannot drive — the M1 shape of a missing/erroring controller).
  if audit_match conductor-ctrl '"repo":"a4/web4"' '"event":"escalate"'; then
    ok "J1 non-runnable controller → escalate (attempt recorded, not silent)" J J1
  else
    bad "J1 non-runnable controller escalates" J J1 "no escalate for a4/web4"
  fi

  # J2: worktree creation fails → a LOUD escalate (never a silent scratch fallback).
  netcurl -X POST http://sink-catcher:8080/_reset >/dev/null
  force conductor-fail merge_conflict acme/web#1 /etc/conductor/conductor.yaml >/dev/null
  if wait_for 30 audit_match conductor-fail '"repo":"acme/web"' '"event":"escalate"'; then
    ok "J2 worktree creation failure → loud escalate (not silent scratch)" J J2
  else
    bad "J2 worktree failure escalates" J J2 "no escalate on conductor-fail"
  fi
  # The escalate also reaches the notify sinks (escalate ∈ notify.on).
  if wait_for 20 sink_body_has '[escalate]'; then
    ok "J2 escalate delivered to notify sinks" J J2-notify
  else
    bad "J2 escalate reaches sinks" J J2-notify "no [escalate] sink post"
  fi

  # J3 needs the ACP transport wired to detect a mid-session crash.
  skip J "J3 ACP agent crashes mid-session" "M3/T3.x ACP transport not wired (fakeacp FAKE_ACP_CRASH ready)"
}

sink_body_has() { # substring
  local caps; caps="$(netcurl http://sink-catcher:8080/_captured)"
  printf '%s' "$caps" | grep -qF -- "$1"
}

group_skips() {
  banner "Groups pending later milestones (scaffolded, not yet asserted)"
  skip C "native / resumable / oneshot session_model" "M2/T2.1 session broker not landed"
  skip D "D1 burst → one live session"                "M2 session broker (dispatch-level queueing unit-tested)"
  skip D "D2 restart mid-session re-attach"           "M2/T2.1 session broker not landed"
  skip E "E1-E4 HandoffChannel (web-link/Slack/revise/discard)" "M2/T2.2-2.4 handoff not landed"
  skip F "F1-F2 capability degradation"               "M2/M3 portable capability layer not landed"
}

# ---- main -------------------------------------------------------------------

main() {
  if [ "$MODE" = "live" ]; then
    echo "live mode: real agents + mounted keys are required; see README.md."
    echo "This runner ships the hermetic stub path; wire live controllers via docker-compose.live.yml."
  fi
  trap teardown EXIT
  setup
  group_A_resolution
  group_B_fixer
  group_I_identity
  group_G_notify
  group_H_webhook
  group_J_failure
  group_skips
  print_matrix
  [ "$FAIL" -eq 0 ]
}

main "$@"
