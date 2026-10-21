# Commands

```
conductor run [--config PATH]              start the daemon
conductor run <name> [--input k=v ...] [--json '{…}']  fire a manual trigger via the running daemon
conductor once <trigger> [--event PATH] [--event-name NAME] [--fail-on LIST] [--require-match]
                                           run ONE event through ONE trigger, for real, no daemon; exit = outcome
conductor validate [--config PATH] [--strict]  load & validate config, then exit (--strict: fail if any connector ended up disabled)
conductor replay <event.json>              run a saved webhook through the pipeline, verbs stubbed
conductor sweep [--now]                    one catch-up sweep (dry-run print / signal the daemon)
conductor force <kind> <owner/repo>#<n>    force an action for a target now (via the daemon)
conductor status                           live agents, in-flight workflows, stuck/attention
conductor report [--days N]                dispatches by kind/outcome + attention counts + usage per runtime/model (per usage.mode)
conductor usage [--since 24h]              model usage per runtime/model: observed by conductor, and what usage.sources report
conductor runs [--limit N]                 recorded executions, newest first (id, status, cost)
conductor runs <id>                        one run's step-by-step detail (also: conductor run <id>)
conductor runs retry <id> [--from <step>] [--force-replay]  re-run a recorded execution (recorded inputs pinned; succeeded steps need --force-replay)
conductor watch [<run-id>] [--json]        tail the live run event stream (steps, gates, outcomes)
conductor pause | resume                   stop / resume dispatch at runtime (no restart; also verbs: conductor.pause/resume)
conductor reload                           tell the running daemon to re-read its config (SIGHUP → re-exec; also verb: conductor.reload)
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
conductor config migrate                   removed with the legacy config schema — run it on the release before the plugin contract, then upgrade
conductor mcp memory --socket <path>       stdio MCP server for the live agent tools (memory + run_step; launched by runtimes, not by hand)
conductor workflows [ls]                   config workflows and triggers by name, then saved (agent-promoted) workflows with review state and health
conductor workflows review <name>          print the workflow's provenance + FULL steps, then clear it for reuse (dry-run it first; runs stay policy-guarded)
conductor workflows rm <name>              remove a saved workflow
conductor browser logins                   saved browser sessions, login sites, credentials (never values) — [[Browser]]
conductor browser seed --identity ID --site SITE [--url U]   log in yourself in a window; only SITE's session is kept
conductor browser cookies import --identity ID --site SITE FILE   import one domain's cookies as a saved session
conductor browser logout --identity ID --site SITE  drop a saved session
conductor browser sessions | peek <session> [--out F.png]   live agent browsers; a screenshot of one
conductor browser passkey register <credential> [--url U]   register a dedicated passkey for a credential (a takeover)
conductor browser credential add|ls|usage|check|test|set|unset|bind|rm …   logins you add: typed (write-only), from a vault, or your own sign-in
conductor browser vault-items <vault> [--query Q] | vault-item <vault> <item>   a vault's items for picking: names and fields, never a value
conductor browser profile add|set|unset|bind|rm …   personal details conductor fills by meaning (never shown to an agent)
conductor browser card add|set|unset|bind|freeze|unfreeze|rm …   payment cards: merchants, limits, the kill switch
conductor browser purchases [--status pending] | purchase show|approve|deny <id>   purchase approvals (signed with your operator key)
conductor browser spend                    profiles, cards, spend per card, identity, bot and team
conductor browser budget <bot> <case> [--amount N [--currency C] [--card X] [--merchant O]… | --clear]   a ticket's spend budget
conductor browser <action> …               (inside an agent) open, snapshot, click, fill, login --credential, … — conductor browser --help
conductor bot ls [--flat]  bots by team (operator)
conductor bot show|cases|workers|proposals …  bots: state, open work, proposals (operator)
conductor bot approve|deny <bot> <id>  decide a bot proposal — SSH-signed with your operator key ([[Bots]])
conductor bot rotate|say|deliver|consolidate <bot> …  drive a bot from the operator side
conductor bot memory|knowledge index|recall <bot|team> …  the memory index a bot's sessions see, and ranked recall over it ([[Bots#reading-memory]])
conductor bot templates [--search …] [--registry …]  community bots: bot templates installed packs and a registry ship ([[Bot-Templates]])
conductor bot template <ref>                 one template: inputs, requested verbs with their maximum tiers, suggestions, seed, charter
conductor bot new <name> --from <instance>/<template> [--connector i=c]… [--grant verb=tier]… [--write]  instantiate a template: planned on a copy of your config, validated, shown as a diff
conductor bot grant <bot> <verb>=<tier>… [--write]  widen a template bot, the same way
conductor bot template-check <bot>           compare a template bot's memory with its template now (a newer one opens a proposal)
conductor bot teams | team <team>  bot teams: bots, routing, routed cases, delegations, knowledge, the proactive pool ([[Bots]])
conductor bot delegations <bot>  a bot's delegation edges: work it handed to peers, and work it holds ([[Bots]])
conductor version
```

## Notes

- **validate** — runs each connector's own checks and the connectors-model
  semantic pass (position-scoped references, verb options, workflow
  inputs/outputs, cycles). Service start gates on it.
- **run `<name>`** — fires the `on: manual` trigger with that name through the
  running daemon's control socket: same validation, policy, quiet-hours, and
  audit as any firing. `--input k=v` (repeatable, string values) and `--json`
  (one structured object; `--input` overlays it) land in the trigger context
  as `{{.inputs.*}}`. The connectors-model successor to `force`. Errors
  clearly when the daemon is down or the name is unknown.
- **once `<trigger>`** — the no-daemon entry point ([[One-Shot]]): takes ONE
  event (`--event`, default `$GITHUB_EVENT_PATH`), matches it to the named
  trigger, and runs that trigger's steps **for real** in this process, to
  completion — with none of the daemon's background loops. Exit `0` on success
  or a non-match, `3` on a `--fail-on` outcome, `1` when it could not run.
  Built for GitHub Actions; `--fixture` takes a `replay` fixture for local
  testing. paseo and interactive hand-offs are refused up front.
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
- **config migrate** — removed with the legacy config schema; run it on the
  release before the plugin contract, then upgrade. See [[Migration]].
- **pause / resume** — the runtime kill switch (a control file, no restart);
  in config, disable one connector or trigger in place with its own
  `enabled: false` (there is no policy-level kill switch — see [[Policy]]).
- **reload** — re-read the config without a manual restart. Sends `SIGHUP` to
  the running daemon (pid from the pidfile), which re-execs itself in place so
  the new config loads at boot; in-flight runs checkpoint and resume on the new
  process. The same restart the `conductor.reload` verb triggers.

Related: [[Configuration]] · [[Secrets]] · [[Migration]]
