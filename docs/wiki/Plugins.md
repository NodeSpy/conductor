# Plugins (external connectors & runtimes)

conductor has one idea for extending its capabilities: **there is no "built-in"
vs "plugin" — there are only plugins.** Some are **bundled** (they ship in the
binary and run in-process — github, slack, rest, cron, …; paseo, acp, opencode,
agent-deck) and some are **external** (a subprocess binary the daemon fetches
and runs out-of-process). One registry, one config surface, one
`conductor plugin list`/`show`.

An external plugin lets you add a custom **connector** (a new `type:`) or a
custom **runtime** (a new `runtime:`) **without recompiling** conductor.

> **An external plugin is arbitrary code the daemon executes.** A connector
> plugin also *receives your credentials* (it makes the API call); a runtime
> plugin *executes your agents*. Installing one is the highest-trust action in
> conductor. Read [Security](#security) before you add a third-party plugin.

## Declaring a plugin

```yaml
plugins:
  jira:
    source: ./plugins/conductor-jira   # local executable (see "Sources" below)
    kind: connector                     # connector | runtime
    provides: jira                      # the type/runtime it registers (default: the map key)
    version: 1.4.0                      # attribution on every audit record
    sha256: 9f2b…                        # REQUIRED: verified before the binary is ever run
    isolation:                          # the sandbox grant (see Security); deny-by-default
      mode: namespace
      network: { egress: ["your-org.atlassian.net:443"] }
    allow_secrets: ["vault:house/jira"] # optional: exact-match creds this plugin may receive
```

`plugins:` only **acquires types** — it configures nothing. You then use the
type exactly like a bundled one:

```yaml
connectors:
  myjira: { type: jira, base_url: https://your-org.atlassian.net, token: ${JIRA_TOKEN} }

agents:
  deployer: { runtime: my-runtime }     # for a kind: runtime plugin
```

Existing configs are unaffected: `plugins:` is new, optional, and
strict-decode-safe. Every bundled connector/runtime keeps working unchanged.

### Sources

In this release `source:` is a **local executable path** (absolute, or relative
to the config file). Remote sources (`github.com/acme/conductor-jira@1.4.0`) with
fetch, a lockfile, and `conductor plugin add` are a documented follow-up (see
[Not yet implemented](#not-yet-implemented)).

## Inspecting plugins

```
conductor plugin list          # bundled connectors AND runtimes (tagged bundled),
                               # plus external plugins (tagged external, sha-verified)
conductor plugin show <name>   # a plugin's Decl + its capability/credential disclosure
conductor plugin remove <name> # guidance (managed remove is a follow-up)
```

`plugin list` never executes a plugin — external entries show a cheap
verify-before-execute health check. `plugin show` spawns and describes a
connector plugin to print its real contract.

## The protocol

A plugin speaks newline-delimited **JSON-RPC 2.0 on stdin/stdout** — the same
transport the ACP runtime uses (`internal/acp/jsonrpc.go`). stdout is the
transport; all logging goes to stderr.

**Connector plugin** (`internal/plugin`):

- `plugin.describe → Decl` — `{protocol_version, type, desc, connection, verbs[],
  events[], capabilities}`. Maps 1:1 to a connector `TypeDecl`.
- `plugin.invoke {instance, verb, options, connection} → {outputs}` — the
  `connection` map carries **only the calling instance's** resolved credentials.

See `test/plugins/acme-echo/` for a complete reference connector plugin, and
`test/plugins/e2e.sh` for an end-to-end demonstration.

**Runtime plugin**: an external runtime is an **ACP-speaking** subprocess (ACP,
shipped in v0.6.0, is the runtime protocol this generalizes). conductor verifies
it, then drives it through the existing ACP controller — session create/resume,
streamed status/output, cancel/cleanup.

## Security

The security model is the core of this feature. A plugin runs behind these
guards (see `internal/plugin`, `internal/connector/external.go`):

| Guard | What it does |
|---|---|
| **Verify-before-execute** | The binary's SHA-256 is checked against the `sha256:` pin — from a path with safe permissions (no world-writable binary or ancestor dir) — **before the binary is ever run**. A mismatch is a hard refusal. `allow_unverified: true` is a deliberate, insecure dev-only opt-in. |
| **Least-privilege credentials** | A connector plugin only ever receives creds for instances of **its own type**, delivered per-call over the RPC transport — never in argv or env (env is visible via `/proc/<pid>/environ`). The child inherits a minimal env allowlist, never the daemon's credential-bearing environment. An optional `allow_secrets` exact-match allowlist gates which refs may cross. |
| **Audit attribution** | Every credential hand-off is audited as `plugin_credential` with the `plugin@version` and the secret **ref name — never the value**. |
| **Transport redaction** | The plugin's stdout/stderr is scrubbed through the secret redactor before it reaches any log or the audit trail. **Best-effort:** it matches known secret *values* (and their common encodings) — a plugin that transforms a credential before printing can still evade it. Redaction reduces, but does not eliminate, leak risk; don't rely on it as the only barrier. |
| **Untrusted output** | Every response is **size-bounded** (the primary guard — a plugin can't OOM the daemon). Responses for verbs that **declare an `Outputs` schema** are additionally validated against it; verbs with dynamic/undeclared outputs are size-bounded only, so treat their output as untrusted input downstream. A connector plugin **cannot forge its identity** — its declared type must match the name you configured. |
| **Enforced sandbox (fail-closed)** | An external plugin with **no `isolation:` block is refused** (deny-by-default) unless `allow_unsandboxed: true` is explicitly set. With an `isolation:` block the subprocess is wrapped through conductor's isolation layer: process/mount/pid isolation, the daemon's config/state/secret env masked away, and **deny-by-default egress** (only hosts under `network.egress` are reachable). See [[Isolation]]. |
| **Supervision** | Every call has a timeout. A crashed or hung plugin degrades to "that connector/runtime is down" and **never takes the daemon with it**, with a restart backoff that cannot crash-loop. |

**Boot vs runtime failure.** A plugin that fails to *verify* or *start* at boot
is **fail-closed** — the daemon refuses to start (a bad SHA is a security event,
not a degraded-boot condition). A plugin that crashes *after* boot degrades
gracefully to "down".

**Disclosure.** `conductor plugin show` prints, before you rely on a plugin,
that a connector plugin receives your credentials / a runtime plugin executes
your agents, and the capabilities it declares vs what your `isolation:` grants.

### Runtime plugins are less isolated than connector plugins (read this)

A `kind: runtime` plugin reuses conductor's existing ACP runtime path. It now
gets **verify-before-execute on every spawn** (via the `plugin-exec` wrapper)
and an **env-scrubbed launch** (only `sandbox.MinimalEnv` — the daemon's
credential-bearing environment is NOT forwarded, unlike bundled ACP runtimes).
It still, unlike connector plugins:

- **bypasses `internal/plugin`'s supervision** (crash-loop cap, size cap,
  stderr redaction) and relies on ACP's own handling.

**Still: only run runtime plugins you fully trust** — a runtime plugin executes
your agents (spawns processes, runs tool calls). A live ACP reference runtime is
a follow-up.

### Sandbox caveats (read these)

- OS-level confinement depends on the `isolation:` mode. `namespace` mode is a
  real boundary but **Linux-only** (it uses `unshare`/`systemd-run`); `container`
  mode is cross-platform (docker/podman); `user` mode is weakest. **A plugin
  with no `isolation:` block is refused** unless you set `allow_unsandboxed:
  true` — an unsandboxed plugin runs same-uid and can read the daemon's files
  (config, App keys). Never opt in for a third-party plugin; add an `isolation:`
  block instead.
- Resource caps (CPU/mem/pids) come from cgroups in `namespace` mode
  (`systemd-run`) or engine flags in `container` mode.

## Not yet implemented

This release lands a coherent, tested core with the security guards real, not
stubbed. The following are **documented follow-ups**, not silent gaps:

- **Remote sources**: fetch from a URL, a lockfile pinning binaries by SHA per
  OS/arch, discovery/search, and `conductor plugin add`/`update`. Today
  `source:` is a local path and the SHA pin is authored by hand. `allow_unverified`
  (the no-pin dev opt-in) is intended only for local development; when remote
  sources land it will be refused for them (a fetched binary must be pinned).
- **Cryptographic signing** (cosign/Sigstore or build attestations). SHA-256
  pinning *is* implemented; signature verification is the next layer.
- **Connector source/event streaming** (`StartSource`) — a plugin *emitting*
  webhook/poll events. Connector plugins are verb-only for now.
- **Runtime plugin supervision depth**: runtime plugins are re-verified per
  spawn and env-scrubbed, but still rely on ACP's own supervision rather than
  `internal/plugin`'s crash-loop cap / size cap / stderr redaction. A live ACP
  reference runtime and unified supervision are the next steps.
- **Multi-instance isolation**: one plugin serving several instances shares a
  process; creds are scoped per-call, but shared-process inter-instance
  hardening is a follow-up.
- **External-overrides-bundled**: a plugin may not replace a bundled type/
  runtime (safe default); opt-in override is a follow-up.

See issue #54 for the full epic.
