# `use:` unification — one field for plugins, connectors, and runtimes

Status: design, being implemented on `feat/use-unification`.
Supersedes the config surface introduced by #54 (`plugins:`) and #59 (remote fetch
+ lockfile) for the connector/runtime plugin path. Packs (#53) are unchanged.

## The problem

Wiring an external capability into conductor currently costs four fields spread
across two blocks:

```yaml
plugins:
  linear:
    source: github.com/acme/conductor-plugins//linear   # where the code is
    kind: connector                                     # what it is
    version: "~> 1.2"
    isolation: { mode: namespace, network: { egress: ["api.linear.app:443"] } }
connectors:
  linear:
    type: linear                                        # which plugin, again
    api_key: ${LINEAR_KEY}
```

The operator says "linear" four times, hand-authors `kind:` (which the plugin
already knows about itself), and must author an `isolation:` block or the plugin
refuses to launch (`internal/plugin/spawn.go`, deny-by-default §8.3). A bundled
connector uses a *different* field for the same idea (`type: github`), so the
two are not interchangeable even though both answer "what implements this?".

## The goal

Adding a plugin should feel like adding a browser or VS Code extension:

- **Declare it once.** One field, `use:`, in the block where it is used.
- **It stays current.** Reference a name, get the latest compatible build.
- **Install state is local.** Not a committed lockfile, not ceremony — the same
  way an extension is installed on *your* machine, not in *your repo*.
- **Security is a visible permission manifest**, not an OS jail. Conductor is a
  privileged app the user chose to run. A plugin they add is too. The honest
  security property is *"here is exactly what this can do, and it cannot exceed
  it"* — not *"we have contained arbitrary code"*.

---

## A. Config surface

`use:` replaces the `plugins:` block, `type:` on connectors, and `type:` on
runtimes. `source:` and `kind:` disappear entirely.

```yaml
connectors:
  gh:      { use: github, app_id: "${GH_APP_ID}", ... }   # builtin
  linear:                                                  # official plugin repo
    use: linear
    api_key: ${LINEAR_KEY}
    network: ["api.linear.app:443"]                        # declared egress
  jira:    { use: acme/conductor-plugins/jira, ... }       # explicit repo

runtimes:
  paseo:  { use: paseo, bin: paseo, default: true }
  gemini: { use: acp, agent: gemini }
  modal:  { use: modal }                                   # official plugin repo

agents:
  reviewer: { runtime: gemini }
```

Most configs declare nothing: the default runtime is still `paseo`, and the
bundled connectors are still bundled.

### What is removed

| Removed | Replaced by |
| --- | --- |
| `plugins:` block | nothing — the reference *is* the declaration |
| `connectors.<n>.type` | `connectors.<n>.use` |
| `runtimes.<n>.type` | `runtimes.<n>.use` |
| `plugins.<n>.source` | the `use:` ref itself |
| `plugins.<n>.kind` | the block the ref appears in, cross-checked against `describe` |
| `plugins.<n>.provides` | the map key |

`type:` elsewhere is **untouched**: `stores:`, `vaults:`, `memory:`, step
`type:`, and connector `auth: { type: oauth2 }` all keep it. This change is
scoped to the two blocks that name an *implementation*.

### What is added

- `connectors.<n>.network: [host:port, ...]` — the instance's declared egress.
- `connectors.<n>.use` / `runtimes.<n>.use` — the reference.
- Version suffix on the ref: `use: linear@^1.2`.

### Back-compat

Old configs do **not** silently degrade. They are handled by migration
(Part F), which runs automatically at boot (`autoMigrateOnBoot`, before
`config.Load`), and by the existing degraded-boot fail-safe
(`holdDegradedUntilLoadable`) if migration cannot fix them. A config-incompat
that hard-crashed would crash-loop an auto-updating fleet, so:

- `connectors:` decodes through `ConnectorRef.UnmarshalYAML`, which is
  header-lenient. A stale `type:` is captured privately and turned into a
  *targeted* validation error naming `conductor config migrate` — never an
  opaque "unknown field".
- `runtimes:` is strict-decoded. A stale `type:` is a decode error, which the
  boot pipeline resolves by migrating first and holding degraded otherwise.

---

## B. `use:` resolution

One kind-aware search path. First match wins. `kind` is the block the ref
appears in: `connectors:` → connector, `runtimes:` → runtime.

| # | Shape | Resolves to |
| --- | --- | --- |
| 1 | bare name, is a builtin of this kind | **builtin** — in-binary implementation |
| 2 | bare name, not builtin | **official** — `NodeSpy/conductor-plugins`, component `<kind>s/<name>` |
| 3 | `owner/repo` or `owner/repo/comp` | explicit **github** repo (github.com assumed) |
| 4 | `host.tld/…//comp`, `https://…` | explicit **non-github** host |
| 5 | `./p`, `../p`, `/p`, `~/p` | **local** executable (development) |

Precedence: builtin beats official on a name clash; an explicit path beats a
bare name (it is not a bare name, so it never enters cases 1–2).

Builtin connectors: `github`, `slack`, `cron`, `webhook`, `rss`, `kv`, `sql`,
`rest`, `graphql`, `web`, `command`, `conductor`, `discord`, `blob`, `memory`,
`workflow`, … — i.e. whatever `connector.Types()` reports, which is the live
registry, not a hand-maintained list.
Builtin runtimes: `paseo`, `acp`, `opencode`, `agent-deck`, `cli`.

### Disambiguation rule

The first path segment decides host-vs-owner: **a first segment containing a
`.` is a hostname**, otherwise github.com is implied.

- `github.com/acme/repo//linear` → strip `github.com/`, case 3.
- `git.corp.example/team/repo//linear` → case 4.
- `acme/repo/linear` → case 3.

The `//` component separator is accepted (it is what #59 wrote) but is no longer
required: after the first two segments, everything remaining is the component.
Both `acme/repo//linear` and `acme/repo/linear` parse identically.

### Version suffix

`use: linear@^1.2` — the constraint is fed to the existing
`config.BestMatch(tags, prefix, constraint)` semver selector. Absent = track
latest compatible. `@v1.2.3` (an exact version) is the opt-in pin: it disables
stay-current for that entry.

### Kind derivation and enforcement

The kind is **never hand-authored**. It is derived, and enforced in two layers:

1. **Static (load time).** A bare name that is a builtin of the *other* kind is
   a load error: `connectors: { x: { use: paseo } }` → "paseo is a builtin
   runtime, not a connector".
2. **Dynamic (install/start time).** `Decl.Kind` from the plugin's own
   `describe` must match the block it was referenced from. A mismatch refuses
   registration. **A connector can never be wired as a runtime.**

`Decl.Kind` does not exist on the wire today (`pkg/plugin/wire.go` has the
`Kind` type but `Decl` carries no field). It is added — additively, so an older
plugin reporting no kind is treated as "unspecified" and trusted to its block
rather than refused.

---

## C. Behavior: app-extension, declaratively reconciled

Same model packs already use: **the config is the desired set**, a resolve step
reconciles reality to it.

### Install state is local

Install state moves out of the committed lockfile and into the state dir
(`config.StateDir()`, e.g. `~/.local/state/conductor`):

```
~/.local/state/conductor/plugins/
  installed.yaml                  # the install state
  connectors/linear/conductor-linear_linux_amd64
  runtimes/modal/conductor-modal_linux_amd64
```

`installed.yaml` records, per `<kind>/<name>`: the `use:` ref as written, the
resolved release tag, the verified sha256, the vendored binary path, and the
**permission manifest recorded at install** (Part D).

This is a *reframing* of #59's `conductor.lock.yaml` `plugins:` section for the
connector/runtime path. Packs keep their committed lockfile — a pack is config,
and config belongs in the repo. A plugin is an *installed binary*, and which
binary is installed on this box is a property of this box.

Consequences:

- Boot loads **offline** from install state. No network on the hot path.
- A fetch happens only for a genuine gap: referenced, not installed.
- Every install and update is **logged**, with the sha, so a surprise change on
  update is visible in the log rather than silent.
- On network failure, conductor **runs what is installed** and retries. It never
  hard-crashes — the degraded-boot fail-safe already covers the config half of
  this; the plugin half degrades the same way.

### Stay-current

Default is stay-current: an unpinned entry is re-resolved by `conductor init`,
by `conductor plugin update`, and by the auto-update cycle when
`update: { deps: true }` is set. An exact `@v1.2.3` pin opts out. `hold: true`
from #59 is subsumed by the exact pin and is dropped.

> Scope note: this change does **not** flip the default of `update.deps`.
> Flipping a daemon-wide auto-update default is a behavior change to the engine,
> and this is a config-surface redesign. Stay-current describes what happens
> *when* resolution runs.

---

## D. Security: a permission manifest, not a jail

The current model is deny-by-default OS confinement: an external plugin without
an `isolation:` block **refuses to launch** (`spawn.go`), and egress is deny-all
unless proxied through an allowlist. That is the right model for running code you
do not trust. It is the wrong model for an extension the operator deliberately
installed, and it is the single biggest reason adding a plugin does not feel like
adding an extension.

The default becomes a **visible permission manifest with can't-exceed-declaration**:

1. **The plugin declares.** `describe` returns `Capabilities`:
   - `egress: [host:port]` — network hosts it calls,
   - `commands: [name]` — commands it spawns (new; `spawns bool` is retained
     and derived),
   - `fs: [path]` — filesystem paths it needs.
2. **Conductor records** the manifest into install state at install time, so it
   is known *before* the plugin runs on any subsequent boot.
3. **Conductor surfaces** it — at `conductor plugin add`, in `plugin list
   --caps` / `plugin show`, and in `connectors ls`. This is what the user
   accepts when they add the plugin.
4. **Conductor confines to it, and only it.**
   - Egress: the effective allowlist is the connector instance's `network:` when
     set, else the plugin's declared `egress`. The instance's `network:` **may
     not exceed** the plugin's declaration — every entry must match a declared
     pattern, or it is a load error.
   - Commands: the spawn's `PATH` is set to a per-plugin directory containing
     links only to the declared commands.

### What this does and does not enforce

Honesty matters more here than a strong-sounding claim:

- The egress confinement is **real** — it runs through the existing proxy, the
  same mechanism `isolation.network.egress` uses.
- The `PATH` confinement is **default-path confinement, not a jail**. A plugin
  that calls an absolute path bypasses it. It is a manifest with teeth against
  accident and drift, not against a determined adversary.
- The plugin's **first** spawn — the install-time `describe` — happens before a
  manifest exists. That spawn is confined to *nothing* (no declared commands, no
  egress), which is safe because `describe` is a pure self-description.
- Anything stronger is the opt-in hardening path below.

### Kept, unchanged

- **Download integrity.** The fetched binary is verified against the release's
  published `checksums.txt`, and the verified sha is remembered in install
  state, so a surprise change on update is visible.
- **Source trust.** `plugin_trust.allow` gates remote sources. The official repo
  (`github.com/NodeSpy/conductor-plugins`) is in the **default** allowlist;
  a third-party repo needs an explicit entry.
- **Verify-before-execute.** The sha is checked from a safe path before exec,
  and re-checked on every spawn for runtimes via `conductor plugin-exec`.

### Demoted to opt-in

The mandatory `isolation:` / namespace / deny-all-egress machinery is **not
deleted**. It becomes optional hardening for a locked-down box:

```yaml
connectors:
  jira:
    use: acme/conductor-plugins/jira
    isolation: { mode: namespace, network: { egress: ["corp.atlassian.net:443"] } }
```

`AllowUnsandboxed` disappears as a *requirement* — absence of `isolation:` is
now the normal case, not a refusal. Runtimes are **not** wrapped in a heavy
sandbox by default: a runtime executes your agents, which is the privilege you
already granted conductor.

---

## E. CLI

| Command | Behavior |
| --- | --- |
| `conductor init` | resolves **all** referenced plugins at once (with packs) |
| `conductor plugin list` | every connector + runtime, with resolved origin and kind |
| `conductor plugin update [name]` | bumps all unpinned entries, or one named |
| `conductor plugin add <ref>` | sugar: surface the manifest, append a stub, resolve |
| `conductor connectors ls` | each instance's resolved `use:` / origin / kind |
| `conductor plugin show <name>` | full decl + permission manifest |

Per-plugin `install` is dropped as the primary flow — `init` and `add` cover it.

---

## F. Migration

`conductor config migrate` (and the automatic boot migration) folds the old shape
into the new. It is a raw-node pass in the style of the existing
`applyNotifyPass` / `applyVaultsPass`, so it does not depend on the removed
struct fields.

| Old | New |
| --- | --- |
| `connectors: { y: { type: github } }` | `connectors: { y: { use: github } }` |
| `plugins: { x: { source: github.com/a/b//x, kind: connector } }` + `connectors: { y: { type: x } }` | `connectors: { y: { use: a/b/x } }` |
| `plugins: { x: { source: ./p, kind: connector, sha256: … } }` + `connectors: { y: { type: x } }` | `connectors: { y: { use: ./p } }` |
| `plugins: { m: { source: …, kind: runtime, provides: modal } }` | `runtimes: { modal: { use: <ref> } }` |
| `runtimes: { r: { type: paseo } }` | `runtimes: { r: { use: paseo } }` |
| `runtimes: { g: { agent: gemini } }` | `runtimes: { g: { use: acp, agent: gemini } }` |
| `plugins.<n>.isolation` | carried onto the connector/runtime entry |
| `plugins.<n>.version` | folded into the ref as `@<version>` |
| `plugins.<n>.hold: true` | dropped, with a note (pin exactly instead) |

The existing lenient-migration posture is preserved: a field the new schema has
no home for is dropped **with a note**, never a hard refusal, because a hard
refusal crash-loops a deployed box on auto-update.

A `plugins:` entry that nothing references still migrates — to a `connectors:`
or `runtimes:` entry named after the plugin — so nothing is silently lost.

---

## G. `conductor-plugins` repo layout

Sources move from `cmd/conductor-<name>/` to `<kind>/<name>/`, matching the
resolver's official-repo path:

```
connectors/github/     connectors/sentry/     connectors/pagerduty/
runtimes/paseo/
```

Release tags become `<kind>/<name>/vX.Y.Z` (`connectors/sentry/v1.0.0`,
`runtimes/paseo/v1.0.0`), and conductor's official-repo resolver prefix-matches
`<kind>/<name>/` instead of `<name>/`. Assets stay
`conductor-<name>_<goos>_<goarch>` plus `checksums.txt`.

The internal-free gate is unchanged and still enforced:
`go list -deps ./... | grep NodeSpy/conductor/internal` must be empty.

---

## Increments

1. This document.
2. Config schema: `use:` / `network:` on connectors and runtimes; `plugins:`,
   `type:`, `source:`, `kind:` removed; load + validate.
3. `use:` resolver: builtin → official → explicit; kind-aware; shorthand; kind
   derived and enforced.
4. App-extension behavior: local install state, stay-current, boot reconcile +
   log + degrade-safe, `init` resolves all.
5. Security: permission-manifest confinement and surfacing; kind enforcement;
   heavy isolation demoted to opt-in.
6. CLI: `init`, `plugin list|update|add|show`, `connectors ls`.
7. Migration + tests.
8. `conductor-plugins` restructure by kind, CI, e2e, README; conductor's
   `<kind>/<name>/` tag prefix.
9. Docs: `config.example.yaml`, wiki (Plugins / Connectors),
   `config.example.legacy.yaml` round-trip.

## Gates

Both repos: `gofmt -l .` empty, `go vet ./...` clean, `go build ./...`,
`go test ./...`. conductor-plugins additionally:
`go list -deps ./... | grep NodeSpy/conductor/internal` empty. The daemon must
still boot and the bundled connectors/runtimes must still work — this is a
config-surface redesign, not a behavior change to the engine or dispatch.
