# Migration (legacy schema → connectors)

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

Related: [[Configuration]] · [[Connectors]] · [[Commands]]
