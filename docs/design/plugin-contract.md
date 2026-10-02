# The plugin contract

**Status:** approved; being built on #164 in the §5 order. **Step A is in**:
the contract types and must-understand declarations (`pkg/plugin`), the host
side with no tiers (`plugin.poll` replacing nudge/force, `translate`,
`validate`, `stop`, `host.state`), host-owned keys stripped and one
reserved-namespace list, git-only distribution (`refs/dist`), tunnels as
exposure connectors (`lan`, `tunnel` builtins in process), the vendor-neutral
`pkg/` (relay client out, boundary test), `pkg/plugintest` and
`sourcekit.Poller`. Steps B, P and C follow. Supersedes the
direction of `plugin-source-abi.md` (the "connector ABI 1" extension and
`trusted_source`), which stays in the tree only as a record of what is being
replaced.

## 0. The principle, and what it rules out

There is **one contract**. Every plugin — a GitHub connector, a Slack
connector, a cron source, a js step engine, an agent runtime — speaks it, and
every plugin gets the same treatment from the engine once it is installed.

The engine never needs a conductor change when a plugin adds an event, a fact,
a verb or a behavior. Anything the engine *acts on* is **declared** by the
plugin, in generic terms, from a fixed vocabulary (§2) that the engine
implements once for everyone. Vendor code lives in the plugin;
conductor's `pkg/` is a vendor-neutral SDK.

That rules out, concretely (each of these exists today):

| Ruled out | Today |
|---|---|
| ABI tiers / behavior switched on a version number | `Decl.abi` ≥ `ConnectorABI` turns on triggers-on-start, routed events, `catch_up`, `_closed`, nudge/force/app_token/target_head (`internal/connector/pluginsource.go:22-32,80-94,154-160,201,264,284-336`); `EngineABI` exact match (`internal/plugin/client.go:510-516`) |
| Requests only some plugins may make, or that only some get | `plugin.app_token`, `plugin.target_head`, `plugin.nudge`, `plugin.force` gated on ABI (`pluginsource.go:287-336`, `external.go:510-523`); the conversation inbox / completion hook exist only for bundled Go types (`cmd/conductor/connectors.go:287-350`, `main.go:1122-1178`) |
| Reserved, engine-interpreted kind names | `core.ReservedKind`: `_*`, `failing_checks`, `merge_conflict`, `review_requested`, `new_comment` (`internal/core/event.go:25-34`) and ~20 kind-name `switch`es in the engine (§3) |
| Per-plugin runtime trust | `trusted_source`, `connector.SourceTrusted` (`internal/connector/external.go:158-183`), `kindFor`'s trust branch (`pluginsource.go:183-225`) |
| Vendor names in the engine | `t.Source == "github"` (`internal/connector/github.go:290-297`), `in.Decl.Type != "github"` (`internal/flow/flow.go:1038-1055`), `ref.TypeName() == "github"` (`internal/config/pack_instantiate.go:1033-1057`), `gh run rerun` / `gh api` shell-outs (`internal/engine/engine.go:1590-1622`), `GH_TOKEN`/`PC_GH_*` env (`internal/dispatch/paseo.go:196-207`), `refs/pull/<n>/head` (`internal/gitwt/gitwt.go:288-296`) … |
| Vendor code in conductor's `pkg/` | `pkg/githubkit/**` |

**Trust is decided once, at install** (the existing `plugin_trust` /
`PluginSourceAllowed` model: official repos admitted by default, a third-party
source needs an `allow` entry, local paths are the operator's own build).
After install every plugin is equal. `trusted_source` is removed outright.

**Builtins** (cron, rss, webhook) implement the same contract in-process
(§1.9). GitHub and Slack stop being builtins (§5); nothing is designed around
keeping them.

---

## 1. The contract

### 1.1 Transport

Unchanged from today, because it is already uniform: newline-delimited
JSON-RPC 2.0 over the plugin's stdin/stdout, stderr for logs (redacted), the
message caps (8 MiB plugin→host, 32 MiB host→plugin), concurrent requests in
both directions, ids opaque. One process per installed plugin; every instance
of it shares the process and each call names its `instance`.

### 1.2 Versioning: one contract that grows additively

- `protocol_version` stays **1**. It is compared for equality and never moves
  for an addition. A breaking change is not allowed; a rename is done by adding
  the new name and keeping the old one readable.
- **Optional fields are ignored by whoever does not know them**, in both
  directions.
- **Optional methods**: answering JSON-RPC `-32601` means "I don't implement
  this". This is the only negotiation, and it is the same for every plugin and
  for the host. There is no capability list to opt into and no tier.
- **Declarations are must-understand.** The one place an unknown key is an
  error is the `semantics` blocks of a `Decl` (§2): if a plugin declares a
  semantic the host does not implement, the host refuses the plugin at load
  ("connector `gh` declares `semantics.events[new_comment].cursor`; this
  conductor does not implement it — needs conductor ≥ X"). Silently ignoring a
  semantic would mean silently changing behavior (no dedupe, no stop-on-close).
  A plugin that wants to run on older hosts lists the keys it can live
  without in the block's `optional: [key, …]`; the host then ignores those
  if unknown (and Serve strips them before replying to a host that
  advertised it lacks them).
- The host sends `host: {version, semantics: [names]}` on `plugin.describe`,
  so an SDK can drop optional blocks an older host lacks before replying.
- `Decl.abi` is **deprecated and ignored**: it is accepted on the wire, so
  existing plugins keep loading, and it never changes behavior. `Decl.kind`
  stays only as an install-layout hint (`connectors/`, `engines/`,
  `runtimes/`); what a plugin *provides* is read from what it declares
  (§1.4).

Every plugin built against today's SDK is therefore a valid implementation of
this contract as-is (§5.4).

### 1.3 Lifecycle

```text
spawn (verify-before-execute, manifest confinement — unchanged)
  │
  ├─ plugin.describe {host}            → Decl           (once per process start)
  │     host checks: type == configured name, must-understand semantics
  │
  ├─ per instance:
  │     plugin.validate {instance, config, triggers}   → {problems[]}   (optional;
  │           also run by `conductor validate`)
  │     plugin.start_source {instance, config, triggers} → {}           (if the Decl has events)
  │           … plugin.event notifications stream back …
  │
  ├─ at any time, for any instance:
  │     plugin.invoke    {instance, verb, options, connection, target?} → {outputs}
  │     plugin.poll      {instance, mode, target?}                       → {events[]}
  │     plugin.translate {instance, delivery}                            → {events[]}
  │     plugin.run       {instance, run_id, code, args, env, inputs}     → {outputs}
  │     host.state / host.kv / host.sql / host.memory  (plugin → host requests)
  │
  ├─ plugin.stop {instance}  → {}      (optional: instance removed or reloaded)
  │
  └─ stdin closed → plugin exits (shutdown); the host waits for in-flight calls
```

**Restart.** A transport failure or a timeout tears the process down; the next
call respawns it (crash-loop guard as today: 3 starts / 30 s parks it; 32 over
the client's life parks it until reload). On respawn the host re-sends
`start_source` for every live instance with the same triggers
(`Client.StreamSource`, already built). A plugin must make `start_source`
idempotent. All dedupe/cursor/attempt state is the engine's (§2) or in
`host.state`, so a restart loses nothing the engine relies on.

**Hot reload.** As today (drain, swap, reset counters), gated on an unchanged
reload surface. The surface is `type`, verbs, events, `semantics` and manifest;
`abi` is no longer part of it.

### 1.4 `plugin.describe` → `Decl`

```jsonc
{
  "protocol_version": 1,
  "type": "github",                  // must equal the configured connector name's type
  "kind": "connector",               // layout hint only
  "desc": "…",
  "connection": { "<field>": Field },            // instance config schema (unchanged)
  "verbs":  [ Verb ],                            // unchanged, plus verb semantics (§2.3)
  "events": [ Event ],                           // unchanged, plus event semantics (§2.2)
  "semantics": { … },                            // connection-level semantics (§2.4)
  "step_engine": { "languages": ["js"] },        // present ⇔ implements plugin.run
  "runtime":     { "role": "agent_backend" },    // present ⇔ is an agent runtime (§1.8)
  "capabilities": { "egress": [], "commands": [], "fs": [], "spawns": false },  // confinement manifest (unchanged)
  "auth": { … },                                 // managed OAuth (unchanged)
  "protocols": [ … ]                             // decision runtimes (unchanged)
}
```

`Field`, `Verb` (`name, desc, usage, options, outputs, ask`) and `Event`
(`name, desc, filters, context, options, dynamic, facts, match_keys`) are
unchanged from `pkg/plugin/wire.go:151-206`. `facts` / `match_keys` (added in
the #164 work) become available to **every** plugin; a plugin that sends only
`filters` / `context` keeps the generic surface.

### 1.5 Sources

**`plugin.start_source {instance, config, triggers[]}` → `{}`**

`triggers` is always sent, to every plugin:
`[{id, name, event, enabled, options, filter}]`. `filter` is the structural
`pkg/sourcekit.Filter` IR. A plugin that ignores `triggers` gets the generic
path (below).

**`plugin.event` (notification, plugin → host)**

```jsonc
{
  "instance": "gh",          // which instance (required when the process serves >1)
  "event": "new_comment",    // a declared event name
  "trigger": "3:gh.new_comment", // optional: fire exactly this trigger (routed)
  "title": "…",
  "target": {                // optional; see §2.2 `target`
    "key": "acme/w#7",       // stable identity within this instance
    "url": "https://…",
    "assigned": true         // the platform assigned this target (not sender-chosen)
  },
  "context": { … },          // facts; names declared in the event's `facts`/`context`
  "dedup": "comment:501",    // optional signature
  "catch_up": false          // true: re-derived by a poll, not a fresh edge
}
```

- **Routing, the same rules for everyone.**
  - An event naming `trigger` fires that trigger and no other, and the host
    does not re-evaluate its filter. The guards are the existing ones: the id
    is one of *this instance's* triggers, and that trigger is `on:` this event.
  - An event without `trigger` is matched against every trigger `on:` it by
    the generic evaluator.
  - The engine options a trigger carries (workflow, shadow, attempt caps,
    declared remediation options) are always lowered from the operator's own
    config, never taken from the wire.
- **The legacy `target` shape** (`Repo, Owner, Name, PR, Issue, Number,
  HeadSHA, BaseRef, HTMLURL, Project` — Go field names, no json tags,
  `pkg/plugin/source.go:77-88`) is still accepted. The host derives `key` from
  it (`Repo#Number`) and maps the rest onto facts, so old plugins keep working.
- **An event may only use the semantics its `Event` declares** (§2.2). That is
  the only gate on what a plugin may emit, and it applies to every plugin
  equally. There are no reserved names.

**`plugin.poll {instance, mode, target?, dry_run?}` → `{events[]}`** (optional)

This one method replaces `plugin.nudge`, `plugin.force`, the `sweep` verb
intercept and `sweeper.SweepOnce`. `mode` is one of:

| `mode` | The host sends it for | Plugin behavior | What the host does with the result |
|---|---|---|---|
| `now` | SIGUSR1, `conductor sweep --now`, the engine verb `<conn>.poll` (§2.4) | Run your catch-up pass now. The plugin may stream the events, or return them, or both. | Routes them like streamed events. They should carry `catch_up: true`. |
| `target` | `conductor force <conn> <target-ref>` | Return the events for this one target (`target` is an opaque target ref: a `key`, or the free text the operator typed). | Marks returned events **forced**: they bypass dedupe, cursor, liveness and backoff. Only events in the *response* can be forced, never streamed ones. |
| `dry_run` | `conductor sweep` (preview) | Return what a pass would emit, without emitting it. | Prints the events and dispatches nothing. |

The plugin owns its own schedule: adaptive interval, backoff, stuck-check
cadence. The SDK provides the scheduler (§1.10). The host only says "now".

**`plugin.translate {instance, delivery: {headers, body}}` → `{events[]}`**
(optional)

This decodes one raw upstream delivery into events, without a listener. It
backs `conductor replay` and `conductor once`, which today work only for the
bundled github and slack (`cmd/conductor/main.go:1290-1325`, `once.go:611-640`).
A plugin may declare `semantics.translate.env` (§2.4) so `once` can find the
delivery in a CI runner's environment without conductor knowing the runner.

**`plugin.validate {instance, config, triggers}` → `{problems: [{path, message}]}`**
(optional)

This is the plugin's own config and trigger checks, run at load and by
`conductor validate`. It replaces the bundled-only `TypeDecl.ValidateTrigger`
hook (which #161 adds) and closes the known gap that `validate` never runs a
plugin's checks.

### 1.6 Verbs

`plugin.invoke {instance, verb, options, connection, target?}` → `{outputs}`.
This is unchanged except for the optional `target` (`{key, …facts}`), which the
host sends when the engine calls a verb *for* a target through a semantic
(§2). Output schemas stay strict.

Every engine-initiated call goes through `invoke` to a verb the plugin
**declared** for that purpose:
- the revision re-read (`run.start_sha` and friends);
- credential minting;
- the flaky-rerun status and rerun;
- posting a conversation message.

No method exists for any of these.

### 1.7 Step engines

`plugin.run` and the `host.kv/sql/memory` callbacks are unchanged
(`pkg/plugin/wire.go:362-431`). A plugin is a step engine because its Decl has
`step_engine`, not because `kind == engine && abi == 1`.

### 1.8 Runtimes

Agent runtimes keep their two role interfaces, now declared rather than
duck-typed:

- **`runtime.role: agent_backend`**: the verb set
  `run, list_agents, inspect, archive_agent, archive_workspace, create_worktree, create_workspace, list_workspaces, clone, send, wait[, logs]`
  (`internal/dispatch/rpc_verbs.go:16-19`).
- **`runtime.role: decision`**: `decide` and `models` with `protocols`.

These are conductor-defined *interfaces*, the same for any plugin that
implements them. They are not privileges. ACP agents (`claude-code-acp` and
the like) are not conductor plugins: ACP stays the transport conductor uses
to drive a third-party agent process, and it is out of scope here.

### 1.9 Host → plugin state: `host.state` (new, optional for the plugin to use)

```text
host.state {instance, op: get|put|delete|list, key, value?, ttl?} → {ok, value?}
```

This is durable key/value storage scoped to the plugin instance, available to
every plugin, for things a source must remember across restarts. Examples: a
review-claim cache, own-status contexts (today lost when the plugin process
dies — a known gap), a poll cursor. It is bounded per instance. The run-scoped
`host.kv/sql/memory` keep their `run_id` capability as today.

### 1.10 Builtins speak the contract

cron, rss and webhook are rewritten as `pkg/plugin` handlers, served through
an **in-process pipe** (`plugin.ServeConn(net.Pipe())`). They are registered
by the same `RegisterExternalType` path as a spawned plugin, with
`Spec{InProcess: handler}` instead of a binary.
- Same JSON encoding, same routing, same semantics checks.
- No `core.Integration` side door (`internal/connector/sources.go:126,363,542`).
- No `TypeDecl.Filter` hook (`internal/connector/connector.go:163-172`).

The engine cannot tell them from a spawned plugin, which is the point.

What stays in-process **and is not a plugin**: conductor's own namespaces
(`kv, sql, memory, workflow, conductor, blob, step, manual, handoff`). They are
conductor's state, not integrations. They keep a `Decl` so flows address them
the same way, and their names are reserved. The two reserved-name lists that
disagree today are unified: `internal/config/connectors.go:1687-1707` lacks
`handoff` and `step`, and `internal/config/vaults.go:55` reserves only a
subset.

### 1.11 Errors

| Code | Meaning | Engine behavior |
|---|---|---|
| -32700/-32600/-32601/-32602/-32603 | JSON-RPC standard (-32601 = not implemented) | -32601 on an optional method means "unsupported"; the others fail the call |
| **-32010 `upstream`** | the upstream answered with an error; `data: {status, retryable}` | step fails; retried by the step's `retry:` only when `retryable` |
| **-32011 `target_gone`** | the target closed/disappeared under the call | the run is **stopped** (stop hooks, no failure) — today's `ErrTargetClosed` (`internal/dispatch/output_schema.go:632`) |
| **-32012 `invalid`** | the request can never succeed (validation) | fail, never retry |
| **-32013 `rate_limited`** | `data: {retry_after}` | retried after `retry_after`, regardless of `retry:` |
| **-32014 `not_ready`** | state not computed yet (e.g. mergeability "unknown") | retried with short backoff, bounded |

A JSON-RPC error answer never tears the process down. Transport errors and
timeouts do. This is the #164 fix, kept.

### 1.12 What the SDK (`pkg/`) provides

Vendor-neutral only, enforced by a boundary test (§1.13):

- `pkg/plugin`:
  - wire types and `Serve` / `ServeConn`;
  - handler interfaces, one per optional method;
  - the semantics types (§2);
  - the `host.*` client.
- `pkg/sourcekit`:
  - the trigger runtime: `Filter` IR, evaluation and lowering of
    `start_source` triggers, plus routed-event helpers;
  - the poll scheduler (adaptive / fixed / per-trigger cadence, `now`
    nudges);
  - the webhook listener with HMAC verification (no relay client: relays and
    tunnels are connectors, §2.3 `exposes`);
  - a delivery-id dedupe ring.
- `pkg/expr`: the expression grammar.
- `pkg/plugintest`, the conformance harness:
  - drives any plugin binary over the real wire against a test-supplied
    upstream (`http.Handler`);
  - asserts on events, routed triggers and declared semantics;
  - can also boot the real engine in-process against the binary with a fake
    runtime, to assert *engine outcomes* (dispatch counts, stop-on-close,
    cursor dedupe), all vendor-neutral.

  The GitHub fake and the GitHub cases live in conductor-plugins and plug into
  this harness.

### 1.13 Boundary tests

- `pkg/boundary_test.go` (exists): nothing under `pkg/` imports `internal/`.
- **New:** no identifier, path, import or string literal under `pkg/` matches
  the vendor list (`github|gh_|slack|discord|gitlab|gitea|jira|sentry|…`),
  except an explicit, reviewed allowlist (e.g. the `sha256=` signature-prefix
  note).
- **New** (after §5 step C): the same check over the engine packages
  (`internal/{engine,flow,dispatch,controller,core,gitwt,store,inbound}`). The
  allowlist covers distribution (`internal/plugin/remote.go`,
  `cmd/conductor/update.go`) and doc strings.

---

## 2. The declaration vocabulary

Each semantic is a key under `semantics` on an `Event`, a `Verb`, or the
`Decl` (connection level). Each has one definition of engine behavior. Facts
are referenced by name and templates by `{{.fact}}`. Everything here is
optional; an event that declares nothing is a plain trigger (dedupe on `dedup`,
fire, done).

### 2.1 Shared notions

- **Target.** What an event is *about*: a PR, an issue, a Slack thread, a
  file. Identified by `target.key`, which is unique within an instance. The
  engine's per-target state is keyed `(instance, key)`. That covers
  serialization (one worker per target), attempts, cursors, closed-ness and
  outcomes.
- **Revision.** An opaque string naming the target's current version (a head
  sha). Per-revision state (attempt caps, verification-failed marks,
  re-engage) keys on it.
- **Facts.** The event's `context`. Templates see them at the root (`{{.repo}}`,
  `{{.pr}}`). The engine stops synthesizing `pr` / `issue` / `head` from a
  GitHub-shaped `core.Target` (4 copies today, §3 row T1); the plugin emits
  them as facts. The packs' templates (`{{.repo}}`, `{{.pr}}`, `{{.number}}`,
  `{{.title}}`, `{{.author}}`, `{{.run_id}}`) are all facts the GitHub plugin
  already declares, so they keep working.

### 2.2 Event semantics

| Key | Shape | Engine behavior (exact) | Replaces |
|---|---|---|---|
| `target` | `{key: tmpl, url?: fact, label?: "pull request", assigned?: bool\|fact, scope?: [{dimension, fact}]}` | Builds the target from facts when the event's `target.key` is absent. `assigned` true means the platform chose the target, not the sender: the target gets implicit scope (each `scope[].dimension` = that `fact`'s value, e.g. `repo`, or Slack's `channel` and `user`), outcome learning and revision reads. A false or absent `assigned` gives none of these. `label` is used in human text ("the pull request closed"). | `TargetTrusted` claim judged by `trusted_source`; `OwnRepo()`; `DimRepo` hard-coded to `Target.Repo` |
| `revision` | `{fact, branch?: fact, base?: fact}` | The target's revision. Per-revision attempts key on it. The `run.start_revision`/`run.revision` facts come from it (aliases `run.start_sha`/`run.head_sha`/`run.head_short` kept). `branch` is the push branch, also used to adopt an open workspace. | `Target.HeadSHA`, `Context.head_ref`, `store.Record.HeadSHA` |
| `checkout` | `{remote: tmpl, fetch_ref?: tmpl, push_branch?: fact, runtime_hints?: {…opaque}}` | How an agent workspace gets the target's code. gitwt fetches `fetch_ref` from `remote` into a private ref and pushes to `push_branch`. `runtime_hints` are passed to the runtime unread. **No `checkout` means no checkout** (synthetic target). | `refs/pull/<n>/head` (`gitwt.go:288-296`), `git@github.com:` (`gitwt.go:414-426`), `Target.PR>0 → checkout-pr`, `--forge github --pr-number` (`paseo.go:454-462,545-548`), `ForceNoCheckout` |
| `closes_target` | `{outcome?: {fact, true: accepted\|rejected, false: accepted\|rejected}, reverts?: {fact, corroborated?: fact}}` | Terminal for the target. The engine: (1) fires no trigger for this event, unless one is explicitly `on:` it; (2) stops every run **bound to** the target (`-32011` semantics: stop hooks, `run.reason` from `label` and outcome); (3) drops queued bound runs; (4) deletes the target's dedupe/attempt state; (5) records the outcome; (6) when `reverts` is set and corroborated, records `reverted` on the named sibling targets (keys built with this event's `target.key` template). | `_closed` (`core/event.go:11-14`), `engine.go:778-785`, `engine/flow.go:172,201-246`, `outcome.go:59-138`, `runfacts.go:86-91` |
| `bound_to_target` | `true` | A run started by this event dies with the target (`closes_target` above) and is dropped if the target closed while it queued. | `core.BranchFixKind` (`core/event.go:237-243`, duplicated `ghsource/types.go:39`), `controller/runner.go:276-293` |
| `completion` | `edge` (default) \| `{level: {rearm_on?: revision}}` | `edge`: dedupe on `dedup`, record done, cap attempts. `level`: the condition is external, current state that a poll re-derives. Never recorded done; while an agent is live on the target, a new occurrence is skipped (not queued); `rearm_on: revision` re-engages a parked target when its revision changes. | `livenessGated` (`engine.go:1450-1456` + uses), the `review_requested` re-engage special case (`engine.go:901-906`) |
| `cursor` | `{id: fact, stream?: tmpl}` | A monotonic high-water mark. An event whose numeric `id` is ≤ the stored mark for `(instance, target, event#variant, stream)` is dropped unless forced. The mark advances when the event is accepted. | the comment marks: `commentMarked`/`commentKind`/`commentMarkKind` (`engine.go:828-832,1647-1690`), `LastComIDs` (`store/store.go:24-39`) |
| `attempts` | `{cap: per_revision\|none}` | `per_revision` (default): the trigger's `max_attempts_per_revision` applies. `none`: each event is a distinct item and the cap does not apply. | the `new_comment` exemption (`engine.go:926,1392-1395`) |
| `priority` | `interactive \| normal` | `interactive` takes the high slot-priority lane. | `slotPriority` keyed on `review_requested` (`engine/slots.go:21-26`) |
| `feedback` | `true` | Feedback on work in flight: may adopt the operator's open workspace on the target's `revision.branch` (`adopt_open_workspaces`). | `isFeedbackKind` (`dispatch/paseo.go:1232`), `AdoptOpenWorkspaces` doc |
| `verification_failed` | `true` | Records `verification_failed` once per `(target, revision)` (outcome learning, report). | `case "failing_checks"` in `outcome.go:59-77`, `MarkCIFailure` |
| `remediate` | `{option: name, run: fact, status: {verb, done_when: expr}, action: {verb}, budget: n}` | A pre-dispatch remedy the operator enables per trigger with `options.<option>: {enabled: true}`. Before dispatching: invoke `status.verb` with `{run}` until `done_when` holds (bounded wait), then invoke `action.verb` up to `budget` times per `(target, revision)`. Dispatch only once the budget is spent. Both verbs are the plugin's own and use the plugin's credentials. | `flaky_rerun` (`engine.go:847-877`), `gh run rerun`/`gh api …/runs` shell-outs (`engine.go:1590-1622`), `lowerEngineOptions` |
| `author` | `{login: fact, automated?: fact}` | The event's author. Used by `policy.ignore.users`, by bot-reply guidance in the prompt, and by `reply_to_bots` (with verb `conversation_post`, §2.3). | `Context["author"]`/`["author_is_bot"]` reads (`flow/flow.go:372-377`, `engine/flow.go:54-62`) |
| `labels` | `fact` | The target's labels: `policy.pause_label` and per-target opt-out. | `Context["labels"]` (`engine.go:741-765,797`) |
| `private` | `[fact…]` | Never rendered into an agent prompt (still in templates). | `eventprompt.go:15-24` list (`app_token, gh_token, installation_id, reaction_subjects`) |
| `secret` | `[fact…]` | Never persisted, redacted everywhere, re-derived on resume via the credential semantic. | `sanitizeContext` (`engine.go:1298-1310`) |
| `conversation_reply` | `{id: tmpl, author: fact, text: fact}` | A reply in a conversation an `opens_conversation` verb started. Before trigger matching, the engine routes it to the pending ask whose `(instance, id)` matches; `approvers` are enforced on `author`. If consumed, it fires no trigger; if not, it is an ordinary event. | `slack.SetReplyHook` + inbox (`handoff/slack.go:186-265`, `cmd/conductor/connectors.go:287-350`, `main.go:1152-1177`) |

`catch_up: true` on the wire (a poll re-derived the event) is per-occurrence,
not declared. With `completion: level` the engine skips it while an agent is
live, as today (`dispatch/paseo.go:1183-1214`).

### 2.3 Verb semantics

| Key | Shape | Engine behavior | Replaces |
|---|---|---|---|
| `reads_revision` | `{args: {opt: tmpl}, revision: output, state: output, states: {open: […], closed: […], accepted: […]}}` | The engine's only way to read a target's current revision and state: run start, every end phase, `set_status`-style hooks. Called only for an `assigned` target of this instance. | `plugin.target_head`, `HeadReader` + `t.Source=="github"` (`connector/head.go`, `github.go:290-297`) |
| `mints_credential` | `{credential: name}` | This verb mints the named credential (§2.4). | `plugin.app_token`, `appTokener`, `gh auth token` |
| `opens_conversation` | `{id: output, approvers?: option}` | Posting with this verb registers a pending ask keyed `(instance, id)`. Replies arrive as `conversation_reply` events. | `AskChanneler`, `handoff/*` channels |
| `conversation_post` | `true` | A message *to the event's author*. With `reply_to_bots: off`, the engine skips it when the triggering event's `author.automated` is true. | `in.Decl.Type != "github"` + `verb == comment/reply` (`flow/flow.go:1038-1065`) |
| `host_only` | `true` | The engine may invoke this verb (through a semantic), but flows, skills and agents may not. Credential minters are `host_only`, so a token never lands in a step's outputs. | — (today `app_token` is a method, not a verb) |
| `exposes` | `{local: option, url: output, lease: output, release: verb}` | This verb makes a local address reachable from outside (a tunnel or a relay). When a consumer that names this connector in `expose:` needs public reachability for local address A, the engine invokes the verb with `{local: A}`, uses `url`, and invokes `release` with `{lease}` when done. Long-lived exposures are re-opened after a plugin restart, and the consumer is given the new URL. Everything is released on shutdown, and the plugin kills its children when its stdin closes. A plugin may always reach the address it was handed in `local`, even under egress confinement (loopback is never proxied). An `expose:` naming a connector without an `exposes` verb is a load error. | the hard-coded provider `switch` (`internal/handoff/tunnel.go:44-102`), the smee client (`internal/inbound/smee.go`, `pkg/sourcekit`) |
| `target_args` | `{opt: tmpl}` | Defaults this verb's options from the target when a step omits them. Only for an `assigned` target. | `readVerb` defaulting `repo`/`pr` (`engine/steps.go:555-581`) |

### 2.4 Connection-level semantics (`Decl.semantics`)

| Key | Shape | Engine behavior | Replaces |
|---|---|---|---|
| `credentials` | `[{name, role: read\|write, mint: {verb, args: {opt: tmpl}}, env: [VAR…], template: key, expires: output, refresh: resume\|expiry, git_author?: {name: output, email: output}, guidance?: text}]` | For each agent dispatched on this instance's event, mint each credential (cached until `expires`) and put it in the agent's env under `env` and in templates as `{{.credentials.<template>}}`. Re-mint on resume or retry from the persisted facts (`refresh: resume`). Append `guidance` to the prompt. The read/write split is declared. No first-integration-wins global. | `dispatch.Tokens{App,User}`, `GH_TOKEN/GITHUB_TOKEN/PC_GH_*` (`dispatch/paseo.go:196-207`, `provision.go:90-106`, `local.go:39-49`), `ghwrite.go` guidance, `dispatchTuner`/`identitySource` (`main.go:1198-1240`, `external.go:393-452`), `RefreshAppToken` + `installation_id` (`main.go:1093-1119`), `gh auth token` (`main.go:1915-1930`) |
| `scope` | `{dimension: name, option: field, consent?: true}` | The connection's operator-chosen scope (e.g. `repo` / `repos`). It bounds `assigned` targets: one outside the operator's list is treated as unassigned. With `consent: true`, an armed pack trigger on this connector must name scope values. | `sourceIsRepoScoped`: `TypeName()=="github"` (`pack_instantiate.go:1033-1057`), `githubRepoKey` (`:908-1031`), `TriggerArm.Repos` |
| `poll` | `{verb_name?: "poll"}` | The engine provides a verb `<conn>.poll` (default name `poll`; `sweep` kept as an alias for one release) that calls `plugin.poll{mode: now}` on this instance. | `VerbSweep` intercept (`external.go:457-466`, `connector/conductor.go:94-131`) |
| `translate` | `{env?: {event_path: VAR, event_name: VAR}}` | Lets `conductor once` read a delivery from a CI runner's environment. | `GITHUB_EVENT_PATH` / `GITHUB_EVENT_NAME` defaults (`once.go:101-102,127-128,188-189,291-294`) |
| `listeners` | `[{listen: field, expose: field, url_to: field}]` | The plugin runs an inbound listener on the address in config field `listen`. When the operator sets `expose: <conn>` in the `expose` field, the engine opens an exposure on that connector for the listen address at instance start, and passes the public URL to the plugin in config field `url_to` on `start_source`. | smee/relay plumbing inside sources |
| `preflight` | `{commands: [..]}` | Checked at boot like today's PATH preflight, but per plugin. | `preflightPATH` requiring `gh` (`main.go:1245`) |

### 2.5 Generic engine options (any trigger)

`max_attempts_per_revision` (alias `max_attempts_per_head`) and any `option`
named by an event's `remediate` semantic. Nothing else is engine-interpreted.
Vendor trigger options (`ignore_checks`, `stuck_after`, `poll_interval`,
`reviewer`, `assignee`, `include_prereleases`, merge gates …) are the
plugin's: declared in `Event.options`, delivered on `start_source.triggers`,
and never parsed by the engine. The `config.Action` fields that carry them
today (`internal/config/config.go:901-941,988-1072`) are deleted.

### 2.6 Outcome vocabulary

`accepted | rejected | reverted | verification_failed`. These replace
`merged | closed | reverted | ci_failed` in `store/outcomes.go`,
`engine/outcome.go:186-253` and `cmd/conductor/report.go:161-200`. A one-time
read-side mapping keeps existing state files valid. Display text comes from
the plugin's `target.label` ("pull request merged" is the GitHub plugin's
rendering of `accepted`).

---

## 3. Mapping: every special case today → its replacement

Line numbers are at `feat/githubkit-extraction` `ba754ff`; non-test code only.
**SEM `x`** means the generic semantic `x` from §2. **PLUGIN** means the code
moves into the plugin. **DEL** means it is deleted. **NAME** means a rename only
(PR → target), with no behavior change. Test hits (~3,000 lines) follow their
code.

### 3.1 Target lifecycle and outcomes

| # | Where | What it does today | Replacement |
|---|---|---|---|
| L1 | `internal/core/event.go:11-14` | `KindClosed = "_closed"` | SEM `closes_target` |
| L2 | `internal/core/event.go:25-34` | `ReservedKind`: `_*`, `failing_checks`, `merge_conflict`, `review_requested`, `new_comment` | DEL. The only rule left is "an event may use only the semantics it declares" (§1.5) |
| L3 | `internal/core/event.go:237-243`; `pkg/githubkit/ghsource/types.go:39-46` | `BranchFixKind` set | SEM `bound_to_target` |
| L4 | `internal/engine/engine.go:767-785` | `_closed`: delete state, `markClosed`, `stopFixers`, no dispatch | SEM `closes_target` (1)–(4) |
| L5 | `internal/engine/flow.go:172-177, 201-246` | drop queued fixer for a closed PR; `markClosed`/`stopFixers`/`closedSince` | SEM `bound_to_target` + `closes_target` (keyed `(instance,key)`) |
| L6 | `internal/controller/runner.go:171-173, 271-293` | `StopTarget` stops only `BranchFixKind` sessions → `ErrTargetClosed` | carry `bound_to_target` on the session |
| L7 | `internal/dispatch/output_schema.go:632`; `internal/flow/flow.go:468-486,631-633,961` | `ErrTargetClosed` "stopped: target PR closed" → stop hooks, no retry | NAME `ErrTargetGone`; plugin error `-32011` maps to it |
| L8 | `internal/flow/runfacts.go:86-91`; `flow/flow.go:483` | `stopReason` "the PR merged/closed" | SEM `target.label` + outcome |
| L9 | `internal/engine/outcome.go:42-139` | `switch t.Kind {_closed, failing_checks}`; `Context["merged"]`, `["reverts"]`, `["reverts_corroborated"]`; `store.TargetKey(repo,n)` | SEM `closes_target.outcome/reverts`, `verification_failed`; sibling keys from `target.key` |
| L10 | `internal/engine/outcome.go:186-253`; `internal/store/outcomes.go:156-185,269`; `engine.go:101-102`; `cmd/conductor/report.go:161-200` | `merged/closed/reverted/ci_failed`, `MarkCIFailure`, "$/merged" | §2.6 vocabulary with a read-side state mapping |
| L11 | `internal/controller/affinity.go:561-601` | `end_on: [gh._closed]` observe | `end_on` names any event; "closes" comes from SEM `closes_target` |

### 3.2 Dedupe, gating, attempts, priority

| # | Where | What it does today | Replacement |
|---|---|---|---|
| D1 | `internal/engine/engine.go:1450-1456`, uses at `884-913, 1036-1044, 1168-1179`; `engine/flow.go:90,97,328` | `livenessGated`: `review_requested, merge_conflict, changes_requested` | SEM `completion: level` |
| D2 | `internal/engine/engine.go:901-906` | re-engage parked `review_requested` on new head | SEM `completion.level.rearm_on: revision` |
| D3 | `internal/engine/engine.go:926, 1392-1395` | `new_comment` exempt from the attempt cap | SEM `attempts: {cap: none}` |
| D4 | `internal/engine/engine.go:828-832, 1194-1198, 1647-1690`; `engine/flow.go:105-109,333-337`; `engine.go:80-81` | comment high-water marks: `comment_id`, `comment_kind`, a `changes_requested:` namespace, per-variant | SEM `cursor {id: comment_id, stream: "{{.comment_kind}}"}`; the engine always keys per `event#variant` |
| D5 | `internal/store/store.go:17-39, 190-231` | `Record.HeadSHA`, `Attempts["kind@head"]`, `LastComIDs`, `CommentKindIssue/Review` | NAME (revision, cursor-per-stream); the two constants become PLUGIN; the legacy migration is kept |
| D6 | `internal/engine/slots.go:21-26` | `review_requested` gets high priority | SEM `priority: interactive` |
| D7 | `internal/dispatch/paseo.go:1232, 1243-1262` | `isFeedbackKind` (`new_comment‖changes_requested`) adopts the workspace via `head_ref` | SEM `feedback` + `revision.branch` |
| D8 | `internal/dispatch/paseo.go:1183-1214` | `queueOrAdopt`; skip a `CatchUp` when an agent is live | generic (kept), keyed `(instance,key)` |
| D9 | `internal/engine/engine.go:840-843, 911`; `engine/flow.go:86-95,259-268,343-348` | `kind#variant` signature dedupe, `coalesceQueued` | generic (kept) |
| D10 | `internal/inbound/target.go:68-103` | `DeliveryDedup` "copied from the github integration" | moves to `pkg/sourcekit` (SDK), used by plugins |
| D11 | `internal/config/config.go:189-192` | `AdoptOpenWorkspaces` documented for `new_comment`/`changes_requested` | SEM `feedback` |

### 3.3 CI remediation

| # | Where | What it does today | Replacement |
|---|---|---|---|
| R1 | `internal/engine/engine.go:845-877` | `failing_checks && FlakyRerun && run_id` → wait for completion, rerun once per head | SEM `remediate` |
| R2 | `internal/engine/engine.go:1590-1622` | shells out to `gh run rerun --failed` and `gh api …/runs/<id> --jq .status` with `GH_TOKEN` | PLUGIN verbs `rerun_run`, `get_run` (already exist, `pkg/githubkit/invoke.go:622,690`) |
| R3 | `internal/engine/engine.go:124-125, 137, 247-252, 313-320` | `Rerun`/`RunStatus` seams, `runWait` | become `invoke` calls; `runWait` stays |
| R4 | `internal/connector/github.go:247-257`; `pluginsource.go:270-277` | `lowerEngineOptions` (`flaky_rerun`, `max_attempts_per_head`) | §2.5: generic `max_attempts_per_revision` plus the declared remediate option |
| R5 | `internal/config/config.go:925, 1063` | `FlakyRerun` action field | generic remediate option block |

### 3.4 Run facts, revision reads, checkout

| # | Where | What it does today | Replacement |
|---|---|---|---|
| H1 | `internal/flow/runfacts.go:40-81`; `flow/flow.go:427-439` | `start_sha/head_sha/head_short/pushed` via `in.TargetHead` | SEM `revision` + verb `reads_revision`; old names kept as aliases |
| H2 | `internal/connector/head.go:9-46`; `connector/github.go:290-297`; `external.go:510-523` | `HeadReader`; `t.Source=="github"`; ABI-gated `target_head` | SEM `reads_revision` (DEL the gates) |
| H3 | `internal/dispatch/paseo.go:454-462, 545-548, 586-589`; `backend.go:140-144`; `rpc_backend.go:140`; `cli_backend.go:184-195` | `Target.PR>0 → checkout-pr`; `--pr-number N --forge github` | SEM `checkout` (`runtime_hints` passthrough; see §3.10 X1) |
| H4 | `internal/gitwt/gitwt.go:288-296, 414-426, 519-531` | `refs/pull/<n>/head`; `git@github.com:<repo>.git`; `prBranch` ← `head_ref` | SEM `checkout.fetch_ref/remote/push_branch` |
| H5 | `internal/inbound/target.go:11-19, 57-66`; #161 `ForceNoCheckout` fix | synthetic target and forced no-checkout | absence of SEM `checkout` |
| H6 | `internal/flow/flow.go:1756-1767, 1940-1975` | `expect_push` / `no_progress` | generic (git-level), kept |
| H7 | `internal/controller/controller.go:83` | `Capabilities.CheckoutPR` | NAME |

### 3.5 Credentials for agents

| # | Where | What it does today | Replacement |
|---|---|---|---|
| C1 | `internal/dispatch/dispatch.go:24-28` | `Tokens{App, User}` | SEM `credentials` (named, with a role) |
| C2 | `internal/dispatch/paseo.go:196-207`; `provision.go:90-115`; `local.go:38-49` | `GH_TOKEN, GITHUB_TOKEN, PC_GH_WRITE_TOKEN, PC_GH_APP_TOKEN` (×3 copies) | SEM `credentials[].env`; one code path |
| C3 | `internal/dispatch/ghwrite.go:14-36`; uses `engine.go:1007`, `engine/steps.go:99`, `flow/flow.go:1570,1582-1583` | `WriteWrapperGuidance`, `BotReplyGuidance` (GitHub text) | SEM `credentials[].guidance`; bot guidance keyed on `author.automated` |
| C4 | `internal/dispatch/dispatch.go:397-411`; `internal/flow/validate.go:13-19` | template keys `app_token`, `gh_token` | `{{.credentials.<name>}}` (old keys as aliases from the GitHub decl's `template:`) |
| C5 | `internal/dispatch/eventprompt.go:15-24`; `engine.go:1298-1310` | prompt and persistence strip lists | SEM `private` / `secret` |
| C6 | `internal/engine/engine.go:1019-1028, 1315-1379`; `engine/flow.go:492-504`; `engine/retry.go:108-113`; `engine.go:126,253-256,321` | `Context["app_token"]`, `refreshTok`, `RefreshAppToken` | SEM `credentials[].refresh: resume` |
| C7 | `cmd/conductor/main.go:1093-1119`; `pkg/plugin/source.go:155-166,194-198`; `pluginsource.go:328-336` | `appTokener` via `Context["installation_id"]`; `plugin.app_token` | SEM `credentials[].mint` with args `{installation_id: "{{.installation_id}}"}`; DEL the method |
| C8 | `cmd/conductor/main.go:454-466, 1183-1241`; `connector/external.go:393-452`; `integrations/github/github.go:344,353` | `dispatchTuner`/`identitySource`, `gh_auth`/`app` keywords, first integration wins, `retry:` from github | SEM `credentials` per instance; `retry:` becomes a generic dispatch setting |
| C9 | `cmd/conductor/main.go:1915-1930`; `once.go:482-483`; `main.go:682-683` | `exec gh auth token`; wiring | PLUGIN (the github plugin's `user_token` mint verb) |
| C10 | `internal/connector/connector.go:334-336` | `Deps.UserToken` (unused) | DEL |
| C11 | `internal/engine/engine.go:1624-1631` | `userToken()` "your GitHub token" | SEM `credentials` role `write` |

### 3.6 Polling, force, replay, once

| # | Where | What it does today | Replacement |
|---|---|---|---|
| P1 | `pkg/plugin/source.go:31-45`; `internal/plugin/client.go:242-283` | `plugin.nudge`, `plugin.force`, `VerbSweep` | `plugin.poll` (§1.5); DEL |
| P2 | `internal/connector/github.go:263-270`; `external.go:457-466`; `connector/conductor.go:94-131`; `main.go:1023-1034` | `sweep` verb intercepted, `SetSweepHook` | SEM `poll` engine verb |
| P3 | `cmd/conductor/main.go:899-919, 1828-1843` | SIGUSR1 → `sweepNower.SweepNow` | `plugin.poll{now}` to every source that implements it |
| P4 | `cmd/conductor/main.go:1352-1389` | `cmdSweep` dry-run over legacy `integrations:` only | `plugin.poll{dry_run}` |
| P5 | `cmd/conductor/main.go:1403-1408, 1522-1531, 1644-1705`; `core/event.go:108-111,204-210`; `pkg/plugin/source.go:139-153` | `force <kind> owner/repo#n`, `core.Forcer`, `ForceRequest{Repo,Number}` | `conductor force <conn> <target-ref>` → `plugin.poll{target}` |
| P6 | `cmd/conductor/main.go:1290-1325`; `once.go:611-640` | `translator.Translate` (bundled github/slack only) | `plugin.translate` |
| P7 | `cmd/conductor/once.go:30-36, 101-102, 127-128, 145, 188-189, 291-294` | `GITHUB_EVENT_PATH/NAME` defaults | SEM `translate.env` |
| P8 | `internal/connector/pluginsource.go:264`; `core/event.go:103-107` | `catch_up` honoured only at ABI ≥ 1 | always honoured (DEL the gate) |
| P9 | `internal/engine/engine.go:696`; `engine/flow.go:169` | "sweep will retry" assumptions | hold for any `completion: level` event; documented |

### 3.7 Authors, bots, labels, scope, consent

| # | Where | What it does today | Replacement |
|---|---|---|---|
| A1 | `internal/flow/flow.go:372-377, 1038-1065, 2009-2012` | `author_is_bot`; `reply_to_bots` only when `Decl.Type=="github"` and verb is `comment`/`reply` | SEM `author.automated` + verb `conversation_post` |
| A2 | `internal/engine/flow.go:54-62` | `ignore.users` vs `Context["author"]` | SEM `author.login` |
| A3 | `internal/engine/engine.go:741-765, 797`; `engine/flow.go:50`; `config/config.go:378-381` | `Context["labels"]` + `pause_label` | SEM `labels` |
| A4 | `internal/connector/scope.go:28, 73-81, 98-107`; `connector/slack.go:187-202` | `DimRepo` implicit from a trusted `Target.Repo`; `ScopeContexter` bundled-only | SEM `target.scope` (any dimension, any plugin) |
| A5 | `internal/flow/scoperender.go:89` | closed set `number, owner, name, repo, kind` of platform-assigned facts | SEM `target.assigned` + `target.scope` |
| A6 | `internal/flow/skillverbs.go:499-528` | untrusted-target warning only for `ref.Type=="webhook"` | warn for any event whose `target.assigned` is false |
| A7 | `internal/config/pack_instantiate.go:467-493, 908, 934-1057`; `config/pack.go:259-283` | consent: `TypeName()=="github"`, `githubRepoKey="repo"`, `TriggerArm.Repos` | SEM `scope {dimension, option, consent}`; `TriggerArm.Repos` kept as an alias of `scope:` |

### 3.8 Conversation / hand-off / completion

| # | Where | What it does today | Replacement |
|---|---|---|---|
| V1 | `internal/handoff/slack.go:1-265`, `slack_poster.go:1-104` | Slack channel, DM, poster, inbox keyed `channel:thread_ts` | PLUGIN (slack) + SEM `opens_conversation` / `conversation_reply`; the engine keeps a generic inbox keyed `(instance, id)` |
| V2 | `internal/handoff/discord*.go` | Discord REST and gateway | PLUGIN (discord) |
| V3 | `internal/handoff/registry.go:39-170`; `config/config.go:169-179, 443-534, 773-781, 1436-1460, 1692-1858` | legacy `handoffs:` block, `HandoffConfig{Web,Slack,Discord}`, validators | DEL legacy block (§5 open question Q4); hand-off = any verb with `opens_conversation` |
| V4 | `internal/handoff/tunnel.go:36-616` | the web hand-off's `Tunnel` interface plus a provider `switch`: static, lan, cloudflared, ngrok, tailscale, ssh (localhost.run/serveo/pinggy), localxpose, command | SEM `exposes`. `lan` and `tunnel` (runs any tunnelling command, reads the URL off its output) become vendor-neutral builtin exposure connectors on the contract (§1.10); `static` is the web connector's own `base_url`. cloudflared, ngrok, tailscale, localxpose and ssh become small plugins. **Straight cutover** (no current users) |
| V4a | `internal/config/config.go:488-510, 1677-1815` | `TunnelConfig`, `validTunnelProviders`, `validateTunnel` | DEL; the web connector takes `expose: <conn>` or `base_url` |
| V4b | `internal/connector/web.go:19, 38, 64-68`; `internal/handoff/web.go:77, 129` | the web connector builds its tunnel from `tunnel:` and opens it per draft | `expose:` names an exposure connector; the engine calls `exposes` per draft. The web page itself stays a core surface |
| V4c | `internal/inbound/smee.go:14-144`; the smee client in `pkg/sourcekit` | the smee.io relay transport inside conductor | a `smee` plugin declaring `exposes`; a source plugin's listener uses it through `listeners` |
| V5 | `internal/handoff/handoff.go:66-82` | `parseReply` keywords (approve/lgtm/👍 …) | generic, kept in the engine inbox |
| V6 | `internal/connector/ask.go:17-19, 50`; `engine/flow.go:442-464` | `AskChanneler`, then legacy registry | SEM `opens_conversation` |
| V7 | `cmd/conductor/connectors.go:287-350`; `main.go:43, 884-889, 1146-1178` | `slackInboxer`/`discordConnector`/`webConnector` duck types; `slack.SetReplyHook` fan-in; `RunDiscordGateway`; `anySlackIntegration` | DEL; replies arrive as `conversation_reply` events |
| V8 | `cmd/conductor/main.go:662-666, 1121-1146`; `core/event.go:180-191`; `engine.go:1575-1577` | `core.CompletionHook` (single global, legacy path only) → Slack `on_done`/`on_fail` | DEL; flow hooks (`at: done/fail/stop`, #163) already do this generically |
| V9 | `internal/flow/validate.go:539` | "hand-offs need slack/discord/web" | text: "need a connector with an `opens_conversation` verb" |

### 3.9 Registration, ABI, trust, lowering (host layers)

| # | Where | What it does today | Replacement |
|---|---|---|---|
| G1 | `internal/config/use.go:631-645, 686-739` | `builtinConnectors` lists github, slack, discord, … | shrink to the in-process contract builtins (cron, rss, webhook) + core namespaces |
| G2 | `internal/connector/external.go:18-21, 32-91, 106-111`; `cmd/conductor/plugins.go:134-147, 206-215` | override refusal; `…InPlaceOfBundled`; `builtinUsers` | DEL (no bundled github to stand in for) |
| G3 | `internal/connector/github.go` (all); `internal/integrations/github/**`; `pkg/githubkit/**` | bundled github | PLUGIN |
| G4 | `internal/connector/slack.go` (all); `internal/integrations/slack/**` | bundled slack | PLUGIN |
| G4a | `internal/connector` `ntfyDecl`, `pushoverDecl`, `notifiarrDecl`, `discordDecl` (registered at `init` like any builtin) | vendor notification/chat connectors compiled into conductor | PLUGIN (conductor-plugins already ships pushover and notifiarr; ntfy and discord ported in P) |
| G5 | `internal/connector/sources.go:61, 126, 178, 363, 471-473, 542`; `github.go:397-412` (`buildIntegration`); `cmd/conductor/main.go:54-58`; `internal/core/registry.go:21,32` | cron/rss/webhook via `core.Build` side door | in-process contract builtins (§1.10); DEL `core.Integration` |
| G6 | `pkg/plugin/source.go:5-28`; `wire.go:239-256, 129-132`; `connector/external.go:185-192, 342-344, 389`; `internal/plugin/resolve.go:285-292`; `cmd/conductor/reload.go:24,75` | `ConnectorABI`, `Decl.ABI`, triggers sent only at ABI 1, ABI part of reload surface | DEL (`abi` accepted and ignored) |
| G7 | `pkg/plugin/wire.go:45-49`; `internal/plugin/client.go:510-516`; `plugin.go:46-52`; `cmd/conductor/plugins.go:182-188` | `EngineABI` exact match; empty kind refused for engines | `step_engine` declared; must-understand semantics |
| G8 | `internal/connector/pluginsource.go:139-160, 183-254` | `kindFor` trust/ABI rules, `_closed` path, `TargetTrusted = trusted && claim` | DEL; SEM `target.assigned` bounded by SEM `scope` |
| G9 | `internal/connector/external.go:149, 158-183, 235`; `config/connectors.go:58-73, 93, 101, 1716-1720` | `SourceTrusted`, `trusted_source` field, builtin refusal, reserved-key strip | DEL |
| G10 | `internal/config/pack_trust.go:37-42, 68-121`; `internal/plugin/remote.go:89-96`; `install.go:107-113`; `resolve.go:173-175, 214-233`; `plugin.go:104-113`; `manager.go:73` | `IsOfficialSource` also feeds event trust; `release_verified` exists for it | keep for install-time provenance and integrity only; DEL the runtime consumer |
| G11 | `internal/connector/github.go:135-239`; `config/config.go:866, 901-941, 945-1072` | lowering into vendor `config.Action` fields (`Reviewer`, `IgnoreChecks`, `StuckAfter`, `Gates`, `Exclude` …) | PLUGIN (options delivered on `start_source.triggers`); DEL the fields |
| G12 | `internal/connector/connector.go:163-172`; `connector/slack.go:102-105, 241-276` | `TypeDecl.Filter` hook (slack, rss) | DEL; plugins evaluate their own match keys or use the generic evaluator |
| G13 | `internal/connector/connector.go:322-324, 485` | `InstanceDecler` (rest, graphql) | optional `plugin.describe {instance}` returning instance-specific verbs (Q6) |
| G14 | `internal/connector/github.go:272-284`; `integrations/github/github.go:337-340` | `set_status` → `NoteOwnStatusContext` (own-status loop guard) | PLUGIN-internal, made durable via `host.state` |
| G15 | `internal/connector/external.go:235` | reserved connection keys omit `use, network, isolation, allow_secrets`, which leak to the plugin | fix: strip all host-owned keys |
| G16 | `internal/config/connectors.go:1687-1707`; `config/vaults.go:55`; `connector.go:503-528` | reserved-name lists disagree (`handoff`, `step` missing) | one list |
| G17 | `internal/config/config.go:536-546, 563`; `internal/notify/notify.go:32-50, 237-353` | legacy `notify:` Slack/Discord/ntfy/Pushover/Notifiarr sinks | DEL with legacy blocks (Q4); the sinks are plugin verbs via `notify.via` |
| G18 | `internal/config/config.go:105, 346-374, 1496-1497`; `cmd/conductor/main.go:306-326` | legacy `integrations:` layer, `actionLister` | DEL (Q4) |
| G19 | `internal/migrate/{github,actions,sources,migrate,notify,legacy_extracted}.go` | legacy-config translator importing the github/slack integrations | DEL in step C (Q4) |
| G20 | `config/config.go:648-651, 1422-1423` | `MaxTrackedPRs` | NAME `max_tracked_targets` (alias kept) |

### 3.10 Cannot be generalized — called out

| # | Item | Why | Options |
|---|---|---|---|
| X1 | paseo's own worktree command takes `--forge github --pr-number N` (`dispatch/paseo.go:545-548, 586-589`) | the agent **runtime** has its own forge integration | (a) **recommended for now:** `checkout.runtime_hints` passed through unread, so the GitHub plugin declares `{forge: github, number: "{{.pr}}"}` and the paseo runtime plugin interprets it — the engine stays neutral; (b) drop the paseo forge path and always branch-off from the declared `fetch_ref` (loses paseo's PR-aware workspace UI) |
| X2 | Plugin and self-update fetches use the `gh` CLI against GitHub Releases (`internal/plugin/remote.go:207, 221`; `cmd/conductor/update.go:78-111, 364`); sources must be `github.com/…` (`remote.go:29-49`, `config/use.go:159`) | **Decided: git only, any host, no `gh`.** Each release's release workflow (an ordinary committed workflow file) also publishes the per-platform binaries plus `checksums.txt` as a commit on `refs/dist/<tag>` (e.g. `refs/dist/connectors/github/v2.0.0`). Ordinary clones never fetch `refs/dist/*`. Versions: `git ls-remote <url> 'refs/dist/<prefix>/v*'`, so only releases with published binaries are listed. Fetch: a shallow, blob-less fetch of that one ref, then read only this platform's binary and `checksums.txt`. Integrity: the checksum is verified, the install record pins the commit sha, and verify-before-execute is unchanged. Sources are any git URL (`github.com/…`, `gitlab.com/…`, `https://…`, `ssh://…`, `git@host:…`); bare names keep defaulting to the official public repo. Private repos use the daemon user's own git credentials. Self-update does the same against the conductor repo. Packs already fetch over git (`config/pack_resolve.go:577`). GitHub Release assets can keep being published, so older conductors that still use `gh` keep updating |
| X3 | The official-repo default allow at install (`pack_trust.go:68-121`) | a trust anchor is needed | keep — it *is* the decided-once-at-install model |
| X4 | Core namespaces (`kv, sql, memory, workflow, conductor, blob, step, manual, handoff`) | conductor's own state | in-process with a `Decl`, reserved names (§1.10) |
| X5 | `core.Target`'s GitHub shape leaks into 4 template-scope builders (`engine/flow.go:372-385`, `engine/steps.go:641-652`, `flow/template.go:459-466`, `dispatch/dispatch.go:383-399`), log tags (`engine.go:1406-1416`), labels `pr=` (`dispatch/paseo.go:964-1047, 1218`), branch slugs (`paseo.go:924, 1033-1039`), `byPR` (`controller/runner.go:262-268`), `eventprompt.go:133-163`, `flow/plan.go:716`, `skillverbs.go:558` | generalizable, but it is the largest single refactor | do it in PR B as a mechanical NAME pass behind the `(instance,key)` target; keep root-scope facts identical so pack templates do not change; the paseo agent label `pr=` is read by `ListAgents` — migrate it with a dual-read for one release |
| X6 | Outcome state already on disk (`store/outcomes.go`) | persisted vocabulary | read-side mapping `merged→accepted`, `closed→rejected`, `ci_failed→verification_failed` |

---

## 4. GitHub and Slack, purely through the contract

Both are ordinary plugins in conductor-plugins. Everything below is their
`Decl` plus code inside the plugin. Nothing is conductor-side except the
generic semantics of §2.

### 4.1 GitHub

**Where each of today's behaviors goes:**

| Behavior | Lives in | Contract surface it uses |
|---|---|---|
| Webhooks (direct listener, HMAC, delivery dedupe; smee relay) | plugin, built on the `pkg/sourcekit` listener and dedupe. `webhook.smee_url` stays accepted as the plugin's own shorthand (the relay client is plugin code), so existing configs don't change; the generic route is `webhook.expose: <conn>` | `start_source`, `plugin.event`, connection `listeners` |
| Adaptive catch-up sweep and stuck-checks poller | plugin, built on the `pkg/sourcekit` scheduler | `plugin.poll {now, dry_run}`, `catch_up: true`, the `poll` engine verb (`gh.sweep` alias) |
| `conductor force` | plugin `Force` | `plugin.poll {target}` |
| Review and inline-comment folding (one `changes_requested` per review; an approval with suggestions is one `new_comment`; either delivery order; claim per review id) | plugin (`reviewfold`); claims optionally in `host.state` so they survive restarts | event `cursor` on `comment_id` makes the dispatch durable, as today |
| Own-status guard (conductor's own failure status never triggers `failing_checks`) | plugin: `set_status` records its contexts in `host.state`, and the source drops matching statuses | `host.state` |
| Merge-state reads (`merge_conflict` / `pr_behind` from `mergeable_state`; `merge_ready` from the GraphQL gate); retry when mergeability is `unknown` | plugin | `completion: level`, `bound_to_target`, error `-32014` for an on-demand read |
| Flaky rerun | plugin verbs `get_run` / `rerun_run` | `failing_checks.semantics.remediate` |
| Re-request reviewers after a push | unchanged: a flow step calling `gh.rerequest_review` (pack config) | plain verb |
| Progress hooks (👀 / pending on start; ✓ on `start_sha`; ✗ on the current head; stop on merge removes 👀 and resolves pending) | unchanged flow hooks (#163) calling `gh.react` / `gh.set_status` | `revision` + `reads_revision` give `run.start_sha` / `run.head_sha`; `closes_target` gives `at: stop` |
| App installation token for agents; write token; commit author | plugin verbs `app_token` / `user_token` (`host_only`) | connection `credentials` |
| Head reads for run facts | plugin verb `pr_head` | `reads_revision` |
| PR checkout for agents | plugin facts `clone_url`, `pr`, `head_ref` | `checkout` (+ `runtime_hints` for paseo, X1) |
| `_closed` (merged, reverts, corroboration) | plugin event `_closed` (the name is no longer reserved, so existing `end_on: [gh._closed]` keeps working) | `closes_target` |
| `reply_to_bots` | plugin verbs `comment` / `reply` | `conversation_post` + `author.automated` |
| Pack consent ("an armed trigger must name repos") | — | connection `scope {consent: true}` |
| `conductor once` in Actions | plugin `Translate` | `plugin.translate`, `translate.env` |

**Decl sketch** (abridged; `connection`, verb options and outputs as today in
`ghplugin/decl.go`):

```yaml
type: github
semantics:
  scope:     { dimension: repo, option: repos, consent: true }
  poll:      { verb_name: sweep }          # `gh.sweep` keeps its name
  translate: { env: { event_path: GITHUB_EVENT_PATH, event_name: GITHUB_EVENT_NAME } }
  listeners: [{ listen: webhook.listen, expose: webhook.expose, url_to: webhook.public_url }]
  credentials:
    - name: app
      role: read
      mint: { verb: app_token, args: { installation_id: "{{.installation_id}}", repo: "{{.repo}}" } }
      env: [PC_GH_APP_TOKEN]
      template: app_token                  # keeps {{.app_token}} working
      expires: expires_at
      refresh: resume
    - name: write
      role: write
      mint: { verb: user_token }           # identity.write_token: gh_auth | pat | app, parsed by the plugin
      env: [GH_TOKEN, GITHUB_TOKEN, PC_GH_WRITE_TOKEN]
      template: gh_token
      git_author: { name: author_name, email: author_email }
      guidance: |
        GH_TOKEN acts as you; use it for git push and gh writes. PC_GH_APP_TOKEN is the App's read token…

x-pr: &pr                                   # shared event semantics (YAML anchor, sketch only)
  target:   { key: "{{.repo}}#{{.number}}", url: url, label: pull request, assigned: true,
              scope: [{ dimension: repo, fact: repo }] }
  revision: { fact: head, branch: head_ref, base: base }
  checkout: { remote: "{{.clone_url}}", fetch_ref: "refs/pull/{{.pr}}/head", push_branch: head_ref,
              runtime_hints: { forge: github, pr_number: "{{.pr}}" } }
  author:   { login: author, automated: author_is_bot }
  labels:   labels
  private:  [installation_id, reaction_subjects]
  secret:   [app_token]

events:
  - name: new_comment
    semantics: { <<: *pr, bound_to_target: true, feedback: true, attempts: { cap: none },
                 cursor: { id: comment_id, stream: "{{.comment_kind}}" } }
  - name: changes_requested
    semantics: { <<: *pr, bound_to_target: true, feedback: true,
                 completion: { level: {} }, cursor: { id: comment_id, stream: "{{.comment_kind}}" } }
  - name: review_requested
    semantics: { <<: *pr, priority: interactive, completion: { level: { rearm_on: revision } } }
  - name: merge_conflict
    semantics: { <<: *pr, bound_to_target: true, completion: { level: {} } }
  - name: pr_behind
    semantics: { <<: *pr, bound_to_target: true }
  - name: failing_checks
    semantics:
      <<: *pr
      bound_to_target: true
      verification_failed: true
      remediate:
        option: flaky_rerun
        run: run_id
        status: { verb: get_run, done_when: "status == 'completed'" }
        action: { verb: rerun_run }
        budget: 1
  - name: merge_ready        # semantics: *pr
  - name: self_review        # semantics: *pr
  - name: _closed
    semantics:
      <<: *pr
      closes_target: { outcome: { fact: merged, true: accepted, false: rejected },
                       reverts: { fact: reverts, corroborated: reverts_corroborated } }
  - name: issue_matched
    semantics:
      target:   { key: "{{.repo}}#{{.number}}", label: issue, assigned: true, scope: [{ dimension: repo, fact: repo }] }
      checkout: { remote: "{{.clone_url}}" }          # branch-off from the default branch
  - name: stuck_checks       # target: the PR; no checkout needed by ci-unsticker, so none declared
  - name: release            # target: { key: "{{.repo}}@{{.tag_name}}" }, checkout of the repo
  - name: deployment_status  # likewise
  - name: dependabot_alert
  - name: secret_scanning_alert

verbs:
  - { name: pr_head, semantics: { host_only: true, reads_revision: {
        args: { repo: "{{.repo}}", pr: "{{.pr}}" }, revision: sha, state: state,
        states: { open: [open], closed: [closed], accepted: [merged] } } } }
  - { name: app_token,  semantics: { host_only: true, mints_credential: { credential: app } } }
  - { name: user_token, semantics: { host_only: true, mints_credential: { credential: write } } }
  - { name: comment,    semantics: { conversation_post: true } }
  - { name: reply,      semantics: { conversation_post: true } }
  - { name: pr_get,     semantics: { target_args: { repo: "{{.repo}}", pr: "{{.pr}}" } } }
  # … the other ~45 verbs, unchanged (react, set_status, rerun_run, get_run, rerequest_review …)
```

### 4.2 Slack

| Behavior | Lives in | Contract surface it uses |
|---|---|---|
| Socket Mode (`apps.connections.open`, websocket, reconnect, ACK every envelope) | plugin | `start_source` |
| Deciding an `interactive` envelope before the ACK (modal validation errors, `views.open` inside the 3 s `trigger_id` window — #161) | plugin; it owns the socket, so the ACK is local | — |
| `app_mention`, `reaction_added`, slash commands, `message_shortcut`, form submissions (#161) | plugin events | `plugin.event`; `target` synthetic (no `checkout`) |
| Channel and user filters, `callback_id`, `users:` required on form/shortcut triggers | plugin match keys + `plugin.validate` | `start_source.triggers`, `plugin.validate` |
| Implicit scope for the triggering channel and user | — | `target.scope: [{channel}, {user}]` |
| Hand-off asks (thread or DM, approvers, first reply wins, approve/discard/revise keywords) | plugin verb `ask` posts; engine inbox matches replies | `opens_conversation`, event `reply` with `conversation_reply` |
| Ack / on_done / on_fail feedback | flow hooks (`at: start/done/fail`) calling `slack.react` / `slack.post` | plain verbs (the legacy `CompletionHook` is deleted) |
| `thread`, `download` verbs (#161) | plugin | plain verbs; downloaded files need Q7 |

```yaml
type: slack
x-msg: &msg
  target: { key: "slack:{{.slack.channel}}:{{.slack.ts}}", label: message, assigned: true,
            scope: [{ dimension: channel, fact: slack.channel }, { dimension: user, fact: slack.user }] }
  author: { login: slack.user, automated: slack.is_bot }
  secret: [slack_bot_token]
events:
  - { name: app_mention,      semantics: *msg }
  - { name: reaction_added,   semantics: *msg }
  - { name: slash_command,    semantics: *msg }
  - { name: message_shortcut, semantics: *msg }
  - name: reply                       # a thread reply or a DM, as Socket Mode delivers them
    semantics:
      <<: *msg
      conversation_reply: { id: "{{.slack.channel}}:{{.slack.thread_ts}}", author: slack.user, text: slack.text }
verbs:
  - { name: ask,  semantics: { opens_conversation: { id: conversation_id, approvers: approvers } } }
  - { name: post, semantics: { conversation_post: true } }
  - { name: react }
  - { name: thread }
  - { name: download }
```

A reply nobody is waiting on is an ordinary `reply` event, so a trigger can be
`on: slack.reply` if wanted. Today an unconsumed reply is simply rule-matched.

---

## 5. Migration

### 5.1 Where the work goes, and in what order

All conductor work lands in **one PR, #164**, as a series of commits in the
order below. The plugins-repo work lands in its existing companion draft.
Each step leaves the branch building and passing; the steps are commit groups,
not separate PRs or releases.

| Step | Repo / PR | Contents | Behavior change at that commit |
|---|---|---|---|
| **A** | conductor, #164 | **Contract core.** `pkg/plugin` semantics types; `describe {host}`; must-understand; `plugin.poll / translate / validate / stop`, `host.state`; triggers sent and `catch_up` honored for every plugin; `abi` ignored; error codes §1.11; the in-process contract transport (§1.10) and the exposure builtins on it; `pkg/sourcekit` scheduler and dedupe; `pkg/plugintest`; reserved-name unification; G15 fix. **Git-only distribution** (X2). **Tunnels cut over** (V4–V4c). | none for existing plugins or configs; the `tunnel:` block is removed (no current users) |
| **B** | conductor, #164 | **Engine reads semantics.** cron, rss and webhook move onto the in-process contract (their synthetic targets and no-checkout become declarations, so they wait for the engine to read them). Every row of §3.1–§3.8 replaced by a semantic lookup. The still-bundled github and slack are re-wired as in-process contract plugins declaring exactly the §4 semantics, so the existing unit and e2e suites prove equivalence before anything is removed. Delete `trusted_source`, `SourceTrusted`, `ConnectorABI`, `kindFor`, `ReservedKind`, `BranchFixKind`, the ABI-gated methods, the `sweep` intercept, `identitySource`/`dispatchTuner`, `lowerEngineOptions`, the vendor `config.Action` fields. Outcome vocabulary with a read-side mapping. | none observable |
| **P** | conductor-plugins, companion draft | `connectors/github`, `connectors/slack`, `connectors/discord` on the contract; exposure plugins `cloudflared`, `ngrok`, `tailscale`, `localxpose`, `ssh-tunnel`, `smee`; `githubkit`, `ghsource`, `ghplugin`, the GitHub fake and the conformance cases move here; `pkg/plugintest` conformance plus engine-outcome scenarios (the incident list) in CI; release workflow publishes `refs/dist/<tag>` (X2). | new plugin versions |
| **C** | conductor, #164 | **Plugins-first boot**, then the removal. Boot fetches and verifies the official plugin for every connector that names a removed builtin before any connector starts. Then remove the github, slack and discord builtins and the other vendor connectors compiled in (`ntfy`, `pushover`, `notifiarr`; plugins exist or are ported in P), `internal/integrations/{github,slack}`, the `internal/handoff` vendor channels, the legacy `integrations:` / `handoffs:` / `notify:` blocks (Q4), and `pkg/githubkit` (moved in P). Strict vendor boundary tests (§1.13). e2e runs against the plugin builds from P. | `use: github` / `use: slack` resolve to the official plugins |
| **K** | conductor-packs | Raise `requires.conductor` for pr-autopilot, ci-unsticker and pr-review-team to the release that ships #164. No YAML change. | — |

**Merge order:** P's plugin releases are tagged and published first, so the
official plugins exist when #164 merges. Then #164 merges and is released.
Then K.

### 5.2 How a deployed daemon moves

There is one release, so there is no intermediate step:

1. **Before switching (optional, recommended):** run the new binary's
   `conductor validate` against a copy of the config. It reports whether each
   official plugin can be fetched and verified from this machine, any legacy
   block (Q4), and a write credential the confined plugin cannot reach (Q8).
2. **Update the usual way** (auto-update included). On first boot the release
   fetches and verifies the official github/slack plugins before starting
   connectors. `use: github` then resolves to that plugin. **No config change**:
   the connection fields (`app`, `token`, `webhook`, `sweep`, `me`, `repos`,
   `identity`, `retry`, `project_map`, `api_base`) keep their names and are
   parsed by the plugin; `policy:` and dispatch `retry:` become generic
   host-owned connection keys.
3. **If a fetch fails** (network down at that boot): only the affected
   connector stays down, with a loud error and a background retry until the
   plugin is in place. Every other connector, trigger and run proceeds (Q12).
4. **Rollback** to the previous release works: it resolves `use: github` to
   its builtin and ignores the installed plugin record.

### 5.3 Open work items

- **#161 (Slack hand-over, draft): combined into #164, then closed.** After
  steps A–C are built out on #164, #161's work is carried over and #161 is
  closed:
  - The vendor-neutral half becomes commits on #164: the `detach:`, `repo:`,
    `branch:`, `images:` and `mode:` step fields, `HonorsLaunchFields`, the
    `ForceNoCheckout` fix and the detach launcher.
  - `TypeDecl.ValidateTrigger` becomes `plugin.validate`.
  - The Slack half (forms, shortcuts, the interactive ACK, `thread` /
    `download`) lands in the slack plugin in the companion plugins PR, since
    #164 moves Slack out of conductor. Downloaded files use the Q7 staging
    directory.
  - #161 is closed once both halves are in, with a pointer to #164.
- **#163-era hooks and verbs (merged).** Unchanged. The hooks are already
  generic flow config. `react` / `set_status` are plain plugin verbs.
  `run.start_sha` / `run.head_sha` come from `revision` + `reads_revision`.
  The own-status guard is plugin-internal and becomes durable through
  `host.state`.
- **Existing small plugins.**
  - sentry, fswatch, audiobookshelf, libation, aws-sqs and the ~80 others keep
    working unmodified. They speak protocol 1, use the generic event path, and
    ignore `describe {host}`. Nothing they use is removed.
  - Optional mechanical change: sentry's plural-alias facts
    (`connectors/sentry/main.go:116-121`) become declared `facts` /
    `match_keys`.
  - Engines (js, cel, lua, …) keep working. `abi: 1` is accepted and ignored,
    and `step_engine` is inferred from `kind: engine` for any Decl that lacks
    it.
  - Runtimes (paseo, jev) keep working. Their role is inferred from the verb
    set exactly as today, until they declare `runtime.role`.

---

## 6. What happens to the #164 / plugins-repo work

**Reused**

| Piece | Where it goes |
|---|---|
| `internal/expr` → `pkg/expr` | stays |
| `pkg/sourcekit.Filter` IR, JSON form, `ParseFilter`; `config.Filter` delegating to it | stays: it is the trigger runtime |
| `start_source.triggers` + routed events (`trigger` id, instance routing, guards) | stays, ungated, for every plugin |
| `Event.facts` / `match_keys` | stays, for every plugin |
| Plugin client: RPC errors no longer tear down the process; `StreamSource` supervision; per-instance event sinks | stays (§1.3) |
| `FetchRemoteVerified` / `release_verified` (the integrity precondition) | stays as install-time integrity; its trust consumer is deleted |
| `pkg/githubkit`, `ghsource`, `ghplugin` | move to conductor-plugins (P) |
| `ghfake` (stateful GitHub, OpenAPI-validated responses, GraphQL, webhooks, admin API) | moves to conductor-plugins (P) and backs the github conformance and e2e |
| `ghsourcetest` cases and the `Starter`/`Driver`/`Want`/`Diff` runner | runner generalized into `pkg/plugintest`; GitHub cases move to P |
| e2e plugin mode (`E2E_GITHUB=plugin`, staged binary) | becomes the default once C lands |

**Discarded**

- `ConnectorABI`.
- The methods `plugin.nudge`, `plugin.force`, `plugin.app_token` and `plugin.target_head`. They are replaced by `plugin.poll` and declared verbs.
- `VerbSweep` and its intercept.
- `trusted_source`, `SourceTrusted` and `IsOfficialSource`'s event-trust use.
- `kindFor`'s trust branch.
- `RegisterExternalConnectorInPlaceOfBundled`.
- `identitySource` / `dispatchTuner`.
- `lowerEngineOptions`.
- `internal/connector/github.go` and the `internal/integrations/github` adapter.
- The trust-model docs and tests added in the WIP commits.

**#164 itself** carries all of it: this document, then steps A, B and C as
commits on the same branch. The superseded WIP work (the `trusted_source`
trust model, ConnectorABI) is removed by those commits within the PR, and the
GitHub fake moves out to the plugins repo in step P. The plugins-repo companion
draft carries step P.

---

## 7. Decisions (formerly open questions)

All resolved on review: Q5 as discussed, the rest as recommended.

| # | Question | Decision |
|---|---|---|
| Q1 | Should an `assigned` claim be believed for every installed plugin, bounded by the operator's `scope` (§2.4)? | **Decided:** yes: install was the trust decision, and scope is defense in depth |
| Q2 | Must-understand for unknown `semantics` keys (refuse the plugin) vs warn-and-ignore? | **Decided:** refuse, with `optional: [keys]` as the escape hatch |
| Q3 | paseo's forge path (X1): opaque `runtime_hints` vs always branch-off | **Decided:** `runtime_hints` now; revisit when paseo exposes a forge-neutral worktree call |
| Q4 | Legacy `integrations:` / `handoffs:` / `notify:` blocks and `internal/migrate` | **Decided:** removed in the release. Run `conductor migrate` with the current release first; the new binary's `conductor validate` names any legacy block still present |
| ~~Q5~~ | **Decided:** tunnels and relays are connectors declaring `exposes` (V4–V4c). `lan` and `tunnel` (any tunnelling command) are the vendor-neutral builtins, and a fixed origin stays the web connector's `base_url`; a plugin may always reach the local address it is handed; straight cutover with no compatibility shim. The web approve/revise page stays a core surface with no tunnel code | — |
| Q6 | rest/graphql `InstanceDecler` (instance-specific verbs) | **Decided:** `plugin.describe {instance}` (optional) |
| Q7 | Binary verb outputs (Slack `download` → agent `images:`): the wire cannot carry `BinaryOut` today | **Decided:** the host gives each instance a staging directory inside its fs capability; outputs return paths under it, and the engine accepts only those |
| Q8 | The GitHub write credential via `gh auth token` inside a confined plugin (needs `commands: [gh]` and read access to gh's config) | **Decided:** declare it in the github plugin's capabilities; the `pat` / `token:` paths need neither |
| Q9 | `host.state` limits and lifetime | **Decided:** per-instance quota, entries survive restarts, dropped when the instance is removed |
| Q10 | In conductor-plugins, `internal/` or `pkg/` for githubkit and the fake? | **Decided:** `internal/` until a third party asks. Conductor's own e2e runs the fake and the github plugin as built binaries or containers, never as a Go import, so conductor never depends on conductor-plugins' code (that repo already depends on conductor's SDK) |
| Q11 | Discord hand-off: plugin in P, or drop it? | **Decided:** plugin in P (it is small), so nothing regresses |
| Q12 | The release that removes the builtins is the same one that must install their replacements (one PR, one release) | **Decided:** plugins-first boot: the release fetches and verifies the official plugins before starting connectors. A failed fetch keeps only that connector down, loudly, with a background retry; nothing else is affected. `conductor validate` on the new binary reports it in advance. Rollback to the previous release works |
| Q13 | Where plugins and conductor itself are fetched from | **Decided:** git only, no vendor lock-in and no `gh` (X2). The release workflow publishes binaries on `refs/dist/<tag>`; any git host; private repos via the daemon's git credentials. **Accepted operator requirement:** for a private source, the daemon must run with git credentials it can reach on its own (a deploy key, or a credential helper). An SSH agent from a login session is not visible to a background service. This is documented in the install docs, and `conductor validate` reports a source it cannot fetch |
