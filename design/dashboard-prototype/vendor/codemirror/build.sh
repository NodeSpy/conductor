#!/usr/bin/env bash
# Build vendor/codemirror/codemirror.min.js (window.CM) from entry.js with the versions pinned in package.json.
# node_modules lives outside the repo (BUILD_DIR, default ~/.cache/conductor-dashboard-codemirror) so nothing big is
# committed; only the bundle and its licence notice are.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
build="${BUILD_DIR:-$HOME/.cache/conductor-dashboard-codemirror}"
mkdir -p "$build"
cp "$here/package.json" "$here/entry.js" "$build/"
(cd "$build" && npm install --no-audit --no-fund --silent)
(cd "$build" && npx esbuild entry.js --bundle --minify --format=iife --global-name=CM --target=es2020 --legal-comments=none --outfile="$here/codemirror.min.js")
{ echo "CodeMirror 6 and Lezer, MIT licence. Bundled versions:"; (cd "$build" && node -e "const p=require('./package.json'); for (const d of Object.keys(p.dependencies)) console.log('  ' + d + ' ' + require('./node_modules/' + d + '/package.json').version)"); echo; cat "$build/node_modules/@codemirror/view/LICENSE"; } > "$here/LICENSE.txt"
ls -l "$here/codemirror.min.js"
