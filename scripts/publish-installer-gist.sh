#!/usr/bin/env bash
# Re-publish scripts/install-release.sh to the public installer gist so the
# `curl -fsSL <gist>/raw/install-release.sh | bash` one-liner stays current.
# Run this after editing install-release.sh.
set -euo pipefail

GIST_ID="${PASEO_CONDUCTOR_GIST_ID:-3504ed91ac1014b3f073f44916e443a7}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

gh gist edit "$GIST_ID" --filename install-release.sh "$here/scripts/install-release.sh"
echo "==> updated gist $GIST_ID"
echo "    raw: https://gist.githubusercontent.com/$(gh api user --jq .login)/$GIST_ID/raw/install-release.sh"
