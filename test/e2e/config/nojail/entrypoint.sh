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
set -euo pipefail

ROW="${ROW:?set ROW=prejail-v1|rewritten-v089|fresh}"
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
*)
  echo "nojail entrypoint: unknown ROW=$ROW (want prejail-v1|rewritten-v089|fresh)" >&2
  exit 1
  ;;
esac

exec conductor run --config "$CONFIG"
