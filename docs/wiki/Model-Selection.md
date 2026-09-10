# Model selection — fleets, `prefer:`, and bare launch

Conductor picks a model the way a spot fleet picks an instance: you say what is
**acceptable**, in what order, and what should happen when nothing acceptable
is available. It then resolves that against what your box can actually run.

This matters because a config is portable and a model list is not. A pack that
hardcodes `claude-opus-5` is broken for someone running codex; a pack that asks
for "a strong reviewer model" runs for both.

See also: [Model discovery](Model-Discovery.md) (where the available list comes
from) and [Runtimes](Runtimes.md).

## The three places a model can be decided

| Where | What it says |
| --- | --- |
| `runtimes.<name>.models` | what this runtime may run, prefers, and defaults to |
| `models:` (top level) | named **fleets** — reusable acceptable-model lists |
| `model:` on a step | which fleet/model this particular step needs |

## `runtimes.<name>.models`

```yaml
runtimes:
  paseo:
    models:
      default: claude-opus-5                  # optional
      prefer:  [claude-opus-5, gpt-5.6-sol]   # ranking among acceptable models
      allow:   ["claude-opus-*", "gpt-5.6-*"] # a ceiling on what may ever run
```

Every field is optional, and so is the whole block. Omit it and the runtime is
fully automatic: its roster is discovered, nothing is restricted, and its
default is a **bare launch**.

- **`default:`** — the model to pass when nothing else resolves one. Its
  *absence is meaningful*: no default means bare launch.
- **`prefer:`** — ranks acceptable models when a fleet offers a choice. The
  pack proposes; the consumer disposes. `prefer:` also decides the effective
  default when `default:` is unset and several models are available.
- **`allow:`** — filters the discovered roster. A model outside it can never
  run on this runtime, whatever a fleet asks for.

## Fleets

```yaml
models:
  reviewer:
    any: ["claude-opus-*", "gpt-5.6-*", "gemini-3.*-pro"]
    required: true      # nothing available -> HARD ERROR (don't downgrade a reviewer)
  summarizer:
    any: ["claude-haiku-*", "*-mini", "gemini-2.5-flash*"]
    required: false     # nothing available -> bare launch
```

- **`any:`** is ordered, best-first. Literals and wildcards.
- **`required:`** decides the fallback posture. `true` refuses to run rather
  than quietly using something weaker; `false` falls through to a bare launch.

### Wildcards

Patterns are globbed against the **discovered roster**, so a wildcard can never
conjure a model you cannot run.

- Forms: `claude-opus-*`, `gpt-5.6-*`, `"*-mini"`, `"*pro*"`, and the full
  wildcard `"*"`.
- **Quote any pattern that starts with `*`.** A bare `*` is a YAML alias
  indicator and will not parse as you meant. Write `"*"`, `"*-mini"`, `"*pro*"`.
- Literals keep their listed priority. A wildcard expands **in place**,
  newest-first as the catalog reports, de-duped against literals already
  listed. Then `prefer:` re-ranks the result.
- A pattern that matches nothing contributes nothing — resolution falls through
  to the next entry, then to `required:`/bare launch.

## `model:` on a step

```yaml
steps:
  - { id: architecture, type: agent, model: reviewer }
  - { id: security,     type: agent, model: ["claude-opus-*", "gpt-5.6-*"] }
  - { id: audit,        type: agent, model: { any: ["claude-opus-*"], required: true } }
  - { id: summarize,    type: agent, model: gemini-2.5-flash }
  - { id: lint,         type: agent, model: "*" }
```

- **string** — *map-key-wins*: if it names a key in `models:` it is a fleet
  reference, otherwise it is a model id or a wildcard.
- **array** — an inline fleet, sugar for `{ any: [...], required: false }`.
- **object** — the full `{ any, required }`.

A step may also pin **where** it runs with `runtime: <name>`; otherwise
resolution picks the runtime that offers the winning model.

## The resolution ladder

For each `model:` reference, in order:

1. a **consumer override** (a pack instance's `models:` overlay) — use it;
2. otherwise **intersect** the fleet's `any:` (wildcards expanded) with the
   union of the configured runtimes' allow-filtered rosters, rank by `prefer:`,
   and run the winner **on the runtime that offers it**;
3. otherwise, if `required: false`, **bare launch**;
4. otherwise (`required: true`, nothing matched) a **hard error** naming the
   fleet and acceptable-vs-available.

Ties between runtimes offering the same model are broken by `default: true`,
then by name.

A step with **no `model:` at all** takes the runtime's `models.default:`, then
its first available `prefer:` entry, then a bare launch.

## Bare launch

Bare launch is a **first-class outcome, not a failure**. Conductor dispatches
with no `--model` and the runtime uses its own built-in default. It happens
when:

- the runtime declares no `models.default:` and the step names no model;
- a fleet with `required: false` matches nothing available;
- the runtime cannot enumerate its models at all — it still works, you just
  cannot `prefer:`/`allow:`-filter it.

`model: "*"` is **not** bare launch. `"*"` resolves to a *concrete* model
through `prefer:`; bare launch passes no model at all.

## Two behaviours worth knowing

**An exact pin survives an un-enumerable runtime.** If you write
`model: some-private-model` and no configured runtime can enumerate, conductor
passes it through. It is not the authority on what exists when it cannot see.
If a runtime *did* enumerate and does not offer the model, the ladder is
followed instead (and you get a notice saying so).

**`required: true` is only enforced against a real answer.** Conductor errors
when discovery ran and found nothing acceptable. When discovery is unavailable
— no network, no credentials — it has learned nothing, not that a model is
gone, so it does not fail the load. Hard-failing there would crash-loop an
auto-updating fleet on the first network blip.
