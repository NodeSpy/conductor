# Skill capability injection + the pack connector interface

Follow-up on PR #61, on top of the `skill:`-moves-to-step and pack overlay work
already landed. Two coupled areas, all approved in conversation:

- **A–C: the skill surface** — conductor teaches agents how to use it, deny-by-default
  with wildcard grants, and a pack can never grant outside the connectors it declared.
- **D–E: the pack connector interface** — `requires.connectors` gains versions and
  becomes the single interface; `requires.roles` is removed as vestigial.

Everything here keeps the existing invariants: deny-by-default, secret-egress
barriers unconditional, `conductor.*`/`workflow.*` blocked from the skill surface,
peer-bound short-TTL tokens, strict-decode + degraded-boot.

---

## A. Capability injection — conductor teaches the agent, prompts state intent

Today a workflow prompt hand-codes the transport mechanics (`conductor discover`,
`conductor call <verb> --comments '<json>'`). That leaks conductor's CLI surface
into every workflow: duplicated, and a verb change silently breaks every prompt.
Move it into conductor.

**Two layers, and the guidance RIDES THE GRANT** (an agent with no grant gets
nothing — see B):

- **Layer 0 — general guidance (mechanics/awareness).** Whenever a step has a
  non-empty skill grant, conductor injects a baseline preamble: *"You can act on
  external systems through conductor verbs — do that rather than shelling out. Run
  `conductor discover` to see the verbs available to you and their signatures; call
  them as `conductor call <verb> --<opt> <value>`."* Generated, not authored.
- **Layer 1 — the capability card.** conductor renders the granted verbs from the
  **verb registry**, scoped to exactly the grant:
  - **CLI-transport runtimes** get a generated preamble listing each granted verb
    with its options schema and the `conductor call` form (the example below).
  - **MCP runtimes (paseo/opencode)** get the same registry entries as native tool
    schemas (already the mechanism) — so no redundant prompt text is needed there
    either.

CLI card shape (rendered from each verb's `Decl`):

```
## Conductor verbs available to you
Act through these — do not shell out to git/gh/network for them.
Call as: conductor call <verb> --<option> <value>

• github.submit_review — submit a pull-request review
    --repo string (required)  --pr integer (required)
    --event APPROVE|REQUEST_CHANGES|COMMENT (required)
    --body string   --comments json [{path,line,body}]
```

Add an optional **`Usage string`** field to `pkg/plugin` `Verb` (and the daemon
alias) — a one-line "what/when" hint rendered into the card and the MCP tool
description, so a verb can describe itself once instead of every prompt doing it.

`conductor discover` returns **exactly** the granted set (see B) — so the card the
agent is told about, the tools it sees, and what the daemon enforces are all
rendered from one source and can never drift.

**Result:** workflow prompts drop to intent — *"submit the reconciled review as the
PR review"* — with no verb name, no `discover`, no `--comments '<json>'`. A verb
rename/signature change updates every agent's card on the next dispatch; zero
workflow edits.

## B. Deny-by-default grant, with wildcard forms

`skill.verbs` stays a deny-by-default allowlist (no block → no surface, nothing
injected, all denied). Make these grant forms first-class (the enforcement point
`matchAny(id.Verbs, uses)` already pattern-matches; ensure `*` and `<connector>.*`
are supported and documented):

```yaml
skill:
  verbs: ["*"]                    # all verbs, all connectors
  # verbs: [github.*]             # all verbs of one connector
  # verbs: [github.*, sentry.*]   # several connectors
  # verbs: [github.submit_review] # specific verbs
```

- `discover`, the injected card, and enforcement are all driven by this one list.
- Reads are NOT open by default — a read verb outside the grant is denied like any
  other. Breadth is an explicit dial (`github.*`, `*`), never a default.

## C. A pack cannot grant outside `requires.connectors`

`requires.connectors` is the pack's capability boundary. A pack's `skill.verbs` may
only name connectors it declares:

- **Static (pack lint):** any `skill.verbs` pattern in a pack whose connector is not
  in `requires.connectors` → lint error naming the pack, the pattern, the undeclared
  connector.
- **Runtime (instantiate):** intersect the pack's granted skill surface with its
  `requires.connectors` as a belt — a hand-authored pack that skipped lint still
  cannot exceed its declared interface.
- **Wildcards are bounded by requires:** in a pack, `skill.verbs: ["*"]` means "all
  verbs of *my required connectors*", and `github.*` requires `github` in
  `requires.connectors`. A pack's `*` can never reach a connector it did not declare.

This makes a distributable pack safe: it can only ever hand an agent access to the
connectors it declared — never quietly scope onto the consumer's pagerduty, secrets
connector, etc. Composes with B (the consumer still grants; the pack still cannot
exceed its `requires`).

## D. Version-aware `requires.connectors`

`requires.conductor` and `requires.packs` already carry version constraints;
`requires.connectors` is a bare name list. Make it version-aware, list form as sugar:

```yaml
requires:
  conductor: ">=0.8.2"
  connectors:
    github: "*"        # any (same as the bare-list form)
    jira:   ">=2.0"    # a plugin connector at a compatible version
  # connectors: [github]   # still valid — sugar for { github: "*" }
```

- **Plugin connector** → constraint gates the consumer's resolved plugin release
  (their `use: jira@…` must satisfy `>=2.0`).
- **Builtin connector** → its version is the daemon version; the constraint resolves
  to a scoped daemon-version check.
- Checked at instantiate against the connector's **resolved version** using the
  existing semver machinery (`checkConductorConstraint` / `version_resolve.go`).
  It **gates, does not fetch** (connectors are bind-only) — a clear load error naming
  pack + connector + required-vs-actual, same failure mode as a bad
  `requires.conductor`. Degrade-safe.

## E. Remove `requires.roles` / `RoleReq`

Vestigial after the agents removal — both jobs are covered:
- "which model/agent fills the role" → **fleets** + runtime roster + the mirrored
  overlay.
- "capabilities a binding must satisfy" → **`requires.connectors`** (bounds skill,
  §C) + the pack's own shipped step `skill.verbs`.

Remove `PackRequires.Roles`, `RoleReq`, the instantiate role-capability check
(pack_instantiate.go ~429), and the stale pack-lint check (`"requires.roles: %q has
no bundled agent"` — there are no bundled agents now). **Verify nothing else depends
on `RoleReq` before deleting.** Simplify the migration/example configs that still
mention roles.

## Downstream (separate, in the conductor-packs repo — NOT this PR)

Once A–C land, the `pr-review-team` pack's hand-off step drops its
`conductor discover` / `conductor call …` incantation to intent-only, and its
`roles:` block is removed. Note it in the PR description as the follow-up; do not
edit conductor-packs from this branch.

## Tests

- capability card: rendered from the registry, scoped to the grant; CLI preamble +
  MCP tool-schema parity; `Usage` surfaced; `discover` output == granted set == what
  enforcement allows (assert the three agree from one fixture).
- grant forms: `*`, `<connector>.*`, multi-connector, specific; a verb outside the
  grant denied; reads not open by default.
- guidance rides the grant: no grant → nothing injected; grant → guidance + card.
- pack skill boundary: `skill.verbs` naming an undeclared connector → lint error;
  instantiate intersects; pack `*`/`github.*` bounded by `requires.connectors`.
- version-aware requires.connectors: plugin under/over constraint (gate pass/fail);
  builtin resolves to daemon version; bare-list = any; clear error text.
- roles removal: config that used `requires.roles` migrates/loads without it; no
  dangling refs; example packs updated.
- `go test ./...` green, `gofmt -l` clean, `go vet ./...` clean.

---

## Implementation status (branch `feat/runtimes-fleets-packs`, PR #61)

All of A–E is implemented. Maintained with the code.

| § | Where |
|---|---|
| A capability injection | `internal/flow/capability.go` (preamble + card), `flow.GrantedVerbs` as the one resolution, `engine.skillGuidance{CLI,MCP}`; `Usage` on `pkg/plugin.Verb` → `connector.VerbDecl` → `mapDecl` |
| B grant forms | `config.SkillPolicy.Verbs` docs; `flow.matchAny` (already pattern-matched — now documented and covered) |
| C pack skill boundary | `internal/config/pack_skill.go`; lint via `LintPackManifest`, belt via `applyPackSkillBoundary` at instantiate |
| D version-aware requires.connectors | `internal/config/pack_connectors.go`; resolved versions injected by `cmd/conductor.publishConnectorVersions` |
| E roles removed | `PackRequires.Roles`, `RoleReq`, the instantiate role check, the stale lint check, and the now-dead `agentGrantsSkill` all deleted |

### How card == discover == enforcement is proven

`flow.GrantedVerbs(patterns)` is the single resolution of a grant against the
verb registry. `CapabilityCard` renders it, `SkillVerbCatalog` (→ the
`verb_list` IPC op → `conductor discover` and the MCP tool list) serializes
it, and `RunSkillVerb` gates with the same `matchAny` over the same patterns.

`TestGrantIsOneSourceOfTruth` (internal/flow/capability_test.go) derives all
three views independently from one fixture — parsing the ids back out of the
rendered card text, out of the catalog maps, and out of the enforcement
predicate — and asserts set equality for every grant form including `["*"]`,
`<connector>.*`, specific verbs, a pattern matching nothing, and the empty
grant.

### Two judgement calls

**An unknown connector version warns rather than fails.** §D says gate at
instantiate. When the resolved version is genuinely unknown — a dev build, a
plugin not yet installed, a daemon with no install state published — there is
nothing to compare against. Failing there would crash-loop an auto-updating
fleet on a box that may be perfectly fine, so it warns and skips, matching
how `requires.conductor` treats an unversioned daemon.

**The pack skill boundary bounds the PACK, not the consumer.** It runs before
the mirrored overlay, deliberately. A consumer who writes
`packs.<n>.steps.<role>.skill` in their own config is granting for
themselves — exactly as they would on their own step, visible in their own
config. What a pack can never do is reach a connector it did not declare.

### Downstream follow-up (NOT this PR)

In the `conductor-packs` repo, `pr-review-team` can now drop its
`conductor discover` / `conductor call …` incantation to intent-only (the
capability card supplies the mechanics) and remove its `roles:` block.
