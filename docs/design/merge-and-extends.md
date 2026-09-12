# Two reuse mechanisms: `<<:` (dumb) and `extends:` (smart)

Follow-up on PR #61, AFTER the registry-removal batch lands (both edit the same
step/merge code — do not run concurrently). Approved in conversation.

Two ways to reuse a chunk of config, side by side:

## `<<: *anchor` — plain YAML merge (dumb)

Native YAML anchor merge, already shipped. Overrides scalars AND lists (last wins),
map-only, resolves at parse time. Use it when you just want a copy of a base.
Conductor does nothing special — it sees the already-merged result.

## `extends: *anchor` — conductor field-aware merge (smart)

`extends:` takes a **YAML alias to an `x-*` anchor** (or an inline map) — NOT a
registry name (there is no registry). Conductor receives the base as data and merges
it with the step's own fields using compose-style, field-aware rules:

- **scalars** (model, workspace, mode…) → child **overrides**.
- **lists** (network, skill.verbs…) → **append** (child items after base items).
- **`guidance`** → **always appends** (treated as a stack; base tone under child).
- **maps** → **deep-merge**.

```yaml
x-reviewer: &reviewer
  model: heavy
  guidance: "Terse and human."
  network: ["api.github.com:443"]

workflows:
  f:
    steps:
      - extends: *reviewer
        guidance: "Also cite file:line."   # APPENDS → "Terse and human.\nAlso cite file:line."
        model: light                        # OVERRIDES (scalar)
        network: ["api.linear.app:443"]     # APPENDS → both hosts
```

### Escape-hatch tags (compose-style) — read from the YAML node tag

`yaml.v3` exposes each node's tag, so conductor honors custom tags during its merge:

- **`!override <value>`** — replace instead of append (for a field that would
  otherwise stack).
- **`!reset`** — drop the inherited value entirely (empty/absent).

```yaml
      - extends: *reviewer
        guidance: !override "Only this."     # replace, don't append
      - extends: *reviewer
        guidance: !reset                     # clear inherited guidance
        network:  !reset                     # works on any inherited list/map/scalar
```

The legacy `guidance: {replace: …}` form (#149) becomes an alias for
`guidance: !override …` — keep it working, document the tag as preferred.

### Notes / constraints

- The append/tag semantics ONLY work under `extends:` — conductor must see both
  layers. Under `<<:` (parse-time) it cannot, so `<<:` stays dumb-override. Document
  the distinction plainly.
- `extends:` target is an anchor alias or inline map — never a top-level registry
  name (there is none). Same `x-*` anchors the `<<:` path uses.
- Chains: `extends: *a` where `*a` itself was built with `extends:` — resolve
  base-first, append order preserved (base tone underneath). Guard against cycles
  (an alias can't be self-referential in YAML, but an inline `extends` chain could;
  cap depth / detect and error).
- `extends:` is pure CONFIG merge — it does NOT touch identity. Identity stays
  `name:` → structural (`<workflow>/<id-or-index>`) → fingerprint, unchanged.
- Precedence when both are present on one node (`<<:` and `extends:`): resolve `<<:`
  first (it's YAML-level, already merged by parse), then apply `extends:` field-aware
  on top. Document; a config using both on one step is unusual but must be defined.

## Tests

- `<<:` still overrides (scalars and lists); unchanged.
- `extends: *anchor`: scalars override, lists append, guidance appends, maps
  deep-merge; multiple layers stack in base-first order.
- tags: `!override` replaces; `!reset` drops; `{replace:}` behaves as `!override`.
- guidance-always-append holds without any per-step opt-in; `!reset`/`!override`
  escape it.
- identity unaffected by `extends:`.
- cycle/among inline `extends` chains → bounded error, not a hang.
- `gofmt -l` clean, `go vet ./...` clean, `go test ./...` green.
