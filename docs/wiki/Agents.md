# Agents

An `agents:` profile is a named, reusable definition of *what* to run — a provider, a model, a mode,
a workspace policy, and optional tone guidance — referenced by `agent:` on any `type: agent` step
across every trigger. It says nothing about *how* the process is executed; that's the job of a
[[Runtimes|runtime]], which the profile selects via `runtime:` (the legacy `controller:` key still
works). A profile may also pin `host:` — a [[Hosts]] SSH target its runtime launches on (cli/acp/
agent-deck runtimes).

A step's `agent:` may be a **template**, so a [[Workflows|workflow]] can choose the profile — and so
the runtime — per invocation instead of hard-coding it: `agent: "{{.inputs.reviewer}}"` with an
`inputs: { reviewer: { default: fixer } }`, invoked as `conductor run rev --input reviewer=other`.
The name is resolved against the step's data at dispatch; an unknown resolved name fails there (a
literal unknown name is still caught at load).

## Config

```yaml
agents:
  fixer:
    # extends: base                   # inherit another profile's fields (see [[Reuse]])
    provider: claude                  # -> paseo run --provider  (or provider/model shorthand)
    model: claude-opus-5              # -> --model     (optional; omit for the provider's default)
    thinking: ""                      # -> --thinking  (optional)
    mode: ""                          # -> --mode      (optional; one of the provider's modes)
    workspace: worktree               # local | worktree -> --new-workspace
    wait_timeout: 30m                 # -> --wait-timeout
    archive_when_done: true           # reaper archives the agent+worktree once it goes idle
    labels: { team: autopilot }       # extra --label pairs
    # runtime: paseo                  # optional: run this agent on a runtimes: entry (default:
                                      #   the default: true runtime; legacy `controller:` still
                                      #   works). provider/model above apply to paseo/opencode/
                                      #   agent-deck runtimes; an acp or cli runtime IS the named
                                      #   agent, so they're ignored for it (don't pair
                                      #   provider: claude with an acp gemini runtime)
    # host: build-box                 # pin this profile's runtime launches to a hosts: entry
    # guidance: |                     # per-agent tone/format; STACKS on the scoped baseline
    #   One or two sentences, plain and direct.   #   (policy.guidance / agent_guidance). List or
    #                                 #   { replace: … } also accepted — see [[Reuse]].
    # memory: true                    # opt into shared-memory prompt injection (see below); or a
                                      #   filter: memory: { scopes: [global, repo], tags: [ci], limit: 10 }
    # session:                        # session affinity: one live session per rendered key,
    #   key: "{{.repo}}#{{.pr}}"      #   shared across every trigger using this agent (see below)
    #   idle_ttl: 12h
    #   max_lifetime: 7d
    #   end_on: [ gh._closed ]        #   github's close-or-merge signal
  planner:                            # cheaper/faster model for planning/triage steps
    provider: claude
    model: claude-haiku-4-5
    workspace: local
    archive_when_done: true
```

| Field | Meaning |
| --- | --- |
| `extends` | Inherit from another `agents:` profile: unset fields are filled from the parent, `labels` deep-merge, and `guidance` stacks (parent tone under the child's). Chains allowed; cycles/unknown targets are load errors. See [[Reuse]]. |
| `provider` | Paseo provider name, or the `provider/model` shorthand (e.g. `codex/gpt-5.5`). Maps to `paseo run --provider`. |
| `model` | Model ID from `paseo provider models <provider>`. Maps to `--model`. Omit to use the provider's default. |
| `thinking` | Maps to `--thinking`. Optional. |
| `mode` | One of the provider's modes, from the `MODES` column of `paseo provider ls` (e.g. `plan`, `default`, `bypass`). Maps to `--mode`. |
| `workspace` | `local` or `worktree`. Maps to `--new-workspace` — whether the dispatched agent runs in the existing checkout or a fresh git worktree. |
| `wait_timeout` | Maps to `--wait-timeout` — how long a foreground (`Wait: true`) dispatch waits before giving up. |
| `archive_when_done` | Whether the reaper archives the agent (and its worktree) once it goes idle. |
| `labels` | Extra `--label key=value` pairs attached to the dispatched agent. |
| `runtime` | Name of a `runtimes.<name>` entry to run this agent on (default: the `default: true` runtime, else the built-in `paseo`). The legacy `controller:` key still works. See [[Runtimes]]. |
| `host` | A [[Hosts]] SSH target this profile's runtime launches on (cli/acp/agent-deck), overriding the runtime's own `host:`. |
| `guidance` | Per-agent tone/format that **stacks on** the scoped baseline ([[Policy\|`policy.guidance`]] / `agent_guidance`) rather than replacing it. A string, a list of blocks, or `{ replace: … }` to drop the baseline (and `{ replace: "" }` to disable guidance for this agent). Unset → inherit the baseline unchanged. See [[Reuse]]. |
| `memory` | Opt this agent into shared-memory prompt injection ([[Memory]]). `true` appends the global + target-repo + own-agent-scoped memories (newest first, capped) through the same path as `guidance`; a map `{ scopes, tags, limit }` narrows it. Absent → no injection, no token cost. Needs a top-level `memory:` section. |
| `session` | Session affinity: `{ key, idle_ttl, max_lifetime, end_on }` binds a live session to the rendered `key` — every event resolving to the same value reaches the same agent as a follow-up. Absent → a fresh agent per dispatch. See below. |
| `skill` | Opt this agent into the conductor skill (verb tools + the secret broker over the daemon socket), gated per profile: `{ verbs, secrets_via, allow_secrets, identity, max_calls }`. Absent → neither. See [[Agent-Skill]]. |
| `isolation` | Per-dispatch sandboxing for this profile's launches: `{ mode: user\|namespace\|container, user, container, limits, network }`. Wins over the runtime's own `isolation:`; requires a runtime conductor launches itself (not paseo). See [[Isolation]]. |
| `budget` | This profile's hard spend cap over a rolling window: `{ window, max_cost_usd, max_tokens }` — checked beside the global and workflow-scope budgets; over-cap dispatches shed and notify. See [[Cost-Accounting]]. |
| `outcome_feedback` | `true` appends a one-line track record (merged / closed / rejected / reverted counts) to this agent's guidance. See [[Outcomes]]. |

## Behavior

- `agents.<name>` profiles are referenced by name from `agent:` on any `type: agent` step, in any
  trigger — github, cron, webhook, sentry/pagerduty, rss, and slack triggers all share the same
  pool (legacy `integrations:` actions reference them the same way).
- `conductor validate` cross-checks every `agent:` reference against a defined `agents.<name>`
  profile — an unresolved reference fails validation before the daemon starts.
- `provider`/`model`/`mode`/`thinking` are validated against your **Paseo daemon**, not against
  conductor's own config schema — a provider that isn't installed/enabled in Paseo, or a model ID
  that provider doesn't recognize, fails at dispatch time even though `conductor validate` accepted
  the YAML shape. Check what's actually available with:
  ```sh
  paseo provider ls                    # providers + status (available/enabled) + available modes
  paseo provider models claude         # model IDs for a provider (use the ID column as `model:`)
  paseo provider diagnostic claude     # troubleshoot a provider's install/auth/availability
  ```
  Example output of `paseo provider models claude`:
  ```
  ID                     MODEL          DESCRIPTION
  claude-opus-5          Opus 5         Opus 5 · Latest release
  claude-sonnet-5        Sonnet 5       Sonnet 5 · Best for everyday tasks
  claude-haiku-4-5       Haiku 4.5      Haiku 4.5 · Fastest for quick answers
  claude-opus-4-8[1m]    Opus 4.8 1M    Opus 4.8 with 1M context window
  ```
  Put the ID column in `model:` and the provider name in `provider:`.
- `workspace` governs the profile's default checkout lifecycle (a persistent local checkout vs. a
  fresh worktree per dispatch); the action's own `checkout:` (`checkout-pr` | `branch-off` | `none`)
  governs what git state that checkout is put into for a specific trigger (an existing PR branch, a
  fresh branch off base, or no repo at all). The two are independent knobs — a `workspace: worktree`
  profile can still be dispatched with `checkout: none` for a triage-only step.
- Every step of a multi-step [[Workflows|workflow]] can name a different agent — a common pattern is
  a cheap `planner` profile assessing an issue, then handing off to a stronger `fixer` profile only
  if the assessment justifies it (`if: "{{.evaluate.has_context}} == true"`).
- `archive_when_done: true` agents are still protected from premature cleanup: the reaper skips one
  that's paused on a permission prompt, and an agent can hold itself alive by creating a
  `.paseo-hold` marker in its worktree (guidance for this is added to its prompt automatically).

## Session affinity (`session:`) — one agent per PR

By default each dispatch gets a fresh agent. A `session:` block instead
binds a live agent session to the rendered `key`, and every event resolving
to the same value — across ALL triggers that dispatch this agent — reaches
the SAME session as a follow-up prompt with full prior context:

```yaml
agents:
  reviewer:
    provider: claude
    runtime: paseo
    session:
      key: "{{.repo}}#{{.pr}}"            # same value → same live session
      idle_ttl: 12h                        # reap after this long idle (default 24h)
      max_lifetime: 7d                     # hard age cap (default 7d)
      end_on: [ gh._closed ]               # evict the moment the PR closes or merges

triggers:
  - on: [ gh.new_comment, gh.changes_requested, gh.failing_checks ]
    steps:
      - type: agent
        agent: reviewer
        prompt: "New activity ({{.kind}}) on {{.repo}}#{{.pr}} — continue."
```

The first event for a PR spawns the session; a later comment, review-change,
or check failure on the same PR arrives as a follow-up — the agent keeps the
whole conversation. How it behaves:

- **Shared pool.** The registry is keyed `(agent, key value)`, global across
  triggers and event types.
- **Serialized per key.** At most one prompt in flight per session;
  concurrent same-key events queue — the `group:` one-run-per-key guarantee,
  extended across the session's life.
- **Durable.** The key→session map persists in conductor's own state
  (`affinity.json`, beside audit/dedup); after a restart or auto-update the
  session resumes via the runtime's native handle (paseo re-binds the agent
  id, ACP `session/load`). While bound, the agent is held from the reaper.
- **Eviction.** Idle past `idle_ttl`, older than `max_lifetime`, or an
  `end_on` event (matched as `<connector>.<kind>`, a bare `<kind>`, or
  `<type>.<kind>`; rendered against the event's own context so it ends
  exactly that key's session). The event must be one conductor actually
  processes: any event kind a trigger can fire on qualifies, and github's
  PR close **and** merge both arrive as the internal close signal
  `_closed` — so `end_on: [ gh._closed ]` is the evict-when-done form.
- **`_closed` is eviction-only.** It clears per-PR state and matches
  `end_on:`, but it is not a trigger event — `on: gh._closed` fails
  validation.
- **Runtime support.** Needs a session-persistent runtime — paseo or ACP.
  One-shot `cli` runtimes fall back to a fresh agent per event; pair the
  profile with `memory:` ([[Memory]]) for continuity there.
- **vs `group:` and memory.** `group:` batches a burst into one run; memory
  injects durable facts; session affinity reuses a live conversation. They
  compose: group a burst, route it to the keyed session, with memory
  injected when the session first spawns.

Follow-up turns return `Queued` run refs: on paseo the prompt is delivered
via `paseo send` (no captured output for later steps); an ACP follow-up
returns the turn's output. A follow-up to a dead session (archived by hand)
is detected, unbound, and replaced by a fresh spawn.

## Agent-driven workflows

An agent step's final output may carry a ` ```plan ` block — steps in the
normal grammar that conductor validates, guards (`policy.agent_authored`,
[[Policy]]), and runs deterministically. Failures route back to this
agent's **session** (above) for a bounded revise loop, so a planning agent
should keep sessions; pairing with `memory:` lets it recall what worked.
The full loop — plan, choose from the catalog, supervise, promote — is in
[[Workflows]].

## Explanation

An agent and a runtime answer different questions. The **agent** profile answers "what should
run" — which provider, which model, what tone, what workspace lifecycle. The **runtime** answers
"how is it run" — which process or API actually executes it. A `fixer` profile with
`provider: claude` can run on the built-in `paseo` dispatcher, on `agent-deck`, or through
opencode's HTTP API, unchanged, just by pointing `runtime:` at a different entry — the provider
and model still route through whichever runtime is selected. The one place this decouples is an
**ACP** or **cli** runtime: there, the runtime's own `agent:`/`command:` names the tool directly
(e.g. `gemini` over ACP), so the profile's `provider`/`model` fields have nothing to route and are
ignored. With no `runtimes:` configured at all, every agent profile runs on `paseo`, so this
distinction is invisible until you actually introduce a second runtime. See [[Runtimes]] for the
full resolution order and runtime kinds ([[Controllers]] is the legacy name).
