# Config reuse via YAML anchors (compose-style) — main config AND packs

Replaces the bespoke top-level `steps:` templates section with plain YAML anchors,
in both the main config and packs. Approved in conversation.

## Why

The `steps:` templates section (a top-level map of reusable step behavior reached
via `extends:`) reads like a workflow's own `steps:` and is a conductor-specific
concept. YAML already has the mechanism docker-compose uses — `&anchor` / `*alias` /
`<<:` merge — and conductor's `yaml.v3` supports all of it, INCLUDING `<<:` merge
under strict decode (verified). The only blocker is that strict decode rejects the
`x-*` holder key. Fix that one thing and reuse becomes pure YAML.

## What to build

### 1. `x-*` passthrough under strict decode (the one enabling change)

Top-level keys beginning with `x-` are IGNORED (not rejected) by strict decode —
exactly docker-compose's extension-field convention. They exist only to park
anchors:

```yaml
x-templates:
  reviewer: &reviewer
    model: heavy
    workspace: worktree
    guidance: "Review only what the diff changes. Cite file:line."

workflows:
  review-team:
    steps:
      - <<: *reviewer            # merge the base, add this step's specifics
        id: review
        for_each: "{{.inputs.lenses}}"
        prompt: | …
```

- Apply in: the main `Config` strict decode, EVERY imported file (imports share the
  loader), and the pack manifest decode. One helper, used in all three.
- Implementation note: `yaml.v3` KnownFields has no prefix matching. Do it via a
  custom decode step — parse to a `yaml.Node`, drop top-level mapping entries whose
  key starts with `x-`, then strict-decode the filtered node. Alias nodes elsewhere
  keep their pointer to the anchor's value node, so `<<: *reviewer` STILL resolves
  after the `x-templates` entry is dropped (verified: the merge resolves even when
  the holder key is removed). Only `x-` at the TOP level of a file is ignored; `x-`
  nested elsewhere is unaffected.
- Scope to the `x-` prefix so ordinary typos are still caught by strict decode.

### 2. Remove the bespoke `steps:` templates section

- Delete the top-level `steps:` templates map and its role as an `extends:` target
  (`config/steps.go`'s section, `Steps.md`). Reuse of step BEHAVIOR is now anchors.
- KEEP the generic `extends:` for map sections (#150) and trigger `extends:`/
  `abstract:` (#151) — those serve map-section and cross-file reuse and are NOT
  removed. Only the step-templates-via-named-section pathway goes away.
- If removing the section cleanly is blocked by something the migration needs,
  flag it rather than half-removing.

### 3. Identity is unchanged; anchors carry CONFIG only

The step-identity ladder stays: `name:` → structural (`<enclosing workflow>/<id>`)
→ fingerprint. Clarify in code + docs:

- An anchor/`<<:` merge shares **configuration**, not identity. A merged step's
  identity is still structural (its enclosing workflow + `id`) unless it sets `name:`.
- **`name:` is ONLY the cross-workflow/cross-trigger identity-SHARING override** —
  use it when two different steps (often in different workflows) must be the SAME
  identity for memory/session/outcome. It is NOT required just because you used an
  anchor, and it is redundant for a step used once. `id:` remains the workflow-local
  slot (references + the structural-identity component), distinct from `name:`.

### 4. Migration — preserve history, emit anchors

The `agents:` → steps migration currently emits the `steps:` section + `extends:`.
Re-target it to anchors: emit an `x-templates:` block with `&anchor` definitions and
`<<: *anchor` on each referencing step. Same fail-safe contract (transform →
re-validate → refuse+restore).

- Preserve track-record continuity: set `name: <old-agent-name>` in the merged
  fragment so memory/session/outcome keep keying off the old identity (this is the
  legitimate cross-workflow-sharing case — the old `agent:X` was shared).
- If clean anchor EMISSION from Go is impractical, the acceptable fallback is
  inlining each step's fields with `name: <old-agent-name>` (duplicated config, but
  identity preserved) — flag which you chose. Prefer anchors for readable output.
- Update migration tests to load the migrated output through the full pipeline and
  compare identities, as before.

### 5. Examples + docs (main config AND packs)

- Rewrite the in-repo example pack (`examples/packs/review-kit`) and the relevant
  part of `config.example.yaml` to use `x-templates:` anchors + `<<:`, with NO
  gratuitous `name:` (the review sub-steps don't share identity).
- Docs must show anchors as the reuse mechanism in BOTH the main config and packs,
  note the `x-` holder convention, and state the two caveats:
  - anchors are single-file — they do NOT cross `imports:`; use `extends:` for
    cross-file/named reuse.
  - `name:` is the identity-sharing override, separate from the anchor.

## Tests

- strict decode ignores top-level `x-*` in main config, an imported file, and a pack
  manifest; a non-`x-` unknown key is STILL rejected; a nested `x-` key is untouched.
- `<<: *anchor` merges into a step/runtime/connector under strict decode and the
  merged fields are present; anchor resolves even though the `x-` holder is dropped.
- identity: an anchored step with no `name:` gets structural identity; with `name:`
  gets the shared identity; two steps sharing a `name:` share one identity.
- migration: `agents:` → anchors round-trips, re-validates, refuses+restores on a
  config that wouldn't validate, and preserves the old agent name as identity so
  outcome/memory/session history carries over.
- `go test ./...` green, `gofmt -l` clean, `go vet ./...` clean.
