#!/usr/bin/env bash
# conductor-action entrypoint: translate the action's inputs into one
# `conductor once` invocation, and let its exit code be the step's.
#
# Positional args come from action.yml's args:, in order:
#   $1 config  $2 trigger  $3 event  $4 event-name  $5 fail-on  $6 require-match
#
# Empty event / event-name fall through to $GITHUB_EVENT_PATH and
# $GITHUB_EVENT_NAME, which Actions sets for every job — the normal case.
set -euo pipefail

config="${1:-.conductor/config.yaml}"
trigger="${2:-}"
event="${3:-}"
event_name="${4:-}"
fail_on="${5:-step-error,gate-reject}"
require_match="${6:-false}"

if [[ -z "$trigger" ]]; then
  echo "conductor-action: the 'trigger' input is required — name the trigger to run" >&2
  exit 1
fi
if [[ ! -f "$config" ]]; then
  echo "conductor-action: no config at $config (did you run actions/checkout first?)" >&2
  exit 1
fi

# The write identity. A config whose identity.write_token reads GITHUB_TOKEN
# resolves it from here. App-less is the natural mode in a runner: the event
# arrived through Actions, not over a webhook, so there is nothing to verify
# and no installation to mint a token for.
export GH_TOKEN="${GH_TOKEN:-${GITHUB_TOKEN:-}}"

# Install whatever the config references and the image did not bake in (packs,
# connector plugins, engine plugins). A no-op when everything is present;
# a genuine gap surfaces as the run's own error rather than here.
if ! conductor init --config "$config"; then
  echo "conductor-action: init failed — continuing; a missing plugin will fail the run with its own message" >&2
fi

args=(once "$trigger" --config "$config" --fail-on "$fail_on")
if [[ -n "$event" ]]; then
  args+=(--event "$event")
fi
if [[ -n "$event_name" ]]; then
  args+=(--event-name "$event_name")
fi
if [[ "$require_match" == "true" ]]; then
  args+=(--require-match)
fi

exec conductor "${args[@]}"
