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
    source: github.com/your-org/packs//review-kit@v1.0.0  # @ref PINS (tag/branch/sha)
    version: 1.0.0                             # metadata only, NOT a pin — recorded in the
                                                # lockfile; see Lockfile and reproducibility
    auth:       house/gh-pat                   # OPTIONAL fetch credential for a private source,
                                                # resolved through your secrets:/vaults:
    preset: claude                             # pick a settings preset
    settings: { heavy_model: claude-opus }     # or override individual settings
    connectors: { github: gh }                 # BIND the pack's required github -> your gh
    secrets:    { review_token: house/review } # BIND a required secret -> your vault ref
    policy:     { budget: { max_cost_usd: 5 } } # deep-merges onto the pack's bundled policy
    agents:
      reviewer: my-opus                        # BIND a role to your global agent
      handoff:  { workspace: local }           # OVERRIDE the bundled agent (deep-merge)
    triggers:
      on_review_request:                        # the pack ships this DISARMED
        enabled: true                           # you arm it
        repos:   [your-org/app]                 # required for a github trigger — this IS the consent
```

The block **is** the override surface — no separate drop-in files. `policy:`
deep-merges in order bundled pack policy <- instance `policy:` <- a trigger
arm's own `policy:` (most specific wins).

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
- **Arming** = the two environment-only things: `enabled: true` **and**, for a
  github-sourced trigger, `repos:`. The repo list **is** the consent: arming a
  github pack trigger with `enabled: true` but no `repos:` is a **hard error at
  load**, not a silent no-op — the github matcher treats an empty repo set as
  "match every repo", so an unscoped arm would otherwise run the pack on every
  repo the consumer's connector can reach. A non-repo-scoped source (`manual`,
  `rss`, …) has no repo concept and is exempt from this requirement.

Guarantee: a freshly-added pack does **nothing** until you arm a trigger, and a
github trigger **cannot be armed at all** without explicitly scoping its repos.

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

The instance block's `version:` does **not** select a ref — it is metadata,
recorded in the lockfile and used as the default `version:` for a child
dependency that omits its own. The real pin is `@<tag|branch|sha>` appended to
`source:` (e.g. `source: github.com/your-org/packs//review-kit@v1.0.0`); the
lockfile's `resolved:` sha is what actually reproduces the fetch.

## CLI

```
conductor init [--allow-unlisted] # fetch the packs: block, write the lockfile, preview the effect
conductor pack list               # configured instances + lock status
conductor pack plan               # preview what the packs add (agents, skill grants, armed triggers)
conductor pack add <source>       # fetch a pack, show its install review + a ready-to-paste block
conductor pack lint <pack-dir>    # validate a pack is well-formed (author tooling)
conductor pack show <pack-dir>    # render a pack's docs: settings, requires, exports, example
conductor pack remove <instance>  # clear a pack's vendored tree + lockfile entries (alias: rm)
conductor pack update [--allow-unlisted] # re-resolve the packs: block and diff the lockfile
conductor update --packs          # alias for `conductor pack update`
```

`conductor pack add` and `remove` do **not** edit your config: `add` prints a
block for you to paste (so you review the binds first), and `remove` clears the
vendored tree + lockfile and tells you which `packs:` block to delete.

Typical flow: edit `packs:` → `conductor init` → `conductor pack plan` → arm a
trigger → `conductor validate`.

## Authoring a pack

A pack is a directory with a `conductor-pack.yaml` manifest. See
[`examples/packs/review-kit`](https://github.com/NodeSpy/conductor/tree/main/examples/packs/review-kit)
for the reference. The manifest carries identity/discovery/compat metadata
(`pack:`), typed `settings:` + `presets:`, the public `exports:` surface, and the
bundled behavior. Run `conductor pack lint` before publishing.

## Trusted sources

An optional operator-level allowlist gates **where** packs may come from — the
trust surface the lockfile can't provide (the lockfile proves *unchanged*, not
*trusted*):

```yaml
pack_trust:
  allow:
    - github.com/your-org/*
    - github.com/acme/conductor-packs*
```

With `pack_trust:` set, `conductor init` refuses any **remote** pack source — at
any depth, including a dependency's — that matches no `allow:` glob (`*` matches
any run of characters). Local sources (your own disk) are exempt. Override once
with `conductor init --allow-unlisted`.

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

- **Signature verification** (cosign / build attestation, §21 Phase B) is not yet
  implemented. The source allowlist (`pack_trust:`, below) and lockfile digest
  verification (drift warns at load) are.
- **Ref-rewriting** covers agent/workflow/check refs, connector prefixes in
  `uses`/`on`/hooks (scalar and list-form), the `store:` selector, team roles,
  `skill.verbs`, `skill.allow_secrets`, `session.end_on`, and pack-local
  `extends:` — but **not** the free-form runtime env-access templates
  `{{ vault … }}`, `{{ secret … }}`, and `{{ kv … }}`. Those are not rebound:
  they resolve the consumer's *global* vault/secret/store by name, so a pack can
  reach undeclared environment through them. The loader **warns** on every such
  template it finds in a pack's behavior (at `conductor init` / `conductor pack
  plan`), so the reach is never silent — bind the value through `requires:` or
  pass it via a setting/workflow input instead. (`http://`/`git://` plaintext
  pack sources are also refused — an unauthenticated fetch can't be safely
  sha-pinned.)
- **Pack policy** is folded onto the pack's own triggers; a pack workflow called
  from *your* trigger does not carry the pack's policy.
- **Pack-scoped memory/state namespace** (§24) is not yet implemented: a pack's
  agents may opt into memory (behavior), but two packs share the same memory
  namespace. (The manifest-level `memory:` *backend* is bind-only and rejected —
  it selects a store/dir, which is environment.)
- **Registry / discovery search** — packs are URL/path-addressable; there is no
  central index yet.
