# review-kit

A distributable **conductor pack** (see issue #53): multi-lens PR review with an
optional human hand-off. This directory is the **format reference** for authoring
a pack and the fixture the pack e2e scenario installs.

A pack ships pure **behavior** — agents, workflows, policy, checks, and a
disarmed trigger — and **no environment**: no connectors, no secrets. The
consumer binds those. That is the security boundary: a pack fetched from a
stranger can define prompts and workflows but cannot smuggle in a credential or
point at infrastructure.

## Install

```yaml
packs:
  review:                                   # instance name == the namespace
    source: ./examples/packs/review-kit     # or github.com/your-org/packs//review-kit@v1.0.0
    preset: claude                          # or codex
    connectors: { github: gh }              # bind the pack's required github -> your connector
    secrets:    { review_token: house/review }
    agents:     { reviewer: my-opus }        # bind a role to a global (or omit for the bundled default)
    triggers:
      on_review_request:
        enabled: true                        # arm it
        repos:   [your-org/app]              # scope it — the repo scope IS the consent
```

Then:

```
conductor init            # fetch the source, write conductor.lock.yaml
conductor pack plan       # preview what it adds (agents, skill grants, armed triggers)
conductor validate        # confirm the effective config is valid
```

## What you get

- `review/review-flow` — reviews a PR across the configured lenses, then posts
  the result. Call it from your own trigger as `workflow: review/review-flow`,
  or arm the shipped `on_review_request` trigger.
- `review/reviewer`, `review/handoff` — the bundled agents (override or bind).

## Customize without editing this file

- **Settings** — `lenses`, `heavy_model` (see `conductor pack show`). Placeholders
  (`${settings.NAME}`) are substituted as text, so they live in string fields
  (prompts, options), never in a numeric field like a gate's `max_revisions`.
- **Presets** — `claude` / `codex` pre-fill the settings; pick one with `preset:`.
- **Agents** — bind a role to your global (`reviewer: my-opus`) or deep-merge an
  override (`reviewer: { workspace: local }`).
- **Provider/model** — omitted here on purpose, so agents fall through to your
  fleet-default runtime. Zero-config still runs.

## Inert until armed

Everything except the trigger is passive. A freshly-added pack does nothing until
you arm a trigger (`enabled: true` + `repos:`). You can always run its workflows
by hand.
