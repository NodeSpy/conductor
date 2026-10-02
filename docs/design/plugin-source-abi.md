# The plugin source extension (connector ABI 1)

**Status:** implemented. SDK surface in `pkg/plugin/source.go`; daemon side in
`internal/connector/pluginsource.go`, `internal/connector/external.go`,
`internal/plugin/client.go`. First user: the github connector
(`pkg/githubkit/ghplugin`, served by `conductor-plugins/connectors/github`).

## Why

`docs/design/connector-extraction.md` explains why github could not leave core:
the daemon did not merely *route* github events, it reached into the bundled
integration for things the plugin protocol had no way to carry. A source
plugin could do exactly one thing — stream payload-shaped events the daemon then
matched with a generic evaluator — and was untrusted input besides. That is the
right model for an alert forwarder (sentry, pagerduty) and the wrong one for a
source whose events are *decisions*:

| What the bundled github source does | Why a plain source plugin could not |
|---|---|
| evaluates each trigger's `filter:` with its own match keys (`label_any`, `branch`, merge-gate toggles…) and identity gates (`reviewer`, `assignee`, ownership of the PR) | it never saw the triggers; the daemon's generic evaluator only knows "context key equals value" |
| fires one trigger per matching variant, skips expensive reads when no trigger needs them (merge_ready's GraphQL gate, the sweep's thread/comment reads) | same — no triggers, no variants |
| runs an adaptive catch-up sweep that SIGUSR1, `conductor sweep --now` and the `sweep` verb nudge | no daemon→plugin request outside `invoke` |
| marks sweep-recovered events catch-up, so a busy target is not re-nudged | no field for it |
| `conductor force` builds events for one target on demand | no request for it |
| re-mints an App installation token when a persisted run resumes | no request for it |
| emits `_closed`, `new_comment`, `review_requested`, `merge_conflict`, `failing_checks` — kinds the engine *acts on* | refused: a third-party source may not assert those facts (round-13) |
| its targets are trusted (own-repo scope) | refused: a plugin's target is sender-chosen until proven otherwise (round-8) |
| supplies the dispatch credential policy (`identity:`, `retry:`) | no seam |
| answers run-fact head reads (`{{.run.start_sha}}`) | no seam |

The extension closes each row without a second protocol and without loosening
anything for a plugin that does not opt in.

## Negotiation

`Decl.abi` on a connector. `protocol_version` stays 1 (it is compared for exact
equality and must never move for an addition). A connector plugin reporting
`abi >= 1` (`plugin.ConnectorABI`) gets the extension; one reporting nothing —
every connector plugin built before this — is driven exactly as before: no
triggers on `start_source`, events matched by the daemon, none of the new
methods ever called, every extension field it might send ignored.

Each new method is independently optional: a plugin that does not implement one
answers JSON-RPC method-not-found, which the daemon reads as "not supported".

## The surface

```text
daemon → plugin  plugin.start_source   + triggers[]          (the instance's triggers)
plugin → daemon  plugin.event          + trigger, catch_up, instance, target_trusted
daemon → plugin  plugin.nudge          {instance}            → {nudged}
daemon → plugin  plugin.force          {instance, kind, repo, number} → {events[]}
daemon → plugin  plugin.app_token      {instance, installation_id}    → {token}
daemon → plugin  plugin.target_head    {instance, target}             → {sha, state}
event decl       facts, match_keys     (the unified filter surface, as a bundled connector declares it)
verb             sweep                 conductor-defined: the daemon answers it
```

### Triggers in, routed events out

`start_source` carries `triggers: [{id, name, event, enabled, options, filter}]`.
`filter` is the trigger's `filter:` in the **structural** form of the public IR
(`pkg/sourcekit.Filter` — `{"op":"and","kids":[…]}`), so a plugin evaluates the
operator's filter without implementing the surface grammar, with the same
evaluator and the same `expr` grammar (`pkg/expr`) the daemon uses. `id` is the
daemon's opaque handle (`<index>:<on>`).

An event naming `trigger: <id>` fires **that trigger and no other**, and the
daemon does not re-evaluate its filter — the plugin owns its match keys, as a
bundled connector does. Guards on the daemon side:

- the id must be one of *this instance's* triggers, and that trigger must be
  `on:` the event the plugin says this is — a plugin cannot fire a trigger
  written for a different event by naming its id;
- an event naming another `instance` is not this instance's (one plugin process
  serves every instance of its type; the client now routes events per
  instance, falling back to the last-started one for events that name none);
- what the trigger DOES — its workflow, `shadow`, the engine options
  `max_attempts_per_head` / `flaky_rerun` — is lowered on the daemon side from
  the operator's own config, never taken from the wire.

`catch_up: true` sets `Trigger.CatchUp` (the engine then skips a target an
agent is already working rather than queueing). An event with no `trigger` falls
back to the old path (match every trigger on `on:`, generic evaluator).

### The sweep

The plugin runs its own sweep loop inside its source (it has the credentials and
the reads). What it could not do was be *told* to sweep. `plugin.nudge` is that
request; the daemon's source adapter implements `SweepNow` with it, so SIGUSR1,
`conductor sweep --now` and the `sweep` verb reach a plugin source exactly as
they reach the bundled one.

`sweep` is a **conductor-defined verb** (`plugin.VerbSweep`): a ConnectorABI
plugin that declares it is declaring "run the catch-up sweep now", and the
daemon answers it itself — nudging every source with a sweep, daemon-wide — and
never forwards it. The verb therefore means the same thing whichever
implementation backs the connector.

No daemon state is needed by the sweep. The comment high-water marks every
recovered `new_comment` / `changes_requested` is deduplicated against already
live in the engine and key on the `comment_id` / `comment_kind` facts in the
context, whatever the source. The review-claim cache that makes review folding
dispatch once is in-memory in the source, per process — as it is in the daemon.

### force, app_token, target_head, dispatch identity

- `plugin.force` returns the events for one target; the daemon routes them like
  any other and marks them forced (`Trigger.Force`, which bypasses the engine's
  dedup/backoff gates). They come back in the response rather than on the
  stream precisely so a plugin cannot mark a streamed event forced.
- `plugin.app_token` is the resume-time re-mint the engine asks the bundled
  github integration for.
- `plugin.target_head` answers run-fact head reads. The daemon asks only for a
  **trusted** target **this instance emitted**, the same rule the bundled
  connector applies.
- A ConnectorABI plugin whose connection declares `identity` supplies the
  dispatch credential policy (`identity: {read_token, write_token,
  commit_author}`, `retry: {max, backoff}`) with the bundled github meaning. The
  daemon reads these from **its own copy** of the instance config — nothing
  credential-shaped flows back from the plugin for this.

## Trust

The two refusals the extension has to get past are security properties, and
they stay the default.

- A plugin's **target** is untrusted (round-8 #3): it was built from whatever
  payload the plugin was handed. An untrusted target gets no implicit own-repo
  scope.
- A plugin may not emit a kind the **engine interprets** (round-13):
  `_closed` consumes a target's engagements and settles its outcome;
  `failing_checks` can re-run CI with the operator's token.

`trusted_source: true` on a **plugin-backed** connector entry is the operator's
grant that this plugin's events are authoritative for the platform they
describe — built from deliveries it verified or reads it made with this
connector's own credentials. Only with it does the daemon:

- believe the event's `target_trusted` **claim** (absent the grant, every plugin
  target is untrusted, claim or no claim);
- accept an engine-interpreted kind — and only one the plugin **declares** as an
  event, plus `_closed` from a ConnectorABI source (the lifecycle fact those
  kinds are about). An undeclared reserved kind is dropped even from a trusted
  source.

It is refused on a builtin connector (meaningless — conductor's own code) and is
never passed to the plugin. Without it, an untrusted github plugin still runs,
but its `new_comment` / `review_requested` / `merge_conflict` /
`failing_checks` / `_closed` are dropped, with a one-time log line naming the
setting.

Why an operator grant rather than "the official plugin is trusted": the trust
is in *the operator's choice of this binary for this platform*, not in where the
binary came from. It is visible in the config, greppable, and per instance.

## Standing in for a bundled type

A plugin's declared type must equal the name it is configured under (identity
forgery guard), and a plugin may not replace a bundled type (the refusal exists
so credentials are never silently redirected). The github plugin *is* type
`github`. It may now stand in for the bundled `github` when **no connector in
the config resolves to that builtin** — then nothing is redirected: the operator
named the plugin and only the plugin. A config mixing `use: github` (builtin)
with a github plugin is refused at boot, naming the connectors. The bundled
registration is restored when the plugin is unregistered (reload, rollback).

## Robustness fixes the extension needed

- A plugin's **JSON-RPC error answer** no longer tears its process down. It
  did: a verb failing on the platform's side (a 422, a missing repo) killed the
  subprocess, and with it every source stream the plugin served, for the rest of
  the daemon's life. Only transport failures and timeouts tear down now.
- Source streams are **supervised** (`Client.StreamSource`): when the plugin
  process goes away, the stream is re-opened on the restarted process, bounded
  by the client's existing crash-loop guard.

## Shared implementation

The point of the extension is that a plugin can be the builtin, not an
approximation of it:

- `pkg/githubkit/ghsource` — the github event source (was
  `internal/integrations/github`, moved with its tests); the bundled
  integration is now an adapter over it.
- `pkg/githubkit/ghplugin` — the github declaration both implementations
  describe themselves with, and the plugin handler.
- `ghsource.LowerTrigger` / `ghsource.Connection.SourceConfig` — the lowering
  both call, so one config builds one source in either.
- `pkg/githubkit/ghsource/ghsourcetest` — the conformance table conductor runs
  against the builtin, the plugin through the daemon, and the plugin's wire, and
  the plugin repository runs against its release build.

## Known gaps

Listed in the plugin's parity checklist (`conductor-plugins`
`docs/connectors/github.md`) and in the PR: nested secret references in the
connection are not resolved for a plugin (the daemon resolves top-level string
fields only); `conductor validate` does not run the plugin's own config checks
(they run at `start_source`); the `conductor sweep` dry-run preview covers
legacy `integrations:` only (for either implementation); own-status contexts
noted via `set_status` live as long as the plugin process.
