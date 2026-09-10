# Runtimes, models/fleets, packs — and removing `agents:`

Status: DESIGN — approved by the maintainer in conversation, to be implemented on
this branch (`feat/runtimes-fleets-packs`, off PR #60 `feat/remote-plugin-fetch`)
and merged into the PR.

This builds directly on the `use:` unification already merged into #60
(`docs/design/use-unification.md`). Read that first — the resolution rules,
app-extension install model, and permission manifest are unchanged and reused
here verbatim. Nothing below re-litigates `use:`.

The north star: **conductor is a platform, not a paseo wrapper.** Model discovery,
defaults, and selection must work across ANY runtime (paseo, agent-deck, a bare
CLI, one nobody has built yet) without conductor baking in how a single tool does
it. The mechanisms below were validated against the real tools on the maintainer's
box — see "Evidence" at the end; do not weaken them back to a paseo-only path.

---

## 1. `runtimes:` — the single agent-execution section (`agents:` is removed)

`agents:` is deleted. Its two responsibilities split cleanly:

- **which model to run** → chosen per step via *fleets* (§2), resolved against a
  runtime's auto-discovered *roster* (§3). The old `provider:`/`model:` pair on an
  agent collapses to a single `model:`.
- **how the agent behaves** (guidance, skill, session, memory, workspace, timeouts,
  archive, host, isolation) → moves onto the **step/action** that dispatches the
  work, reused across steps via the existing `extends:` mechanism.

### 1.1 Grammar — one plural key, polymorphic value

`runtimes:` (plural, like `connectors:`/`stores:`/`packs:`). The value is one of:

```yaml
runtimes: paseo                       # scalar → one runtime (the common case)
```
```yaml
runtimes: [paseo, claude]             # list → several; each item key-implies-use
```
```yaml
runtimes:                             # map → named runtimes, with config
  paseo:
    models: { prefer: [claude-opus-5, gpt-5.6-sol] }
  claude:
    models: { allow: [claude-opus-5, claude-sonnet-5] }
```

Rules (identical in spirit to `use:` elsewhere):

- **string** = one runtime by name; **list** = several (items are names or
  `{ use:, models:, … }` objects); **map** = named runtimes → config.
- **key-implies-`use:`**: a runtime's name IS its `use:` reference when they match.
  Write `use:` only when the name differs from the implementation, or to point at a
  plugin repo / URL / local path. `runtimes: paseo` needs no `use:`.
- Everything `use:` supports (builtin → official `NodeSpy/conductor-plugins/runtimes/<name>`
  → explicit repo → URL → local path, plus `@version`) applies unchanged.

### 1.2 The `models:` block on a runtime (all optional)

```yaml
runtimes:
  paseo:
    models:
      default: claude-opus-5      # OPTIONAL. Omitted → bare launch (see §4).
      prefer:  [claude-opus-5, gpt-5.6-sol]   # ranking among acceptable models
      allow:   [claude-opus-*, gpt-5.6-*]     # allowlist; roster is filtered to this
```

- `default:` — optional; its ABSENCE is meaningful (§4: bare launch).
- `prefer:` — ranking used when a fleet offers a choice; consumer "disposes."
- `allow:` — restricts what this runtime may ever run (supports wildcards, §2.1).

Omit the whole block and the runtime is fully auto: roster discovered, default =
bare launch, no restrictions.

### 1.3 Existing `RuntimeConfig` fields

Keep the existing operational fields (`bin`, `host`, `default`, isolation, etc.).
`default: true` (which runtime is picked when a step names none) stays. Add the
`models:` block. Do NOT remove anything already shipped in #60's runtimes.

---

## 2. Fleets — named acceptable-model lists (spot-fleet model)

A fleet is a named, ranked list of acceptable models plus a fallback posture. Packs
(and configs) reference a fleet instead of hardcoding a model, so the same config
runs on whatever the consumer actually has.

```yaml
models:                                   # top-level (or inside a pack, §5)
  reviewer:
    any: [claude-opus-*, gpt-5.6-*, gemini-3.*-pro]   # acceptable, best-first
    required: true      # nothing available → HARD ERROR at load (don't downgrade a reviewer)
  summarizer:
    any: [claude-haiku-*, "*-mini", gemini-2.5-flash*]
    required: false     # nothing available → bare launch (runtime's own default)
```

- `any:` — ordered acceptable list; literals and wildcards (§2.1).
- `required:` — `true` → no acceptable model in the roster is a load-time error
  naming the fleet + acceptable-vs-available; `false` → fall through to bare launch.

### 2.1 Wildcards in `any:` (and `allow:`)

Patterns are globbed against the **resolved roster** (§3) — a wildcard never
conjures an unavailable model.

- Forms: `claude-opus-*`, `gpt-5.6-*`, `"*-mini"`, `"*pro*"`, and the full
  wildcard `"*"` (match everything).
- **YAML gotcha (enforce in docs + examples):** a pattern that STARTS with `*`
  must be quoted — bare `*` is a YAML alias indicator. Quote `"*"`, `"*-mini"`,
  `"*pro*"`.
- **Ordering:** literals keep their listed priority; a wildcard expands in place,
  newest-first by the catalog (§3.3), de-duped against literals already listed.
  Then the consumer's runtime `prefer:` ranks the final acceptable set (consumer
  disposes).
- A wildcard that matches nothing contributes nothing → fall through to the next
  entry, then `required:`/bare-launch.
- `"*"` = "any available model, honoring `prefer:`". This DIFFERS from bare launch
  (§4): `"*"` resolves to a concrete model via `prefer:`; bare launch passes no
  model at all. `required: true` with `"*"` is unsatisfiable only when the roster
  is genuinely empty.

### 2.2 `model:` on a step — string | array | object

```yaml
steps:
  - name: architecture
    model: reviewer                                   # named fleet (map-key-wins)
  - name: security
    model: [claude-opus-*, gpt-5.6-*]                 # inline array = { any:[...], required:false }
  - name: deep-audit
    model: { any: [claude-opus-*], required: true }   # inline object
  - name: summarize
    model: gemini-2.5-flash                           # exact pin (single model)
  - name: lint
    model: "*"                                        # any available, honoring prefer
```

- **string** → map-key-wins: if it names a key in `models:` it's a fleet reference;
  otherwise it's a model id or wildcard.
- **array** → inline fleet, sugar for `{ any: [...], required: false }`.
- **object** → full `{ any, required }`.

### 2.3 Resolution ladder (per fleet reference)

1. **consumer override** (`packs.<pack>.models.<fleet>: <model>`, §5) → use it.
2. else **intersect** the fleet's `any:` (wildcards expanded) with the union of the
   configured runtimes' rosters, ranked by `prefer:` → run the winner AND the
   runtime that offers it.
3. else if `required: false` → **bare launch** (§4).
4. else (`required: true`, no match) → **hard error at load**, naming the fleet and
   acceptable-vs-available.

A model id maps to a runtime via the roster (which runtime lists it); conductor
runs the winning model on that runtime. Ties/overlaps broken by runtime `default:`
then declaration order.

---

## 3. Model discovery — a per-runtime adapter capability

Discovery is NOT one mechanism. It is a per-runtime adapter method
(`list_models()`), each runtime implementing it however it can. A runtime that
cannot enumerate contributes nothing to fleets/wildcards and simply runs its
bare-launch default. **Do not funnel discovery through paseo.**

### 3.1 Per-runtime strategies (implement these three; make the seam pluggable)

- **paseo** → native. Ask paseo directly (`paseo provider ls` +
  `paseo provider models <provider> --json`). paseo owns it; no models.dev, no
  key handling.
- **agent-deck** → resolve its **profiles → provider**, then models.dev (§3.3).
  A profile identifies the provider/account behind it; models.dev turns that
  provider into a model list + metadata.
- **bare CLI (claude / codex / gemini)** → try the provider's own API with the
  tool's stored creds FIRST; on success use that (account-scoped truth), else fall
  back to models.dev. Concretely:
  - claude: `GET https://api.anthropic.com/v1/models` with the OAuth token from
    `~/.claude/.credentials.json` (`claudeAiOauth.accessToken`), headers
    `authorization: Bearer …`, `anthropic-version: 2023-06-01`,
    `anthropic-beta: oauth-2025-04-20`. This WORKS today (proven).
  - codex: `~/.codex/auth.json` is `auth_mode: chatgpt`; `GET /v1/models` returns
    `403 missing scopes: api.model.read`. There is no live call — fall through to
    models.dev.
  - gemini: Google's models API needs an API key / registered identity; the CLI's
    OAuth token doesn't authorize it — fall through to models.dev.

The adapter interface is the contract; the three above are the initial
implementations. A new runtime implements `list_models()` its own way or leaves it
empty (declared/bare-launch fallback).

### 3.2 Default is per-runtime too — and its absence is the answer

No source (live API, SDK, or models.dev) marks a cross-provider default; the live
`/v1/models` response carries no default flag. So conductor does NOT try to
discover "the default model." Default resolves as:

1. explicit `models.default:` (or a resolved `prefer:` hit) → pass that model;
2. nothing declared/resolved → **bare launch** (§4).

`prefer:` (already needed for fleet ranking) also picks which provider/model is the
effective default when several are available. `default:` is the explicit override.

### 3.3 models.dev — the public catalog (primary for the fallback path)

`https://models.dev/api.json` — public, no auth, ~4.5MB, 213 providers, current
(carries `claude-opus-5`, `gpt-5.6-sol`, etc.) AND rich metadata (context window,
pricing, modalities) that neither the SDK types nor `/v1/models` provide.

- Use it as the **catalog + metadata source** for the agent-deck and CLI-fallback
  paths, and to enrich any roster (context/pricing for reports/budgets).
- **Cache** it (state dir, with a TTL, e.g. 24h) — do not fetch 4.5MB on the hot
  path. Degrade-safe: on fetch failure use the last cached copy; if none, the
  affected runtimes simply can't enumerate (bare-launch still works).
- The live provider API is an OPTIONAL entitlement *filter* layered on top where
  creds allow (claude): it narrows the catalog to what the account can actually
  run. It is never required.
- LiteLLM's `model_prices_and_context_window.json` is an acceptable secondary
  source if models.dev is unreachable; not required for v1.

Secret handling: read cred files to mint the Authorization header only; never log,
echo, or persist token values. Redact in any error/debug output.

---

## 4. Bare launch — a first-class resolution outcome

"Default = the absence of a model." When resolution lands on bare launch, conductor
dispatches the agent with **no `--model` override**, letting the runtime use its own
built-in default. This is NOT an error path:

- a runtime whose models we cannot enumerate at all still works — you just can't
  `prefer:`/`allow:`-filter it, and it runs its own default;
- a fleet with `required: false` and no roster match → bare launch;
- omitting `models.default:` → bare launch.

Contrast with `model: "*"` (§2.1), which resolves to a concrete preferred model.

---

## 5. Packs — drop-in, connector-owned scope, addressable internals

Builds on the existing packs implementation (issue #53, `pack*.go`). Changes:

### 5.1 Reference a pack once, via the `packs:` key (key-implies-`use:`)

No separate top-level `use:` list for packs. The `packs:` map key IS the reference
(official lookup `NodeSpy/conductor-packs/<name>`); `use:` only to point elsewhere.

```yaml
packs:
  pr-review-team:              # key ⇒ use: pr-review-team (official)
    # …consumer overlay (below)
  house-style:
    use: ./packs/house-style   # local folder
```

### 5.2 Scope lives on the CONNECTOR, not the pack

A pack bundles triggers from possibly several sources (github, gitlab, pagerduty…).
`repos:` is meaningless to a pagerduty trigger, so it CANNOT be a pack-level field.
Each trigger binds to the connector **of its own source type**; scope (repos, pd
service) lives on that connector, configured once.

```yaml
connectors:
  github:    { repos: [me/app, me/api] }   # scopes the github-sourced triggers
  pagerduty: { service: PROD }             # scopes the pagerduty-sourced trigger

packs:
  incident-responder: {}                   # each trigger auto-binds to its source's connector
```

- **Disambiguation** when the consumer has >1 connector of a type:
  `packs.<name>.connectors: { github: work-github }` (only for the ambiguous type).
- **Missing connector** (pack has a pagerduty trigger, consumer has no pagerduty):
  that trigger stays **dormant + surfaced** as a load-time notice; the rest of the
  pack runs. Degrade-safe. A pack author MAY mark a source `required` to turn the
  notice into a hard error.

There is NO pack-level `repos:` and NO `instances:` key. Both were removed as
mis-leveled; see §5.4/§5.5.

### 5.3 Overriding pack internals — mirrored-section deep-merge

The `packs.<name>:` block MIRRORS the pack's own sections; keys deep-merge onto the
pack by name, using the EXISTING `extends:`/additive-guidance merge machinery
(#149–#151). No pack-specific override language.

```yaml
packs:
  pr-review-team:
    on:                                        # its triggers, by qualified name (§5.4)
      github.pull_request: { filters: { labels_not: [wip] } }   # ADD a filter (merge)
      gitlab.merge_request: { enabled: false }                  # turn one off
    steps:                                     # its steps, by name
      security:  { guidance: "focus on authz + SSRF" }          # ADDITIVE — appended
      summarize: { enabled: false }
    models:                                    # its fleets, by name
      reviewer: claude-opus-5                  # override the fleet (top of the ladder, §2.3)
    connectors: { github: work-github }        # instance disambiguation (§5.2)
```

Merge semantics (reuse existing): deep-merge by key; `guidance:` is appended
(additive), not replaced; `enabled: false` disables any trigger/step; consumer
wins on conflict. Only NAMED members are addressable — packs must name their
steps/triggers/fleets (anonymous inline steps can't be targeted).

### 5.4 Triggers are named/qualified — the key is the address

A bare event name (`pull_request`) is not an identity. The trigger's map key is its
stable address, and a `source.event` key implies `on:`:

```yaml
triggers:
  github.pull_request:  { steps: [architecture, security] }   # key ⇒ on: github.pull_request
  gitlab.merge_request: { steps: [architecture, security] }   # distinct source → distinct key
  pagerduty.incident:   { steps: [triage] }
```

- key is a valid `source.event` → implies `on:` (disambiguates cross-source for free).
- need TWO triggers on the same `source.event` (e.g. review + autolabel, both
  github PRs) → give them free names and set `on:` explicitly:
  ```yaml
  triggers:
    review:    { on: github.pull_request, steps: [architecture, security] }
    autolabel: { on: github.pull_request, steps: [label] }
  ```
- dedup/state keys off the qualified trigger identity (name) + event, as today.

### 5.5 Trigger instances — array vs object, no keyword, no names

A trigger's value is polymorphic (same detection as `runtimes:`/`model:`):

- **object** → one trigger.
- **array** → multiple instances; each element **deep-merges onto the base** and
  fires independently.

```yaml
# pack ships the base (object)
triggers:
  review: { on: github.pull_request, steps: [architecture, security] }
```
```yaml
# consumer fans it out — array = instances; base supplies on:/steps:, each adds only what varies
packs:
  pr-review-team:
    triggers:
      review:
        - { repos: [me/app, me/payments], filters: { labels: [ready] } }
        - { repos: [me/api] }
```

- **No `instances:` keyword. No `extends:`/`abstract:` ceremony for this. No names.**
  An instance's identity is its CONTENT (its `repos:`/`filters:`) — that is what
  makes it distinct. Dedup keys off the event + that scope, not a label.
- Instance `repos:`/`filters:` are a NARROWING within the connector's scope — they
  can only subset what the connector covers, never widen it.
- Cross-connector variation is the same shape: an instance that sets
  `connectors: { github: work-github }`.

---

## 6. Migration (`internal/migrate/`) — `agents:` → runtimes/fleets/steps

Add a migration alongside the existing `type:`→`use:` one (`internal/migrate/use.go`),
same fail-safe contract: transform, then re-validate; if the result does NOT
validate, REFUSE and restore the original, emitting a clear message. Never leave a
half-migrated config (a config-incompat crash-loops the auto-updating fleet — see
the repo's degraded-boot invariant).

Mechanics (implementer to confirm exact homes against current flow/trigger wiring):

- Each `agents.<name>` profile:
  - `provider`+`model` → a `model:` value on the steps that referenced the agent
    (exact pin — migration must not invent a fleet). If only `provider` was set
    (no model), that maps to the runtime's bare-launch default → omit `model:`.
  - behavior fields (`guidance`, `skill`, `session`, `memory`, `workspace`,
    `wait_timeout`, `archive_when_done`, `host`, `isolation`, `runtime`) → moved
    onto the referencing step, or a reusable step-template referenced via
    `extends:` where multiple triggers shared one agent (preserve the reuse the
    named profile gave).
- `agent_guidance:` (layer-0 house rules) → keep as-is if it stays a global; else
  fold into the step-guidance stack. Do not silently drop it.
- Remove the `agents:` field, `AgentProfile`, and `Agents` handling once flows,
  triggers, dispatch, sessions, and tests are moved over.
- Ship the migration in the SAME change as the schema removal so an existing config
  keeps working across the upgrade.

**NOTE for the implementer / maintainer sign-off:** the destination of the agent
*behavior* fields (onto the step vs a `templates:`/step-profile map reached via
`extends:`) is the one part of this design not nailed down in conversation. Pick the
option that best fits how steps are currently defined and referenced in
flows/triggers, keep the reuse `extends:` gave, and flag it in the PR description
for review. Everything else above is settled.

---

## 7. Docs (ship in the same PR — repo rule: docs travel with the change)

- New `docs/design/runtimes-models-packs.md` = this file.
- `config.example.yaml`: replace the `agents:` section; expand `runtimes:` with the
  `models:` block; add a fleets example; show the pack overlay (named triggers,
  array instances, connector-owned scope). Remove every `agents:` reference.
- Wiki: update the runtimes/agents, packs, and model-selection pages. Add a
  model-discovery page (per-runtime strategies, models.dev, bare launch, `prefer:`/
  `default:`). Note `agents:` is removed and how to migrate.
- README: adjust any `agents:`/model snippets.

## 8. Tests

- config: `runtimes:` scalar/list/map parsing + key-implies-`use:`; `models:`
  block; fleet parsing incl. wildcards and `"*"`; `model:` string/array/object.
- discovery: table-drive the per-runtime adapters with the provider responses
  mocked (claude 200 list; codex 403; models.dev catalog fixture); models.dev
  cache + degrade-safe fallback; NO network in unit tests.
- resolution ladder: override → intersect(prefer, wildcard-expanded) → bare launch
  → hard error; bare-launch vs `"*"`; `required:` behavior.
- packs: mirrored-section overlay deep-merge (trigger filters, additive step
  guidance, fleet override, `enabled:false`); connector-owned scope + missing-
  connector dormancy + `required` source; instance disambiguation.
- triggers: `source.event` key-implies-`on:`; two-of-same-source named form;
  array-vs-object instances; instance identity by content; dedup keys stable across
  array reorder.
- migration: `agents:` → runtimes/steps round-trips and re-validates; fail-safe
  refuse+restore on a config that wouldn't validate; `agent_guidance` preserved.
- Keep `go test ./...` green; `gofmt -l` clean; `go vet ./...` clean.

---

## Evidence (validated on the maintainer's box — do not regress to paseo-only)

- claude live discovery WORKS: `GET api.anthropic.com/v1/models` with the on-box
  OAuth token returned the live list (opus-5, sonnet-5, haiku-4.5, fable-5.1, …).
- codex CANNOT list live: token is `auth_mode: chatgpt`, `/v1/models` →
  `403 missing scopes: api.model.read`. Fallback = models.dev.
- gemini CANNOT list live with the CLI token (Google API needs a key). Fallback =
  models.dev.
- paseo has no proprietary registry: it reads the vendor SDKs' model enumerations
  (`gpt-5.6-sol` etc. ship in the bundled OpenAI SDK type; gemini ids in
  `@ai-sdk/gateway`) and calls `api.anthropic.com` for claude — i.e. exactly the
  per-runtime strategy above.
- models.dev/api.json: public, no auth, 213 providers, current (has `claude-opus-5`,
  `gpt-5.6-sol`, `claude-opus-4-8`) + context/pricing metadata.
