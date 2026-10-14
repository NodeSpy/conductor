#!/usr/bin/env bash
# Hermetic end-to-end test for distributable config packs (issue #53).
#
# Drives the REAL conductor binary through the full pack lifecycle against the
# shipped examples/packs/review-kit fixture — from BOTH a local-path source and
# a git source (a local repo served over git::file://, mirroring the GitHub
# flow with no network). Asserts: init fetches + writes a sha-pinned lockfile,
# the plan surfaces skill grants + the armed trigger + repo scope, and the
# instantiated config validates. No Docker, no secrets, no egress.
#
# Usage:  bash test/e2e/packs.sh
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="$(mktemp -d)"
BIN="$WORK/conductor"
trap 'rm -rf "$WORK"' EXIT

pass=0; fail=0
ok()  { echo "  PASS: $1"; pass=$((pass+1)); }
bad() { echo "  FAIL: $1"; fail=$((fail+1)); }
have() { grep -qF -- "$2" "$1" && ok "$3" || { bad "$3"; echo "    (expected substring: $2)"; }; }

echo "== build =="
( cd "$REPO" && go build -o "$BIN" ./cmd/conductor )
echo "built $("$BIN" version)"

# The github connector is a plugin: GITHUB_PLUGIN_BIN names a local build, else
# it is built from conductor-plugins at the version the Docker e2e pins (one
# pin, test/e2e/Dockerfile). Go uses its normal caches; everything conductor
# writes goes under $WORK.
GH_PLUGIN="$WORK/conductor-github"
if [ -n "${GITHUB_PLUGIN_BIN:-}" ]; then
  cp "$GITHUB_PLUGIN_BIN" "$GH_PLUGIN"
else
  pin="$(sed -n 's/^ARG GITHUB_PLUGIN_VERSION=//p' "$REPO/test/e2e/Dockerfile")"
  ( cd "$WORK" && GOBIN="$WORK/gobin" CGO_ENABLED=0 go install "github.com/NodeSpy/conductor-plugins/connectors/github@$pin" )
  mv "$WORK/gobin/github" "$GH_PLUGIN"
fi
chmod 0755 "$GH_PLUGIN"

# Isolate every conductor state/config/cache path: `init` installs plugins
# and writes state, and must never touch the operator's own dirs.
export GOMODCACHE="$(go env GOMODCACHE)" GOCACHE="$(go env GOCACHE)" GOPATH="$(go env GOPATH)"
export HOME="$WORK/home" XDG_STATE_HOME="$WORK/state" XDG_CONFIG_HOME="$WORK/xdgconfig" XDG_CACHE_HOME="$WORK/cache"
mkdir -p "$HOME" "$XDG_STATE_HOME" "$XDG_CONFIG_HOME" "$XDG_CACHE_HOME"

# Dummy env so the demo github connector resolves (hermetic, no real secrets).
export GH_TOKEN=dummy
mkdir -p "$WORK/vault"

# --- write a consumer config that installs the example pack from a LOCAL source ---
write_config() {
  local src="$1"
  cat > "$WORK/config.yaml" <<EOF
connectors:
  gh:
    use: $GH_PLUGIN
    identity: { read_token: env:GH_TOKEN, write_token: env:GH_TOKEN }
    webhook: { listen: "127.0.0.1:8787", path: /webhook, secret: env:GH_TOKEN, verify_signature: false }
    me: { logins: [conductor-bot] }
vaults:
  house: { type: file, dir: $WORK/vault }
packs:
  review:
    source: $src
    preset: claude
    connectors: { github: gh }
    secrets:    { review_token: house/review }
    triggers:
      on_review_request:
        enabled: true
        repos: [acme/app, acme/api]
EOF
}

run_lifecycle() {
  local label="$1"
  echo
  echo "== [$label] conductor init =="
  "$BIN" init --config "$WORK/config.yaml" | tee "$WORK/init.out"
  echo "== [$label] conductor.lock.yaml =="
  cat "$WORK/conductor.lock.yaml" | tee "$WORK/lock.out"
  echo "== [$label] conductor validate =="
  "$BIN" validate --config "$WORK/config.yaml" 2>&1 | tee "$WORK/validate.out" || true

  echo "== [$label] assertions =="
  have "$WORK/init.out"     "review-kit"                 "$label: init resolved review-kit"
  have "$WORK/init.out"     "grants skill: gh.comment, gh.submit_review" "$label: plan surfaces the skill grant (namespaced/rebound)"
  have "$WORK/init.out"     "ARMED"                      "$label: plan shows the trigger armed"
  have "$WORK/init.out"     "acme/app"                   "$label: plan shows the repo scope (consent)"
  have "$WORK/lock.out"     "name: review-kit"           "$label: lockfile pins the pack by canonical name"
  have "$WORK/lock.out"     "sha256:"                    "$label: lockfile carries a tree digest"
  have "$WORK/validate.out" "ok:"                        "$label: instantiated config validates"
}

# 1) LOCAL-PATH SOURCE
write_config "$REPO/examples/packs/review-kit"
run_lifecycle "local"

# 2) GIT SOURCE (local repo over git::file://, no network) — the GitHub flow.
if command -v git >/dev/null 2>&1; then
  GITREPO="$WORK/packs-repo"
  mkdir -p "$GITREPO/review-kit"
  cp "$REPO/examples/packs/review-kit/conductor-pack.yaml" "$GITREPO/review-kit/"
  ( cd "$GITREPO"
    git init -q -b main
    git -c user.name=t -c user.email=t@e add -A
    git -c user.name=t -c user.email=t@e commit -q -m "review-kit"
  )
  write_config "git::file://$GITREPO//review-kit"
  run_lifecycle "git"
  have "$WORK/lock.out" "resolved: " "git: lockfile records a resolved revision"
  # A git source pins a 40-char commit sha (not the local sentinel).
  if grep -qE 'resolved: [0-9a-f]{40}' "$WORK/lock.out"; then
    ok "git: lockfile pins a concrete 40-char commit sha"
  else
    bad "git: lockfile pins a concrete 40-char commit sha"
  fi
else
  echo "  (git not available — skipping git-source scenario)"
fi

echo
echo "== summary: $pass passed, $fail failed =="
[ "$fail" -eq 0 ]
