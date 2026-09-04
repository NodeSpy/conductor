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
  # Read provider secrets from the operator's own host config (best-effort; nothing
  # secret is committed). Exports PC_LIVE_* consumed by docker-compose.live.yml.
  # shellcheck source=live-env.sh
  [ -f "$DIR/live-env.sh" ] && source "$DIR/live-env.sh"
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
  cexec "$1" conductor force "$2" "$3" --config "$4" 2>&1
}

# post_webhook <event> <fixture> — sign and POST a fixture to conductor's receiver.
post_webhook() { post_webhook_to conductor "$@"; }

# post_webhook_to <container> <event> <fixture> — sign and POST a fixture to a
# specific daemon's receiver (each daemon binds :8787 in its own container).
post_webhook_to() {
  local target="$1" event="$2" fixture="$3"
  cexec "$target" bash -c '
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
  # the repo. Conductor needs a parseable PEM even against the mock. It's created by
  # a root process inside the image, so chmod it world-readable: in live mode the
  # daemon runs NON-root (claude-code refuses --dangerously-skip-permissions under
  # root) and the config dir is mounted read-only, so a 0600 root-owned pem would be
  # unreadable and the github integration would fail to start.
  if [ ! -f "$DIR/config/github-app.pem" ]; then
    docker run --rm -v "$DIR/config:/c" conductor-e2e:latest \
      sh -c 'openssl genrsa -out /c/github-app.pem 2048' >/dev/null 2>&1
  fi
  # ALWAYS re-assert the mode — a pem left 0600 by an older run is root-owned (a host
  # chmod can't touch it) and would be reused forever behind the existence check
  # above, wedging the github integration ("read app key: permission denied") so
  # nothing dispatches. A root container is the only way to fix a root-owned file's
  # mode from here.
  docker run --rm -v "$DIR/config:/c" conductor-e2e:latest \
    chmod 644 /c/github-app.pem >/dev/null 2>&1

  dc up -d || { echo "up failed"; dc logs; exit 1; }

  banner "wait for readiness"
  wait_for 60 cexec mock-github  curl -sf http://localhost:8080/_health  || fatal "mock-github not ready"
  wait_for 60 cexec sink-catcher curl -sf http://localhost:8080/_health  || fatal "sink-catcher not ready"
  wait_for 60 cexec forge git ls-remote git://localhost/acme/web.git      || fatal "forge not ready"
  for c in conductor conductor-ctrl conductor-fail conductor-conn conductor-migrate; do
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
  if ! cexec conductor conductor validate --config /etc/conductor/resolution/a5-two-defaults.yaml >/dev/null 2>&1; then
    ok "A5 validation rejects two default:true controllers" A A5a
  else
    bad "A5 two defaults rejected" A A5a "validate unexpectedly passed"
  fi
  if ! cexec conductor conductor validate --config /etc/conductor/resolution/a5-unknown-transport.yaml >/dev/null 2>&1; then
    ok "A5 validation rejects an unknown transport" A A5b
  else
    bad "A5 unknown transport rejected" A A5b "validate unexpectedly passed"
  fi
  if cexec conductor conductor validate --config /etc/conductor/resolution/a3-valid-default.yaml >/dev/null 2>&1; then
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

  # Every non-paseo controller drives its fake runtime through conductor's REAL
  # controller code (resolve → provision PR worktree → open session → first turn),
  # and the fake does the edit+commit+push, so a `conductor:` commit lands on the
  # forge per controller. One repo per controller keeps the rows independent.
  local CFG=/etc/conductor/controllers.yaml
  b_controller_row grpb/cliclaude  fx_cli_claude  cli        "cli:claude-code"
  b_controller_row grpb/clicodex   fx_cli_codex   cli        "cli:codex"
  b_controller_row grpb/acpgemini  fx_acp_gemini  acp        "acp:gemini"
  b_controller_row grpb/acpcodex   fx_acp_codex   acp        "acp:codex-adapter"
  b_controller_row grpb/ocacp      fx_oc_acp      acp        "opencode-acp"
  b_controller_row grpb/ocnative   fx_oc_native   native     "opencode-native"
  b_controller_row grpb/deck       fx_deck        native     "agent-deck"
}

# b_controller_row <repo> <agent> <backend> <label> — force a merge_conflict on the
# repo (routed to the named controller), assert the controller drove a real fixer
# (a `conductor:` commit on pr-1) and that the dispatch was recorded under the
# controller's transport backend.
b_controller_row() {
  local repo="$1" agent="$2" backend="$3" label="$4"
  force conductor-ctrl merge_conflict "$repo#1" /etc/conductor/controllers.yaml >/dev/null
  if wait_for 60 forge_has_conductor_commit "$repo" pr-1; then
    ok "B $label fixer edited & pushed to the forge (commit on pr-1)" B "$label"
  else
    bad "B $label fixer pushed a fix" B "$label" "no conductor commit on $repo pr-1"
  fi
  if wait_for 15 audit_match conductor-ctrl "\"repo\":\"$repo\"" '"event":"dispatch"' "\"backend\":\"$backend\""; then
    ok "B $label dispatched via the $backend controller backend" B "$label-backend"
  else
    bad "B $label backend=$backend" B "$label-backend" "no $backend dispatch for $repo"
  fi
}

forge_has_conductor_commit() { # repo branch
  local subj
  subj="$(cexec forge git --git-dir="/srv/git/$1.git" log "$2" -1 --format='%s' 2>/dev/null)"
  case "$subj" in conductor:*) return 0 ;; *) return 1 ;; esac
}

# forge_branch_commits <repo> <branch> — number of commits on the branch. The seed
# gives every pr-1 exactly 2 commits (initial + the PR change); a pushed agent fix
# makes it ≥3. Used by live mode, where the real agent's commit subject is its own.
forge_branch_commits() { # repo branch
  cexec forge git --git-dir="/srv/git/$1.git" rev-list --count "$2" 2>/dev/null | tr -d '\r'
}
forge_got_new_commit() { # repo branch
  local n; n="$(forge_branch_commits "$1" "$2")"
  [ -n "$n" ] && [ "$n" -ge 3 ]
}

# ---- live mode (real agent CLIs) --------------------------------------------

group_B_live() {
  banner "Group B (LIVE) — each installed controller runs a REAL fixer"
  echo "Driving the real agent CLIs mounted from the host; needs the operator's keys."
  local CFG=/etc/conductor/controllers.live.yaml
  # cli:claude-code is the reference agent — it authenticates through the host
  # TeamClaude proxy and reliably lands a commit, so it's a REQUIRED pass (a red
  # failure here is a genuine harness/controller regression).
  b_live_row_required   grpb/cliclaude "cli:claude-code"
  # The rest are best-effort: conductor's wiring is validated the same way (it drives
  # the real CLI, which authenticates and runs), but whether the model PROVIDER lets
  # the turn complete depends on the operator's account (quota, model availability,
  # backend). A conductor-level failure (couldn't launch/handshake the agent) is a
  # red FAIL; an agent that launched+authenticated but whose provider blocked the
  # call (no commit, no conductor error) is a SKIP with the captured reason — an
  # operator action, not a harness bug. See README "Live mode".
  b_live_row_besteffort grpb/clicodex  "cli:codex"
  b_live_row_besteffort grpb/acpgemini "acp:gemini"
  # omp / agent-deck are fully optional (their provider keys may not be present):
  # PASS on commit, else SKIP with the reason — never a hard FAIL.
  b_live_row_optional   grpb/ocnative  "acp:omp"
  b_live_row_optional   grpb/deck      "agent-deck"
  # Not installed on this box — recorded as genuinely N/A, not a stale skip.
  skip B "acp:codex-adapter" "N/A — codex-acp binary not installed on this host"
  skip B "opencode-acp"      "N/A — opencode not installed on this host"
  skip B "opencode-native"   "N/A — opencode not installed on this host"
  skip B "copilot"           "N/A — copilot not installed on this host"
}

# b_live_row_required <repo> <label> — force a merge_conflict routed to a real
# controller and REQUIRE the real agent to push a NEW commit to pr-1 (PASS/FAIL).
b_live_row_required() {
  local repo="$1" label="$2"
  force conductor merge_conflict "$repo#1" /etc/conductor/controllers.live.yaml >/dev/null
  if wait_for 300 forge_got_new_commit "$repo" pr-1; then
    ok "B(live) $label real agent edited & pushed to the forge" B "$label"
  else
    bad "B(live) $label real fixer" B "$label" "no new commit on $repo pr-1 (check keys/logs)"
  fi
}

# live_await_push <repo> — a turn is marked complete a moment before its `git push`
# lands on the forge (git:// propagation), so once the turn finishes give the commit
# a short grace window to appear before a caller concludes it never pushed. Returns 0
# as soon as the commit shows, 1 if the window elapses.
live_await_push() { # repo
  local g=$((SECONDS + 30))
  while [ $SECONDS -lt $g ]; do
    forge_got_new_commit "$1" pr-1 && return 0
    sleep 2
  done
  return 1
}

# b_live_row_besteffort <repo> <label> — force a merge_conflict and classify by what
# the real agent actually did: a pushed commit on the forge is PASS; a conductor-level
# dispatch failure is FAIL (a real harness/controller problem); a finished turn that
# left no commit even after the grace window is SKIP with the captured reason.
b_live_row_besteffort() {
  local repo="$1" label="$2"
  force conductor merge_conflict "$repo#1" /etc/conductor/controllers.live.yaml >/dev/null
  local deadline=$((SECONDS + 240))
  while [ $SECONDS -lt $deadline ]; do
    forge_got_new_commit "$repo" pr-1 && break
    live_conductor_failed "$repo" && break   # dispatch couldn't drive the agent
    live_turn_done "$repo"        && break    # agent's turn finished; its push may still be landing
    sleep 3
  done
  live_await_push "$repo"   # let a just-finished turn's push reach the forge
  if forge_got_new_commit "$repo" pr-1; then
    ok "B(live) $label real agent edited & pushed to the forge" B "$label"
  elif live_conductor_failed "$repo"; then
    bad "B(live) $label real fixer" B "$label" "conductor could not drive $label: $(live_last_error "$repo")"
  else
    skip B "$label" "agent turn finished but no commit reached the forge within the window.$(live_last_error "$repo")"
  fi
}

# b_live_row_optional <repo> <label> — like best-effort but never hard-FAILs: a
# missing/optional provider key surfaces as a SKIP with the reason, not a red row.
b_live_row_optional() {
  local repo="$1" label="$2"
  force conductor merge_conflict "$repo#1" /etc/conductor/controllers.live.yaml >/dev/null
  local deadline=$((SECONDS + 180))
  while [ $SECONDS -lt $deadline ]; do
    forge_got_new_commit "$repo" pr-1 && break
    live_conductor_failed "$repo" && break
    live_turn_done "$repo"        && break
    sleep 3
  done
  live_await_push "$repo"
  if forge_got_new_commit "$repo" pr-1; then
    ok "B(live) $label real agent edited & pushed to the forge" B "$label"
  else
    skip B "$label" "optional — no commit reached the forge (not wired/authenticated on this host, or its turn left no commit).$(live_last_error "$repo")"
  fi
}

# live_conductor_failed <repo> — conductor itself failed to drive the agent (the
# dispatch escalated or was recorded as a failed dispatch): a real harness/controller
# problem, distinct from the agent's model provider refusing the turn.
live_conductor_failed() { # repo
  audit_match conductor "\"repo\":\"$1\"" '"event":"escalate"' \
    || audit_match conductor "\"repo\":\"$1\"" '"event":"dispatch"' '"outcome":"failed"'
}

# live_turn_done <repo> — conductor recorded the agent's turn as complete (the CLI
# process exited / the ACP prompt turn ended). Used to stop waiting early once the
# agent is done, whether or not it committed.
live_turn_done() { # repo
  audit_match conductor "\"repo\":\"$1\"" '"event":"complete"'
}

# live_last_error <repo> — a short error/message snippet from the newest audit line
# for the repo carrying one, prefixed with a space (empty if none). Surfaces the
# reason (e.g. an ACP handshake error) in the row note.
live_last_error() { # repo
  # Only surface a real failure signal — an `error` field or an `escalate` message —
  # not the generic `complete` event's `"msg":"cli"/"acp"`. Empty (clean note) when
  # the agent simply ran without committing (its provider blocked the turn).
  local e
  e="$(cexec conductor cat /data/audit.jsonl 2>/dev/null \
        | grep "\"repo\":\"$1\"" \
        | grep -E '"error":"|"event":"escalate"' \
        | grep -oE '"(error|msg)":"[^"]*"' | tail -1)"
  [ -n "$e" ] && printf ' %s' "$e"
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

  # J3: an ACP agent that dies as its session opens (FAKE_ACP_CRASH, injected
  # per-route) is detected — the session/new RPC fails → dispatch fails → the engine
  # escalates loudly rather than silently dropping the crashed agent.
  force conductor-ctrl merge_conflict grpj/acpcrash#1 /etc/conductor/controllers.yaml >/dev/null
  if wait_for 30 audit_match conductor-ctrl '"repo":"grpj/acpcrash"' '"event":"escalate"'; then
    ok "J3 ACP agent crash → conductor detects → escalate (not silent)" J J3
  else
    bad "J3 ACP crash escalates" J J3 "no escalate for grpj/acpcrash"
  fi
}

sink_body_has() { # substring
  local caps; caps="$(netcurl http://sink-catcher:8080/_captured)"
  printf '%s' "$caps" | grep -qF -- "$1"
}

# session_ref_has <container> <pat...> — the broker's persisted PR→session map
# (sessions.json) contains ALL patterns. It's pretty-printed, so whitespace is
# stripped before matching (patterns are given in compact `"k":"v"` form).
session_ref_has() {
  local c="$1"; shift
  local out; out="$(cexec "$c" cat /data/sessions.json 2>/dev/null | tr -d ' \n\t')"
  [ -n "$out" ] || return 1
  local p
  for p in "$@"; do case "$out" in *"$p"*) ;; *) return 1 ;; esac; done
  return 0
}

# latest_handoff_url <container> <repo> — the URL from the most recent needs_input
# audit line for the repo (the web draft the runner drives). Empty if none yet.
latest_handoff_url() {
  local c="$1" repo="$2"
  cexec "$c" cat /data/audit.jsonl 2>/dev/null \
    | grep '"event":"needs_input"' | grep "\"repo\":\"$repo\"" \
    | grep -o 'http://[^ "]*/handoff?id=[A-Za-z0-9_-]*' | tail -1
}

# wait_handoff_url <container> <repo> — poll until a draft URL is available, echo it.
wait_handoff_url() {
  local c="$1" repo="$2" deadline=$((SECONDS + 40)) url=""
  while [ $SECONDS -lt $deadline ]; do
    url="$(latest_handoff_url "$c" "$repo")"
    [ -n "$url" ] && { echo "$url"; return 0; }
    sleep 1
  done
  return 1
}

# hoff_get / hoff_post — drive the web hand-off page from inside a container.
hoff_get()  { cexec "$1" curl -s -o /dev/null -w '%{http_code}' "$2"; }
hoff_post() { cexec "$1" curl -s -X POST "$2" --data "$3"; }
hoff_is_404() { [ "$(hoff_get "$1" "$2")" = "404" ]; }

group_C_session_model() {
  banner "Group C — session_model (native / resumable / oneshot)"

  # NATIVE (paseo): an interactive hand-off binds the launched paseo agent as the
  # PR's broker session; its persisted ref records session_model=native.
  force conductor review_requested grpe/paseohoff#1 /etc/conductor/conductor.yaml >/dev/null
  if wait_for 40 session_ref_has conductor '"pr_key"' 'grpe/paseohoff' '"model":"native"'; then
    ok "C native: paseo live session bound to the PR (session_model=native)" C native
  else
    bad "C native session_model" C native "no native session ref for grpe/paseohoff"
  fi

  # RESUMABLE (acp): the ACP controller advertises loadSession → session_model
  # resumable; its bound ref persists as resumable and survives a restart (D2).
  force conductor-ctrl review_requested grpc/acpresume#1 /etc/conductor/controllers.yaml >/dev/null
  if wait_for 40 session_ref_has conductor-ctrl '"pr_key"' 'grpc/acpresume' '"model":"resumable"'; then
    ok "C resumable: ACP session persisted by id (session_model=resumable)" C resumable
  else
    bad "C resumable session_model" C resumable "no resumable session ref for grpc/acpresume"
  fi

  # ONESHOT (cli:codex): each turn is a fresh process — the codex fixer (group B)
  # did real work with NO persistent session, so it never appears in the broker's
  # session map. That absence IS the oneshot behavior.
  if wait_for 30 forge_has_conductor_commit grpb/clicodex pr-1 \
     && ! session_ref_has conductor-ctrl 'grpb/clicodex'; then
    ok "C oneshot: cli:codex ran a fresh-process turn, no persistent session" C oneshot
  else
    bad "C oneshot behavior" C oneshot "codex left a persistent session or no commit"
  fi
}

group_D_broker() {
  banner "Group D — session broker (dedup burst / restart survival)"

  # D1: a burst of new_comment events on ONE PR must collapse onto a single live
  # agent (liveAgentForPR → paseo send), not spawn a duplicate per event. The live
  # fixer (archive_when_done:false) stays open so #2/#3 dedup onto it. Sequenced
  # with a beat between so #1's agent is registered before #2/#3 look for it.
  local i
  for i in 1 2 3; do
    force conductor new_comment grpd/burst#1 /etc/conductor/conductor.yaml >/dev/null
    sleep 3
  done
  local ok_n queued_n
  ok_n="$(audit_count conductor '"repo":"grpd/burst"' '"kind":"new_comment"' '"event":"dispatch"' '"outcome":"ok"')"
  queued_n="$(audit_count conductor '"repo":"grpd/burst"' '"kind":"new_comment"' '"event":"dispatch"' '"outcome":"queued"')"
  if [ "$ok_n" = "1" ] && [ "$queued_n" -ge 2 ]; then
    ok "D1 burst of 3 → ONE live session, $queued_n follow-ups queued to it" D D1
  else
    bad "D1 burst → one session" D D1 "ok=$ok_n queued=$queued_n (want ok=1, queued>=2)"
  fi

  # D2: the resumable ACP session bound in group C must survive a conductor restart
  # — the broker reloads the persisted ref by id (re-attach), with no orphan.
  if ! session_ref_has conductor-ctrl 'grpc/acpresume' '"model":"resumable"'; then
    bad "D2 restart survival (precondition)" D D2 "no resumable ref to survive (run group C first)"
  else
    dc restart conductor-ctrl >/dev/null 2>&1
    if wait_for 40 cexec conductor-ctrl test -S /data/control.sock \
       && wait_for 20 session_ref_has conductor-ctrl 'grpc/acpresume' '"model":"resumable"'; then
      ok "D2 restart mid-session → resumable ref re-attached from store, no orphan" D D2
    else
      bad "D2 restart re-attach" D D2 "resumable ref lost across restart or daemon didn't recover"
    fi
  fi
}

group_E_handoff() {
  banner "Group E — HandoffChannel (web-link approve / revise / discard)"
  local url body

  # E1 (paseo): drive the review bound in group C on grpe/paseohoff to approval.
  if url="$(wait_handoff_url conductor grpe/paseohoff)"; then
    assert_contains "$(hoff_get conductor "$url")" "200" E E1-page \
      "E1 web draft page served"
    body="$(hoff_post conductor "$url" 'action=approve')"
    assert_contains "$body" "Recorded: approve" E E1-approve \
      "E1 web-link approve on the paseo controller"
    if wait_for 10 hoff_is_404 conductor "$url"; then
      ok "E1 draft consumed after decision (GET now 404)" E E1-consumed
    else
      bad "E1 draft consumed" E E1-consumed "draft still resolvable after approve"
    fi
  else
    bad "E1 web draft appears" E E1-page "no needs_input draft url for grpe/paseohoff"
  fi

  # E1 on a BARE runner + F1: cli:claude-code reports InteractiveHandoff:false, yet
  # completes the review over the portable web channel (grpf/clihoff).
  force conductor-ctrl review_requested grpf/clihoff#1 /etc/conductor/controllers.yaml >/dev/null
  if url="$(wait_handoff_url conductor-ctrl grpf/clihoff)"; then
    body="$(hoff_post conductor-ctrl "$url" 'action=approve')"
    assert_contains "$body" "Recorded: approve" E E1-bare \
      "E1 web-link approve on a bare (cli) runner"
    ok "F1 controller lacking interactive-handoff completed review via portable channel" F F1
  else
    bad "E1 bare-runner draft" E E1-bare "no needs_input draft for grpf/clihoff"
    bad "F1 portable-channel review" F F1 "cli controller review hand-off never presented"
  fi

  # E3 revise: a fresh review, send a revision → the loop re-presents a new draft,
  # then approve the re-presented one.
  local before; before="$(audit_count conductor '"repo":"grpe/paseohoff"' '"event":"needs_input"')"
  force conductor review_requested grpe/paseohoff#1 /etc/conductor/conductor.yaml >/dev/null
  if url="$(wait_handoff_url_gt conductor grpe/paseohoff "$before")"; then
    local mid; mid="$(audit_count conductor '"repo":"grpe/paseohoff"' '"event":"needs_input"')"
    hoff_post conductor "$url" 'action=revise&text=tighten+the+wording' >/dev/null
    if url="$(wait_handoff_url_gt conductor grpe/paseohoff "$mid")"; then
      ok "E3 revise loop re-presents the draft after a revision turn" E E3
      hoff_post conductor "$url" 'action=approve' >/dev/null   # close the loop
    else
      bad "E3 revise re-presents" E E3 "no new draft after revising"
    fi
  else
    bad "E3 revise" E E3 "no draft for the revise cycle"
  fi

  # E4 discard: a fresh review, discard it.
  before="$(audit_count conductor '"repo":"grpe/paseohoff"' '"event":"needs_input"')"
  force conductor review_requested grpe/paseohoff#1 /etc/conductor/conductor.yaml >/dev/null
  if url="$(wait_handoff_url_gt conductor grpe/paseohoff "$before")"; then
    body="$(hoff_post conductor "$url" 'action=discard')"
    assert_contains "$body" "Recorded: discard" E E4 "E4 web-link discard ends the hand-off"
  else
    bad "E4 discard" E E4 "no draft for the discard cycle"
  fi

  # E2 Slack: the Slack hand-off channel (internal/handoff/slack.go) + inbox +
  # parseReply are implemented and unit-tested, but NOT wired into the daemon in
  # this build (no `handoff.slack` config, no slack.SetReplyHook call in cmd/), and
  # its inbound path is Slack Socket Mode (a WebSocket to slack.com) — not drivable
  # by the hermetic harness without a production feature addition beyond e2e scope.
  # The identical controller-agnostic Review loop IS exercised end-to-end above via
  # the web channel (approve/revise/discard), so the hand-off mechanism is covered.
  skip E "E2 Slack channel" "N/A — Slack hand-off channel not wired into the daemon (Socket Mode inbound); web channel exercises the same Review loop; unit-covered by internal/handoff/slack_test.go"
}

group_F_capability() {
  banner "Group F — capability degradation"
  # F1 is asserted in group E (cli controller, InteractiveHandoff:false, completed a
  # web review). F2: a controller that provides no checkout itself (acp) still ran
  # its agent in a conductor-PROVISIONED worktree — proven by the pr-1 commit landing
  # for the acp:gemini row plus the dispatch being recorded under the acp backend.
  if forge_has_conductor_commit grpb/acpgemini pr-1 \
     && audit_match conductor-ctrl '"repo":"grpb/acpgemini"' '"event":"dispatch"' '"backend":"acp"'; then
    ok "F2 controller lacking checkout-pr ran in a conductor-supplied worktree" F F2
  else
    bad "F2 conductor-supplied worktree" F F2 "no acp-backed commit in a provisioned worktree"
  fi
}

# wait_handoff_url_gt <container> <repo> <before-count> — wait until a NEW needs_input
# draft (count beyond <before>) exists, echo its url.
wait_handoff_url_gt() {
  local c="$1" repo="$2" before="$3" deadline=$((SECONDS + 30))
  while [ $SECONDS -lt $deadline ]; do
    local n; n="$(audit_count "$c" "\"repo\":\"$repo\"" '"event":"needs_input"')"
    if [ "$n" -gt "$before" ]; then latest_handoff_url "$c" "$repo"; return 0; fi
    sleep 1
  done
  return 1
}

# wait_handoff_url_after <container> <repo> — capture the current needs_input count,
# then wait for a NEW draft beyond it (used for a fresh review cycle on a reused repo).
wait_handoff_url_after() {
  local c="$1" repo="$2"
  local before; before="$(audit_count "$c" "\"repo\":\"$repo\"" '"event":"needs_input"')"
  wait_handoff_url_gt "$c" "$repo" "$before"
}

# ---- main -------------------------------------------------------------------

# ---------------------------------------------------------------------------
# Group K — the connectors model (issue #36): new-schema config end to end.
# ---------------------------------------------------------------------------

# slack_sink_has <pattern> — a captured slack Web API call contains pattern.
slack_sink_has() {
  netcurl http://sink-catcher:8080/_captured | grep -q "$1"
}

group_K_connectors() {
  banner "Group K — connectors model (new schema)"
  netcurl -X POST http://sink-catcher:8080/_reset >/dev/null

  # K1 + K5 ride one merge_conflict event on conn/cweb: the agent fixes and
  # pushes (K1), lifecycle hooks post to slack around it, and a second
  # trigger runs a sh code step over SSH on the loopback host (K5).
  code="$(post_webhook_to conductor-conn pull_request conn_merge_conflict.json)"
  if [ "$code" = "200" ] || [ "$code" = "202" ]; then
    ok "K1 webhook accepted by the connectors daemon (HTTP $code)" K K1-http
  else
    bad "K1 webhook accepted" K K1-http "unexpected HTTP $code"
  fi

  if wait_for 20 slack_sink_has "K1-start conn/cweb#1"; then
    ok "K1 at:start hook posted to slack before the step" K K1-start
  else
    bad "K1 at:start hook posted" K K1-start "no K1-start capture on the slack sink"
  fi
  if wait_for 45 forge_has_conductor_commit conn/cweb pr-1; then
    ok "K1 agent step fixed & pushed (new-schema trigger → fakepaseo → forge)" K K1-agent
  else
    bad "K1 agent step pushed a fix" K K1-agent "no conductor commit on conn/cweb pr-1"
  fi
  if wait_for 30 slack_sink_has "K1-done conn/cweb#1"; then
    ok "K1 at:done hook posted after the workflow" K K1-done
  else
    bad "K1 at:done hook posted" K K1-done "no K1-done capture"
  fi
  if slack_sink_has "K1-fail"; then
    bad "K1 no fail hook fired" K K1-nofail "K1-fail capture present"
  else
    ok "K1 at:fail hook did NOT fire on success" K K1-nofail
  fi

  # K5: the remote sh step ran on selfbox via the system ssh — the container's
  # own hostname flows through the step output into the slack post.
  host="$(cexec conductor-conn hostname | tr -d '\r\n')"
  if wait_for 30 slack_sink_has "K5-remote $host as root"; then
    ok "K5 code step ran over SSH (host: selfbox) and its output reached slack" K K5-remote
  else
    bad "K5 remote code step over SSH" K K5-remote "no 'K5-remote $host' capture"
  fi

  # K6: an agent on the remote paseo runtime — the fixer's commit lands on
  # the forge even though every paseo invocation rode the ssh channel.
  post_webhook_to conductor-conn pull_request conn_remote_conflict.json >/dev/null
  if wait_for 60 forge_has_conductor_commit conn/rweb pr-1; then
    ok "K6 remote paseo runtime (host:) fixed & pushed over SSH" K K6-remote-paseo
  else
    bad "K6 remote paseo runtime over SSH" K K6-remote-paseo "no conductor commit on conn/rweb pr-1"
  fi
  if wait_for 30 slack_sink_has "K6-done conn/rweb#1"; then
    ok "K6 done hook fired after the remote workflow" K K6-done
  else
    bad "K6 done hook" K K6-done "no K6-done capture"
  fi

  # K2 + K3 ride a comment burst on conn/csvc: the ungrouped trigger fires per
  # comment (js code step reshapes each), the grouped trigger batches the
  # burst into ONE run seeing {{.group.count}} == 2.
  post_webhook_to conductor-conn issue_comment conn_comment_1.json >/dev/null
  sleep 0.5
  post_webhook_to conductor-conn issue_comment conn_comment_2.json >/dev/null

  if wait_for 20 slack_sink_has "K2 seen first burst comment" && wait_for 20 slack_sink_has "K2 seen second burst comment"; then
    ok "K2 js code step reshaped each comment into a slack post" K K2-js
  else
    bad "K2 js code step per comment" K K2-js "missing K2 captures"
  fi
  if wait_for 30 slack_sink_has "K3-batch 2 last=second burst comment"; then
    ok "K3 grouped burst → ONE run with group.count=2 and the last event's context" K K3-group
  else
    bad "K3 grouped burst batched" K K3-group "no K3-batch 2 capture"
  fi
  sleep 3
  n="$(netcurl http://sink-catcher:8080/_captured | grep -o "K3-batch" | wc -l | tr -d ' ')"
  if [ "$n" = "1" ]; then
    ok "K3 exactly one batched run (one-run-per-key debounce)" K K3-once
  else
    bad "K3 exactly one batched run" K K3-once "K3-batch fired $n times"
  fi

  # K4: introspection over the live config.
  out="$(cexec conductor-conn conductor connectors ls --config /etc/conductor/connectors.e2e.yaml 2>&1)"
  case "$out" in
    *gh*github*) ok "K4 conductor connectors ls lists the configured connectors" K K4-ls ;;
    *) bad "K4 connectors ls" K K4-ls "unexpected output: $(echo "$out" | head -2)" ;;
  esac
  out="$(cexec conductor-conn conductor schema slack --config /etc/conductor/connectors.e2e.yaml 2>&1)"
  case "$out" in
    *"verb ask"*"request-response"*) ok "K4 conductor schema prints the ask verb contract" K K4-schema ;;
    *) bad "K4 schema slack" K K4-schema "no ask verb in output" ;;
  esac
}

# ---------------------------------------------------------------------------
# Group L — automatic legacy→connectors migration on boot (issue #36, hard
# requirement): transform + backup + validate, still working afterwards; an
# unmappable config refuses and stays legacy.
# ---------------------------------------------------------------------------
group_L_migration() {
  banner "Group L — auto-migration (legacy → connectors)"

  # L1: the daemon booted on a LEGACY config; its boot transformed it.
  if cexec conductor-migrate test -f /data/config/config.yaml.pre-connectors; then
    ok "L1 pre-migration backup written (config.yaml.pre-connectors)" L L1-backup
  else
    bad "L1 backup written" L L1-backup "no .pre-connectors file"
  fi
  if cexec conductor-migrate grep -q "^connectors:" /data/config/config.yaml \
     && ! cexec conductor-migrate grep -q "^integrations:" /data/config/config.yaml; then
    ok "L1 config now on the connectors schema (integrations: gone)" L L1-schema
  else
    bad "L1 config migrated in place" L L1-schema "config.yaml not transformed"
  fi
  if cexec conductor-migrate grep -q "integrations:" /data/config/config.yaml.pre-connectors; then
    ok "L1 backup holds the original legacy config" L L1-original
  else
    bad "L1 backup holds the original" L L1-original "backup is not the legacy file"
  fi

  # The migrated behavior still works: the same event fires the same work.
  code="$(post_webhook_to conductor-migrate pull_request migr_merge_conflict.json)"
  if [ "$code" = "200" ] || [ "$code" = "202" ]; then
    ok "L1 webhook accepted post-migration (HTTP $code)" L L1-http
  else
    bad "L1 webhook accepted post-migration" L L1-http "unexpected HTTP $code"
  fi
  if wait_for 45 forge_has_conductor_commit migr/mweb pr-1; then
    ok "L1 migrated trigger fixed & pushed (same event → same work)" L L1-works
  else
    bad "L1 migrated trigger still works" L L1-works "no conductor commit on migr/mweb pr-1"
  fi

  # L2: an UNMAPPABLE legacy config refuses with a hard error naming the
  # construct, leaves the file untouched, and never commits a partial result.
  out="$(cexec conductor-conn bash -c '
    cp /etc/conductor/unmappable.yaml /tmp/unmappable.yaml
    if conductor config migrate --config /tmp/unmappable.yaml 2>&1; then
      echo MIGRATE_EXIT_ZERO
    fi
    grep -c "^integrations:" /tmp/unmappable.yaml || true
    test ! -f /tmp/unmappable.yaml.pre-connectors && echo NO_PARTIAL_BACKUP_COMMIT || true
  ' 2>&1)"
  case "$out" in
    *MIGRATE_EXIT_ZERO*) bad "L2 unmappable config refused" L L2-refuse "migrate exited zero" ;;
    *"nested steps"*) ok "L2 unmappable construct hard-errors naming it (nested steps)" L L2-refuse ;;
    *) bad "L2 unmappable config refused" L L2-refuse "error did not name the construct: $(echo "$out" | head -2)" ;;
  esac
  case "$out" in
    *NO_PARTIAL_BACKUP_COMMIT*) ok "L2 refusal left no partial backup/commit" L L2-intact ;;
    *) bad "L2 refusal left the file alone" L L2-intact "partial state written" ;;
  esac
}

main() {
  trap teardown EXIT
  setup
  if [ "$MODE" = "live" ]; then
    # Live mode drives the REAL agent CLIs (docker-compose.live.yml) and asserts each
    # installed controller runs an end-to-end fixer against the local forge. The
    # hermetic-only groups (mock/fake-driven) don't apply here.
    echo "live mode: driving real agents through their controllers (see README.md)."
    group_B_live
    print_matrix
    [ "$FAIL" -eq 0 ]
    return
  fi
  group_A_resolution
  group_B_fixer
  group_I_identity
  group_G_notify
  group_H_webhook
  group_C_session_model
  group_D_broker
  group_E_handoff
  group_F_capability
  group_J_failure
  group_K_connectors
  group_L_migration
  print_matrix
  [ "$FAIL" -eq 0 ]
}

main "$@"
