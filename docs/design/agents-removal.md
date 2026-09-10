# Removing `agents:` — the identity decomposition

Companion to `docs/design/runtimes-models-packs.md`. This SUPERSEDES that doc's §6
"open decision" and specifies exactly how `agents:` is removed. Approved in
conversation with the maintainer.

## The finding

`agents.<name>` was doing **five** jobs, not one. The blocker for deleting it was
that `Action.Agent` is a load-bearing *identity* across four subsystems, not just a
model/behavior profile. Each job gets a natural, non-agent home:

| agent job | new home |
|---|---|
| which model to run (`provider`/`model`) | **fleets** (`model:` on the step) + the runtime roster |
| execution cost cap (budget) | **the runtime** |
| private memory namespace (`agent:` scope) | **an opaque scope key**, default = the step identity |
| live-session pool (session affinity) | **`(runtime, model, key)`**, key opaque, default = step identity |
| self-improvement track record (outcome) | **an opaque track-record key**, default = step identity |
| behavior (guidance, skill, memory opt-in, workspace, timeouts, archive, host, isolation) | **the step** (reused via `extends:`) |

There is no residual need for an `agents:` section. Remove the field,
`AgentProfile`, and `Agents` handling once every reference below is moved.

## 1. Budget → runtime

Per-agent budgeting/spend caps move onto `RuntimeConfig` (a budget is a cap on
execution cost on a backend; the runtime is the backend). Spend is tracked and
reported per runtime. Keep the existing budget mechanics (§14 cost/token
accounting) — only the anchor changes from agent to runtime. Team/global budgets,
where they exist, are unaffected.

## 2. Memory → opaque scope key (no privileged types)

The memory core must stop knowing about `repo`/`agent` scope *types* — `repo:` bakes
in a GitHub-specific concept and `agent:` bakes in agent identity. New model:

- A memory is **`global`** (no scope key) or stored under an **arbitrary opaque
  scope key** — a string the memory system never interprets. `global` is the empty
  case; it is NOT a keyword.
- `ResolveScope` and the `global`/`repo`/`agent` enum go away. `Recall`/`Remember`
  take opaque keys.
- The ENGINE supplies concrete keys from context as a *convention*, not privileged
  types: the current repo string (when the source has one), the workflow name, and
  the **step identity** (§5) as the default private namespace. These are just keys.
- `memory: { scopes: [...], tags, limit }` takes arbitrary key strings (or
  `${workflow}` / `${step}`-style refs). Default inject set = shared (no-key) + the
  context keys the engine provides. Opt-in per step, as today.

## 3. Session affinity → `(runtime, model, key)`

A live session is **runtime-bound AND model-bound** (a running agent is one model on
one runtime; you cannot resume a paseo session on codex, nor fan an opus step into a
haiku session). Both are STRUCTURAL dimensions, not identities:

- Binding key = **`(runtime, resolvedModel, renderedKey)`**. Drop the agent
  dimension (the store binding is currently `(agent, key)` — re-key it; the
  controller/runtime name is already recorded on the record).
- The `key` is an opaque template rendered against trigger context, as today.
- **Two scopes, both `(runtime, model, key)`:**
  - **overall affinity** — a `session:` on the **runtime**: one agent per key (e.g.
    per PR) shared across steps. Key used as-written (shared).
  - **step affinity** — a `session:` on the **step**: its own pool. Step-level keys
    are **namespaced to the step identity** so they are distinct from the overall
    pool and from other steps, even with an identical human-written key string.
  - resolution per dispatch: step `session:` wins if present, else the runtime's,
    else fresh. A step with no `session:` joins the overall pool.
- Because a session is model-bound and a pack assigns a fleet/model per step, **the
  pack's model assignments partition affinity for free** — same fleet+key → same
  agent; different fleet → different agent automatically. "One agent per PR" is
  really "one per `(runtime, model, key)`", a real limit, not a policy.
- `session:` moves off `AgentProfile`: the runtime provides the capability
  (`session_model`) + is the partition; the step/runtime provides the policy
  (`key`, `idle_ttl`, `max_lifetime`, `end_on`).

## 4. Outcome tracking → opaque track-record key

Currently `outcomeStats[agent]` accumulates merged/closed/rejected/reverted per
agent and `outcomeGuidance` injects that agent's track record into its next prompt
(opt-in `OutcomeFeedback`). Re-key it:

- `outcomeStats[<key>]`, where `<key>` defaults to the **step identity** (§5) — or
  an explicit opaque key the step declares. `Engagement` already carries `Workflow`/
  `SavedWorkflow`; use the step identity as the primary key and keep workflow as the
  coarser grouping it already is.
- `OutcomeFeedback` opt-in moves onto the step. The feedback still matches future
  dispatches to the past record because the step identity is stable across runs (§5).
- `RecordEngagement`/`BumpOutcome`/`AgentOutcomeStats` lose the agent parameter in
  favor of the step-identity key. Update the `report` command / audit accordingly.

## 5. Step identity — the single stable key everything defaults to

There is ONE notion of "step identity", and memory/session/outcome all default their
key to it. It MUST be stable across restarts (a per-run UUID is forbidden — it would
wipe track records, orphan memory, and break session rebind). Resolution ladder:

1. **explicit `name:`/`id:`** on the step → author-pinned. Stable across edits AND
   reusable across triggers (steps sharing a name — or `extends:`-ing a common
   template — share identity, reproducing the old `agent:fixer`-shared-everywhere
   behavior). This is the escape hatch for durability across refactors.
2. **structural id** — the enclosing qualified trigger/workflow identity + the
   step's slot (e.g. `github.pull_request/2`).
3. **deterministic fingerprint** of the step's canonical definition as the automatic
   floor — a pure function of config, recomputed identically every boot, no persisted
   state.

**Default is STRUCTURAL** (enclosing context + slot): editing a step's prompt must
NOT rotate its identity (which would wipe its track record); reordering may. `name:`
pins identity permanently and makes it shareable. No random UUIDs anywhere.

Implement this as one helper (e.g. `Step.Identity()` / a resolver in the engine) and
route memory-default-scope, session step-namespacing, and outcome keying through it,
so there is a single source of truth.

## 6. Behavior fields → the step (reuse via `extends:`)

`guidance`, `skill`, `session`, `memory` (opt-in), `workspace`, `wait_timeout`,
`archive_when_done`, `host`, `isolation`, `runtime`, and `model` move onto the step.
Where multiple triggers shared one named agent, that becomes a named step reached via
`extends:` (existing machinery) — preserve the reuse. `agent_guidance:` (layer-0
house rules) stays global unless it more naturally folds into the step guidance
stack; do not silently drop it.

## 7. Migration (`internal/migrate/`) — same fail-safe contract as `migrate/use.go`

Transform → re-validate → **refuse + restore** if the result would not validate.
Never leave a half-migrated config (config-incompat crash-loops the auto-updating
fleet — the degraded-boot invariant is mandatory).

- Each `agents.<name>`: `provider`+`model` → `model:` (exact pin) on the referencing
  steps; behavior fields → those steps (or a shared named step via `extends:` where
  the agent was shared); `session:` → the step (or runtime if it was a global
  per-PR pattern); budget → the runtime the agent used; `OutcomeFeedback`/`memory`
  opt-ins → the step.
- The migrated step keeps a stable identity: emit an explicit `name:` equal to the
  old agent name, so memory/session/outcome track records CARRY OVER (the old key
  was `agent:<name>`; the new default would be structural — setting `name:` to the
  old agent name preserves continuity). This is important: do not silently reset
  users' accumulated history.
- Remove `agents:`, `AgentProfile`, `Agents`, and per-agent-keyed store paths only
  after every dispatch/engine/session/outcome/memory reference is moved. Ship
  migration in the SAME change as the removal.

## 8. Tests

- budget on runtime: caps + spend attribution + report.
- memory: opaque keys; `global` = no key; no `repo`/`agent` types; engine-supplied
  context keys (repo/workflow/step); inject/recall by arbitrary key.
- session: `(runtime, model, key)` binding; overall (runtime) vs step (namespaced)
  scopes; step wins/inherits/fresh; model partitions (two fleets → two agents at one
  key); rebind across restart.
- outcome: step-identity keying; feedback matches across runs; report/audit updated.
- step identity: name → structural → fingerprint ladder; structural stable across a
  prompt edit; `name:` stable across reorder; NO per-run UUID.
- migration: `agents:` → steps/runtime round-trips and re-validates; refuse+restore
  on a config that wouldn't validate; old agent name preserved as step `name:` so
  memory/session/outcome history carries over; `agent_guidance` preserved.
- `go test ./...` green, `gofmt -l` clean, `go vet ./...` clean.
