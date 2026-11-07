# Migration

## Upgrading: smee moves out of github and the webhook connector

A smee.io channel used to be dialed by whoever received the webhook: the
github plugin (`webhook.smee_url`) and the bundled `webhook` connector
(`smee_url`). It is now one thing, the `smee` **exposure connector** (a
plugin), which relays the channel to a local listener. The receiver only
listens locally and names its exposure with `expose:` — the same field any
tunnel plugin (cloudflared, ngrok, tailscale, …) plugs into:

```yaml
# before                              # after
gh:                                   gh:
  use: github                           use: github
  webhook:                              webhook:
    smee_url: ${GH_SMEE_URL}              secret: ${GH_WEBHOOK_SECRET}
    secret: ${GH_WEBHOOK_SECRET}          listen: 127.0.0.1:8787
                                          path: /webhook
                                          expose: gh-smee
                                      gh-smee:
                                        use: smee
                                        channel: ${GH_SMEE_URL}
```

**Nothing to do by hand.** The daemon rewrites the old shape at boot, before
it loads the config:

- The new smee connector is pinned to the **same channel** (the same
  `${VAR}` reference, verbatim), so whatever sends to it — a GitHub App's or
  repo's webhook — keeps working untouched.
- An existing `listen:` is kept; a missing one gets a free loopback port
  from 8787 up. The github shape gets `path: /webhook` (its default) written
  out. A `webhook` connector's deliveries arrive on its relay path (`path:`,
  default `/`) and are offered to every source, each checking its own
  signature, as the channel did.
- The smee connector made for a `webhook` connector also gets
  `trust: full` and `isolate: true`, with a comment saying why. The relay it
  replaces ran inside conductor, with conductor's trust, and the `webhook`
  connector's listener is one of conductor's own ports, which a jailed
  plugin never reaches. With `isolate: true`, that unjailed process serves
  only this connector. A github connector's smee connector stays jailed:
  github's listener is the plugin's, and smee reaches it through
  `expose_local`.
- The `smee` plugin is installed **first** (the boot gap-fill, bounded to 2
  minutes). If it can't be fetched, nothing is rewritten and the daemon
  starts on the config as it is; the next boot tries again.
- A github connector's rewrite also needs a smee build that declares
  `expose_local`. An older smee is updated first. That update asks for
  `expose_local` and for the channel's host, which would normally hold it
  for `conductor plugin approve`. The migration is the change that wires
  exactly those, so it records the approval itself and logs it:
  `config migrate: approved smee <release> (replacing <release>), … which
  also may: <each new permission>`. An update that asks for anything more
  stays held for you. If no such smee release is available (or the update
  stays held), the **github rewrite is held back** with a warning, and only
  that rewrite. github keeps its own `smee_url` client, which still delivers,
  and the rest of the migration goes ahead. The next boot tries again.
- The rewritten config must load and build every touched connector enabled,
  and each smee connector it wires must be granted the listener it relays
  to, before it replaces yours. Otherwise nothing is written, and the reason
  is logged and escalated.
- Every rewritten file is backed up first as `<file>.pre-smee` (the first
  backup is kept), comments and key order are preserved, and each file is
  logged: `config migrate: rewrote <file> (backup <file>.pre-smee)`. A
  second boot finds nothing to do.
- Imported files (`imports:`, `connectors: {imports: […]}`, entry
  `import:`) are rewritten where the connector lives.
- A connector that sets **both** `smee_url` and `expose:` is left alone and
  named in the log: one listener has one exposure, so pick one by hand.

`conductor config migrate --dry-run` shows the rewrite without writing;
`conductor config migrate` runs it by hand. `conductor validate` (and the
self-update preflight) names a pending rewrite and checks that the smee
plugin can be fetched.

**Release order.** Conductor and plugins update independently, so every
combination keeps working:

1. `smee` plugin v1.2.0: it declares `expose_local`, which a jailed
   plugin needs to deliver to the listener it relays to (see [[Plugins]],
   "What a plugin declares"); plus TCP keep-alive on the channel stream,
   backoff that resets once connected, and egress for a pinned non-smee.io
   channel. Until it is out, the migration leaves github connectors on
   `smee_url` (above): jailed, an older smee cannot reach the listener.
2. `github` plugin v2.12.0 — `webhook.smee_url` still works, with a
   deprecation warning in the log. Every github 2.x release already serves
   `listen` + `expose`, so a migrated config runs on any of them.
3. This conductor release — the rewrite above; core no longer has a smee
   client.
4. A later `github` major removes `webhook.smee_url`, once this release is
   the oldest conductor in use.

**Under the plugin jail.** A smee connector runs jailed like every plugin.
It may connect to the listeners its `expose:` wiring names, and to nothing
else on the machine. That is why the rewrite unjails only the smee connector
made for the bundled `webhook` connector (above). Remove its `trust: full`
and it is refused conductor's own port, and the log names the delivery it
refused. A smee connector you configure yourself for a `webhook` connector
needs the same two keys. Outside a migration, an update of an older smee to
v1.2.0 is held until `conductor plugin approve`, because it asks for
`expose_local`.

**On a host that cannot jail plugins** (root, a default Docker container,
user namespaces blocked; see [[Plugins]], "From a conductor that did not jail
plugins"), the plugins installed before the jail keep running unjailed, and
a plugin installed since is refused. The smee plugin the migration installs
is not refused for being new there. It takes over what the github plugin or
conductor itself did before the jail, so it inherits that treatment
(`pre_jail: true` in `installed.yaml`, logged as `config migrate: smee takes
over from <connector's use>, which ran before the plugin jail …`). It runs
unjailed, with the same warning as the github plugin. It needs no
`expose_local` there, so the whole config migrates even with an older smee.

**Rolling back** to an older conductor after the rewrite: github deliveries
keep flowing (the older conductor already runs the smee connector and the
listener's exposure), but the `webhook` connector's relay path is new — put
`<file>.pre-smee` back if you roll back and rely on it.

## Upgrading to the plugin contract

## Upgrading to the plugin contract

On this release the vendor connectors — `github`, `slack`, `discord`,
`ntfy`, `pushover`, and `notifiarr` — are no longer compiled into the
`conductor` binary. Each is now an **official plugin**, fetched from
[`NodeSpy/conductor-plugins`](https://github.com/NodeSpy/conductor-plugins)'s
`refs/dist/*` refs over plain git — the same mechanism any third-party
connector plugin already used (see [[Plugins]]). `use: github`, `use: slack`,
and so on resolve exactly as they always have; **no config change is
needed**. What changes is that the first boot (or `conductor init`) after
upgrading needs **git access to the plugins repo** to actually fetch them.

### What happens on a box with network access

`conductor init` (or the daemon's own boot-time gap-fill, `bootGapFill`)
fetches any referenced plugin that isn't installed yet, verifies its
checksum, and registers it — the same install path a third-party plugin
always went through. Nothing about this is specific to the vendor
connectors; they simply used to skip it by being compiled in.

### What happens on a box WITHOUT network access (offline/airgapped)

Fetching degrades, it never crash-loops the daemon:

- **Boot**: `bootGapFill` tries once per boot (bounded to 2 minutes) to fetch
  whatever is missing. A plugin it can't reach is logged and left for later;
  every connector that needed it is **disabled with the reason** (`connectors
  ls` and `conductor validate`'s summary both name it) while the rest of the
  config boots normally.
- **Retry**: if anything is still missing after boot, `pendingPluginRetry`
  keeps trying in the background — every 5 minutes at first, backing off to
  once an hour while it stays missing. The moment every pending plugin is
  installed, it re-validates the config against what they actually declare
  (their real verbs/events were never checked while disabled) and only THEN
  restarts the daemon into them. If that validation fails, the daemon does
  **not** restart — it logs loudly, leaves the connector(s) disabled, and
  gives up retrying until you fix the config (or the plugin) and restart by
  hand.
- **Fix**: run `conductor init` from somewhere with network access to the
  plugins repo (or once connectivity is restored on the box itself) — that's
  the one step an offline box needs before any of `github`/`slack`/etc. will
  ever come up.

### Self-update won't make this worse

`conductor update` downloads the new release and runs ITS OWN `conductor
validate --require-plugins` against your config, read-only, before swapping
the binary in. `--require-plugins` fails validate if any referenced plugin is
neither already installed nor fetchable from this box — so an unattended
auto-update refuses to move you onto a release whose connectors would go
dark, rather than applying it and discovering the gap after the fact.
`conductor update --force` skips this one check (the config itself still has
to load) for an operator who already knows a plugin source is unreachable
for a reason they accept.

## Migrating off the legacy schema

The legacy config schema — `integrations:`, `notify:`, `handoff:`/`handoffs:`,
`controllers:`, `control:`, and `paseo_bin` — was removed in this release
(see the plugin-contract design doc, decision Q4).

If you are still on the legacy schema:

1. Run `conductor config migrate` with **v0.60.0** — the last release before
   the plugin contract, and the last one that still carries the automatic,
   fail-safe transform (back up first; `--dry-run` previews it without
   writing anything).
2. Upgrade to this release once the migrated config loads and validates.

This binary no longer reads any of the retired blocks. `conductor validate`
(and the daemon's own boot-time load) names the exact block if one is still
present, with the same message regardless of which one it is:

```
config: `<key>:` was removed with the legacy config schema — migrate it with
`conductor config migrate` on v0.60.0 (the last release that has it), then upgrade
```

If an unattended auto-update already carried you past v0.60.0 without
migrating first, `conductor config migrate`'s own removal error names the
box's actual rollback copy (`<executable>.prev`, saved alongside the current
binary by the update that replaced it) when one is present, so you don't
need to go re-fetch v0.60.0 by hand unless that file is gone too.

Related: [[Configuration]] · [[Connectors]] · [[Plugins]] · [[Commands]]
