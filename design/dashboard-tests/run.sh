#!/usr/bin/env bash
# Run the prototype's interaction tests. npm deps live in a temp project
# (DEPS_DIR), never in the repo or globally; browsers come from the shared
# ~/.cache/ms-playwright (chromium-1208 = @playwright/test 1.58.2).
#   ./run.sh                       # serve the prototype on 127.0.0.1:8799 and run everything
#   BASE_URL=http://host:8767 ./run.sh crawl.spec.js --project=phone
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
DEPS=${DEPS_DIR:-$HOME/Projects/.cache/dashboard-tests-node}
mkdir -p "$DEPS"
cp "$HERE/package.json" "$DEPS/package.json"
if [ ! -x "$DEPS/node_modules/.bin/playwright" ]; then
  (cd "$DEPS" && PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm install --no-audit --no-fund --cache "${NPM_CACHE:-$DEPS/.npm}" >/dev/null)
fi
export OUT_DIR=${OUT_DIR:-$DEPS/out} REPORT=${REPORT:-$DEPS/out/results.json}
cd "$HERE"
NODE_PATH="$DEPS/node_modules" exec "$DEPS/node_modules/.bin/playwright" test -c "$HERE/playwright.config.js" "$@"
