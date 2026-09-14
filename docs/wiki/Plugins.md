# Plugins (external connectors & runtimes)

conductor has one idea for extending its capabilities: **there is no "built-in"
vs "plugin" — there are only plugins.** Some are **bundled** (they ship in the
binary and run in-process — github, slack, rest, cron, …; paseo, acp, opencode,
agent-deck) and some are **external** (a subprocess binary the daemon fetches
and runs out-of-process). One registry, one config surface, one
`conductor plugin list` / `show`.

And there is one field: **`use:`**. It names what implements a connector or a
runtime, and it is the *only* thing you write. Adding a plugin should feel like
adding a browser extension — you name it, it arrives, it stays current.

> **An external plugin is code the daemon executes.** A connector plugin also
> *receives your credentials* (it makes the API call); a runtime plugin
> *executes your agents*. Read [Security](#security) before adding a
> third-party plugin.

## `use:`

```yaml
connectors:
  gh:      { use: github, app_id: "${GH_APP_ID}" }   # bundled
  alerts:  { use: sentry, listen: ":9099" }          # official plugin repo
  tickets:
    use: acme/plugins/jira                            # an explicit repo
    api_key: ${JIRA_TOKEN}

runtimes:
  local: { use: paseo, default: true }
  modal: { use: modal }                               # a runtime plugin

x-templates:
  deployer: &deployer { type: agent, runtime: modal }
```

That is the whole surface. There is no `plugins:` block, no `source:`, no
`kind:`, and no `type:` — `use:` replaced all four.

### How a reference resolves

One search path, shared by `connectors:` and `runtimes:`. **First match wins.**

| You write | It resolves to |
|---|---|
| `use: github` | a **builtin** — compiled into the daemon |
| `use: sentry` | not builtin → the **official repo**, `NodeSpy/conductor-plugins`, at `connectors/sentry` |
| `use: acme/plugins/jira` | an explicit **github** repo (github.com is implied) |
| `use: git.corp.example/team/p//jira` | an explicit **non-github** host |
| `use: ./bin/conductor-jira` | a **local** binary, for developing one |

Two rules make this unambiguous:

- **Builtin beats official.** `use: github` is always the in-binary connector;
  it never reaches for the plugin repo.
- **A first path segment containing a `.` is a hostname.** That is what
  separates `git.corp.example/team/repo//jira` from `acme/repo/jira`.

The `//` component separator still reads (it is what the old `source:` field
wrote), but it is no longer required: after `owner/repo`, everything left is the
component. `acme/repo//jira` and `acme/repo/jira` parse identically.

### The kind is derived, never written

The kind is **the block the reference appears in**: `connectors:` means
connector, `runtimes:` means runtime. You never hand-author it, and it is
enforced twice —

1. **At load.** `connectors: { x: { use: paseo } }` is a config error, because
   `paseo` is a builtin *runtime*.
2. **Against the plugin itself.** The kind the plugin reports in its own
   `describe` must match the block it was referenced from, checked at install
   and again before it is registered.

**A connector can never be wired as a runtime.** A runtime executes your agents;
accepting one where you asked for a connector would silently escalate what you
agreed to.

(A plugin built against an older SDK reports no kind at all. That is treated as
*unspecified* and trusted to its block, rather than refused — an additive wire
change should not break working plugins.)

### Versions

Leave the version off and the plugin **stays current**: it tracks the newest
compatible release, and every move is logged with the sha it came from.

```yaml
use: sentry            # stay current (the default)
use: sentry@^1.2       # stay current within a range
use: sentry@v1.2.3     # PIN — this exact build, no auto-update
```

An exact `major.minor.patch` is the opt-out. A range (`^1.2`, `~> 1.4`) still
tracks, using the same resolver packs use.

A builtin has no version to pin, and a local binary is whatever is on disk —
both **refuse** an `@version` rather than ignoring it.

## Install state is local

`conductor init` installs everything the config references. Where it goes:

```
~/.local/state/conductor/plugins/
  installed.yaml                       # the record
  connectors/sentry/conductor-sentry_linux_amd64
  runtimes/modal/conductor-modal_linux_amd64
```

**This is not a committed lockfile, and that is deliberate.** A pack is config,
and config belongs in the repo. A plugin is an *installed binary*, and which
binary is installed is a property of *this machine* — the same way an extension
is installed in your browser, not in your project.

`installed.yaml` records, per plugin: the `use:` reference as written, the
resolved release tag, the verified sha, the binary path, and the plugin's
**permission manifest** (below).

What follows from that:

- **Boot is offline.** Nothing on the hot path touches the network.
- **A fetch happens only for a genuine gap** — referenced, not installed.
- **A network failure degrades, it does not fail.** conductor keeps running the
  build it already has, logs why, and retries on the next cycle.
- Every install and update is **logged with the sha it moved from**, so a
  surprise change is visible rather than silent.

### Trust

The official repo (`github.com/NodeSpy/conductor-plugins`) is in the **default**
allowlist: installing an official plugin needs no ceremony. Anything else remote
needs an explicit entry, or a one-off `--allow-unlisted`:

```yaml
plugin_trust:
  allow: [github.com/acme/*]
```

"No policy configured" does not mean "any repo on the internet is fine" — a
plugin is a binary conductor executes.

The globs match exactly as [[Packs#writing-the-globs|`pack_trust`]] does: `*`
stays inside one path segment and never crosses a `/`, so `github.com/acme/*`
means *any repo under acme* and cannot reach `github.com/acme-evil/…`.

## Commands

```
conductor init                     # install everything the config references
conductor plugin list              # every connector + runtime, with ORIGIN and kind
conductor plugin list --caps       # …and each one's permission manifest
conductor plugin show <name>       # one implementation's full surface
conductor plugin add <ref>         # install, show the permissions, print the stub
conductor plugin update [name]     # bump everything unpinned, or just one
conductor plugin remove <name>     # drop it from install state and delete the binary
conductor connectors ls            # each instance's resolved use:/origin
```

`plugin list` never executes anything: it shows install state plus a
verify-before-execute health check. `plugin show` spawns and describes a
connector plugin to print its real contract.

`plugin add` deliberately **prints** the config stub rather than editing your
config. The config is your file; a tool that silently rewrites it is a tool you
stop trusting.

`plugin remove` does not touch your config either — the reference *is* the
declaration, so deleting it is your edit to make. It says so if you forget.

## Keeping plugins current

Unpinned plugins move when you resolve them — `conductor init`, `conductor
plugin update` — or automatically, alongside the daemon's own self-update:

```yaml
update:
  auto: true      # the daemon self-updates from its release feed
  deps: true      # ALSO keep packs: and use: plugins current each cycle (opt-in)
```

Every change is logged:

```
plugin sentry: updated connectors/sentry/v1.4.0 -> connectors/sentry/v1.5.0 (sha 9f2b1c… -> a1b2c3…); permissions: no declared capabilities
plugin jira: could not reach github.com/acme/plugins//jira (network unreachable) — keeping the installed build jira/v1.2.0 (7d3e9a…)
```

Freeze one plugin while leaving the rest current by pinning it exactly
(`use: sentry@v1.2.3`).

## Security

The default model is a **visible permission manifest with
can't-exceed-declaration** — *not* an OS jail.

That is a deliberate change of posture. conductor is a privileged app you chose
to run, and a plugin you added is one too. Pretending otherwise costs real
usability (an `isolation:` block you must author before anything works) and buys
a boundary that a determined adversary walks around anyway. What conductor owes
you instead is: *here is exactly what this can do, you saw it before you
accepted it, and it cannot exceed it.*

### The manifest

A plugin declares, in its own `describe`:

- **`egress`** — the `host:port` targets it calls
- **`commands`** — the commands it spawns, by name
- **`fs`** — the filesystem paths it needs

conductor **records** that at install (so it is known before the plugin runs on
any later boot), **surfaces** it (`plugin add`, `plugin list --caps`, `plugin
show`), and **confines the subprocess to it**.

A connector may NARROW the declaration — never widen it:

```yaml
connectors:
  tickets:
    use: acme/plugins/jira
    network: ["your-org.atlassian.net:443"]   # ⊆ what the plugin declared
```

A `network:` entry that is not covered by the plugin's declaration is a **load
error**, not a silent grant.

### What that enforces, exactly

Stated plainly, because a security claim you cannot check is worse than none:

- **Egress confinement is real.** It runs through the same egress proxy the
  `isolation:` path uses. A host outside the effective set is refused.
- **Command confinement is default-path confinement, not a jail.** The child's
  `PATH` becomes a directory holding links to exactly the declared commands, so
  a plugin reaching for an undeclared tool *by name* fails. A plugin that
  invokes an absolute path bypasses it. This has teeth against accident and
  drift, not against a determined adversary.
- **A plugin declaring "I spawn things I am not naming"** gets no `PATH`
  rewrite at all, and is shown as `commands (unnamed)`. conductor does not claim
  a confinement it is not performing.
- **The install-time `describe` runs before any manifest exists**, confined to
  nothing. That is safe because `describe` is a pure self-description — it needs
  neither network nor child processes.
- **A plugin that declares nothing** is confined to nothing beyond the scrubbed
  environment. It declared no needs; inventing an allowlist for it would break
  plugins that predate the manifest.

### What is kept, unchanged

| Guard | What it does |
|---|---|
| **Download integrity** | The fetched binary is verified against the release's published `checksums.txt`, and the verified sha is recorded. Verify-before-execute re-checks it from a safe path (no group/world-writable binary or ancestor dir) before every spawn — a runtime plugin re-verifies on *every* launch via the `plugin-exec` wrapper. |
| **Source trust** | `plugin_trust` gates where remote plugins come from. The official repo is allowed by default; anything else needs an entry. |
| **Least-privilege credentials** | A connector plugin only ever receives creds for instances of **its own** implementation, delivered per-call over the RPC transport — never in argv or env. The child inherits a minimal env allowlist, never the daemon's credential-bearing environment. `allow_secrets:` narrows further. |
| **Audit attribution** | Every credential hand-off is audited as `plugin_credential` with `plugin@version` and the secret **ref name — never the value**. |
| **Transport redaction** | Plugin stdout/stderr is scrubbed through the secret redactor. **Best-effort**: it matches known secret *values*; a plugin that transforms a credential before printing can evade it. |
| **Untrusted output** | Every response is size-bounded (a plugin cannot OOM the daemon). Responses for verbs declaring an `Outputs` schema are validated against it. A connector plugin cannot forge its identity. |
| **Supervision** | Every call has a timeout. A crashed or hung plugin degrades to "that connector is down" and never takes the daemon with it, with a restart backoff that cannot crash-loop. |

**Boot vs runtime failure.** A plugin that fails to verify or start at boot is
fail-closed — a bad sha is a security event, not a degraded-boot condition. A
plugin that crashes *after* boot degrades to "down".

### Opt-in hardening

The OS isolation layer is still there. It is now **optional**, for a locked-down
box:

```yaml
connectors:
  tickets:
    use: acme/plugins/jira
    isolation:
      mode: namespace
      network: { egress: ["your-org.atlassian.net:443"] }
```

With a block present you get process/mount/pid isolation, the daemon's
config/state/secrets masked away inside the mount namespace, and structurally
enforced egress. `namespace` is Linux-only; `container` is cross-platform
(docker/podman); `user` is weakest. See [[Isolation]].

**Runtimes are not wrapped in a heavy sandbox by default.** A runtime plugin
executes your agents, which is exactly the privilege you already granted
conductor. It *is* env-scrubbed (`sandbox.MinimalEnv`, so it does not inherit
`env:`-resolved secrets) and re-verified on every spawn, but it relies on ACP's
own supervision rather than `internal/plugin`'s crash-loop cap and size cap.
**Only run runtime plugins you fully trust.**

## The protocol

A plugin speaks newline-delimited **JSON-RPC 2.0 on stdin/stdout** — the same
transport the ACP runtime uses. stdout is the transport; logging goes to stderr.

**Connector plugin:**

- `plugin.describe → Decl` — `{protocol_version, kind, type, desc, connection,
  verbs[], events[], capabilities}`. Maps 1:1 to a connector `TypeDecl`.
- `plugin.invoke {instance, verb, options, connection} → {outputs}` — the
  `connection` map carries **only the calling instance's** resolved credentials.
- `plugin.start_source` — a source plugin emitting webhook/poll events.

See `test/plugins/acme-echo/` for a reference connector plugin, and
`github.com/NodeSpy/conductor-plugins` for production ones.

**Runtime plugin:** an ACP-speaking subprocess. conductor verifies it, then
drives it through the existing ACP controller — session create/resume, streamed
status/output, cancel/cleanup.

## Migrating from `plugins:`

`conductor config migrate` folds the old shape into the new, and the daemon runs
it automatically at boot — a deployed box crosses this change without an edit.

| Old | New |
|---|---|
| `connectors: { y: { type: github } }` | `connectors: { y: { use: github } }` |
| `plugins: { x: { source: github.com/a/b//x, kind: connector } }` + `connectors: { y: { type: x } }` | `connectors: { y: { use: a/b/x } }` |
| `plugins: { m: { source: …, kind: runtime, provides: modal } }` | `runtimes: { modal: { use: … } }` |
| `runtimes: { r: { type: paseo } }` | `runtimes: { r: { use: paseo } }` |
| `runtimes: { g: { agent: gemini } }` | `runtimes: { g: { use: acp, agent: gemini } }` |
| `plugins.<n>.version` | folded into the ref as `@<version>` |
| `plugins.<n>.isolation` | carried onto the connector/runtime entry |

Retired fields are dropped **with a note naming what replaced them**:

- `sha256` — the verified sha now lives in local install state, recorded when
  `conductor init` fetches the binary. Nothing to pin by hand.
- `allow_unverified` — a local `use: ./path` binary is verified on safe
  permissions rather than a pin (it changes on every build); a fetched one
  always carries its release sha.
- `allow_unsandboxed` — running without OS isolation is now the *default*.
- `hold` — pin an exact version instead (`use: <ref>@v1.2.3`).
- `args` — a plugin is configured over the RPC transport per instance, not by
  process arguments shared across all of them.

A `plugins:` entry nothing referenced still migrates, into an entry named after
the plugin, so nothing is silently lost.

## Not yet implemented

Documented follow-ups, not silent gaps:

- **Cryptographic signing** (cosign/Sigstore, build attestations). Checksum
  verification *is* implemented; signature verification is the next layer.
- **Discovery/search** — a central index of available plugins.
- **Multi-instance isolation**: one plugin serving several instances shares a
  process; creds are scoped per-call, but shared-process inter-instance
  hardening is a follow-up.
- **External-overrides-bundled**: a plugin may not replace a bundled
  implementation (builtin beats official by design); opt-in override is a
  follow-up.
- **Runtime plugin supervision depth**: re-verified per spawn and env-scrubbed,
  but still on ACP's supervision rather than `internal/plugin`'s.
