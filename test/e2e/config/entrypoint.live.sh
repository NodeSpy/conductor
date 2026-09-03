#!/usr/bin/env bash
# LIVE-mode conductor entrypoint. Same git-identity setup as the hermetic
# entrypoint, plus: copy the operator's agent credentials from their READ-ONLY
# staging mounts into the container user's WRITABLE $HOME. The RO staging matters —
# codex refreshes its OAuth token in place (~/.codex/auth.json) and a read-only bind
# mount makes that write fail; copying into $HOME gives the CLIs a writable home.
#
# Provider keys arrive via the environment (compose): ANTHROPIC_BASE_URL +
# ANTHROPIC_API_KEY (host TeamClaude proxy) for claude, GEMINI_API_KEY for gemini,
# OPENAI/codex via the copied auth.json. Secrets never touch the repo — run.sh reads
# them from the operator's own host config at launch.
set -euo pipefail

CONFIG="${1:?usage: entrypoint.live.sh <config.yaml>}"

git config --global user.name "Conductor User"
git config --global user.email "conductor@users.noreply.forge.test"
git config --global --add safe.directory '*'
git config --global init.defaultBranch main

mkdir -p /data /data/fakepaseo "$HOME"

# ---- codex: OAuth tokens (chatgpt auth_mode). Copy ONLY auth.json into a writable
# ~/.codex so codex can refresh the access token in place. We deliberately skip the
# host config.toml — it pins hook-trust hashes at host paths that don't exist here;
# codex runs fine on defaults, and the controller passes
# --dangerously-bypass-approvals-and-sandbox so no project-trust prompt blocks it.
if [ -f /seed-creds/codex/auth.json ]; then
  mkdir -p "$HOME/.codex"
  cp /seed-creds/codex/auth.json "$HOME/.codex/auth.json"
  chmod 600 "$HOME/.codex/auth.json"
  echo "entrypoint.live: staged codex auth.json"
else
  echo "entrypoint.live: WARN no codex auth.json at /seed-creds/codex (cli:codex will fail)" >&2
fi

# ---- gemini: the CLI authenticates via GEMINI_API_KEY (env, from the operator's
# encrypted keychain — see run.sh). Copy the config tree so settings.json's
# selectedType=gemini-api-key is in effect and gemini starts non-interactively.
if [ -d /seed-creds/gemini ]; then
  mkdir -p "$HOME/.gemini"
  cp -r /seed-creds/gemini/. "$HOME/.gemini/" 2>/dev/null || true
  # Force api-key auth explicitly (idempotent) so a stale on-disk selection can't
  # send gemini down an interactive OAuth path in the container.
  cat > "$HOME/.gemini/settings.json" <<'JSON'
{ "security": { "auth": { "selectedType": "gemini-api-key" } } }
JSON
  echo "entrypoint.live: staged gemini config (api-key auth)"
else
  echo "entrypoint.live: WARN no gemini config at /seed-creds/gemini" >&2
fi

exec conductor run --config "$CONFIG"
