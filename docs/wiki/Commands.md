# Commands

```
conductor run [--config PATH]              start the daemon
conductor run <name> [--input k=v ...] [--json '{…}']  fire a manual trigger via the running daemon
conductor validate [--config PATH]         load & validate config (both schemas), then exit
conductor replay <event.json>              run a saved webhook through the pipeline, verbs stubbed
conductor sweep [--now]                    one catch-up sweep (dry-run print / signal the daemon)
conductor force <kind> <owner/repo>#<n>    force an action for a target now (via the daemon)
conductor status                           live agents, in-flight workflows, stuck/attention
conductor report [--days N]                dispatches by kind/outcome + attention counts
conductor pause | resume                   stop / resume dispatch at runtime (no restart; also verbs: conductor.pause/resume)
conductor update [--force] [--tag vX]      self-update to the latest release
conductor service install|sync|uninstall   manage the background service unit
conductor connectors ls                    each connector: state, events, verbs, trigger count
conductor schema <connector>               full event/filter/context/verb/option/output schemas
conductor connector auth ls                each oauth2 connector's login state + token expiry
conductor connector auth <name> [--revoke] one-time OAuth2 login (or clear stored tokens)
conductor secrets check                    unlock every vault, resolve every reference, report (no values)
conductor vault <name> init|add|get|ls|rm  manage a named vaults: entry
conductor unlock                           seed the default vault key for non-interactive restarts
conductor config migrate [--dry-run]       transform a legacy config to the connectors schema
conductor version
```

## Notes

- **validate** — runs each connector/integration's own checks, the cross-config
  agent-profile checks, and the connectors-model semantic pass
  (position-scoped references, verb options, workflow inputs/outputs, cycles).
  Service start gates on it.
- **run `<name>`** — fires the `on: manual` trigger with that name through the
  running daemon's control socket: same validation, policy, quiet-hours, and
  audit as any firing. `--input k=v` (repeatable, string values) and `--json`
  (one structured object; `--input` overlays it) land in the trigger context
  as `{{.inputs.*}}`. The connectors-model successor to `force`. Errors
  clearly when the daemon is down or the name is unknown.
- **replay** — reads a `{"event": …, "body": {…}}` fixture (see `testdata/`),
  translates it, and prints what would dispatch; connectors-model triggers run
  with every outbound verb stubbed and agents mocked, so a workflow can be
  authored without side effects.
- **connectors ls / schema** — the introspection pair: what is configured and
  what each type accepts. `schema` also takes a bare type name.
- **connector auth** — the only interactive auth step. `auth <name>` runs the
  grant's flow (`authorization_code`: consent URL + localhost redirect
  capture; `device`: prints a user code and polls) and stores the access +
  refresh tokens in the connector's `token_vault`; restarts never prompt.
  `auth ls` shows each oauth2 connector's grant, token_vault, login state,
  and access-token expiry; `--revoke` clears the stored tokens. See
  [[Configuration]].
- **secrets check** — unlocks every vault and resolves each connector's
  credentials; failures name the vault/reference, values are never printed.
  A vault that won't unlock is reported and disables its dependents — the
  daemon still boots.
- **vault / unlock** — `vault <name> …` targets a `vaults:` entry (write ops
  only on writable backends); see [[Secrets]] for the unlock chain and why
  it is non-interactive in steady state.
- **config migrate** — the manual face of the automatic on-boot migration;
  `--dry-run` prints the transformed YAML plus a mapping summary. See
  [[Migration]].
- **pause / resume** — the runtime kill switch (a control file, no restart);
  the config-level one is `policy: { enabled: false }`.

Related: [[Configuration]] · [[Secrets]] · [[Migration]]
