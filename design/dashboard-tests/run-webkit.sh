#!/usr/bin/env bash
# The WebKit iPhone projects. The dev host is Arch; Playwright's WebKit build needs Ubuntu's libraries, so this
# runs the suite inside the Playwright Ubuntu image, with browsers, node_modules and output on LOCAL disk under
# ~/.cache/conductor-design (WebKit unpacked into ms-playwright/webkit-2248). Same arguments as run.sh.
#   BASE_URL=http://100.119.192.38:8767 ./run-webkit.sh iphone.spec.js --reporter=line
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
CACHE=${LOCAL_CACHE:-$HOME/.cache/conductor-design}
DEPS=${DEPS_DIR:-$CACHE/dashboard-tests-node}
IMAGE=${PW_IMAGE:-mcr.microsoft.com/playwright:v1.62.0-noble}
: "${BASE_URL:?set BASE_URL to the running prototype (the container shares the host network)}"
exec docker run --rm --network host --ipc=host --user "$(id -u):$(id -g)" \
  -e HOME=/tmp -e WEBKIT=1 -e BASE_URL="$BASE_URL" -e WORKERS="${WORKERS:-2}" \
  -e PLAYWRIGHT_BROWSERS_PATH="$CACHE/ms-playwright" -e NODE_PATH="$DEPS/node_modules" \
  -e OUT_DIR="$CACHE/out/webkit" -e REPORT="$CACHE/out/webkit/results.json" \
  -v "$CACHE:$CACHE" -v "$HERE/..:$HERE/.." -w "$HERE" \
  "$IMAGE" "$DEPS/node_modules/.bin/playwright" test -c "$HERE/playwright.config.js" --project='iphone-*' "$@"
