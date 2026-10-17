#!/usr/bin/env bash
# Run the prototype's interaction tests. npm deps, browsers and test output
# live on LOCAL disk under ~/.cache/conductor-design (never under ~/Projects,
# which is an NFS mount): DEPS_DIR for node_modules, ms-playwright for the
# browsers (chromium-1208 = @playwright/test 1.58.2; webkit-2248 for
# run-webkit.sh), out/ for results. Run suites one at a time.
#   ./run.sh                       # serve the prototype on 127.0.0.1:8799 and run everything
#   BASE_URL=http://host:8767 ./run.sh crawl.spec.js --project=phone
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
LOCAL=${LOCAL_CACHE:-$HOME/.cache/conductor-design}
DEPS=${DEPS_DIR:-$LOCAL/dashboard-tests-node}
export PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH:-$LOCAL/ms-playwright}
mkdir -p "$DEPS"
cp "$HERE/package.json" "$DEPS/package.json"
if [ ! -x "$DEPS/node_modules/.bin/playwright" ]; then
  (cd "$DEPS" && PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm install --no-audit --no-fund --cache "${NPM_CACHE:-$DEPS/.npm}" >/dev/null)
fi
export OUT_DIR=${OUT_DIR:-$LOCAL/out/dashboard-tests} REPORT=${REPORT:-$LOCAL/out/dashboard-tests/results.json}
cd "$HERE"
NODE_PATH="$DEPS/node_modules" exec "$DEPS/node_modules/.bin/playwright" test -c "$HERE/playwright.config.js" "$@"
