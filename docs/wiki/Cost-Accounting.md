# Cost & token accounting

Agents burn money, so spend is a first-class operational control (#36 §14):
every agent run is metered, spend aggregates per run / workflow / repo / day,
and hard `$`/token caps shed work before it launches.

## Capture

When an agent run returns, conductor resolves its usage:

1. **Runtime-reported** — the run's JSON output is scanned for the usage
   shapes the supported runtimes emit: `usage: {input_tokens, output_tokens}`
   (Anthropic / claude-code), `usage: {prompt_tokens, completion_tokens}`
   (OpenAI-style), `tokens: {input, output}` (opencode), a top-level
   `total_cost_usd`/`cost_usd` figure (claude-code), including one level
   under the common `result`/`output` wrapper keys (paseo `run --json`).
2. **Estimated** — anything else is approximated from model + I/O size
   (chars/4 on prompt and output), priced by the model table, and marked
   **approximate**. Estimated figures are a floor, not a claim of precision.

A reported `$` figure wins over a computed one; reported tokens are priced by
the model table when no `$` figure came along.

## Where it lands

- **Run record** — the persisted workflow run carries `tokens` / `cost_usd` /
  `approx_cost`, updated at each checkpoint.
- **Audit** — one `agent_usage` row per agent run (repo, kind, agent, step,
  run id, workflow scope, model, tokens in/out, `$`, approximate), one
  `workflow_cost` row per completed/failed workflow run (cost per run), one
  `budget_shed` row per shed.
- **Turns** — the model turns a run took (claude-code's `num_turns`; one per
  agent run when a runtime reports none), on the run record and each
  `agent_usage` row, beside tokens.
- **Per runtime and model** — the run record keeps its usage per runtime and
  model (`usage:`), and every report shows it that way: usage is **never summed
  across providers** (a Claude run and a Codex run are separate lines).
- **`conductor report`** — a usage section, per runtime/model, with
  breakdowns by repo, by workflow/kind and by day inside each, plus the shed
  count. In `usage.mode: api`, dollar figures too, and the agent-quality
  section ([[Outcomes]]) adds **cost-per-merged-change**.
- **`conductor usage [--since 24h]`** — what conductor observed per
  runtime/model, then what each configured usage source reports (below).

## Showing usage (`usage:`)

```yaml
usage:
  mode: off            # off (default) | subscription | api
  sources:             # optional
    - { name: proxy, type: http, url: "http://127.0.0.1:8080/usage", headers: { Authorization: "Bearer ${PROXY_TOKEN}" } }
```

- **`mode`** decides what reports and notifications show. It is **display
  only**: every cap — runs, turns, tokens or dollars — enforces in every mode,
  `off` included.
  - `off` (the default): no usage or cost figures at all. Existing configs get
    this, and nothing about enforcement changes.
  - `subscription`: what conductor **observed** of its own runs — runs, turns,
    tokens — per runtime/model, labelled as observed. No dollars: on a
    subscription they would be estimates of money nobody bills.
  - `api`: the same, plus dollar figures (API billing).
- **Plan windows and utilization** (a 5-hour window, a weekly limit, how much of
  it is used) come **only** from a configured **usage source**. Conductor
  sees only its own runs, not the account, so without a source it reports
  nothing about the plan.
  - A source is pluggable by `type`. The built-in **`http`** type GETs a JSON
    document from anything that reports its own usage, a proxy or a gateway:
    `{"providers": [{"provider": "…", "model": "…", "window": "5h", "used": 40,
    "limit": 100, "unit": "messages", "utilization": 0.4, "resets_at": "…"}]}`.
    Each entry is shown per provider/model and labelled with the source's name.
    No vendor is assumed.
- **Model spend is optional.** Budgets can cap in units: `max_tokens` here,
  and runs/turns/tokens on a bot's proactive budget ([[Bots]]). On a
  subscription, leave the dollar caps out.

## Pricing

Estimation prices tokens with a built-in model→`$`/1M-tokens table (coarse by
design — model pricing drifts). Override it:

```yaml
pricing:
  models:
    "claude-opus-*":  { input: 15.00, output: 75.00 }   # $ per 1M tokens
    "my-local-model": { input: 0, output: 0 }
  default: { input: 3.00, output: 15.00 }               # unmatched models
```

Patterns are globs over the model name; the profile's `model:` (or the model
the runtime itself reported) selects the row.

## Hard budgets

`budget:` is a hard cap over a rolling window, at three scopes:

| Scope | Where | Ledger key |
|---|---|---|
| global | `policy.budget` | everything |
| profile | `agents.<name>.budget` | that agent profile |
| workflow | a trigger-level (or connector-level) `policy.budget` | that trigger (`on` + variant name) |

```yaml
policy:
  budget: { window: 24h, max_cost_usd: 25 }
runtimes:
  paseo:
    budget: { window: 1h, max_tokens: 500k }   # a budget caps cost on a BACKEND
triggers:
  - on: gh.review_requested
    policy:
      budget: { max_cost_usd: 5 }        # window defaults to 24h
```

- `window` defaults to 24h; `max_cost_usd` and/or `max_tokens`
  (`500k`/`2m` shorthand) must be set — an empty budget is a config error.
- **Every** governing scope must be under cap for a dispatch to launch.
- Scope precedence for the *workflow* cap follows policy merging
  (trigger → connector); a global-only budget is charged once, as global.
- The check **reserves** the dispatch's estimated spend atomically, and the
  reservation counts against the window until it's settled with the actual
  usage (or released if the dispatch never runs) — so a team's parallel
  workers can't all pass an under-cap read and collectively overshoot a hard
  cap. Once a scope has any spend, a dispatch whose estimate wouldn't fit
  under the cap sheds up front.

**Shed semantics** — identical to the agents-per-hour count budget: the
dispatch does not launch, the attempt is recorded (so the backoff/sweep
machinery re-derives PR-kind work once the window frees), a `budget_shed`
audit row is written, and a notification (escalate) goes out. In a running
workflow an over-cap agent step fails that run with the budget error (its
`fail` hooks fire); the re-derive happens at the trigger layer.

The spend meter is in-memory with the same semantics as the agents-per-hour
window: a daemon restart resets the rolling windows; the durable record is
the audit trail.

## Relation to `agent_authored.limits.tokens`

The plan-level token cap (`policy.agent_authored.limits.tokens`, #36 §11)
bounds ONE agent-authored plan's size at guard time. `budget:` meters real
spend across runs over time. They compose; neither replaces the other.

Related: [[Policy]] · [[Steps]] · [[Commands]] · [[Outcomes]]
