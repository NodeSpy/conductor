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
| **Transport redaction** | The plugin's stdout/stderr is scrubbed through the secret redactor before it reaches any log or the audit trail. |
| **Untrusted output** | Every response is **size-bounded** (a plugin can't OOM the daemon) and **schema-validated** against the plugin's declared verb outputs. A connector plugin **cannot forge its identity** — its declared type must match the name you configured. |
| **Enforced sandbox** | With an `isolation:` block the subprocess is wrapped through conductor's isolation layer: process/mount/pid isolation, the daemon's config/state/secret env masked away, and **deny-by-default egress** (only hosts you list under `network.egress` are reachable). See [[Isolation]]. |
| **Supervision** | Every call has a timeout. A crashed or hung plugin degrades to "that connector/runtime is down" and **never takes the daemon with it**, with a restart backoff that cannot crash-loop. |

**Boot vs runtime failure.** A plugin that fails to *verify* or *start* at boot
is **fail-closed** — the daemon refuses to start (a bad SHA is a security event,
not a degraded-boot condition). A plugin that crashes *after* boot degrades
gracefully to "down".

**Disclosure.** `conductor plugin show` prints, before you rely on a plugin,
that a connector plugin receives your credentials / a runtime plugin executes
your agents, and the capabilities it declares vs what your `isolation:` grants.

### Runtime plugins are less isolated than connector plugins (read this)

A `kind: runtime` plugin reuses conductor's existing ACP runtime path, which does
**not** yet get the full connector-plugin guard set:

- **It inherits the daemon's environment.** The ACP spawn seeds the child with
  the daemon's full `os.Environ()` — which can carry `env:`-resolved secrets.
  The `isolation:` block masks *paths* and *network*, not *env vars*. A runtime
  plugin should be treated as receiving the daemon's environment.
- **Verification is at boot, and the binary is re-spawned per session** with no
  re-verification — a lifetime-long TOCTOU window if the on-disk binary is
  swapped after boot.
- **It bypasses `internal/plugin`'s supervision** (crash-loop cap, size cap,
  stderr redaction) and relies on ACP's own handling.

**Only run runtime plugins you fully trust.** Per-spawn re-verification and an
env-scrubbing ACP path are the immediate follow-ups. Connector plugins get the
full guard set (env allowlist, per-call creds, redaction, size cap, supervision).

### Sandbox caveats (read these)

- OS-level confinement depends on the `isolation:` mode. `namespace` mode is a
  real boundary but **Linux-only** (it uses `unshare`/`systemd-run`); `container`
  mode is cross-platform (docker/podman); `user` mode is weakest. **Without an
  `isolation:` block a plugin runs unsandboxed** (same uid as the daemon, able
  to read its files) — the daemon logs a prominent warning. Always add an
  `isolation:` block for third-party plugins.
- Resource caps (CPU/mem/pids) come from cgroups in `namespace` mode
  (`systemd-run`) or engine flags in `container` mode.

## Not yet implemented

This release lands a coherent, tested core with the security guards real, not
stubbed. The following are **documented follow-ups**, not silent gaps:

- **Remote sources**: fetch from a URL, a lockfile pinning binaries by SHA per
  OS/arch, discovery/search, and `conductor plugin add`/`update`. Today
  `source:` is a local path and the SHA pin is authored by hand.
- **Cryptographic signing** (cosign/Sigstore or build attestations). SHA-256
  pinning *is* implemented; signature verification is the next layer.
- **Connector source/event streaming** (`StartSource`) — a plugin *emitting*
  webhook/poll events. Connector plugins are verb-only for now.
- **Runtime dispatch depth**: a live ACP reference runtime + per-spawn
  re-verification (verification is currently at boot) and mid-session
  crash/restart supervision beyond what ACP already provides.
- **Multi-instance isolation**: one plugin serving several instances shares a
  process; creds are scoped per-call, but shared-process inter-instance
  hardening is a follow-up.
- **External-overrides-bundled**: a plugin may not replace a bundled type/
  runtime (safe default); opt-in override is a follow-up.

See issue #54 for the full epic.
