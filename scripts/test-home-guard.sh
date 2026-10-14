#!/usr/bin/env bash
# Run a test command (default: go test ./...) with HOME pointed at an empty
# sentinel dir, then fail if the run left anything in it.
#
# Every test package isolates itself through internal/testhome, and
# TestEveryTestPackageIsolatesHome fails a package that does not. This is the
# backstop for what that check cannot see: a subprocess, a TestMain that runs
# code before isolating, a path that bypasses it. A leak here is a test that,
# on a developer's machine, writes into the live daemon's ~/.conductor,
# ~/.local/state/conductor, ~/.config/conductor, ...
#
# Usage:  bash scripts/test-home-guard.sh [cmd args...]
#   e.g.  bash scripts/test-home-guard.sh go test -race ./...
set -uo pipefail

[ $# -gt 0 ] || set -- go test ./...

# Pin the Go toolchain's dirs (and `go env -w` settings) to the real home's
# before HOME moves, so the run reuses the existing build and module caches.
GOENV="$(go env GOENV)" GOCACHE="$(go env GOCACHE)" GOMODCACHE="$(go env GOMODCACHE)" GOPATH="$(go env GOPATH)"
export GOENV GOCACHE GOMODCACHE GOPATH

sentinel="$(mktemp -d "${TMPDIR:-/tmp}/conductor-test-home.XXXXXX")"
trap 'rm -rf "${sentinel:?}"' EXIT

HOME="$sentinel" env -u XDG_CONFIG_HOME -u XDG_STATE_HOME -u XDG_CACHE_HOME -u XDG_DATA_HOME "$@"
rc=$?

# The go command keeps its telemetry counters in the user config dir; that is
# the toolchain, not a test. Drop it and its parents if that leaves them empty
# (an empty dir a TEST leaves, e.g. ~/.conductor/checkouts, still counts).
rm -rf "${sentinel:?}/.config/go" "${sentinel:?}/Library/Application Support/go"
rmdir "$sentinel/.config" "$sentinel/Library/Application Support" "$sentinel/Library" 2>/dev/null

if [ -n "$(find "$sentinel" -mindepth 1 -print -quit)" ]; then
  echo "test-home-guard: the test run wrote into \$HOME — a test is reaching the real home dir:" >&2
  (cd "$sentinel" && find . -mindepth 1 | sed 's#^\./#  ~/#' | sort | head -50) >&2
  exit 1
fi
exit $rc
