# Wiki step-example modernization

Branch `docs/modernize-step-examples`. Docs change: **4c2e62a**.
Validator: `CGO_ENABLED=0 go build -o /tmp/cond ./cmd/conductor` at that tree.

## The canonical spelling adopted

```yaml
- id: fix              # run-local handle for steps.<id>.outputs.*
  type: agent          # the step form
  name: fixer          # IDENTITY — memory scope, session pool, track record
  model: claude-opus-5 # optional: fleet name / model id / wildcard / {any,required}
  runtime: paseo       # optional: pins WHERE it runs
  prompt: "…"
```

`agent:` is gone as a selector. `Step.Agent` still parses
(`internal/config/connectors.go:825`) but per `docs/wiki/Steps.md:128` it is a
free-form attribution label that selects nothing — so every old-style example was
documenting a no-op.

`name:` is the replacement because it is the identity pin
(`connectors.go:806-819`, `docs/design/agents-removal.md` §5 and §7): two steps
sharing a name share one memory namespace, one session pool, and one track
record — exactly what a shared `agent: fixer` used to give. It is also literally
what the migration emits: *"emit an explicit `name:` equal to the old agent name,
so memory/session/outcome track records CARRY OVER."*

**Repo examples mirrored:**

- `test/e2e/config/controllers.live.yaml:73-78` — `type: agent` / `prompt:` /
  `name: fx_claude` / `workspace:` / `wait_timeout:` / `archive_when_done:`.
  This is the clearest current in-repo agent step with an explicit identity.
- `test/e2e/config/conductor.yaml:21-29` — `name:` + `type: agent` in an
  `x-templates` anchor.
- `config.example.yaml:697-699` — the `fixer: &fixer` template: `type: agent` +
  `model: claude-sonnet-5`.

(`test/e2e/config/legacy-migrate.yaml` and `unmappable.yaml` still carry
`agent: fixer`; they are deliberate legacy fixtures for the migration tests, not
examples to mirror.)

## `provider:` fixes

`provider:` is retired and the strict parser rejects it outright. Confirmed
empirically on the exact pre-change Configuration.md block:

```
error: parse config: yaml: unmarshal errors:
  line 1: field provider not found in type config.plain
```

**One occurrence in a config block**, `docs/wiki/Configuration.md:464`, inside the
session-affinity `x-templates:` snippet. Replaced with `type: agent`.

I did **not** substitute a `model:` here, and this is a judgment call worth
flagging. The task brief suggested `model:` and/or `runtime:`, but:

- `Steps.md:308` (the migration table, unchanged by me) states the rule the
  binary implements: **"`provider` alone | nothing — that named a backend, not a
  model, so it becomes a bare launch."** A `model:` would invent a tier the
  original never specified.
- `Steps.md:234-239` already carries the **identical** `x-templates: reviewer:
  &reviewer` session example, and spells it `type: agent` with no model. Matching
  it makes the two pages agree rather than diverge.

**Prose:** `docs/wiki/Steps.md:330-340` ("Explanation") described an *agent
profile* answering "which provider, which model" and a "`fixer` profile with
`provider: claude`". Rewritten in terms of step vs runtime, stating plainly that
there is no `provider:`. The ACP/cli caveat and the [[Runtimes]] pointer are
preserved. One more prose touch: `Workflows.md:341`'s inline comment said
"session: on the profile" → "on the step".

Grep confirms no `provider:` remains in any wiki config block (the only hit is the
new prose sentence naming it as retired).

## Files and blocks changed, with validation results

Every block below was extracted to a temp file, concatenated with a fixed
connector prelude (github + slack + web + webhook, fake key path), and run through
`/tmp/cond validate --config <file>`. "ok" = the `ok: N connector(s), …` line.
Connector-disabled notices from the fake web connector are ignored as instructed.

| # | File | Block | Change | Result |
|---|---|---|---|---|
| 1 | Examples.md:14 | conflict fixer + hooks | `agent:`→`name: fixer` | **ok** (4 conn, 1 trig) |
| 2 | Examples.md:41 | comment-burst grouping | `agent:`→`name: fixer` | **ok** (4 conn, 1 trig) |
| 3 | Examples.md:67 | incident research | `agent:`→`name: planner` | **ok** † |
| 4 | Examples.md:85 | review triage→draft→ask | `agent:`→`name: planner` | **ok** (4 conn, 1 trig, 1 wf) |
| 5 | Examples.md:182 | one live agent per PR | `agent: pr-agent` → `<<: *pr-agent` | **ok** (4 conn, 3 trig) |
| 6 | Examples.md:211 | agent-driven catalog | `agent:`→`name: planner` | **ok** (4 conn, 1 trig) |
| 7 | Examples.md:5 | preamble prose | "the `fixer`/`planner` agents" → "steps named" | prose |
| 8 | Binary-Data.md:45 | blob → worktree | `agent:`→`name: fixer` | **ok** (4 conn, 1 trig) |
| 9 | Callable-Service.md:79 | `callable:` + token block | `agent:`→`name: fixer` | **ok** (verbatim block) |
| 10 | Configuration.md:231 | trigger `extends:` base | `agent:`→`name: reviewer` | **ok** (4 conn, 2 trig) |
| 11 | Configuration.md:464 | session affinity template | **`provider: claude` → `type: agent`** | **ok** (was a parse error) |
| 12 | Gates.md:20 | `checks.critic` | `agent:`→`name: reviewer` | **ok** (4 conn, 1 trig) |
| 13 | Gates.md:32 | gated fix step | `agent:`→`name: fixer` | (same block as 12) |
| 14 | Grouping.md:12 | grouping example | `agent:`→`name: fixer` | **ok** (4 conn, 1 trig) |
| 15 | Hand-offs.md:11 | ask hand-off | `agent:`→`name: critique` | **ok** (4 conn, 1 trig) ‡ |
| 16 | Integration-PagerDuty.md:22 | incident triage | `agent:`→`name: fixer` | **ok** † |
| 17 | Integration-RSS.md:17 | changelog read | `agent:`→`name: planner` | **ok** † |
| 18 | Integration-Sentry.md:23 | issue dig | `agent:`→`name: fixer` | **ok** † |
| 19 | Integration-Slack.md:45 | app_mention + hooks | `agent:`→`name: fixer` | **ok** (4 conn, 1 trig) |
| 20 | Reuse.md:178 | abstract trigger base | `agent:`→`name: reviewer` | **ok** (4 conn, 2 trig) |
| 21 | Runs.md:37 | proposed diff | `agent:`→`name: fixer` | **ok** (4 conn, 1 trig) ‡ |
| 22 | Steps.md:330-340 | "Explanation" prose | provider framing rewritten | prose |
| 23 | Workflows.md:234 | `assess-and-post` workflow | `agent:`→`name: planner` | **ok** (4 conn, 1 trig, 1 wf) |
| 24 | Workflows.md:293 | catalog recognize→pick→run | `agent:`→`name: planner` | **ok** (4 conn, 1 trig) |
| 25 | Workflows.md:341 | plan→recover→promote | `agent:`→`name: planner`; comment "profile"→"step" | **ok** (4 conn, 1 trig) |

**† Wrapper substitution.** Rows 3, 16, 17, 18 live on `pagerduty` / `rss` /
`sentry` connectors, which are **plugins and are not installed on this box**
(`conductor validate` refuses with *"plugin pagerduty … referenced by your config
but not installed"*, and I did not fetch plugins or touch `~/.config/conductor/`).
Their changed steps were validated **verbatim** under an `on: manual` trigger
instead, which exercises the same `validateStep` path and the same `Step` schema.
What this does **not** cover for those four rows is the connector's own
event-scope template check — e.g. that `{{.item.link}}` is in scope for an RSS
event. That check is unrelated to the `agent:`→`name:` edit and those templates
are unchanged, but I am flagging it rather than claiming full validation.

**‡ Snippet wrapper.** Rows 15 and 21 are bare `steps:` lists on the page (no
root keys). Wrapped in a named `on: manual` trigger, otherwise verbatim.

Rows 5, 9, 11 deserve a note on how they were assembled: row 9 was extracted
**verbatim** from the page with `awk` (first fenced block) rather than retyped,
and validated with `CONDUCTOR_INVOKE_HMAC`/`CONDUCTOR_INVOKE_TOKEN` set, since the
block interpolates them. Row 5 needed a `memory: { type: memory }` root added to
satisfy the page's own parenthetical ("the `memory.remember` hook needs a
top-level `memory:` section"); the trigger and template are verbatim. Row 6's
wrapper initially included an invented `policy.agent_authored` block which was
itself malformed — I removed it and validated the page's actual trigger list, as
the page shows only the trigger list and states the policy requirement in prose.

## The genuinely-broken example

`docs/wiki/Examples.md:182` — "One live agent per PR — session affinity". The page
defines `pr-agent: &pr-agent` under `x-templates:` carrying `memory: true` and the
whole `session:` block, then the trigger step said:

```yaml
      - type: agent
        agent: pr-agent          # ← selects nothing
```

`agent:` does not resolve an anchor; nothing pulled `&pr-agent` in, so the step
had no `session:` and no `memory:` — the example demonstrated the exact feature it
failed to enable. Fixed to `<<: *pr-agent`, which is the idiom the same file
already uses at line 248 (`{ <<: *fixer, id: fix, … }`) and that `Steps.md:27`
documents. This one is a behavior fix, not just a spelling change.

## Things I was unsure about / deliberately did not do

1. **No `model:` pins were invented.** The brief allowed adding `model:`/`runtime:`
   "where the example implies a specific tier." On inspection none of the 20 sites
   does — they all reference a named profile (`fixer`, `planner`, `reviewer`,
   `critique`) whose tier was defined elsewhere, and the pages never state one. A
   bare `name: fixer` step is a valid bare launch (runtime default model), which is
   also what the old `agent: fixer` produced once `agents:` was retired. Adding
   pins would have invented behavior at 20 sites. If you want the example pages to
   show a concrete tier, that is a deliberate content decision I'd rather you make.
2. **Examples.md preamble.** It claimed the recipes assume "the `fixer`/`planner`
   agents" — a retired concept. Changed to "steps named `fixer`/`planner`". Minimal,
   but it is a prose edit slightly outside the two numbered fixes; flagging it.
3. **Left alone, deliberately:** `Steps.md:128` ("`agent:` still parses, but it is
   now a free-form attribution label") and `Steps.md:304-313` (the Old→New migration
   table, including the `agent: <n>` row) — both are *documentation of* the retired
   idiom and correct as written. Likewise `runtimes.<n>.agent:` in Plugins.md:322,
   Runtimes.md:11 and Teams.md:26 (that is the ACP runtime's own field, current),
   and `Memory.md:48`'s `source: { agent: fixer, … }` (provenance output, not a step).
4. **Prose still framed around "profiles"** survives in a few places I did not touch
   because they were outside the brief's scope — e.g. Configuration.md:447 ("an agent
   profile opts into prompt injection with `memory: true` … non-opted profiles pay no
   tokens"). Worth a follow-up sweep if you want the retired vocabulary fully gone.
5. **Not validated end-to-end:** the four plugin-connector blocks (see † above), and
   `Gates.md` row 13 shares one config file with row 12 rather than being validated
   in isolation.
