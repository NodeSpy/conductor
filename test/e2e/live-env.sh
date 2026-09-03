#!/usr/bin/env bash
# Live-mode provider secrets, read from the OPERATOR'S OWN host config at launch so
# `make e2e-live` is turnkey and NOTHING secret is committed to the repo. Sourced by
# run.sh on the host in live mode (MODE=live). Best-effort: a missing source just
# leaves that agent's row to fail with a clear message in the matrix.
#
# Exports (consumed by docker-compose.live.yml):
#   PC_LIVE_ANTHROPIC_BASE_URL / PC_LIVE_ANTHROPIC_API_KEY  → claude via TeamClaude
#   PC_LIVE_GEMINI_API_KEY                                   → gemini (api-key auth)
#   HOST_UID / HOST_GID                                      → build the container
#     user to match the host, so the owner-only credential mounts are readable.

export HOST_UID="${HOST_UID:-$(id -u)}"
export HOST_GID="${HOST_GID:-$(id -g)}"

# --- claude: the host TeamClaude proxy (base URL + key) --------------------------
# The proxy listens on 0.0.0.0:<port> (reachable from the container as
# host.docker.internal); non-loopback callers must present the proxy apiKey.
_tc_json="${TEAMCLAUDE_JSON:-$HOME/.config/teamclaude.json}"
if [ -z "${PC_LIVE_ANTHROPIC_API_KEY:-}" ] && [ -f "$_tc_json" ] && command -v node >/dev/null 2>&1; then
  _tc=$(node -e '
    try {
      const d = require(process.argv[1]);
      const p = d.proxy || {};
      if (p.apiKey && p.port) console.log(p.port + " " + p.apiKey);
    } catch (e) {}
  ' "$_tc_json" 2>/dev/null) || _tc=""
  if [ -n "$_tc" ]; then
    export PC_LIVE_ANTHROPIC_BASE_URL="http://host.docker.internal:${_tc%% *}"
    export PC_LIVE_ANTHROPIC_API_KEY="${_tc##* }"
    echo "live-env: claude → TeamClaude proxy at ${PC_LIVE_ANTHROPIC_BASE_URL}"
  fi
fi
if [ -z "${PC_LIVE_ANTHROPIC_API_KEY:-}" ]; then
  echo "live-env: WARN no TeamClaude proxy key found; cli:claude-code will likely fail." \
       "Start the proxy (teamclaude) or set PC_LIVE_ANTHROPIC_API_KEY / PC_LIVE_ANTHROPIC_BASE_URL." >&2
fi

# --- gemini: api key from the encrypted FileKeychain -----------------------------
# gemini stores its api key AES-256-GCM in ~/.gemini/gemini-credentials.json, keyed
# by scrypt("gemini-cli-oauth", "<hostname>-<user>-gemini-cli"). Decrypt it here on
# the host (same identity that wrote it) and hand gemini a plain GEMINI_API_KEY.
_gem_creds="${GEMINI_CREDS:-$HOME/.gemini/gemini-credentials.json}"
if [ -z "${PC_LIVE_GEMINI_API_KEY:-}" ] && [ -f "$_gem_creds" ] && command -v node >/dev/null 2>&1; then
  _gk=$(node -e '
    const os = require("os"), crypto = require("crypto"), fs = require("fs");
    try {
      const key = crypto.scryptSync("gemini-cli-oauth", `${os.hostname()}-${os.userInfo().username}-gemini-cli`, 32);
      const [iv, tag, ct] = fs.readFileSync(process.argv[1], "utf8").trim().split(":");
      const d = crypto.createDecipheriv("aes-256-gcm", key, Buffer.from(iv, "hex"));
      d.setAuthTag(Buffer.from(tag, "hex"));
      const o = JSON.parse(d.update(ct, "hex", "utf8") + d.final("utf8"));
      // FileKeychain stores the api key nested: gemini-cli-api-key → <server> →
      // JSON string {serverName, token:{accessToken,...}}. Dig it out, tolerating
      // flatter shapes from other versions.
      let v = "";
      const svc = o["gemini-cli-api-key"];
      if (typeof svc === "string") v = svc;
      else if (svc && typeof svc === "object") {
        const first = Object.values(svc)[0];
        const entry = typeof first === "string" ? JSON.parse(first) : first;
        v = (entry && entry.token && entry.token.accessToken) || entry || "";
      }
      v = v || o.apiKey || o.key || "";
      if (typeof v === "string" && v) process.stdout.write(v);
    } catch (e) {}
  ' "$_gem_creds" 2>/dev/null) || _gk=""
  [ -n "$_gk" ] && export PC_LIVE_GEMINI_API_KEY="$_gk" \
    && echo "live-env: gemini → api key from keychain (${#PC_LIVE_GEMINI_API_KEY} chars)"
fi
# Fall back to an already-exported GEMINI_API_KEY (operator override).
if [ -z "${PC_LIVE_GEMINI_API_KEY:-}" ] && [ -n "${GEMINI_API_KEY:-}" ]; then
  export PC_LIVE_GEMINI_API_KEY="$GEMINI_API_KEY"
fi
if [ -z "${PC_LIVE_GEMINI_API_KEY:-}" ]; then
  echo "live-env: WARN no gemini api key found; acp:gemini will likely fail." \
       "Set GEMINI_API_KEY or ensure ~/.gemini/gemini-credentials.json is present." >&2
fi
