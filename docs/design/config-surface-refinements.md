# Config-surface refinements — one cohesive batch

Six naming/ergonomics refinements to the agent-policy and pack surfaces. All are
"make the config say what it means, consistently, with sane defaults." None
loosens a consent/security boundary. Decisions taken with the maintainer:

- **Hard rename**, no back-compat aliases (v0.9.0 has not shipped; the
  `agents:`→steps migration is already the breaking release). Update
  `config.example.yaml`, the wiki, the migration emitter, and every meta-test.
- **Pin: document, don't enforce** for enable-all (#5).

Everything here must keep the closed security classes closed: the raw-`Target`
AST enforcement (`TestEveryDispatchTargetReadIsAuditedOrRouted`), per-verb scope
on both surfaces, memory-scope on every agent face, pack connector + workflow-ref
boundaries, `TargetTrusted`. Re-run all class-closer meta-tests green.

---

## 1 & 2. `policy.agent_authored` → one per-verb `verbs:` map (folds `allow_scopes` + `allow_memory_scopes`)

The plan surface currently has three shapes for one idea (*which verbs, on what*):
`allow:` (access list) + `allow_scopes:` (flat per-dimension map) +
`allow_memory_scopes:` (a separate list). Unify to the **same `verbs:`
list-or-map shape `skill.verbs` already uses** — scope rides WITH its verb.

Before:
```yaml
policy:
  agent_authored:
    allow:   [ kv.*, memory.*, gh.comment, gh.submit_review, code, cli ]
    allow_scopes: { repo: ["org/docs"], channel: ["#code-reviews"], store: [cache] }
    allow_memory_scopes: [ "repo:acme/shared" ]
```
After:
```yaml
policy:
  agent_authored:
    verbs:                                     # was `allow:`
      gh.comment:        {}                     # allowed, no resource scope
      gh.submit_review:  { repo: ["org/docs"] } # allowed + scoped, per verb
      slack.post:        { channel: ["#code-reviews"] }
      kv.*:              { store: [cache] }
      memory.*:          { scope: ["repo:acme/shared"] }   # was allow_memory_scopes
      code:              { store: [cache], scope: ["repo:acme/shared"] }  # run:code ctx.* data scopes
      cli:               {}
    approve: [ cli, code, gh.merge ]            # SEPARATE axis (needs-a-human), unchanged
```
- `verbs:` accepts a **list** (access only, scopes default to ContextScope) or a
  **map** (`verb-or-class → {dimension: [values]}`), exactly like `skill.verbs`.
  Share the parser/type with `skill.verbs`.
- Keys are verb patterns (`gh.*`, `kv.*`) AND step classes (`code`, `cli`,
  `agent`, `workflow`). Step classes carry no connector scope except `code`,
  whose `store`/`scope` entries are the `run:code` `ctx.kv`/`ctx.memory` data
  allowlists (replacing the DataGuard's read of `allow_scopes.store` +
  `allow_memory_scopes`).
- **Enforcement moves per-verb**: `planResourcePolicy`/`checkVerbResources` derive
  a called verb's allowlist by matching the verb against the `verbs:` map (same
  `matchAny`/`ScopesFor` machinery as skill), instead of the flat `ScopeAllow()`.
  Deny-by-default, ContextScope-implicit, `TargetTrusted`, `trust: full`-lifts —
  all unchanged in behavior.
- **REMOVE** (hard): `Allow`, `AllowScopes`, `AllowMemoryScopes`, `AllowSecrets`,
  `AllowStores`, `AllowTargets`. Update the migration, config.example, docs, and
  the scope/memory meta-tests to the new shape. `secret`/`store`/`repo`/`channel`/
  `path`/`scope` dimensions are unchanged — only their config HOME moves.

## 3 (#6). Auto-bind the sole connector of a required type

A pack's `requires.connectors: { github: "*" }` need not be bound when the
consumer has exactly one connector of that type.
- For each `requires.connectors` entry not explicitly bound in
  `packs.<name>.connectors`: if **exactly one** consumer connector of that type
  satisfies the version constraint → bind it automatically; **2+** → require the
  explicit binding (error naming the candidates, as today); **0** → dormant/error
  unchanged. Explicit binding always wins; the sole candidate must satisfy the
  constraint (clear error if not — never silently bind an incompatible one).
- Match by connector **type** (the instance's `use:`), not name.
- **Trigger arming consent (`repos:`) stays explicit** — auto-bind is connector
  plumbing only, not consent.

## 4 (#8). Trust globs may omit `github.com/`

`pack_trust`/`plugin_trust` entries accept the same host-defaulting `use:`/pack
sources already use.
```yaml
pack_trust:
  allow: [ your-org/*, acme/review-kit, gitlab.com/team/* ]   # first two default to github.com
```
- An entry with **no recognized host/scheme prefix** defaults to `github.com/`,
  using the **same host-default function as the `use:`/source resolver** (one
  canonicalizer, shared, so they can't drift). A host that IS written is used
  as-is.
- Normalize BOTH the pattern and the source through that canonicalizer before
  matching. The segment-anchored `*` (does not cross `/`) is preserved — this is
  normalization only, it does not re-open the typosquat class.

## 5 (#N). `triggers: { "*": … }` — arm all pack triggers with one consent

```yaml
packs:
  review:
    triggers:
      "*": { repos: [your-org/app] }      # arm EVERY trigger the pack ships, on these repos
      deploy: { repos: [your-org/infra] } # a named entry overrides/refines that one
```
- The `"*"` key supplies shared arming defaults (repos/filters/gate) to every
  trigger the pack ships; a named key overrides for that trigger. Filters/events
  remain the pack's (it defines them); the operator supplies only the repo
  consent, once.
- **Document** (do not enforce): with a floating version range, a new trigger in
  a later pack release arms on the consented repos at the next `init`/update; the
  lockfile diff surfaces it. An exact pin freezes the set. A stricter
  "exact-pin-required-for-`*`" protection is a later, optional add.

## 6 (#last). Trigger instances — object-or-array under a name

The same pack trigger, armed more than once with different gates/filters/repos.
```yaml
packs:
  review:
    triggers:
      review: { repos: [team/app] }         # object → ONE instance (today's shape)
      deploy:                               # array → N instances of the SAME trigger
        - { repos: [team-a/*], gate: { approve: true } }
        - { repos: [team-b/*], gate: { approve: false }, filter: { label_any: [urgent] } }
```
- `packs.<name>.triggers.<name>` value is polymorphic: **object** (one instance)
  or **array** (N instances). Detect array-vs-object at unmarshal. Each instance =
  `repos`/`filter`/`gate` layered on the pack's base trigger definition.
  (`filters:` was the key when this was written; it is `filter:` as shipped —
  see unified-filter-phase2.md.)
- **Per-instance identity by index** — each instance gets a distinct identity
  (`<pack-ns>/<trigger>#<i>`) feeding `Trigger.Key`/dedup/session/outcome, so two
  instances of one trigger never collide. (Additive to the hardened `Trigger.Key`
  machinery — more granular, not a loosening.)
- **Addressed by index** (`deploy[1]`) for a mirrored overlay — no instance
  names. Closes the old round-1 M4 "overlay can't address array instances" gap.
- Composes with `"*"`: `"*"` sets defaults for all; a named array overrides/adds
  instances for that one.

---

## Verification
- `gofmt -l`/`go vet ./...`/`go test ./...` green. The `agents:`→steps migration
  emits the new `verbs:` shape and round-trips.
- ALL class-closer meta-tests green (raw-`Target`, per-verb scope both surfaces,
  memory-scope every face, pack connector + workflow-ref boundaries, TargetTrusted,
  trust-glob) — the rename must not reopen any.
- Tests: plan-surface `verbs:` map enforces per-verb scope identically to the old
  `allow_scopes`; `code: {store,scope}` gates `ctx.*`; auto-bind picks the sole
  connector and errors on 2+; host-omitted trust globs match; `"*"` arms all;
  an array trigger yields N distinctly-identified armed instances.
