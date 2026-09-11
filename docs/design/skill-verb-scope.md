# Per-verb, connector-driven resource scoping (the `channel` gap, generalized)

Round-3 flagged one item as not-built: an agent granted `slack.*` can post to **any**
channel the token reaches. The reason it had no home is the real finding: conductor
conflated two orthogonal axes and only had a mechanism for one.

- **Verb access** — *which verbs* an agent may call. That's `skill.verbs`
  (`slack.post`), already deny-by-default.
- **Resource scope** — *which resource* a call may name in a specific option:
  `channel` for slack, `repo` for github, `store` for kv, `path` for blob. This is a
  constraint on an option **value**, not on verb access.

Today the second axis is hardcoded: `internal/flow/resources.go` knows the literal
option names `repo`/`store`/`secret` and flat `allow_targets`/`allow_stores`/
`allow_secrets` lists. That doesn't scale — every connector invents its own resource
dimension, and `channel` has nowhere to live. The fix is to make the scope check
**connector-driven** and attach the grant **to the verb** (the verb is where the
option, and its meaning, comes from).

## The two things the connector owns

A verb's option schema gains one field (`pkg/plugin` `Field`, and the builtin
connector schema):

```go
"channel": {Type: "string", Required: true, Scope: "channel"},  // slack.post
"repo":    {Type: "string", Required: true, Scope: "repo"},     // github.*
"store":   {Type: "string",                 Scope: "store"},    // kv.*/sql.*
"text":    {Type: "string", Required: true},                    // NOT scoped — content
```

- **`Scope string`** — a non-empty value marks this option as a *destination/resource*
  and names its dimension. Content options (`text`, `body`) stay unmarked and are never
  gated.
- **`ContextScope(dim, trigger) string`** — a small adapter hook: given a dispatch,
  return the implicitly-allowed value for a dimension. github → the trigger's target
  repo; slack → the channel the event came from (or the configured default); empty when
  the trigger carries no such value. This is what makes "act on your own PR / your own
  channel" work with zero config.

The connector is the *only* thing that knows `channel` is a destination and `text`
isn't — so the knowledge lives there. New connectors get scoping for free by tagging an
option; core stays generic.

## Two surfaces, one primitive

### Skill grant — per-verb (the shape the user settled on)

`skill.verbs` goes polymorphic (like `runtimes:`/`models:` elsewhere in this redesign):
a plain **list** for access only, a **map** (`verb → per-option allowlists`) to scope.
The option keys are that verb's *own* options — namespaced by the verb, so `channel`
under `slack.post` and `repo` under `github.submit_review` never collide.

```yaml
# access only — scoped options fall back to the dispatch's own context
skill: { verbs: [slack.post, github.submit_review] }

# scoped
skill:
  verbs:
    slack.post:           { channel: ["#code-reviews"] }   # only here (+ dispatch's own)
    github.submit_review: {}                                # repo auto-limited to the PR
    kv.*:                 { store: ["shared-kv"] }          # pattern → each matched verb
```

- A scoped option **not listed** on a granted verb defaults to `ContextScope` (the
  dispatch's own repo/channel) — deny everything else.
- Listing widens: `channel: ["#x"]` allows `#x` **and** the context value.
- A constraint on an option the verb doesn't declare as `Scope` → **load error** (typo
  catch).

### Plan surface — policy-level, same dimensions

Agent-authored *plans* don't enumerate verbs up front, so they keep a policy-level
allowlist — but generalized off the connector's dimensions instead of hardcoded names:

```yaml
policy:
  agent_authored:
    allow_scopes:
      repo:    ["org/docs", "acme/*"]   # the trigger's OWN repo is allowed automatically
      channel: ["#code-reviews"]
      store:   ["shared-kv"]
```

`allow_targets` → `allow_scopes.repo`, `allow_stores` → `allow_scopes.store`,
`allow_secrets` → `allow_scopes.secret` are **back-compat aliases** (kept, unioned in,
documented as legacy). `channel` and any future dimension fall out of the same map with
no new field.

**Matching is ContextScope + literal/glob — NOT template variables.** The dispatch's own
resource is allowed with *nothing listed*: `ContextScope` returns the trigger's target
repo (and a connector's own event channel) and it's implicitly in scope. You never write
`${trigger.repo}` — you list only *additional* destinations. Allowlist entries match
exactly, as `*`, or as a `path.Match` glob (`acme/*`, `#team-*`). There is no `${…}` /
`{{…}}` interpolation of allowlist values; a literal `${trigger.repo}` would match a repo
named exactly that (i.e. never). Rely on ContextScope for the triggering resource; use a
glob for a family.

### The chokepoint

Generalize `resources.go`'s `targetOK`/`storeOK`/`secretOK` into one
`scopeOK(dim, value)` that consults `ContextScope(dim) ∪ allow[dim]`. Both the plan
surface (`checkVerbResources`) and the skill surface (`RunSkillVerb`) walk **the called
verb's connector-declared scoped options** and call it — no surface hardcodes an option
name. This is the same single-enforcement-point + meta-test shape round 3 used to close
the other classes.

## Defaults & invariants

- **Deny-by-default** on both surfaces: a scoped option value must be `ContextScope` or
  explicitly allowed.
- **No context value + not listed → deny** (a fixed-channel post whose trigger isn't a
  slack event must list the channel). Strong default; opening it is a one-line grant.
  (Chosen over allow-with-warning — no footgun-as-default.)
- **Operator-authored `uses:` steps are NOT gated** — the operator wrote the config with
  their own credential. Same trusted/untrusted split that exists today; only
  agent-authored plans and skill grants are scoped.
- `trust: full` continues to lift the plan-surface allowlists, unchanged.

## Meta-test (the deliverable)

A test that, for **every** connector, enumerates each verb's `Scope`-tagged options and
asserts both surfaces refuse an out-of-context value that isn't allow-listed — failing
if a new connector adds a scoped option that either surface forgets to enforce. This is
what stops the class from reopening (a future `jira.comment { project }` or
`s3.put { bucket }` is covered the moment it declares `Scope`).

## As built — one deviation, and what got tagged

**`allow` → `allow_scopes`.** The plan-surface map above is spelled
`policy.agent_authored.allow_scopes: {repo: …, channel: …}`, not `allow:`.
`allow:` already exists and means the *other* axis — the verb/step-class access
allowlist (`allow: [code, kv.*, gh.comment]`). Making it polymorphic would leave
any config that needs BOTH access control and resource scoping unable to express
one of them, which is the common case. The prose meaning is unchanged: read
"allow.repo" as `allow_scopes.repo`. `allow_targets`/`allow_stores`/
`allow_secrets` remain aliases of `.repo`/`.store`/`.secret`, unioned in.

**Tagged** across the builtins: github `repo` (35 verbs), slack/discord
`channel` and `user` (a DM is a destination too), kv/sql `store`, vault `key`
(dimension `secret` — it answers to both `k` and the qualified `vault/k` that
`allow_secrets` and `{{ vault … }}` already use), blob `path`, ntfy `topic`,
the discord relay sink's `channel_id`.

**Deliberately not tagged**, because a dimension nothing can answer is a
dimension that denies every call rather than scoping it:
- `webhook.post url` — arbitrary egress, not a resource inside a namespace the
  connector owns; the SSRF guard and the egress machinery own that question.
- `memory scope` — already has its own dimension and its own semantics
  (`memoryScopeOK`: unscoped means deny, the triggering repo's scope is
  implicit).
- github gist ids, and repo-relative `path` on `file`/`put_file` — the repo
  dimension already bounds the paths; gists are user-scoped with no dimension
  defined yet. Both are one-line follow-ups now that the mechanism exists.

`ContextScope` for the repo dimension is answered in core for every connector
(`core.Trigger.Target.Repo`) rather than by a github-specific hook, so a
`repo:` option on any connector gets the same treatment. Below the hook, a
connector's own configured default option value is implicitly in scope — that
is what makes "the configured default channel" work without per-connector code.

## Round 5 — allowlist entries are parameterizable

The doc above describes allowlist entries as literals, and they no longer are:
an entry may carry `${settings.NAME}` (substituted at load from the main
config's own top-level `settings:` block, the same mechanism packs had) or
`{{ .fact }}` (rendered per dispatch against that event's trusted facts, with
a restricted function set, no secrets, no agent input, and fail-closed).
Matching is unchanged — literal or glob, on whatever the entry resolved to.
See docs/design/scope-templating.md.

## Round 4 — three fixes, and the semantics they pinned

- **F3.** The skill surface enforced through the PLAN policy, which is nil
  with no `policy.agent_authored` block (and under `trust: full`) — so a grant
  spelling out `{channel: ["#x"]}` did no scoping at all in such a config. The
  grant's scoping is intrinsic to the grant: it now resolves through
  `skillResourcePolicy`, which is never nil. A policy only WIDENS. `trust:
  full` is plan latitude and does not lift a constraint the operator wrote
  onto a named verb; `allow_scopes` still widens under it, so the escape hatch
  stays one line. The plan surface's nil path stays as it was and is moot —
  `guardPlan` refuses an agent-authored plan outright with no policy block,
  which `TestAgentAuthoredPlansNeedAPolicy` now pins.
- **F2.** The override path deep-merges maps, so once `skill.verbs` grew a map
  form a consumer could no longer NARROW a bundled grant — it failed open in
  the new spelling only. Permission sets are now replaced whole whichever form
  they take (`permissionSets` in pack_refs.go).
- **F1.** A secret's identity is (vault, key). The flat `allow_scopes.secret`
  list was matched bare, so `["house/prod-token"]` also authorized
  `shared.read {key: "house/prod-token"}`. Secret matching is now
  connector-bound: a qualified entry reaches only the vault it names; a bare
  entry names a key within whichever vault is calling.

## Tests
- `slack.post` skill grant with `channel: ["#x"]` → post to `#x` ok; post to `#y`
  refused; post to the dispatch's own channel ok with no list.
- `github.submit_review: {}` → refused for a repo other than the dispatch's PR.
- constraint on a non-`Scope` option (`text`) → load error.
- plan surface: `allow.channel` governs a slack step; `allow_targets` still works as the
  `allow.repo` alias.
- back-compat: an existing config using `allow_targets`/`allow_stores` loads and behaves
  identically.
- meta-test green; `gofmt -l`/`go vet ./...`/`go test ./...` green.
