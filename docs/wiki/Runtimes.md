# Runtimes

A runtime is where agents run — the block previously named `controllers:`
(which still loads; the two merge into one registry). The paseo runtime's
`bin:` replaces the old top-level `paseo_bin`.

```yaml
runtimes:
  paseo:  { type: paseo, bin: paseo, default: true }
  deck:   { type: agent-deck }
  gemini: { agent: gemini }                        # ACP transport
  remote: { type: cli, tool: claude-code, host: build-box }

agents:
  fixer: { provider: claude, runtime: paseo }      # `controller:` still accepted
```

## Fields

| field | meaning |
|---|---|
| `type` | built-in kind: `paseo` \| `agent-deck` \| `opencode` \| `cli` (mutually exclusive with `agent`) |
| `agent` | an agent runtime driven over a transport (gemini, opencode, …); implies `transport: acp` |
| `transport` | `acp` \| `native` \| `cli` |
| `session_model` | `native` \| `resumable` \| `oneshot` |
| `default` | the fleet default (at most one across runtimes + legacy controllers) |
| `bin` | the runtime binary (paseo, agent-deck) |
| `tool` / `command` | the bare-CLI recipe for `transport: cli` |
| `host` | a [[Hosts]] entry — cli/acp/agent-deck launches run there over SSH; rejected on paseo/opencode types (see [[Hosts]] for why) |

Resolution order for an agent: its explicit `runtime:` (or legacy
`controller:`) → the `default: true` entry → the built-in paseo. A profile's
own `host:` overrides the runtime's. Exactly one paseo binary per conductor:
two paseo runtimes naming different `bin:` values is a config error.

Everything else — session models, the session broker, capability
degradation, interactive hand-offs — carries over from the controllers
design unchanged; a runtime that owns an interactive surface is the default
hand-off for background review steps ([[Hand-offs]]).

Related: [[Agents]] · [[Hosts]] · [[Hand-offs]] · [[Configuration]]
