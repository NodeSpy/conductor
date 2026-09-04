# Commands

```
conductor run [--config PATH]              start the daemon
conductor validate [--config PATH]         load & validate config (both schemas), then exit
conductor replay <event.json>              run a saved webhook through the pipeline, verbs stubbed
conductor sweep [--now]                    one catch-up sweep (dry-run print / signal the daemon)
conductor force <kind> <owner/repo>#<n>    force an action for a target now (via the daemon)
conductor status                           live agents, in-flight workflows, stuck/attention
conductor report [--days N]                dispatches by kind/outcome + attention counts
conductor pause | resume                   stop / resume dispatch at runtime (no restart)
conductor update [--force] [--tag vX]      self-update to the latest release
conductor service install|sync|uninstall   manage the background service unit
conductor connectors ls                    each connector: state, events, verbs, trigger count
conductor schema <connector>               full event/filter/context/verb/option/output schemas
conductor connector auth <name>            one-time OAuth2 consent bootstrap for a rest/graphql connector
conductor secrets check                    resolve every secret reference and report (no values)
conductor vault init|add|show|ls|rm        the built-in encrypted vault
conductor unlock                           seed the vault key for non-interactive restarts
conductor config migrate [--dry-run]       transform a legacy config to the connectors schema
conductor version
```

## Notes

- **validate** — runs each connector/integration's own checks, the cross-config
  agent-profile checks, and the connectors-model semantic pass
  (position-scoped references, verb options, workflow inputs/outputs, cycles).
  Service start gates on it.
- **replay** — reads a `{"event": …, "body": {…}}` fixture (see `testdata/`),
  translates it, and prints what would dispatch; connectors-model triggers run
  with every outbound verb stubbed and agents mocked, so a workflow can be
  authored without side effects.
- **connectors ls / schema** — the introspection pair: what is configured and
  what each type accepts. `schema` also takes a bare type name.
- **connector auth** — the only interactive auth step: prints the provider's
  consent URL, captures the localhost redirect, exchanges the code, and stores
  the refresh token in the vault. Applies to rest/graphql connectors with
  `auth.type: oauth2` and an authorization-code/refresh-token grant; restarts
  never prompt. See [[Configuration]].
- **secrets check** — resolves `secrets:` entries and each connector's
  credentials; failures name the reference, values are never printed.
- **vault / unlock** — see [[Secrets]] for the key-resolution order and why
  unlocking is non-interactive in steady state.
- **config migrate** — the manual face of the automatic on-boot migration;
  `--dry-run` prints the transformed YAML plus a mapping summary. See
  [[Migration]].
- **pause / resume** — the runtime kill switch (a control file, no restart);
  the config-level one is `policy: { enabled: false }`.

Related: [[Configuration]] · [[Secrets]] · [[Migration]]
