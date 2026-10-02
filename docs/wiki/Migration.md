# Migration (legacy schema → connectors)

The legacy config schema — `integrations:`, `notify:`, `handoff:`/`handoffs:`,
`controllers:`, `control:`, and `paseo_bin` — was removed in this release
(see the plugin-contract design doc, decision Q4).

If you are still on the legacy schema:

1. Run `conductor config migrate` with the **release before the plugin
   contract** — that binary still carries the automatic, fail-safe
   transform (back up first; `--dry-run` previews it without writing
   anything).
2. Upgrade to this release once the migrated config loads and validates.

This binary no longer reads any of the retired blocks. `conductor validate`
(and the daemon's own boot-time load) names the exact block if one is still
present, with the same message regardless of which one it is:

```
config: `<key>:` was removed with the legacy config schema — migrate it with
`conductor config migrate` on the release before the plugin contract, then upgrade
```

Related: [[Configuration]] · [[Connectors]] · [[Commands]]
