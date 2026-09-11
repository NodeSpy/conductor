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
    allow:
      repo:    ["${trigger.repo}", "org/docs"]
      channel: ["#code-reviews"]
      store:   ["shared-kv"]
```

`allow_targets` → `allow.repo`, `allow_stores` → `allow.store`, `allow_secrets` →
`allow.secret` become **back-compat aliases** (kept, documented as legacy). `channel`
and any future dimension now fall out of the same map with no new field.

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
