#!/usr/bin/env bash
# Shared assertion + evidence helpers for the e2e runner (run.sh). Results are
# accumulated into PASS/FAIL counters and a per-scenario matrix printed at the end.
# Every assertion prints "expected vs actual" so a failure is self-explanatory.

PASS=0
FAIL=0
declare -a RESULTS   # "GROUP|scenario|PASS/FAIL|detail"

_record() { # group scenario status detail
  RESULTS+=("$1|$2|$3|$4")
  if [ "$3" = "PASS" ]; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); fi
}

ok()   { printf '  \033[32m✓\033[0m %s\n' "$1"; _record "$2" "$3" PASS "$1"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$1"; _record "$2" "$3" FAIL "$4"; }

# assert_contains "<haystack>" "<needle>" "<group>" "<scenario>" "<desc>"
assert_contains() {
  local hay="$1" needle="$2" group="$3" scen="$4" desc="$5"
  if printf '%s' "$hay" | grep -qF -- "$needle"; then
    ok "$desc" "$group" "$scen"
  else
    bad "$desc" "$group" "$scen" "expected to contain: $needle"
  fi
}

# assert_not_contains "<haystack>" "<needle>" "<group>" "<scenario>" "<desc>"
assert_not_contains() {
  local hay="$1" needle="$2" group="$3" scen="$4" desc="$5"
  if printf '%s' "$hay" | grep -qF -- "$needle"; then
    bad "$desc" "$group" "$scen" "unexpectedly contained: $needle"
  else
    ok "$desc" "$group" "$scen"
  fi
}

# assert_true <group> <scenario> <desc> <cmd...> — passes if cmd exits 0.
assert_true() {
  local group="$1" scen="$2" desc="$3"; shift 3
  if "$@" >/dev/null 2>&1; then
    ok "$desc" "$group" "$scen"
  else
    bad "$desc" "$group" "$scen" "command failed: $*"
  fi
}

# wait_for <timeout_s> <cmd...> — poll until cmd exits 0 or timeout. Returns cmd status.
wait_for() {
  local timeout="$1"; shift
  local deadline=$((SECONDS + timeout))
  while [ $SECONDS -lt $deadline ]; do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}

# skip <group> <scenario> <reason> — record a scenario deferred to a later milestone.
skip() {
  printf '  \033[33m•\033[0m %s (%s)\n' "$2" "$3"
  RESULTS+=("$1|$2|SKIP|$3")
}

print_matrix() {
  echo
  echo "================ e2e results matrix ================"
  printf '%-6s %-40s %-6s %s\n' "GROUP" "SCENARIO" "RESULT" "NOTE"
  printf '%-6s %-40s %-6s %s\n' "-----" "--------" "------" "----"
  for row in "${RESULTS[@]}"; do
    IFS='|' read -r g s st d <<<"$row"
    local color=32
    [ "$st" = "FAIL" ] && color=31
    [ "$st" = "SKIP" ] && color=33
    printf '%-6s %-40s \033[%sm%-6s\033[0m %s\n' "$g" "$s" "$color" "$st" "$d"
  done
  echo "===================================================="
  printf 'PASS=%d  FAIL=%d  (skipped scenarios are pending their milestone)\n' "$PASS" "$FAIL"
}
