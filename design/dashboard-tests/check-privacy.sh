#!/usr/bin/env bash
# Fails if a personal name appears in any tracked file under design/ (the repository is public). The pattern is
# written with a character class so this script doesn't match itself.
set -euo pipefail
cd "$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
if git grep -n -i "dan[i]el" -- design; then echo "personal name found under design/" >&2; exit 1; fi
echo "privacy: no personal names under design/"
