# Runtimes

A runtime is where agents run — the block previously named `controllers:`
(which still loads; the two merge into one registry). The paseo runtime's
`bin:` replaces the old top-level `paseo_bin`.

```yaml
runtimes:
  paseo:  { use: paseo, bin: paseo, default: true }
  deck:   { use: agent-deck }
  gemini: { use: acp, agent: gemini }              # ACP transport
  remote: { use: cli, tool: claude-code, host: build-box }
  modal:  { use: modal }                           # a runtime PLUGIN, co-equal

x-templates:
  fixer: &fixer { type: agent, runtime: paseo }           # a step pins its backend
```

`use:` resolves a runtime exactly like a connector or an engine — a **built-in**
first (`paseo`, `agent-deck`, `acp`, `opencode`, `cli`), then the **official plugin repo**
(`runtimes/<name>`, e.g. `modal`), then an explicit repo (`use: acme/plugins/modal`)
or a local binary. The official runtimes live in the catalog:
**[conductor-plugins/docs/runtimes](https://github.com/NodeSpy/conductor-plugins/tree/main/docs/runtimes)**
(and the [index](https://github.com/NodeSpy/conductor-plugins/blob/main/docs/README.md)) —
each with its own setup and config reference. See [[Plugins]] for versioning and trust.

## Three shapes, and the name implies `use:`

The common case is one word. A runtime's **name is its `use:` reference** when
the two match, so you write `use:` only when the name differs from the
implementation — or when it points at a plugin repo, a URL, or a local path.

```yaml
runtimes: paseo                    # scalar: one runtime
```
```yaml
runtimes: [paseo, claude]          # list: several; each item's name implies its use:
```
```yaml
runtimes:                          # map: named, with config
  paseo:
    models: { prefer: [claude-opus-5, gpt-5.6-sol] }
  claude:
    models: { allow: ["claude-opus-*"] }
```

A list item may also be an object, in which case it must carry `use:` (its name
is taken from the reference): `- { use: acme/plugins/modal }`.

The implication is applied **after** `extends:` resolves, so a child that
inherits a parent's `use:` keeps it rather than falling back to its own key.

## Choosing a model

The optional `models:` block on a runtime says what it may run, how it ranks a
choice, and what it defaults to:

```yaml
runtimes:
  paseo:
    models:
      default: claude-opus-5                  # omit -> BARE LAUNCH (no --model)
      prefer:  [claude-opus-5, gpt-5.6-sol]   # ranking when a fleet offers a choice
      allow:   ["claude-opus-*", "gpt-5.6-*"] # a ceiling on what may ever run
```

Omit the block and the runtime is fully automatic: roster discovered, nothing
restricted, default = bare launch. See [Model selection](Model-Selection)
for fleets and the resolution ladder, and
[Model discovery](Model-Discovery) for where the roster comes from.

## Fields

A runtime may `extends:` another runtimes entry to inherit unset fields (e.g. several `cli`
runtimes sharing a `host:`/`isolation:` — the child overrides only `command`). See [[Reuse]].

| field | meaning |
|---|---|
| `extends` | inherit unset fields from another `runtimes:` entry (see [[Reuse]]) |
| `use` | **what implements it**: a builtin — `paseo` \| `acp` \| `agent-deck` \| `opencode` \| `cli` — or a plugin reference. Same resolution as a connector's `use:`; see [[Plugins]]. **Defaults to the entry's own name**, so `paseo: {}` needs no `use:` |
| `models` | optional model policy — `{ default, prefer, allow }`; see [Model selection](Model-Selection) |
| `agent` | the agent the ACP transport drives (gemini, …). Valid **only** with `use: acp`, and required by it |
| `transport` | `acp` \| `native` \| `cli` |
| `session_model` | `native` \| `resumable` \| `oneshot` |
| `default` | the fleet default (at most one across runtimes + legacy controllers) |
| `bin` | the runtime binary (paseo, agent-deck) |
| `home` | **paseo only** — the daemon home conductor targets. paseo 0.9 gained multi-home daemons and the `--home` selector (default `~/.paseo`); conductor detects the paseo version at boot and passes `--home <home>` on 0.9+, omitting it on older paseo (which has no such flag). `~` is expanded. Empty → the `PASEO_HOME` env, else paseo's own default. Set this when your paseo daemon runs at a non-default home (e.g. `paseo daemon run --home ~/work`) so clones/launches reach it instead of failing with `DAEMON_NOT_RUNNING` |
| `tool` / `command` | the bare-CLI recipe for `transport: cli` |
| `session` | the OVERALL session-affinity pool for this runtime: `{ key, idle_ttl, max_lifetime, end_on }`, shared by every step without its own. See [[Steps]] |
| `budget` | this backend's hard spend cap: `{ window, max_cost_usd, max_tokens }`. A budget caps EXECUTION COST, and the runtime is where execution happens — this is where per-agent budgets moved to. See [[Cost-Accounting]] |
| `host` | a [[Hosts]] entry — the runtime executes there over SSH: cli/acp/agent-deck wrap their launch, a paseo runtime runs its whole CLI remotely (with a dedicated dispatcher), and opencode is reached through an `ssh -W` forward (see [[Hosts]]) |

Resolution order for a step: its explicit `runtime:` → the `default: true`
entry → the built-in paseo. A step's own `host:` overrides the runtime's
(cli/acp/agent-deck). Each paseo runtime
with its own `bin:` or a `host:` gets a dedicated dispatcher; the
default local one is the primary that command steps and provisioning share.

Reclaim is **done-driven — nothing scans paseo looking for things to
archive.** Conductor records every agent and workspace it launches in an
ownership ledger (`owned.json` beside the state file), and an archive can only
ever name an id in that ledger: a workspace conductor did not create is
structurally untouchable. A foreground step's agent (and its conductor-created
workspace) is archived the moment the step finishes; a hand-off's when it calls
`handoff.done` (or its `idle_timeout` fires); any other agent when it calls the
auto-granted `step.done` — every agent is instructed to do so as its final
action. A dispatch that failed before the agent came up tears its own workspace
down immediately. The trade is deliberate: a conductor-launched agent that dies
without signalling leaves its own workspace behind (archive it by hand), and in
exchange conductor can never touch anything it didn't launch. A retry that
finds a prior run's `conductor/<kind>-<n>` worktree still present **reuses** it
(launching another agent into it) rather than colliding on the deterministic
branch name.

Everything else — session models, the session broker, capability
degradation, interactive hand-offs — carries over from the controllers
design unchanged; a runtime that owns an interactive surface is the default
hand-off for background review steps ([[Hand-offs]]).

## Session persistence

A `session:` block — on this runtime (the overall pool) or on a step (its
own), see [[Steps]] — needs a runtime whose sessions survive between
dispatches by id: **paseo** (follow-ups via `paseo send`; resume re-binds
the agent id) and **acp** (follow-ups via `session/prompt`; resume via
`session/load` where the agent negotiates it). One-shot runtimes (`cli`)
don't participate — a `session:` on them silently stays fresh-per-event and
leans on [[Memory]] for continuity.

A runtime conductor launches itself (acp / cli / opencode / agent-deck) may
carry an `isolation:` block — per-dispatch sandboxing and the network egress
allowlist for every launch it performs; a step's own `isolation:` wins.
Not applicable to paseo runtimes (their agents are the paseo daemon's
children) — `conductor validate` rejects that combination. See [[Isolation]].

Related: [[Steps]] · [[Hosts]] · [[Hand-offs]] · [[Configuration]] · [[Isolation]] · [[Plugins]]
