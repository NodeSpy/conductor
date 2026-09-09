# Packs

**Packs** make a conductor configuration **distributable**. A pack is a
self-contained, versioned bundle of behavior — workflows, agents, policy, checks,
and (disarmed) triggers — that anyone can install from a source, parameterize,
override, and compose. The model is **Terraform-modules-for-conductor**: a
top-level `packs:` block where each entry is a sourced, versioned, parameterized
*instance* of a pack, namespaced under its instance name.

The design goal in one line: **turnkey to install, impossible to auto-arm,
cleanly updatable.**

> Status: this page documents the pack **mechanism** shipped so far — the
> `packs:` block, the manifest, resolve/lockfile, namespacing + binding, settings,
> disarmed triggers, dependencies, and the `init`/`pack` CLI. See
> [Limitations](#limitations) for what is not yet covered.

## The `packs:` block

```yaml
packs:
  review:                                      # instance name == the namespace
    source: github.com/your-org/packs//review-kit
    version: 1.0.0                              # the lockfile records the resolved sha
    preset: claude                             # pick a settings preset
    settings: { heavy_model: claude-opus }     # or override individual settings
    connectors: { github: gh }                 # BIND the pack's required github -> your gh
    secrets:    { review_token: house/review } # BIND a required secret -> your vault ref
    agents:
      reviewer: my-opus                        # BIND a role to your global agent
      handoff:  { workspace: local }           # OVERRIDE the bundled agent (deep-merge)
    triggers:
      on_review_request:                        # the pack ships this DISARMED
        enabled: true                           # you arm it
        repos:   [your-org/app]                 # you scope it — this IS the consent
```

The block **is** the override surface — no separate drop-in files.

## Governing rule: define behavior / bind environment

This single rule decides what a pack may *ship* vs must *bind*:

- **Define-in-pack** (shipped, namespaced, overridable): `agents:`, `workflows:`,
  `policy:`, `checks:`, `memory:`, and its disarmed `triggers:`. Pure behavior.
- **Bind-only** (declared in `requires:`, wired in the block, **never shipped**):
  `connectors:`, `secrets:`, `vaults:`, `stores:`, `runtimes:`, `hosts:`,
  `handoffs:`. Anything carrying credentials, endpoints, or infra identity.

This is the security boundary. A pack fetched from a stranger can define prompts,
policy, and workflows, but it **cannot smuggle in a credential or point at
infrastructure**. A pack that ships any bind-only section is **rejected at
install**.

## Namespacing

Everything a pack defines is auto-scoped under the instance name:
`agents.handoff` → `review/handoff`, `workflows.review-flow` →
`review/review-flow`. Two packs can both define `reviewer` and never collide.

Refs **inside** the pack are written **bare** and resolve pack-local — the author
writes no prefixes. The loader scopes them. The one boundary that reaches global
names is `requires:`: a required connector/store/secret/handoff, or an agent role
**bound** to a global, resolves in the consumer namespace. You reference a pack's
entry point qualified: `workflow: review/review-flow`.

## Satisfy a resource: default / override / bind

Every resource a pack declares is satisfied one of three ways, by the value's
shape:

| shape   | meaning  |
|---------|----------|
| absent  | the pack's bundled default |
| string  | **bind**: swap in one of your existing globals entirely |
| map      | **override**: keep the bundle, deep-merge changes onto it |

```yaml
agents:
  reviewer: my-opus            # bind
  handoff:  { workspace: local } # override
  # (omit a role entirely to keep the bundled default)
```

Override deep-merges with **replace** semantics: nested maps merge recursively,
but scalars and **list fields are replaced**, not appended. So an override of a
bundled agent's `skill.verbs` fully replaces the bundled list — you can *narrow*
a bundled agent's capabilities, not only widen them. (This differs from
`imports:`, where lists concatenate; a pack override is a deliberate restriction
surface.)

## `requires:` — the interface

A pack manifest declares the resources it needs and, for roles, the capabilities
a binding must satisfy:

```yaml
requires:
  conductor: ">=0.8"                        # daemon-version compat (the fleet auto-updates)
  connectors: [github]
  stores:     [cache]
  secrets:
    review_token: { desc: "token the review-poster uses" }
  roles:
    handoff:  { skill: [github.submit_review] }  # a bound agent MUST provide this
    reviewer: {}
  packs:
    base: { source: github.com/your-org/base-kit, version: "^2.0" }
```

`conductor init` checks each socket is satisfied and warns when a bound agent
lacks a required skill.

## Settings and presets

A pack ships typed `settings:` with defaults, plus named `presets:` (pre-filled
setting bundles). The consumer picks a preset and/or overrides individual
settings — **without editing the pack**. Settings are substituted into the pack's
templated fields at instantiate time with `${settings.NAME}`.

> `${settings.NAME}` is substituted as **text**, so it must sit in a
> **string-valued** field (a prompt, an option, guidance) — not a numeric field
> like a gate's `max_revisions`.

Agents that omit provider/model fall through to your **default runtime**, so a
well-made pack runs with near-nothing bound.

## Triggers ship disarmed — consent is load-bearing

Everything a pack ships except triggers is passive: a workflow runs only when a
trigger fires it or you `conductor run` it. So triggers are the only active
surface, and that is the only place consent lives.

- The pack **ships** its triggers (you never rebuild the event mapping / gates /
  steps), but they arrive **disarmed** — disabled, with **no repos**.
- **Arming** = the two environment-only things: `enabled: true` **and** `repos:`.
  A trigger with no repo scope matches nothing, so even an accidental
  `enabled: true` fires nothing. **The binding you must do is the consent.**

Guarantee: a freshly-added pack does **nothing** until you arm a trigger.

## Composition and dependencies

- **Workflows are addressable by qualified name** (`review/review-flow`) — call
  them from your own triggers or nest them in your own workflows.
- **Pack dependencies** (`requires.packs`) are satisfied by the same recursive
  `packs:` instance block. Behavior can be overridden at any depth; environment
  can only be **forwarded** down — the concrete binding to a real credential/repo
  happens only at the top, with the consumer.
- Guards mirror the workflow engine: a **depth cap** (`MaxPackDepth`), **cycle
  detection** (reports the chain), and a **total-count backstop**.

## Lockfile and reproducibility

`conductor init` writes `conductor.lock.yaml` next to your config — the whole
resolved graph, each node pinned by a resolved revision and a tree digest. Commit
it: `conductor init` on another machine yields a byte-identical setup, and a
changed remote is tamper-evident on the next `init`.

## CLI

```
conductor init                    # fetch the packs: block, write the lockfile, preview the effect
conductor pack list               # configured instances + lock status
conductor pack plan               # preview what the packs add (agents, skill grants, armed triggers)
conductor pack lint <pack-dir>    # validate a pack is well-formed (author tooling)
conductor pack show <pack-dir>    # render a pack's docs: settings, requires, exports, example
```

Typical flow: edit `packs:` → `conductor init` → `conductor pack plan` → arm a
trigger → `conductor validate`.

## Authoring a pack

A pack is a directory with a `conductor-pack.yaml` manifest. See
[`examples/packs/review-kit`](https://github.com/NodeSpy/conductor/tree/main/examples/packs/review-kit)
for the reference. The manifest carries identity/discovery/compat metadata
(`pack:`), typed `settings:` + `presets:`, the public `exports:` surface, and the
bundled behavior. Run `conductor pack lint` before publishing.

## Security model

- Packs ship **no connectors and no secrets** — bind-only, enforced at install.
- **Install review** (`conductor init` / `conductor pack plan`) surfaces what a
  pack can do: agents, `skill:` grants (loudly), the triggers it wants armed and
  on which repos, and the resolved dependency tree.
- **Lockfile** pins revisions → tamper-evident updates.
- **Inert until armed** — triggers are the only active surface and ship disarmed.
- Strict-decode + the degraded-boot fail-safe apply, so a bad pack can't
  hard-crash the daemon.

## Limitations

The following are **not yet** implemented and are called out honestly:

- **`conductor add` / `remove` / `update --packs`** — the config-mutating install
  helpers. Use the `packs:` block + `conductor init` directly for now.
- **Trust/provenance** — the lockfile digest is verified at load (drift warns),
  but a source allowlist and signature verification (cosign/attestation) are not
  yet implemented.
- **Ref-rewriting** covers agent/workflow/check refs, connector prefixes in
  `uses`/`on`/hooks (scalar and list-form), the `store:` selector, team roles,
  `skill.verbs`, `skill.allow_secrets`, `session.end_on`, and pack-local
  `extends:` — but **not** connector/store/secret references buried inside
  free-form `code:` step bodies or runtime `{{ vault … }}` templates.
- **Pack policy** is folded onto the pack's own triggers; a pack workflow called
  from *your* trigger does not carry the pack's policy.
- **Pack `memory:`** is parsed but not yet applied (it warns on load).
- **Cycle detection** keys on the dependency alias in the chain; a diamond that
  reaches the same pack twice under two different aliases is bounded by the depth
  cap rather than reported as a cycle.
- **Registry / discovery search** — packs are URL/path-addressable; there is no
  central index yet.
