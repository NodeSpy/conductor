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

agents:
  fixer: { provider: claude, runtime: paseo }      # `controller:` still accepted
```

## Fields

A runtime may `extends:` another runtimes entry to inherit unset fields (e.g. several `cli`
runtimes sharing a `host:`/`isolation:` — the child overrides only `command`). See [[Reuse]].

| field | meaning |
|---|---|
| `extends` | inherit unset fields from another `runtimes:` entry (see [[Reuse]]) |
| `use` | **what implements it** (required): a builtin — `paseo` \| `acp` \| `agent-deck` \| `opencode` \| `cli` — or a plugin reference. Same resolution as a connector's `use:`; see [[Plugins]] |
| `agent` | the agent the ACP transport drives (gemini, …). Valid **only** with `use: acp`, and required by it |
| `transport` | `acp` \| `native` \| `cli` |
| `session_model` | `native` \| `resumable` \| `oneshot` |
| `default` | the fleet default (at most one across runtimes + legacy controllers) |
| `bin` | the runtime binary (paseo, agent-deck) |
| `tool` / `command` | the bare-CLI recipe for `transport: cli` |
| `host` | a [[Hosts]] entry — the runtime executes there over SSH: cli/acp/agent-deck wrap their launch, a paseo runtime runs its whole CLI remotely (with a dedicated dispatcher and reaper), and opencode is reached through an `ssh -W` forward (see [[Hosts]]) |

Resolution order for an agent: its explicit `runtime:` (or legacy
`controller:`) → the `default: true` entry → the built-in paseo. A profile's
own `host:` overrides the runtime's (cli/acp/agent-deck). Each paseo runtime
with its own `bin:` or a `host:` gets a dedicated dispatcher and reaper; the
default local one is the primary that command steps and provisioning share.

Everything else — session models, the session broker, capability
degradation, interactive hand-offs — carries over from the controllers
design unchanged; a runtime that owns an interactive surface is the default
hand-off for background review steps ([[Hand-offs]]).

## Session persistence

An agent profile's `session:` block (session affinity — one live agent per
key, see [[Agents]]) needs a runtime whose sessions survive between
dispatches by id: **paseo** (follow-ups via `paseo send`; resume re-binds
the agent id) and **acp** (follow-ups via `session/prompt`; resume via
`session/load` where the agent negotiates it). One-shot runtimes (`cli`)
don't participate — a `session:` profile on them silently stays
fresh-per-event and leans on [[Memory]] for continuity.

A runtime conductor launches itself (acp / cli / opencode / agent-deck) may
carry an `isolation:` block — per-dispatch sandboxing and the network egress
allowlist for every launch it performs; a profile's own `isolation:` wins.
Not applicable to paseo runtimes (their agents are the paseo daemon's
children) — `conductor validate` rejects that combination. See [[Isolation]].

Related: [[Agents]] · [[Hosts]] · [[Hand-offs]] · [[Configuration]] · [[Isolation]] · [[Plugins]]
