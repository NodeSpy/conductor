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
- The `smee` plugin is installed **first** (the boot gap-fill, bounded to 2
  minutes). If it can't be fetched, nothing is rewritten and the daemon
  starts on the config as it is; the next boot tries again.
- The rewritten config must load and build every touched connector enabled
  before it replaces yours; otherwise nothing is written and the reason is
  logged and escalated.
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

1. `smee` plugin v1.2.0 (TCP keep-alive on the channel stream, backoff
   that resets once connected, egress for a pinned non-smee.io channel).
   Older smee releases also work.
2. `github` plugin v2.12.0 — `webhook.smee_url` still works, with a
   deprecation warning in the log. Every github 2.x release already serves
   `listen` + `expose`, so a migrated config runs on any of them.
3. This conductor release — the rewrite above; core no longer has a smee
   client.
4. A later `github` major removes `webhook.smee_url`, once this release is
   the oldest conductor in use.

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
