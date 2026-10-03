# Plugins (external connectors, runtimes & engines)

conductor has one idea for extending its capabilities: **there is no "built-in"
vs "plugin" — there are only plugins.** Some are **bundled** (they ship in the
binary and run in-process — github, slack, rest, cron, …; paseo, acp, opencode,
agent-deck) and some are **external** (a subprocess binary the daemon fetches
and runs out-of-process). One registry, one config surface, one
`conductor plugin list` / `show`.

And there is one field: **`use:`**. It names what implements a connector, a
runtime, or a **code-step engine**, and it is the *only* thing you write.
Adding a plugin should feel like adding a browser extension — you name it, it
arrives, it stays current.

> **An external plugin is code the daemon executes.** A connector plugin also
> *receives your credentials* (it makes the API call); a runtime plugin
> *executes your agents*; an engine plugin *executes your code steps* and can
> ask conductor to touch your stores on their behalf. Read
> [Security](#security) before adding a third-party plugin.

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

**Three kinds of runtime plugin, auto-detected.** A `runtimes:` plugin is driven
one of three ways, chosen from what it declares at describe time (not from config):

- a **dispatch (paseo-style) runtime** declares the agent-lifecycle verbs
  (`run`, `list_agents`, `create_worktree`, `send`, `wait`, …) — conductor drives
  it as its dispatch backend (it launches/monitors agents in *its own* daemon,
  e.g. paseo), giving it a dedicated dispatcher. It
  coexists with the builtin `use: paseo` (they don't interfere).
- an **ACP runtime** declares no such verbs — conductor speaks ACP to a fresh,
  re-verified subprocess per session (wrap a coding-agent CLI as a runtime).
- a **decision runtime** declares decision `protocols` (`system_one/v1`) and
  serves the `decide` and `models` verbs — it answers [decide steps](Decide-Steps)
  and never runs an agent. Its credentials come from the runtime's `connection:`
  block.

You don't choose; the declared verbs decide. One current limit: a dispatch-style
runtime plugin can't yet take a `host:` (it runs as a local subprocess of the
daemon) — use the builtin `use: paseo` with `host:` for a remote paseo.

A third block uses the same field without being a block at all: a **code step's
`use:`** names its engine, and a name that is not builtin is an engine plugin.

```yaml
steps:
  - { id: shape, use: cli, command: [make, test] }    # the builtin engine
  - { id: build, use: wasmtime, code: "…" }           # an ENGINE PLUGIN
```

See [Code-Steps → Engine plugins](Code-Steps#engine-plugins) for what an engine
does with a step, and [Authoring an engine plugin](#authoring-an-engine-plugin)
below for how to write one.

### How a reference resolves

One search path, shared by `connectors:`, `runtimes:` and a step's engine
`use:`. **First match wins.**

| You write | It resolves to |
|---|---|
| `use: github` | a **builtin** — compiled into the daemon |
| `use: sentry` | not builtin → the **official repo**, `NodeSpy/conductor-plugins`, at `connectors/sentry` (an engine reference looks in `engines/<name>` instead) |
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

The kind is **where the reference appears**: `connectors:` means connector,
`runtimes:` means runtime, a code step's `use:` means engine. You never
hand-author it, and it is enforced twice —

1. **At load.** `connectors: { x: { use: paseo } }` is a config error, because
   `paseo` is a builtin *runtime*.
2. **Against the plugin itself.** The kind the plugin reports in its own
   `describe` must match where it was referenced from, checked at install and
   again before it is registered.

**A connector can never be wired as a runtime**, and neither can be wired as an
engine. A runtime executes your agents; an engine executes your code steps *and*
is handed a callback into your stores. Accepting one where you asked for another
would silently escalate what you agreed to.

(A plugin built against an older SDK reports no kind at all. That is treated as
*unspecified* and trusted to its block, rather than refused — an additive wire
change should not break working plugins. An ENGINE is the one exception: it must
say `kind: engine`, because it is a new kind and there are no older engine
plugins to be compatible with.)

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

### Local builds are snapshotted

A `use: ./bin/conductor-widget` reference is not installed — it is whatever
the operator built, verified for safe permissions rather than pinned to a
release sha. It still goes through install-state's spirit, not its letter:
the moment it is RESOLVED (boot, a reload, `conductor plugin add`/`show`),
conductor hashes it once and copies it into a private, content-addressed
snapshot:

```
~/.local/state/conductor/plugins/local/
  <sha256>/widget      # 0500 — read+execute only, no write, for anyone
```

(the `local/` directory itself is `0700`). Every process that resolution
spawns — the type probe, a per-instance probe, the live subprocess, a
crash-respawn — runs from that one immutable snapshot, never the mutable
source path again, and verify-before-execute pins against the snapshot's own
sha exactly as it would a release's. Rebuilding the source has no effect on
anything already running: the new build is picked up only the NEXT time the
reference is resolved (a reload or a restart), never mid-life. A snapshot
nothing currently resolved still needs is garbage-collected on the daemon's
next boot.

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
stop trusting. It also only ever *adds*: installing one plugin never disturbs
the records of the others.

`init` and `plugin update` additionally drop install-state records nothing in
the config references any more, so `installed.yaml` does not grow forever (the
binary stays on disk — removing it is `plugin remove`'s job). An engine used
**inside a pack** counts as referenced: if a pack's own steps `run: js`, your
config keeps `engines/js` installed even though you never wrote `js` anywhere.

`plugin remove` does not touch your config either — the reference *is* the
declaration, so deleting it is your edit to make. It says so if you forget.

## Keeping plugins current

Unpinned plugins move when you resolve them — `conductor init`, `conductor
plugin update` — or automatically, alongside the daemon's own self-update:

```yaml
update:
  auto: true       # the daemon self-updates from its release feed
                   # deps follows auto: packs: and use: plugins are kept current on the same cycle
# update: { auto: true, deps: false }  # keep the binary current but freeze deps to explicit updates
```

`deps` defaults to whatever `auto` is: turning on unattended binary updates opts
you into unattended dependency updates too. Set `deps: false` to move plugins and
packs only on an explicit `conductor init` / `plugin update` / `pack update`.

**A moved plugin is applied by a hot-reload — no daemon restart — when it can be.**
The daemon swaps the plugin's subprocess in place (draining in-flight calls first)
whenever the new build's interface is unchanged (same verbs, ABI, kind, and
permissions). It falls back to a full restart when the interface changed, a
**pack** moved, or the plugin can't be swapped live (a source connector, or an
ACP runtime plugin) — so a reload is never less safe than the restart it replaces.
`update.reload` defaults to follow `deps`; set `reload: false` to force
restart-always (the fleet kill-switch).

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

### Multi-instance isolation

Configure the same plugin more than once —

```yaml
connectors:
  gh:       { use: github, app_id: "${GH_APP_ID}" }
  ghlisten: { use: github, app_id: "${GH_APP_ID_2}" }
```

— and by default each configured instance gets its **own subprocess**: its
own OS-level sandbox (when `isolation:` is set), its own scrubbed/granted
environment, and its own staging directory. `gh` and `ghlisten` above are two
separate `github` plugin processes, not one process serving both. That means:

- a crash, a hang, or a crash-loop in one instance's process never touches a
  sibling instance — `gh` going down does not take `ghlisten` with it;
- `host.state`, `host.auth` and `host.log` are naturally scoped to the
  process that calls them, on top of the existing per-call instance check —
  one instance's process cannot even ADDRESS a sibling's state or managed
  token, let alone read it;
- a hot reload (a moved plugin binary) swaps every configured instance's
  process, one at a time;
- `conductor connectors ls` and the daemon log show each instance's own pid,
  so two instances of one plugin are visibly two processes.

The one process-level resource conductor shares regardless: the type-level
`plugin.describe` probe (no instance) that runs once at load/install, to
learn what the BINARY declares — a throwaway process, closed the moment it
answers, since nothing instance-specific lives in it.

**This costs memory and file descriptors**: N configured instances of one
plugin is N processes. An operator running many instances of the same plugin
who wants the old, pre-isolation behavior back — one process for all of
them — opts out explicitly, on any instance:

```yaml
connectors:
  gh:       { use: github, app_id: "${GH_APP_ID}", shared_process: true }
  ghlisten: { use: github, app_id: "${GH_APP_ID_2}" }   # shares gh's process too
```

`shared_process: true` on ANY instance of a plugin shares the WHOLE plugin's
process — it is a property of the binary conductor spawns, not of one
`connectors:` entry — so set it once, on any instance, and every instance of
that plugin shares it. With it set, isolation between instances is back to
scoping by call only (credentials, `host.state`/`host.auth`/`host.log`'s
per-instance checks), exactly as every plugin behaved before this existed.

An **in-process builtin** (cron, rss, webhook, rest, graphql, the exposure
connectors) is unaffected either way: it is trusted code served over an
in-memory pipe, not a subprocess, so there is nothing to isolate by spawning
more of it — every instance of a builtin type keeps sharing the one
in-process client it always has.

A **runtime** or **engine** plugin has no "several configured instances of
one plugin" shape to isolate in the first place: a `runtimes:` entry already
gets its own process (it is keyed by the `runtimes:` map name, not shared
with another entry that happens to reference the same binary), and a
code-step engine's one process is deliberately shared by every step that
names it — a step is not a connector instance with its own credentials or
sandbox to separate.

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
- **An ENGINE that declares nothing is confined to NO egress** — the opposite
  default, deliberately. An engine's job is to execute your code against
  conductor's data plane; "and reach the internet too" is a thing it should have
  to say out loud. There are no engine plugins predating the manifest, so there
  is no field to break. (Same mechanism, same limits: it confines a cooperating
  client, not a determined one. An `isolation:` block is what makes it a wall.)

### What is kept, unchanged

| Guard | What it does |
|---|---|
| **Download integrity** | The fetched binary is verified against the release's published `checksums.txt`, and the verified sha is recorded. Verify-before-execute re-checks it from a safe path (no group/world-writable binary or ancestor dir) before every spawn — a runtime plugin re-verifies on *every* launch via the `plugin-exec` wrapper. |
| **Source trust** | `plugin_trust` gates where remote plugins come from. The official repo is allowed by default; anything else needs an entry. |
| **Least-privilege credentials** | A connector plugin only ever receives creds for instances of **its own** implementation, delivered per-call over the RPC transport — never in argv or env. The child inherits a minimal env allowlist, never the daemon's credential-bearing environment. `allow_secrets:` narrows further. A plugin whose platform CLI reads a token from the environment declares the variable (`capabilities.env`); it is passed only if the connector also grants it (`allow_env: [GH_TOKEN]`, within the declaration). |
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
- `plugin.describe {instance, config} → Decl` (optional) — the SAME method,
  called once per CONFIGURED instance with that instance's own connection
  config, for a plugin whose verbs/events depend on it (rest/graphql's
  user-declared verbs; webhook's one concrete event per configured source).
  A plugin that does not implement this answers method-not-found, and its
  type-level `Decl` stands for every instance. The returned `Decl` must be a
  *refinement* of the type-level one: a verb present in both must keep
  identical semantics, a brand-new verb may carry none at all, and
  connection-level semantics/capabilities must match exactly — only events
  are free to vary. See docs/design/plugin-contract.md §1.4 for the exact
  rule; the host refuses an instance whose declaration does not refine.
- `plugin.invoke {instance, verb, options, connection} → {outputs}` — the
  `connection` map carries **only the calling instance's** resolved credentials.
- `plugin.start_source` — a source plugin emitting webhook/poll events.

A verb declaring `exposes` (it makes a local address reachable from outside —
a tunnel or a relay) **must also declare `host_only: true`**, the same rule a
`mints_credential` verb follows: the host refuses a declaration that doesn't.
Without it, a flow step or an agent could invoke the verb directly with an
arbitrary local address and tunnel any local service to the public internet.
The bundled `lan`/`tunnel` exposure builtins already declare `host_only`.

See `test/plugins/acme-echo/` for a reference connector plugin, and
`github.com/NodeSpy/conductor-plugins` for production ones.

**Source extension (connector `abi: 1`).** A source plugin that reports
`abi: 1` becomes a full event source rather than a payload forwarder — the
surface the github connector needs to run out of process with the builtin's
behavior ([design](https://github.com/NodeSpy/conductor/blob/main/docs/design/plugin-source-abi.md)):

- `plugin.start_source` also carries the instance's **triggers** — id, name,
  event, options, and the `filter:` in structural form (`pkg/sourcekit.Filter`)
  — and the plugin evaluates them itself (its own match keys, identity gates);
- each `plugin.event` may name the **trigger** it fired for (that trigger alone
  fires, and the daemon does not re-evaluate its filter), mark itself
  **catch-up** (sweep-recovered), name its **instance**, and **claim** its
  target is the platform's;
- `plugin.nudge` (run the catch-up sweep now — SIGUSR1, `conductor sweep
  --now`), `plugin.force` (`conductor force`), `plugin.app_token` (re-mint on
  resume), `plugin.target_head` (run facts);
- event declarations may carry `facts` / `match_keys` — the unified `filter:`
  surface, validated at load exactly as a bundled connector's is;
- `sweep` is a **conductor-defined verb**: declared by an `abi: 1` plugin, the
  daemon answers it itself (daemon-wide nudge) and never forwards it.

There are no tiers: every plugin speaks the same contract, an optional method
it does not implement answers method-not-found, and what the engine does with
an event comes from the semantics the plugin DECLARES for it — never its name
(docs/design/plugin-contract.md). Trust is decided once, at install
(`plugin_trust:`); after that every plugin is equal.

**Runtime plugin:** an ACP-speaking subprocess. conductor verifies it, then
drives it through the existing ACP controller — session create/resume, streamed
status/output, cancel/cleanup.

**Decision runtime plugin:** a runtime whose `Decl` sets `protocols:
["system_one/v1"]` and declares two verbs, both over `plugin.invoke`:

- `decide {protocol, model, state, questions} → {answers, model, usage}` — the
  `system_one/v1` request body plus the protocol name. `answers` is the v1
  answers object; conductor validates every answer against the questions before
  anything reads it. `model` is the model that answered (it may resolve an alias).
- `models → {models: [{id, name, released}]}` — the roster fleets resolve
  against.

The `connection` map is the runtime's resolved `connection:` block. See
`test/plugins/acme-decider/` for a reference decision runtime.

**Engine plugin:**

- `plugin.run {instance, run_id, code, args, env, inputs} → {outputs}` — one
  code step, out of process.
- `host.kv` / `host.sql` / `host.memory` — the plugin→daemon direction, valid
  only while one of its own `plugin.run` calls is in flight.

### Versioning: `protocol_version` never moves, `abi` does

`protocol_version` is **1**, and the daemon compares it for *exact equality*.
Bumping it would refuse every plugin already installed — including ones a newer
daemon understands perfectly — so it does not move for an addition.

New surface negotiates through a separate `Decl.abi` field instead:

```jsonc
// a connector built before engines existed — unchanged, and still accepted
{"protocol_version": 1, "type": "acme-echo", "verbs": [...]}

// an engine
{"protocol_version": 1, "kind": "engine", "abi": 1, "type": "wasmtime"}
```

`abi` is **absent/zero on every existing plugin**, and the daemon reads it per
kind: for `kind: engine` it selects the `plugin.run` / `host.*` shape; for a
connector, `abi: 1` opts into the source extension above. A runtime that sets
it is describing something nobody asks about. That is the whole negotiation, and it is deliberately boring:
a new field whose zero value means "the old thing" cannot break an old plugin,
because an old plugin never emits it and the daemon never requires it.

### The host callbacks

Until engines, traffic was one-way: the daemon called the plugin, and a *source*
plugin sent one-way event notifications back. An engine adds the missing
direction — a **request the plugin issues and the daemon answers**, multiplexed
on the same stdio.

```jsonc
--> {"id":1,"method":"plugin.run","params":{"run_id":"9f3c…","code":"…","inputs":{…}}}
<-- {"id":"h1","method":"host.kv","params":{"run_id":"9f3c…","kind":"kv","op":"get",
                                            "resource":"cache","args":["run","attempts"]}}
--> {"id":"h1","result":{"ok":true,"value":3}}
<-- {"id":1,"result":{"outputs":{"attempts":3}}}
```

- **`run_id` is a capability, not a name.** conductor mints 32 random bytes per
  run, hands them over in that run's `plugin.run`, and answers a `host.*` request
  only while that run is in flight — checked in constant time, revoked the
  instant the run returns. A stale token, a guessed token, or a token from
  another run is refused before any policy is consulted. It is the plugin wire's
  spelling of `CONDUCTOR_CTX_TOKEN` (the `cli` engine's socket), with the same
  rules: do not log it, do not persist it.
- **A connector plugin gets nothing RUN-SCOPED from this.** It is never given
  a `plugin.run`, so it holds no `run_id`, so every `host.kv`/`host.sql`/
  `host.memory` call it could make is refused. It DOES get two
  INSTANCE-scoped callbacks with no `run_id` at all: `host.state` (durable
  key/value storage for a source to remember across restarts) and `host.auth`
  (a polled source's live managed-OAuth2 token — see [[Authoring-Connectors]]
  § Sources). Both are scoped to "an instance this plugin was actually
  handed," checked the same way a `run_id` is, just without one.
- **The method is the kind.** A `host.kv` request whose body claims `sql` is
  refused rather than reconciled.
- **Refusals are in-band.** `{"ok":false,"refused":true,"error":"…"}` means
  *conductor will not let this step do that*; a plain `{"ok":false,"error":"…"}`
  means the op failed. JSON-RPC errors are kept for the transport's own problems
  (unknown method, params that will not decode).
- **Enforcement is host-side, always.** `host.*` lands in the same handler, with
  the same guard, that `ctx.store`/`ctx.sql`/`ctx.memory` go through for a
  `use: cli` step. The engine never receives a store handle, a connection string,
  or a capability — only the ability to ask, one op at a time.

### Authoring an engine plugin

The SDK gives you `EngineFunc` (the mirror of `ConnectorFunc`) and a typed
`*Host`, so you write ops rather than JSON-RPC:

```go
package main

import (
	"context"
	"github.com/NodeSpy/conductor/pkg/plugin"
)

func main() {
	plugin.Serve(plugin.EngineFunc(
		func() plugin.Decl {
			return plugin.Decl{
				Kind: plugin.KindStep,   // wire value "engine"
				ABI:  plugin.EngineABI,
				Type: "wasmtime",        // must equal the name the step uses
				Capabilities: plugin.Capabilities{}, // declare egress if you need it
			}
		},
		func(ctx context.Context, req plugin.RunRequest, host *plugin.Host) (plugin.RunResult, error) {
			n, err := host.KV().Get(ctx, "cache", "run", "attempts")
			if plugin.IsRefused(err) {
				// conductor's policy said no — surface it, do not retry
				return plugin.RunResult{}, err
			}
			return plugin.RunResult{Outputs: map[string]any{
				"attempts": n,
				"saw":      req.Inputs["repo"],
			}}, nil
		},
	))
}
```

Points worth knowing:

- **`Type` must equal the engine name the step writes.** conductor refuses a
  plugin that claims a different one (identity anti-forgery), the same rule a
  connector's `type:` follows.
- **`host` is per run.** It stops answering when `Run` returns; do not stash it.
  `host.Available()` is false when the run was granted no data plane — behave
  like a remote `use: cli` step rather than failing.
- **`Run` may be called concurrently**, once per step in flight.
- **`env:` is your step configuration**, delivered per-call over the transport.
  An engine's own process environment is the scrubbed minimal one every plugin
  gets; a step's `env:` never joins it.
- **Declare egress if you make network calls.** An engine that declares none is
  confined to *none* — deny-by-default, unlike a connector (see
  [the manifest](#the-manifest)).

See `test/plugins/acme-engine/` for a complete reference engine, including both
denial paths.

## Migrating from `plugins:`

The legacy schema is gone from this release, and so is `conductor config
migrate`. Run `conductor config migrate` on the previous release to fold the
old shape into the new, then upgrade (design doc §5). The table records what
that migration did:

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
- `allow_unverified` — a local `use: ./path` binary is snapshotted by content
  hash the moment it is resolved (boot, reload, `plugin add`/`show`) into a
  private, content-addressed copy, and THAT sha is what every verify-before-
  execute check pins against from then on — the same guarantee a fetched
  release's sha gives, just re-established on every resolution instead of
  once at install. A rebuild is picked up only on the NEXT resolution (a
  reload or restart), never mid-life, which is what closes the gap an
  unpinned raw path left open: every separate verify-then-exec of it (the
  type probe, a per-instance probe, the live spawn, a crash-respawn) could
  otherwise each see different bytes if a rebuild landed in between.
- `allow_unsandboxed` — running without OS isolation is now the *default*.
- `hold` — pin an exact version instead (`use: <ref>@v1.2.3`).
- `args` — a plugin is configured over the RPC transport per instance, not by
  process arguments shared across all of them.

A `plugins:` entry nothing referenced was migrated too, into an entry named
after the plugin, so nothing was silently lost.

## Not yet implemented

Documented follow-ups, not silent gaps:

- **Cryptographic signing** (cosign/Sigstore, build attestations). Checksum
  verification *is* implemented; signature verification is the next layer.
- **Discovery/search** — a central index of available plugins.
- **External-overrides-bundled**: a plugin may not replace a bundled connector
  type (the vendor-neutral builtins: cron, rss, webhook, rest, graphql, lan,
  tunnel, and the data/flow connectors). Registering one is refused. Vendor
  connectors are not bundled at all — `use: github` resolves to the plugin.
- **Runtime plugin supervision depth**: re-verified per spawn and env-scrubbed,
  but still on ACP's supervision rather than `internal/plugin`'s.
