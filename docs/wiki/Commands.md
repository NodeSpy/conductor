# Commands

```
conductor run [--config PATH]              start the daemon
conductor run <name> [--input k=v ...] [--json '{…}']  fire a manual trigger via the running daemon
conductor validate [--config PATH]         load & validate config (both schemas), then exit
conductor replay <event.json>              run a saved webhook through the pipeline, verbs stubbed
conductor sweep [--now]                    one catch-up sweep (dry-run print / signal the daemon)
conductor force <kind> <owner/repo>#<n>    force an action for a target now (via the daemon)
conductor status                           live agents, in-flight workflows, stuck/attention
conductor report [--days N]                dispatches by kind/outcome + attention counts + spend
conductor runs [--limit N]                 recorded executions, newest first (id, status, cost)
conductor runs <id>                        one run's step-by-step detail (also: conductor run <id>)
conductor runs retry <id> [--from <step>] [--force-replay]  re-run a recorded execution (recorded inputs pinned; succeeded steps need --force-replay)
conductor watch [<run-id>] [--json]        tail the live run event stream (steps, gates, outcomes)
conductor pause | resume                   stop / resume dispatch at runtime (no restart; also verbs: conductor.pause/resume)
conductor update [--force] [--tag vX]      self-update to the latest release
conductor service install|sync|uninstall   manage the background service unit
conductor connectors ls                    each connector: resolved use:/origin, state, events, verbs, triggers
conductor schema <connector>               full event/filter/context/verb/option/output schemas
conductor plugin list [--caps]             every connector + runtime, with ORIGIN and kind (--caps: permissions)
conductor plugin show <name>               one implementation's surface (Decl + permission manifest + disclosure)
conductor plugin add <ref> [--runtime]     install it, show the permissions it declares, print the config stub
conductor plugin update [name]             bump everything unpinned, or just one
conductor plugin remove <name>             drop it from local install state and delete its binary
conductor connector auth ls                each oauth2 connector's login state + token expiry
conductor connector auth <name> [--revoke] one-time OAuth2 login (or clear stored tokens)
conductor secrets check                    unlock every vault, resolve every reference, report (no values)
conductor vault <name> init|add|get|ls|rm  manage a named vaults: entry
conductor unlock                           seed the default vault key for non-interactive restarts
conductor config migrate [--dry-run]       transform a legacy config to the connectors schema
conductor mcp memory --socket <path>       stdio MCP server for the live agent tools (memory + run_step; launched by runtimes, not by hand)
conductor workflows [ls]                   config + saved (agent-promoted) workflows with review state and health
conductor workflows review <name>          print the workflow's provenance + FULL steps, then clear it for reuse (dry-run it first; runs stay policy-guarded)
conductor workflows rm <name>              remove a saved workflow
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
- **runs / watch** — the run-inspection pair ([[Runs]]): `runs` reads the
  history directory straight off disk (works with the daemon down); `watch`
  and `runs retry` go through the running daemon's control socket. `run <id>`
  shows the same detail when the argument names a recorded run rather than a
  manual trigger (a configured trigger name always wins).
- **report** — three sections over the audit window: dispatches by
  kind/outcome + attention counts, spend ([[Cost-Accounting]]), and agent
  quality ([[Outcomes]]).
- **connectors ls / schema** — the introspection pair: what is configured and
  what each type accepts. `schema` also takes a bare type name.
- **mcp memory** — the stdio MCP server behind the live agent memory tool
  ([[Memory]]). The daemon launches it into agent sessions on runtimes that
  support live tools (ACP `mcpServers`), with the socket path and the
  dispatch's provenance baked into the flags — there is no reason to run it
  by hand.
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
  in config, disable one connector or trigger in place with its own
  `enabled: false` (there is no policy-level kill switch — see [[Policy]]).

Related: [[Configuration]] · [[Secrets]] · [[Migration]]
