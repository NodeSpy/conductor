# Plugins (external connectors, runtimes & engines)

conductor has one idea for extending its capabilities: **there is no "built-in"
vs "plugin" — there are only plugins.** Some are **bundled** (they ship in the
binary and run in-process — blob, command, conductor, cron, graphql, handoff,
kv, lan, memory, rest, rss, sql, step, tunnel, web, webhook, workflow; paseo,
acp, opencode, agent-deck) and some are **external** (a subprocess binary the
daemon fetches and runs out-of-process — github, slack, sentry, and anything
else in the official or a third-party plugin repo; see `conductor plugin
list` for the exact, current bundled set). One registry, one config surface,
one `conductor plugin list` / `show`.

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
  gh:      { use: github, app_id: "${GH_APP_ID}" }   # official plugin repo
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
| `use: rest` | a **builtin** — compiled into the daemon |
| `use: github` / `use: sentry` | not builtin → the **official repo**, `NodeSpy/conductor-plugins`, at `connectors/github` / `connectors/sentry` (an engine reference looks in `engines/<name>` instead) |
| `use: acme/plugins/jira` | an explicit **github** repo (github.com is implied) |
| `use: git.corp.example/team/p//jira` | an explicit **non-github** host |
| `use: ./bin/conductor-jira` | a **local** binary, for developing one |

Two rules make this unambiguous:

- **Builtin beats official.** `use: rest` is always the in-binary connector;
  it never reaches for the plugin repo — a name that is actually compiled in
  always wins over a same-named official plugin. `github` and `slack` are
  **not** builtin (vendor connectors never shipped in the binary; see
  [Not yet implemented](#not-yet-implemented)) — `use: github` always
  resolves to the official plugin.
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

**Pre-releases are opt-in.** A pre-release tag (`v1.2.3-rc.1`) is selected
only by a reference that names one, e.g. `use: github@1.2.3-rc.1`. An
unpinned or ranged reference never resolves to a pre-release, and neither
does the daemon's own self-update. A box moves only to a strictly newer
stable release, never to an older tag.

**Ordering:** among tags of the identical `major.minor.patch`, a release
always outranks a pre-release (`v1.2.3` beats `v1.2.3-alpha`). Two pre-releases of the same core are ordered by semver
precedence (`v1.2.3-alpha` < `v1.2.3-beta`); beyond that — a genuine tie, or
a tag that doesn't parse as semver at all — the comparison falls back to a
plain byte-wise compare of the full tag text: deterministic, but not a claim
that either side is actually newer.

### Side-by-side versions

Side-by-side versions are for **released versions from one remote source** —
two connectors pinning different git tags of the SAME repo/component, each
fetched, verified, and run independently. A **local** `use: ./path` build is
not eligible for this: `config.validatePluginRefs` refuses two connectors
naming the same plugin identity from two DIFFERENT sources (remote or
local) in one config, and for a local reference its "source" is the path
itself — two local binaries at two different paths, even built from the
same code, can never be told apart from two genuinely different
implementations the way two tags of one remote repo can. A local build is
therefore **one path per plugin name**: there is no "pin local build A
alongside local build B" the way there is for a remote plugin's versions.
What you get instead is time, not space — rebuild the binary at that one
path and the NEW bytes are picked up on the next reload or restart (see
"Local builds are snapshotted" below), never mid-life, and never two builds
running at once.

Two connectors can pin **different** versions of the same plugin:

```yaml
connectors:
  gh-stable: { use: github@v1.4.0 }
  gh-canary: { use: github@v2.0.0-rc1 }
```

Each pin resolves, is installed, and runs **independently** — `gh-stable` is
served by the v1.4.0 binary's own process, `gh-canary` by v2.0.0-rc1's, and
each sees only its own version's declared verbs/events/schema. Nothing
silently "picks one for both": that used to be exactly the bug here — a
plugin was keyed by name alone, so the first connector `conductor` happened
to resolve decided the version *every* connector of that name got, no matter
what the others asked for.

**Reconcile resolves EACH configured instance against its OWN `use:`
constraint, always — never a representative instance's.** This matters past
the first install, too: picture `gh-stable` pinned at `@v1.0.0` and
`gh-canary` left unpinned (stay-current), both presently sitting on
`v1.0.0` — they share one process (below). When `v2.0.0` is published,
`gh-canary` moves to it; `gh-stable`'s own pin is unaffected and **stays
on `v1.0.0`, still installed, still running** — never uninstalled out from
under it just because a sibling instance that happened to share its process
moved on. The two are now two independent groups, each with its own
process, exactly as if they had been pinned to different versions from the
start. The set of installed versions garbage-collected at the end of a pass
is *every version some currently-configured instance resolves to*, computed
after every instance's own resolution — never a version a pinned instance
still needs, and never driven by whichever instance a stale, pre-resolution
grouping happened to treat as representative. Resolving every instance on
its own never means a duplicate fetch: two instances whose constraint TEXT
differs but who land on the identical release are downloaded and described
exactly once per pass, not once per instance.

**The split is by resolved version, not by the `use:` text you wrote.** Two
instances whose constraints resolve to the *same* concrete build still share
one process, exactly like two non-isolated instances of a single-version
plugin always have (see [Multi-instance isolation](#multi-instance-isolation)
below) — `use: github` (stay current) on one connector and `use: github@^1`
on another land in the same process the moment they both resolve to, say,
`v1.4.0`. `isolate: true` still applies *within* whichever group an instance
lands in.

A local `use: ./path` build is split by its content-addressed snapshot sha
(see "Local builds are snapshotted" below): two connectors pointed at the
same bytes share a process; different bytes — even under the same declared
name — never do.

**`single_process` plugins are the one exception, and conductor never
refuses BOOT over it.** A plugin that declares `capabilities.single_process`
keeps some box-global resource only one process can hold (the running
example throughout this doc is a tailscale funnel lease) — running two
resolved versions of it side by side would mean two processes each
believing they alone own that resource. Since which versions are currently
resolved can change with no config edit (an unpinned instance's own
auto-update), this is caught and refused at `validate`/pre-update-apply
time (see "Multi-instance isolation" below for the full write-up), and
degrades — disables every connector using either version, never the rest of
the daemon — if it is ever discovered only at boot:

```
plugin tailscale: connector(s) gh-stable, gh-canary: plugin tailscale declares single_process — it keeps
one box-global resource that only ONE process can own, so two different resolved versions of it
cannot run side by side; pin every connector using it to the SAME version, or see
docs/wiki/Plugins.md "Side-by-side versions" — its connectors are disabled
```

The fix is always the same: pin every connector that uses a `single_process`
plugin to the one version you want it running.

## Install state is local

`conductor init` installs everything the config references. Where it goes:

```
~/.local/state/conductor/plugins/
  installed.yaml                       # the record
  connectors/sentry/v1.4.0/conductor-sentry_linux_amd64
  connectors/sentry/v2.0.0-rc1/conductor-sentry_linux_amd64   # side by side, when two versions are pinned
  runtimes/modal/v1.0.0/conductor-modal_linux_amd64
```

**This is not a committed lockfile, and that is deliberate.** A pack is config,
and config belongs in the repo. A plugin is an *installed binary*, and which
binary is installed is a property of *this machine* — the same way an extension
is installed in your browser, not in your project.

`installed.yaml` records, per **(plugin, resolved version)** pair: the `use:`
reference as written, the resolved release tag, the verified sha, the binary
path, and the plugin's **declaration** (see [Security](#security)). A plugin referenced at
only one version — the overwhelmingly common case — has exactly one record,
exactly as it always has; a plugin pinned at two versions side by side (above)
has two, each with its own subdirectory so the two binaries never collide on
disk. An `installed.yaml` written before side-by-side versions existed loads
unchanged — the format didn't move, only the rule that at most one record
exists per plugin NAME did.

What follows from that:

- **Boot is offline.** Nothing on the hot path touches the network.
- **A fetch happens only for a genuine gap** — referenced, not installed.
- **A network failure degrades, it does not fail.** conductor keeps running the
  build it already has, logs why, and retries on the next cycle.
- Every install and update is **logged with the sha it moved from**, so a
  surprise change is visible rather than silent.
- **A version nothing references any more is garbage-collected** the same
  pass that drops a whole unreferenced plugin — if you repin every connector
  of a plugin off `v1.4.0` onto `v2.0.0`, the next `conductor init` /
  `plugin update` uninstalls `v1.4.0`'s binary once nothing resolves to it,
  while a version still in use (by this plugin or any other) is left running.
- **A cross-process advisory lock** (an flock on a lockfile next to
  `installed.yaml`, best-effort — a no-op on a platform without flock)
  guards the whole load-mutate-save cycle, so a `conductor plugin update`
  you run by hand and the daemon's own auto-update cycle — two separate OS
  processes, each otherwise unaware of the other — never race a write and
  silently lose one side's change.

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

**The binary's file name must equal its declared type.** `use:
./bin/conductor-widget` registers as `widget` (the `conductor-` prefix is
stripped) — not whatever the binary's own `describe` claims to be. If the
plugin describes itself as a *different* type than the name the path implies,
it is refused as identity forgery, the same refusal a remote plugin gets for
claiming a type other than the one it was fetched to provide: the type a
connector serves is the operator's configured name, never whatever the
binary says about itself. There is no `conductor plugin update` for a local
reference to fall back on here — fix it by renaming the binary to the type
it declares, or by pointing `use:` at a file already named after its type.

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
conductor plugin list --caps       # …and what each one declares
conductor plugin show <name>       # one implementation's full surface
conductor plugin add <ref>         # install, show the permissions you agree to, print the stub
conductor plugin update [name]     # bump everything unpinned, or just one
conductor plugin update <name> --force  # take a re-published release (see below)
conductor plugin approve [name]    # list the updates waiting for approval, or apply one
conductor plugin decline <name>    # keep the running build instead of its held update
conductor plugin remove <name>     # drop it from install state and delete the binary
conductor connectors ls            # each instance's resolved use:/origin
```

`plugin list` never executes anything: it shows install state plus a
verify-before-execute health check. `plugin show` spawns and describes a
connector plugin to print its real contract.

`conductor schema <type>` for a bare, unconfigured type name shows every
resolved-version group's declaration when more than one is configured side
by side — each under its own `--- <type>: version N of M (<group>) ---`
header — rather than silently whichever one happened to register first.
For a specific, configured connector instance it always resolves that
instance's own bound group, never an arbitrary sibling's.

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

**An unattended update that doesn't load is never left on disk.** The cycle resolves
packs into a staging dir under `.conductor/`, loads your config against it, and swaps
it in only when that load succeeds. A refused update (a pack release that needs a
newer connector, say) leaves `.conductor/packs/` and `conductor.lock.yaml` exactly as
they were, so a later restart boots the last config that loaded.

**Plugins and packs move together.** The cycle refreshes plugins first, then loads
the staged packs against the plugins it just installed. So a pack release that needs
a newer plugin is taken in the same cycle as that plugin. When the newest packs still
don't load, each pack takes its newest release that does, within its `version:`
constraint and never below the one it's on, and the log says which pack was held
back and why. A pack that can't move never holds back the plugins or the other packs.

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

A plugin says up front what it needs, and runs **jailed to exactly that**.
You see the list before you agree to it. Anything the plugin did not declare
is denied, and the plugin fails with an error naming what it reached for.

conductor never guesses a plugin's needs. It does not read meaning into
config key names, add hosts for you, or keep per-plugin exceptions. If a
plugin needs something, the plugin declares it.

### What a plugin declares

A plugin's `plugin.describe` carries a `capabilities` block:

| Field | What it grants | Forms |
|---|---|---|
| `egress` | network destinations | `host:port` (host may glob, `*.example.com:443`; a bare host means `:443`), or `config:<field>[:<port>]` |
| `listen` | addresses it serves on | `[host]:port`, or `config:<field>` |
| `read` | files and directories it reads | `/abs`, `~/rel`, `env:VAR[/rel]`, `config:<field>[/rel]`, `command:<name>[/rel]` |
| `write` | files and directories it reads and writes | same forms as `read` |
| `commands` | programs it runs, by name | `gh`, `git` |
| `exec` | files and directories whose programs it (or a command) runs beyond `commands`: a tool's helper programs | same forms as `read`, e.g. `command:git/../../lib/git-core` |
| `env` | environment variables it reads | `GH_TOKEN` |
| `keychain` | (macOS) a login stored in the user's Keychain | `true` |
| `keyring` | logins in your OS keyring that it (or a command it runs) reads, each named by the attributes it is stored under | `[{label: "gh login for github.com", attributes: {service: "gh:github.com"}}]` |
| `expose_local` | (an exposure) connecting to the listeners conductor wires to it | `true` |

#### Keyring logins

A CLI such as `gh` or `glab` keeps your login in the OS keyring: on Linux
the Secret Service (GNOME Keyring, KWallet, KeePassXC) over the desktop
session's D-Bus bus, on macOS the Keychain. A plugin that runs such a CLI
declares each login it reads under `keyring`:

```json
"keyring": [{"label": "gh login for github.com", "attributes": {"service": "gh:github.com"}}]
```

An item matches when it carries every declared attribute with the same
value; `{"service": "gh:github.com"}` covers each account gh keeps for
github.com. Each item names at least one attribute that says whose item it
is: `service`, `server`, `host`, `domain`, `url`, `origin` or
`application`. Attributes many programs' items share (`xdg:schema`,
`protocol`, `port`, a user name) cannot make up a declaration on their own,
since they would read all of those programs' items. A label is a few words
without control or formatting characters.

One attribute value per item may name the instance's own config or a
declared variable, as a path or destination can:

| In a value | Is |
|---|---|
| `${config:<field>}` | the connection field's value (a list gives an item per entry) |
| `${env:<VAR>}` | the daemon's variable (one the plugin declares in `env`) |
| `${host:config:<field>}`, `${host:env:<VAR>}` | that value's host: a URL's host name, `host:port`'s host |

So `{"service": "gh:${host:config:api_base}"}` is gh's login for the host
of the instance's `api_base`. A field or variable that is not set gives no
item. A value with no host, or with a control character, fails the launch
naming the field. As with `config:` destinations, the field may not be a
secret one, and the variable may not look like a credential (`…TOKEN`,
`…PASSWORD`, `…API_KEY`, …). A variable your entry's `allow_env` leaves out
gives no item. You are shown the reference (`service=gh:<host of api_base
from its config>`). `conductor plugin show` lists the items it resolves to
on this machine and how many items in your keyring each matches now. A
conductor that does not resolve references is not sent such an item. You
are shown what is enforced, every item with those attributes, and the
plugin's label in its own words:
`reads every item in your keyring with service=gh:github.com ("gh login for github.com", in its words)`.

- **Linux.** The jail never gets your session bus: it reaches every program
  in your desktop session, and the Secret Service has no per-item access
  control, so even a bus filtered to the Secret Service would expose every
  secret you keep. Instead the jail's only session bus is a socket conductor
  serves outside it (`DBUS_SESSION_BUS_ADDRESS` points there). It answers as
  a read-only Secret Service whose only items are your keyring items that
  match a declared one. Each lookup is made in your keyring at that moment
  (a login gh refreshes is read as it is now), with the declared attributes
  added to the CLI's search, so your keyring is never searched for anything
  else. An item is checked against the declaration again before its secret is
  read. Nothing can be written, deleted, locked or unlocked, and no other
  service on the bus exists. Both lookups CLIs use work: go-keyring's
  (`gh`, `glab`) and libsecret's (`secret-tool`, with its encrypted session).
  Your keyring daemon serves your whole desktop, so the broker is bounded:
  at most 8 connections at a time, a message at most 64 KiB, a search at
  most 32 attributes of at most 1 KiB, the connection handshake within 10
  seconds, an idle connection closed after 5 minutes, and every plugin's
  calls to your keyring paced together (50 a second). A string your bus
  would refuse never reaches it.
- **macOS.** A declared item grants the Keychain read-only: its services,
  and `~/Library/Keychains` to read, not write (`keychain: true` grants it
  read-write). macOS cannot confine a program to some Keychain items, so
  the plugin may read any item the Keychain's own per-item access control
  lets it read, and the consent line says so:
  `reads your macOS Keychain, read-only, for every item with service=gh:github.com ("gh login for github.com", in its words): …`.
  Writes the Keychain makes through its own system service (the
  data-protection keychain) are outside what the sandbox's file rules
  stop.
- **Locked keyring.** conductor never unlocks your keyring, and never shows
  an unlock prompt, for a plugin. The CLI fails as it would with a locked
  keyring, and the log says
  `plugin github: its keyring item "gh login for github.com" is locked: unlock your keyring …`.
  Unlock it by logging in to your desktop session, or in your keyring
  manager.
- **No keyring.** On a box with no session bus (a server, a container), or
  no Secret Service on it, a lookup finds nothing, and a CLI that also keeps
  its login in a file uses that, as it would without the jail (gh's
  `hosts.yml` is read when the plugin declares its config dir). The log says
  `plugin github: its keyring items (gh login for github.com) cannot be read: no session bus: …`.
  conductor finds your session bus from `DBUS_SESSION_BUS_ADDRESS`, else
  `$XDG_RUNTIME_DIR/bus`. It never launches a bus.
- **Updates.** A release that adds a keyring item, or gives a declared
  one a new label (the label is what you agreed to), asks for a new
  permission, so it is held like any other (below) until you approve it. A build that
  an older conductor installed with the item withheld is held the same way
  on the first start after you upgrade conductor (below). On macOS, a
  build that already has `keychain: true` is not held for an item, since
  the Keychain grant already covers it. A conductor older than this does not
  grant `keyring`, so the SDK withholds it from that conductor. When you
  upgrade, the item is held for approval, not recorded silently.

A `config:<field>` entry points at a field of the plugin's **own** connection
config, and only because the plugin wrote that entry; it may not name a
secret field. conductor resolves it from each configured instance: a URL
gives its host and port (`http`/`ws` mean 80, `https`/`wss` mean 443), a
`host:port` value is used as is, and a path is used as a path. A value that
globs (`*`, `?`, `[`, `]`), has no port, or is a relative path is an error,
not a guess. A field that is not set on an instance grants nothing.

A resolved path is refused, failing the launch with the reason and the
declared entry it came from, when:

- it is `/`, your home directory, or a directory containing conductor's own
  state or config dir;
- it lies inside conductor's own state or config dir. Nothing there may be
  written or run; a file there may be read only when its path is a
  `config:` value (a GitHub App key you configured), never because the
  plugin names it;
- it is, contains, or lies inside a directory where other programs serve
  sockets: `/run`, `/var/run`, `/run/user/<uid>`, `$XDG_RUNTIME_DIR`,
  `/tmp`, `/var/tmp`, `/dev/shm`, conductor's own temp dir. A grant of a
  directory includes connecting to every socket in it (a read-only bind
  does not stop that), and a directory in one may be another program's
  (`$XDG_RUNTIME_DIR/systemd`, holding systemd's). The one thing granted
  there is the socket file a plugin needs, declared under `write`
  (`/run/docker.sock`), which consent shows as `talks to the program
  serving the socket: …`;
- it is an `exec` path that is, or contains, a directory of programs or a
  top-level system directory: the `PATH` directories, `/usr`, `/usr/lib`,
  `/usr/local`, `/etc`, `/run`, `/proc`, `/var`, `/tmp`, `/opt`, your home's
  `bin`, `.local` and `.local/bin`, and the like. A tool's own directory
  inside one (`/usr/lib/git-core`, `/usr/local/aws-cli`) is fine;
- it lies inside another path the plugin may write (it could swap what is
  there for a symlink);
- it goes through a symlink root does not own (one you made, such as a
  dotfiles-managed `~/.config/tool`): a plugin could have made it. The error
  names the symlink and what it points to; replace the symlink with what it
  points to, give the real path where the plugin's config takes one, or set
  `trust: full`.

An entry starting with `?` is optional: `?command:tool/..`. When its path is
refused it is skipped, and logged, instead of failing the launch. A path
that does not exist is skipped either way.

A declared loopback destination may not be a port conductor itself listens
on.

An exposure plugin (one with an `exposes` verb: a tunnel, a relay such as
`smee`) connects to the local address it is handed to expose. Declaring
`expose_local: true` asks for exactly that, and conductor, which does the
wiring, decides the address: each listener a configured connector exposes
through one of the plugin's instances (`expose: <instance>`), at its
`listen` address (a `0.0.0.0` or `:port` listen is reached on
`127.0.0.1`), and nothing else. Consent shows it as `connects to the local
listeners conductor exposes through it`. It is not granted, and the log
says why, for:

- a listener not on `127.0.0.1` (or `localhost`, `0.0.0.0`, `:port`);
- a port conductor itself listens on — a builtin's listener, such as the
  bundled `webhook` connector's, is one. Set `trust: full` on the exposure
  connector to relay to it (with `isolate: true` when another connector
  shares its plugin's process), or expose a plugin's listener. The smee
  rewrite (see [[Migration]]) does this for the connector it makes for a
  `webhook` connector;
- a build whose install record does not declare `expose_local` (an older
  release): update it, and approve the update.

The addresses are fixed when the daemon starts, from the config; a shared
plugin process gets those of the instances it serves.

`env:VAR` is only allowed when `VAR` is also in `env`. `command:<name>/rel`
is relative to the declared command's resolved file (after symlinks):
`command:node/..` is the directory the program is in, `command:node/../..`
the install tree of a tool that reads its own files, and
`command:git/../../lib/git-core` the helper programs `git` runs. It must
stay in the command's install tree: the directory the file is in, or, when
that is a `bin` or `sbin` directory, the prefix above it (`/usr` for
`/usr/bin/git`). A tool that may be a bundle in its own directory or a
single file in `/usr/bin` declares `?command:tool/..`: the bundle's
directory where there is one, nothing where there is not.

A declared command that is a script runs with its `#!` interpreter (and,
for `#!/usr/bin/env prog`, the `prog` it finds): both are bound with it.
A command and its interpreter are checked like a declared path: each must
be an executable file reached through symlinks root owns only, and is
refused, failing the launch with the reason, when it lies inside conductor's
own dirs, a runtime dir, or a path the plugin may write (its `write` paths,
its staging dir), where it could rewrite the `#!` line to name any path.
A command installed as a symlink you own (`~/.local/bin/tool`, as npm,
pipx and uv make them) fails the launch, naming the fix: put the directory
it points into on conductor's `PATH` ahead of it, or set `trust: full`.

The older fields still load: `fs` means `write`, and `spawns` grants nothing
(name the commands instead).

A declaration conductor cannot check is rejected when the plugin is
described: an unknown `config:` field, a relative path, a command given as a
path, a bad port, a path with a `..` component (a `command:` or `env:`
reference may have one), or any entry holding a control character, a
terminal escape, or a space other than ` ` (it would be printed at consent
as written).

### What the user sees

The declaration is recorded at install and shown, in plain words:

- by `conductor plugin add` and `conductor init`, before you agree to it;
- by `conductor plugin show` and `conductor plugin list --caps`.
  `plugin add`, `plugin show` and `plugin approve` also say what the paths
  are on this machine (`on this machine it runs programs in:
  /usr/lib/git-core`), and where the declared commands are;
- in the daemon log each time the plugin starts:
  `plugin gh: jailed to what it declares: connects to api.github.com:443, …`,
  and when its connector type registers. That line shows the **recorded**
  declaration, the one enforced. If the running build declares more than
  its record, the line adds `NOT granted (declared by this build, not in its
  install record): …`, and the build gets none of it until you approve it.

Variables that look like credentials (`*_TOKEN`, `*_SECRET*`, `*API_KEY`,
…) are called out, because agreeing to them hands the plugin their values
when they are set in conductor's environment. A plugin that declares
managed OAuth shows where conductor will send your client credentials and
tokens.

What the official plugins declare is in their own source, in
[conductor-plugins](https://github.com/NodeSpy/conductor-plugins); `conductor
plugin show <name>` prints it for the build you have.

A plugin that declares nothing reads as `nothing: …`, and gets nothing: no
network, no files outside its own binary and staging dir, no commands.

### Updates that change what a plugin may do

Installing a plugin is agreeing to what it declares: `conductor plugin add`
and `conductor init` show it. After that, every update is compared with the
build it would replace **before** it is installed:

- **The same declaration**: the update replaces the running build, as
  usual (live where it can be, see
  [Keeping plugins current](#keeping-plugins-current)).
- **A different declaration** (anything added or removed: a destination,
  path, command, exec path, variable, listen address, Keychain access,
  keyring item or OAuth host): the update is **held**. The running build keeps running
  exactly as it was, the same binary under the same install record, and its
  connectors keep working. The new build is fetched, verified and staged in
  a directory of its own, and never run until you approve it.

That is the rule for every plugin, the official repo's included, and for
every third-party source `plugin_trust` allows. A narrowing is held too: it
still changes what the plugin does, and approving it is one command.

The daemon logs

```
plugin jira: update jira/v1.4.0 -> jira/v1.5.0 HELD: it changes what the plugin may do — adds: connects to uploads.example.com:443. jira/v1.4.0 keeps running as it is; `conductor plugin approve jira@3c1f0a9e2b7d` accepts it
```

and sends it once, as a `plugin_update_held` notification (a
`conductor.escalate` event, so a trigger alerting on escalations hears it),
with the running and held versions and what it adds and removes; the audit
log records it. A restart does not send it again. A newer release held in
its place is sent anew, compared with the running build. Until you decide:

- `conductor plugin list` marks the plugin `update <version> waiting for
  approval`, with each `+` addition and `-` removal and the commands below;
- `conductor status` lists it;
- the dashboard's **Plugins & Connections** screen shows it first: running
  version → held version, what it adds and removes, an **Approve** button
  and a **Keep** button.

```bash
conductor plugin approve                     # list what waits, what each adds and removes
conductor plugin approve jira                # apply the update waiting for jira now
conductor plugin approve jira@3c1f0a9e2b7d   # apply exactly the declaration listed
conductor plugin decline jira                # keep the running build
```

Approving installs the held build in place of the running one; the daemon
picks it up at its next reload (the dashboard reloads it for you). With
dashboard sign-in on, approving there needs a passkey (or SSH) step-up
bound to the plugin, both versions and the change. `jira@3c1f0a9e2b7d`
names the declaration (a digest of what it may do), not the build: if an
update pass held a different one since you looked, it is not approved, and
`approve` says what is waiting instead. A build's sha prefix, tag or
version still names it too.

An approval is of a declaration: a newer build that declares the same as
the one you approved installs without asking. Declining keeps the running
build and stops asking about that declaration; a later release that
declares something else is held and sent again, and `approve` still takes a
declined one.

Destinations compare as the jail grants them: a bare `api.example.com` is
`api.example.com:443` on both sides, so it never covers another port. The
same comparison applies when you change a `use:` constraint or pin to a
version that is not installed yet: it is compared with the build you have.

A release whose binary changed under a tag you have installed (its asset
was re-published: a new sha for the same tag) is refused, and the installed
build keeps running:

```
plugin jira: release jira/v2.0.0 now publishes a different build (sha …) than the one installed from it (sha …): its asset was replaced after it was installed. Refused; …
```

`conductor plugin update <name> --force` takes it, compared with the build
it replaces like any update. The new bytes are staged beside the installed
ones (`<version>+<sha>`), never over them, so a held re-publish leaves the
running build intact. A pruning update pass removes such a directory once
nothing runs from it or waits in it: the replaced build's directory once
the re-publish is approved, and the re-publish's with its version.

`conductor plugin update <name>` (and `--force`) also re-describes a build
that is already current. If it now declares other than its record, that is
held the same way; the running build stays jailed to its record until you
approve.

A build installed by a conductor that did not jail plugins keeps running
as it did before the jail while an update of it is held or declined (see
[Upgrading](#upgrading)); approving the update replaces it with the new
build, jailed to the new declaration.

What waits for approval, what you declined, and whether it was announced
are kept in install state, so they survive restarts, and `conductor plugin
approve` (its own process) finds them.

### Narrowing, and opting out

Config can **narrow** what a plugin declared, never widen it:

```yaml
connectors:
  tickets:
    use: acme/plugins/jira
    network: ["your-org.atlassian.net:443"]   # within what the plugin declared
    allow_env: [JIRA_TOKEN]                    # within its declared env
```

A `network:` entry outside the declaration fails the launch with an error
(a `config:` destination covers the host that instance configures). Unset,
`network:` and `allow_env:` pass everything the plugin declares; set, they
pass only the entries listed. `allow_env: []` passes none; `network: []`
narrows nothing, as it did before the jail (the config warns):

```yaml
    allow_env: []      # no variables at all
```

Instances that share one process should narrow it the same way: a different
`network:` or `allow_env:` on two of them gives the process both, with a
warning (`isolate: true` one of them to keep them apart).

`trust: full` runs a plugin **without** its jail (environment scrubbed only),
for a plugin you trust that cannot run inside one:

```yaml
connectors:
  tunnel: { use: ngrok, trust: full }
runtimes:
  paseo:  { use: paseo, trust: full }
```

The daemon logs `trust: full — running WITHOUT its jail` each time it starts
it. Instances that share one process are unjailed only if **all** of them say
`trust: full`; a mix is a load error (`isolate: true` the one you trust).

`isolation:` on a plugin connector, runtime or engine no longer applies (the
config warns): the declaration is what confines a plugin now (see
[Upgrading](#upgrading)).
`isolation:` stays for agents and the built-in `cli`/`script` runtimes (see
[[Isolation]]).

### Upgrading

From a conductor that did not jail plugins (an upgrade never breaks a
working setup):

- **A host that cannot jail plugins keeps running the plugins it had.**
  Conductor checks once at start whether this host can build a plugin jail.
  It cannot when conductor runs as root (a user namespace maps root to root),
  inside a default Docker container (its seccomp profile blocks user
  namespaces), with `kernel.apparmor_restrict_unprivileged_userns=1` (the
  Ubuntu 23.10+ default), with user namespaces disabled, or on macOS without
  `sandbox-exec`. There, a plugin installed before the jail runs as it did
  before: unjailed, its environment scrubbed, its declared egress (a
  `config:` destination resolved to its configured host) at conductor's
  egress proxy, its `PATH` narrowed to its declared commands, and an
  `isolation:` block applied as it was. The log and a notification say
  `WARNING: this host cannot jail plugins: <why>` with the fix. A plugin
  installed since is refused there with the same reason and fix. Set
  `trust: full` on an entry to run it unjailed on purpose; when every plugin
  has it, the warning stops. Which plugins predate the jail is kept in `installed.yaml`
  (`pre_jail: true`, carried to each new build of the plugin); a state file
  a jail-era conductor rewrote without being able to describe a plugin (its
  record still `legacy: true`) counts, so a box that could not start its
  plugins after the upgrade recovers on its own.
- **A build that ran before the jail keeps running as it did, on any host.**
  Its declaration was written for a conductor that enforced none of it, so
  its record may lack something it uses (a key file it reads), and jailing
  it would stop it working. So the build a conductor without the jail
  installed (`ran_before_jail: true` in `installed.yaml`: its record is
  still in the format that conductor wrote, a version 1 record not yet
  adopted or one whose adoption could not describe the build) runs from its
  first launch after the upgrade as it did then: unjailed, its environment
  scrubbed, its egress at conductor's proxy, its `PATH` narrowed to its
  declared commands. That is decided from install state alone, before it
  starts, so it holds with dependency updates off too. The log says
  `plugin <name>: running UNJAILED, as before the plugin jail … this build ran before the jail`.
  A build a jailing conductor already recorded stays jailed, however old
  its binary. The next build of it (an update that declares the same, or
  one you approve) was installed under the jail, and runs jailed. To jail an old
  build sooner, update it: `conductor plugin update <name>`.
- **`isolation:` on plugin connectors, runtimes and engines no longer
  applies.** The config loads, with a warning naming it; delete the block.
  The plugin runs jailed to what it declares; the block is used only where
  the host cannot jail plugins (above). To narrow a plugin use `network:`
  and `allow_env:`; to run it unconfined set `trust: full`.
- **Instances sharing one process that narrow it differently** load, with a
  warning: the process gets the union of their `network:`/`allow_env:`, as
  before. Set the same narrowing on both, or `isolate: true` one of them.
- **Install records are completed on the first start.** The state file a
  conductor without the jail wrote (`installed.yaml` version 1) holds only
  part of each declaration: egress, commands, env, OAuth endpoints and
  `single_process`, but no reads, writes, listen addresses, exec paths or
  Keychain. On the first start (and on any update pass) the new conductor
  describes each installed build once. What the old record could not hold
  is recorded without asking: the build ran with it unconfined, so this
  only narrows it. What it did hold is compared: a build that now declares
  more egress, commands, env, OAuth endpoints or `single_process` is held
  like an update; the build keeps running as it did before the jail (above). The log says
  `plugin github: recorded what … declares (its install record predates declared permissions, …)`.
  Only records loaded from a version 1 file are adopted this way; the file
  is written back as version 2.
- **Records an earlier jailing conductor wrote are completed once, too.**
  The plugin SDK withholds from a conductor what it cannot grant: `config:`
  and `env:` entries from v0.88 and v0.89, `expose_local` from those and
  v0.90. Their records (`manifest_schema: 1`) lack them, so the first start
  of a conductor that grants them describes each such build once and
  records them without asking: they name your own config and the listeners
  conductor wires from it. The log says
  `plugin github: recorded what … declares that the conductor which installed it could not record: …`.
  Anything else the build declares beyond its record is held as above. That
  includes `keyring`, which the SDK withholds from every conductor that does
  not grant it. Records those conductors wrote (`manifest_schema: 1` or `2`)
  are described once on the first start, and a keyring item the build
  declares is held for `conductor plugin approve`: it is a new permission,
  not a reference to your config. A build that declares none is recorded
  without a word.
- **Rolling back.** A conductor without the jail does not read a version 2
  state file: it treats it as empty and installs every plugin again from its
  release (so the network must be up for that start), writing version 1.
  The jail keeps a copy of what you agreed to in `consented.yaml` beside the
  state file, which the older conductor does not touch. Upgrading again
  adopts the version 1 records against that copy: a build you agreed to gets
  exactly that record back (held if it now declares more), held updates the
  older conductor dropped are restored when their binary is still there,
  and a build it installed meanwhile (the newest release, which may be an
  update the jail held) is compared with what you last agreed to: when it
  declares more it is held again and logged as a `WARNING` naming what it
  adds, and the build you agreed to runs again when its binary is still
  there (otherwise the newer one runs jailed to that agreement).
- **`network: []` narrows nothing**, as before: the plugin gets every
  destination it declares. A config that has one loads, with a warning; list
  the destinations to narrow it. `allow_env: []` passes none, as it always
  did.
- **A declared command found through a symlink you own fails the launch**
  (`~/.local/bin/paseo` from npm, a pipx or uv tool). Put the directory it
  points into on conductor's `PATH` ahead of it, or set `trust: full`.
- **Variables the plugin declares now pass through by default.** Check
  conductor's environment for ones you do not want a plugin to have
  (`GH_TOKEN`, …), or narrow with `allow_env:`.
- **A root daemon cannot jail plugins** (see the first item). Run
  conductor as a non-root user, or set `trust: full` on the plugin's entry.

### Multi-instance isolation

Configure the same plugin more than once —

```yaml
connectors:
  gh:       { use: github, app_id: "${GH_APP_ID}" }
  ghlisten: { use: github, app_id: "${GH_APP_ID_2}" }
```

— and by default `gh` and `ghlisten` **share one process**: one `github`
plugin subprocess, serving both configured instances, jailed to what the
declaration resolves to for each of them (each instance's own `config:`
values), narrowed by the `network:`/`allow_env:` they share. This is the resource-frugal default — N configured instances
of one plugin is one process, not N — and it is also, honestly, a smaller
blast radius than it sounds: see "What isolation does and doesn't protect
against" below before you reach for `isolate:` as a security boundary.

Give one instance its own process with `isolate: true`:

```yaml
connectors:
  gh:       { use: github, app_id: "${GH_APP_ID}" }
  ghlisten: { use: github, app_id: "${GH_APP_ID_2}", isolate: true }
```

Now `gh` shares the (still default) process with any other non-isolated
instance of `github`, and `ghlisten` gets its own dedicated subprocess — its
own jail, its own
scrubbed/granted environment, and its own staging directory, confined to
EXACTLY `ghlisten`'s own grant, never `gh`'s and never the shared union.
`isolate: true` is a property of that one `connectors:` entry, not of the
plugin binary — set it on just the instances that need their own process.
That means, for an isolated instance:

- a crash, a hang, or a crash-loop in its process never touches the shared
  process or a sibling isolated instance;
- `host.state`, `host.auth` and `host.log` are naturally scoped to the
  process that calls them, on top of the existing per-call instance check —
  the shared process cannot even ADDRESS an isolated instance's state or
  managed token, let alone read it, and vice versa;
- its process is jailed to exactly what the declaration resolves to for
  ITS OWN config, `network:`, `allow_secrets:` and `allow_env:` — never a sibling's, and never the shared
  process's union;
- a hot reload (a moved plugin binary) swaps the shared process AND every
  isolated instance's own process, one at a time;
- the daemon log's `subprocess started (pid N)` line shows it as its own
  process, distinct from the shared one. (`conductor connectors ls` does NOT
  show this: it builds its own, throwaway, short-lived stack to describe
  each connector for display, so any pid it could show would be that one-off
  process's, not the running daemon's — it prints no pid at all, rather than
  one that looks live but isn't.)

Non-isolated instances sharing the default process get the coarser,
pre-isolation guarantee instead: isolation between THEM is scoping by call
only (credentials, `host.state`/`host.auth`/`host.log`'s per-instance
checks) — a crash in the shared process takes every non-isolated instance of
that plugin down together, and the process sees what the declaration
resolves to for every one of them (so `gh`'s process DOES see `ghlisten`'s
`config:` values, an App key path say, if `ghlisten` is not isolated —
isolate the instance that needs to be kept separate).

Non-isolated instances that share a process share one jail: the union of
what the declaration resolves to for each of them (each instance's own
`config:` values), narrowed by their `network:`/`allow_env:`. Those must be
the same on every instance sharing the process — one process cannot honour
two narrowings, and the union would widen each — so a differing pair is a
load error (`share one plugin process but narrow it differently`). The shared
process runs without a jail only if every instance sharing it says
`trust: full`; a mix is a load error. `isolate: true` the instance that
should be different.

**The one process-level resource conductor always isolates**: the
type-level `plugin.describe` probe. When a connector key has a shared
process at all, that process describes ITSELF — there is nothing
instance-specific in a type-level describe, so the one process that is
going to run anyway is the right one to ask. Only a connector whose EVERY
configured instance isolates has no shared process to ask; for that one case
a throwaway process (closed the moment it answers) learns the plugin's
declared surface instead, in an empty jail — no network, secrets, env,
commands or files — since a pure self-description needs none of it.

**Some plugins cannot be split into more than one process at all, and say
so.** A plugin that keeps a BOX-GLOBAL resource every configured instance
must agree on — a lease refcount on a shared OS-level mapping, a listener
bound to one fixed port — declares `capabilities.single_process: true` in
its `plugin.describe` response. `isolate: true` on any instance of such a
plugin is refused, never silently folded back into the shared process — and
neither is two DIFFERENT resolved versions of it ever run side by side (see
"Side-by-side versions" above), since each would believe it alone owns that
resource.

Both conflicts are properties of the RESOLVED set (which version each
instance currently lands on, and what that version's manifest says), not
the written config — an auto-update that moves an unpinned instance onto a
release that newly declares `single_process`, colliding with a pinned
sibling or an `isolate: true` instance, can create either shape with no
config edit at all. That means boot itself never refuses outright over
one — doing so would mean a single plugin's auto-update, days after the
config was last touched, could take the ENTIRE daemon down:

- `conductor validate` and the auto-update pre-restart gate (the daemon
  checks this again right before deciding to apply a self-refreshed set of
  dependencies) both check it against what is **currently installed**,
  offline — a statically knowable problem given the box's present state —
  and refuse: `validate` fails, and an update that would newly CREATE the
  conflict is logged and simply not applied, so the daemon never restarts
  into a boot it would then have to degrade;
- the running daemon's own boot path never refuses over it. `isolate: true`
  colliding with its own group's `single_process` plugin disables just that
  one connector, loudly, with the reason — every other connector, including
  every non-isolated instance of the SAME plugin, proceeds normally. Two
  different resolved versions configured side by side has no such partial
  fix (the resource can't run twice no matter who shares which process), so
  every connector using either version is disabled instead — still never the
  rest of the daemon.

`conductor plugin show`/`plugin list --caps` label this explicitly
(`single_process: the plugin declares every instance must share one process
— isolate: true is refused for it`), so it is clear the restriction comes
from the plugin's own declaration, not a config choice. Changing whether a
plugin declares this between builds is an interface change like any other
(`SameReloadSurface`): a hot reload is refused and the daemon falls back to
a full restart, since which instances the Manager has folded into which
process cannot change under a running daemon.

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

#### What isolation does and doesn't protect against

Be honest about what `isolate: true` buys you, because a security claim you
cannot check is worse than none:

- **It is the same binary in every process**, shared or isolated. `isolate:
  true` does not run different code, verify anything differently, or trust
  the plugin any less or more — it is a resource/blast-radius knob, not a
  trust boundary around a plugin you don't trust. That is the jail (see
  [Security](#security)), which every process gets either way.
- **What it DOES contain**: a crash, a hang, or an unhandled panic triggered
  by one instance's input (a malformed webhook payload, a hostile upstream
  response) stays in that instance's own process — it does not take a
  sibling instance down with it, and it cannot address a sibling's
  `host.state`/`host.auth`/`host.log` calls, narrowing what one compromised
  or merely-buggy instance's process can reach.
- **What it does NOT contain**: a plugin binary that is itself malicious —
  not merely buggy, but intentionally hostile — gains nothing from being
  isolated INTO its own process; it is still the same trusted-to-run code,
  with the same declared manifest, just running alone instead of alongside
  a sibling. Isolating it narrows what a SEPARATE bug or exploit arriving
  via a different instance's input could reach FROM that process, which is
  a different claim from "this plugin is contained."
- **It narrows the jail.** An isolated instance's process is jailed to
  what its own config resolves to — smaller than the shared process's
  union — with the same enforcement every plugin gets.

### What the jail enforces, exactly

Stated plainly, because a security claim you cannot check is worse than
none. Each item below is asserted by `TestDeclaredJail`
(`internal/plugin/jail_integration_test.go`, with the path checks in `jail_redteam_test.go`), which runs a probe
through the real launch path.

**Linux** (unprivileged user, PID, mount, IPC and network namespaces, then
`pivot_root` into a fresh root):

- **Files.** The root holds `/usr` (read-only), a short list of `/etc`
  files (users, name resolution, time zone, TLS roots, the dynamic loader
  cache), a read-only `/dev`, `/proc`, and an empty noexec `/tmp` and
  `/dev/shm`. Not `/home`, `/opt`, `/srv`, `/var`, the rest of `/etc`, the
  daemon's config, state or secrets. Each declared `read` path is bound
  read-only and noexec, each `write` path read-write and noexec, each `exec`
  path read-only; paths that do not exist are skipped. The root itself is read-only after the pivot.
- **Commands.** The bin dirs under `/usr` (`/usr/bin`, `/usr/sbin`,
  `/usr/local/bin`, `/usr/local/sbin`, and the games dirs) are empty except for the declared commands,
  bound in by name, then made read-only. The rest of `/usr` (and a
  split-usr `/lib`, `/lib64`, `/libexec`) is `noexec`, except the top level
  of the dynamic linker's library directories (the standard ones and those
  in `ld.so.conf`), so shared libraries load but no program among them, and
  none in their subdirectories, runs: a tool's helper programs
  (`/usr/lib/git-core`), a daemon's binaries (`/usr/lib/ssh`). A program
  that runs helpers declares them in `exec` (bound read-only, executable).
  Every writable place is `noexec`, so a plugin cannot drop a program and
  run it. Read-only and `noexec` reach all the way down a bind, its
  submounts included (a writable directory with a mount inside it is
  `noexec` there too). A declared path is bound from its real location,
  never through a symlink that is not root's: it is opened without
  following any symlink (`openat2`, or one component at a time on an older
  kernel) and bound by that open file (`open_tree`, or its `/proc/self/fd`
  link), so a directory swapped for a symlink after the check is refused,
  not followed. A kernel with neither fails the launch rather than bind by
  path.
- **Network.** The network namespace has only loopback. Outbound traffic
  goes through conductor's egress proxy, which allows exactly the declared
  destinations (`HTTPS_PROXY`/`HTTP_PROXY` are set; a plugin with raw TCP
  uses `plugin.DialContext`, which tunnels through the same proxy). A
  declared loopback destination (say a local service at `127.0.0.1:8123`) is
  relayed to the host port, that port only. A declared `listen` address is
  bound on the host by the daemon and forwarded into the jail.
- **Environment.** A minimal allowlist (`PATH`, `HOME`, `USER`, `TERM`,
  `TZ`, locale, `TMPDIR` and a few more) plus the declared variables, never
  the daemon's credentials.
- **Keyring.** No session bus. With `keyring` items declared, the only
  bus is conductor's read-only Secret Service for those items
  ([Keyring logins](#keyring-logins)). Without them there is none, even if
  `DBUS_SESSION_BUS_ADDRESS` is declared in `env`.

**macOS** (Seatbelt, `sandbox-exec`, deny by default):

- **Commands.** Only the plugin binary, the declared commands and what is
  in the declared `exec` paths may be executed (for an Xcode stub in
  `/usr/bin`, also what `xcrun --find` resolves it to; a `command:` path is
  relative to that).
- **Files.** Reads of the OS dirs (`/usr/lib`, `/usr/share`, `/System`, a
  few `/etc` files) and the declared paths; writes of the declared `write`
  paths and the staging dir. Not `/opt/homebrew`, `/usr/local` or the home
  dir, unless declared.
- **Network.** Only the egress proxy's loopback port, declared loopback
  ports, declared listen ports and declared unix sockets (a `write` path).
- **Keychain.** The Keychain services and `~/Library/Keychains` only when
  `keychain: true` (read-write) or a `keyring` item (read-only) is declared.

Other operating systems have no plugin jail: a plugin there runs only with
`trust: full`.

**Describe runs in an empty jail.** The install-time `plugin.describe` (and
the first describe of a local build) runs before any declaration is known,
so it gets nothing at all. A local build is then jailed to what it declared.

**What the jail does not do.** It does not limit CPU or memory, and the
proxy allows a declared host, not particular URLs on it. A declared glob
(`*.ghe.com:443`) allows every host it matches. `noexec` stops the jail from
*running* undeclared programs; a plugin is itself code, and could still load
a program's bytes it can read into its own memory and run them, inside the
same jail and with the same reach. A plugin with `trust: full` has your
user's access, as every plugin did before the jail.

### What is kept, unchanged

| Guard | What it does |
|---|---|
| **Download integrity** | The fetched binary is verified against the release's published `checksums.txt`, and the verified sha is recorded. Verify-before-execute re-checks it from a safe path (no group/world-writable binary or ancestor dir) before every spawn — a runtime plugin re-verifies on *every* launch via the `plugin-exec` wrapper. |
| **Source trust** | `plugin_trust` gates where remote plugins come from. The official repo is allowed by default; anything else needs an entry. |
| **Least-privilege credentials** | A connector plugin only ever receives creds for instances of **its own** implementation, delivered per-call over the RPC transport — never in argv or env. `allow_secrets:` narrows further. A plugin whose platform CLI reads a token from the environment declares the variable (`capabilities.env`); `allow_env:` can narrow that. |
| **Audit attribution** | Every credential hand-off is audited as `plugin_credential` with `plugin@version` and the secret **ref name — never the value**. |
| **Transport redaction** | Plugin stdout/stderr is scrubbed through the secret redactor. **Best-effort**: it matches known secret *values*; a plugin that transforms a credential before printing can evade it. |
| **Untrusted output** | Every response is size-bounded (a plugin cannot OOM the daemon). Responses for verbs declaring an `Outputs` schema are validated against it. A connector plugin cannot forge its identity. |
| **Supervision** | Every call has a timeout: 30 s, or the verb's declared `call_timeout` (at most 10 minutes). A call that passes it fails alone; the plugin is told to cancel it and keeps running. A plugin whose process exits, closes its connection, or does not answer a liveness ping after a timed-out call is restarted, and so is one where 3 calls in a row time out, even if it answers the ping; an answered call resets the count. These restarts count toward the restart backoff, so a plugin that never finishes its calls ends up parked. A crashed or hung plugin degrades to "that connector is down" and never takes the daemon with it, with a restart backoff that cannot crash-loop. |

**Boot vs runtime failure.** A plugin that fails to verify or start at boot is
fail-closed — a bad sha is a security event, not a degraded-boot condition. A
plugin that crashes *after* boot degrades to "down".

**Which plugins this covers.** Connectors, engines, and runtime plugins
that conductor drives over the plugin protocol (Backend-RPC and decision
runtimes) are jailed to their declaration. An **ACP runtime plugin** is an
agent: conductor launches it per session through the agent controller, so it
runs under the agent sandbox (on by default; the step's `isolation:` applies),
the same as a built-in agent runtime, not under its declaration's jail.

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
- **Declare egress if you make network calls.** An engine that declares none
  gets none, like every plugin (see [What a plugin declares](#what-a-plugin-declares)).

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
