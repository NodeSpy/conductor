#!/usr/bin/env bash
# Entrypoint for the "hosts that cannot jail plugins" e2e (test/e2e/nojail.sh,
# see README.md "Hosts that cannot jail plugins"). Seeds ~/.local/state/
# conductor/plugins/installed.yaml for ONE of three install-state ROWS before
# exec'ing the daemon — the same pattern entrypoint-conn.sh uses for the
# reference acme-engine plugin (a pre-seeded record standing in for what
# `conductor init` would have fetched; this harness has no network, so the
# fetch is the only part skipped — resolution, verify-before-execute and
# spawn are the real ones).
#
# Both github (connectors/github) and js (engines/js) are referenced as
# INSTALLED, remote-origin plugins (`use: github` / `use: js` in nojail.yaml)
# rather than by local path: a local `use: /path` reference is always treated
# as pre-jail (internal/plugin/asbefore.go's runsAsBefore), which would never
# exercise a FRESH (post-jail) install record.
#
#   prejail-v1      version: 1 (no manifest_schema at all) — what a conductor
#                   without the plugin jail wrote. Both plugins must work.
#   rewritten-v089  version: 2, manifest_schema: 1, legacy: true — what
#                   v0.88/v0.89 left behind when they could not describe a
#                   pre-jail plugin themselves. Both plugins must still work.
#   fresh           version: 2, manifest_schema: 1, NO legacy/pre_jail on the
#                   js record — a genuine post-jail install. github keeps its
#                   legacy record (still pre-jail, still works); only js is
#                   fresh, and must be REFUSED at launch.
#   smee-migration  version: 1 with github alone, and smee.yaml (the retired
#                   smee_url shape) copied somewhere writable: boot migrates
#                   it, installing smee from a local release repo (a bare git
#                   repo with the image's smee build published under
#                   refs/dist, the real fetch path) — a FRESH install that
#                   must still run, unjailed, because it takes over from the
#                   pre-jail github plugin. A mocksmee on 127.0.0.1:3300 is
#                   the channel.
set -euo pipefail

ROW="${ROW:?set ROW=prejail-v1|rewritten-v089|fresh|smee-migration}"
CONFIG="${1:-/etc/conductor/nojail.yaml}"

mkdir -p "$HOME/data"
PLUGIN_DIR="$HOME/.local/state/conductor/plugins"
mkdir -p "$PLUGIN_DIR"

GH_BIN=/usr/local/bin/conductor-github
JS_BIN=/usr/local/bin/conductor-js
GH_SHA="$(sha256sum "$GH_BIN" | cut -d' ' -f1)"
JS_SHA="$(sha256sum "$JS_BIN" | cut -d' ' -f1)"

gh_record() { # <extra yaml lines, already indented>
  cat <<EOF
  - key: connectors/github
    kind: connector
    name: github
    use: github
    source: github.com/NodeSpy/conductor-plugins//connectors/github
    resolved: connectors/github/v1.0.0
    sha256: $GH_SHA
    path: $GH_BIN
$1
EOF
}

js_record() {
  cat <<EOF
  - key: engines/js
    kind: engine
    name: js
    use: js
    source: github.com/NodeSpy/conductor-plugins//engines/js
    resolved: engines/js/v1.0.0
    sha256: $JS_SHA
    path: $JS_BIN
$1
EOF
}

case "$ROW" in
prejail-v1)
  { echo "version: 1"; echo "plugins:"; gh_record ""; js_record ""; } > "$PLUGIN_DIR/installed.yaml"
  ;;
rewritten-v089)
  legacy="    manifest_schema: 1
    legacy: true"
  { echo "version: 2"; echo "plugins:"; gh_record "$legacy"; js_record "$legacy"; } > "$PLUGIN_DIR/installed.yaml"
  ;;
fresh)
  legacy="    manifest_schema: 1
    legacy: true"
  freshrec="    manifest_schema: 1"
  # github keeps its pre-jail record (it must keep working in this row too);
  # js gets a clean post-jail record (manifest_schema set, no legacy/pre_jail).
  { echo "version: 2"; echo "plugins:"; gh_record "$legacy"; js_record "$freshrec"; } > "$PLUGIN_DIR/installed.yaml"
  ;;
smee-migration)
  { echo "version: 1"; echo "plugins:"; gh_record ""; } > "$PLUGIN_DIR/installed.yaml"
  # The smee release the migration installs: this image's smee build,
  # published like a real release (refs/dist/<tag>/<platform>: the binary
  # and checksums.txt), and the official source's URL pointed at it.
  case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; *) arch="$(uname -m)" ;; esac
  asset="conductor-smee_linux_$arch"
  forge="$HOME/forge.git"
  git init -q --bare "$forge"
  blob=$(git --git-dir="$forge" hash-object -w /usr/local/bin/conductor-smee)
  sums=$(printf '%s  %s\n' "$(sha256sum /usr/local/bin/conductor-smee | cut -d' ' -f1)" "$asset" | git --git-dir="$forge" hash-object -w --stdin)
  tree=$(printf '100755 blob %s\t%s\n100644 blob %s\tchecksums.txt\n' "$blob" "$asset" "$sums" | git --git-dir="$forge" mktree)
  commit=$(GIT_AUTHOR_NAME=e2e GIT_AUTHOR_EMAIL=e2e@localhost GIT_COMMITTER_NAME=e2e GIT_COMMITTER_EMAIL=e2e@localhost \
    git --git-dir="$forge" commit-tree "$tree" -m connectors/smee/v1.1.0)
  git --git-dir="$forge" update-ref "refs/dist/connectors/smee/v1.1.0/linux_$arch" "$commit"
  git config --global "url.file://$forge.insteadOf" https://github.com/NodeSpy/conductor-plugins
  LISTEN=127.0.0.1:3300 mocksmee >"$HOME/data/mocksmee.log" 2>&1 &
  # The migration rewrites the config in place: give it a writable copy.
  mkdir -p "$HOME/cfg"
  cp "$CONFIG" "$HOME/cfg/conductor.yaml"
  CONFIG="$HOME/cfg/conductor.yaml"
  ;;
*)
  echo "nojail entrypoint: unknown ROW=$ROW (want prejail-v1|rewritten-v089|fresh|smee-migration)" >&2
  exit 1
  ;;
esac

exec conductor run --config "$CONFIG"
