# Paseo provider

paseo agents can run in conductor's [[Isolation|workspace jail]] without paseo knowing anything about jails. conductor ships a small paseo plugin that adds a **`conductor` provider** to paseo. An agent created on it is launched and held by the conductor daemon, not by paseo:
- conductor starts the real agent (claude-code, codex, gemini, … over ACP) on one of its own acp runtimes, always in the workspace jail;
- it relays ACP between that agent and paseo.

paseo shows and drives the agent like any other. The process, its jail, its host commands, its environment and its credentials are conductor's.

```
paseo ──ACP──▶ conductor's socket ──▶ the relay ──ACP──▶ the agent, in the workspace jail
         (plugin: one connector)       (conductor)           (conductor's acp runtime)
```

## Setup

1. An acp runtime for the agent, and the `paseo_provider` block naming it:

   ```yaml
   runtimes:
     acp_claude:
       use: acp
       agent: claude-code
       command: [claude-agent-acp]    # the ACP adapter; an npm install in ~/.local/bin is fine
   paseo_provider:
     runtime: acp_claude              # required: an acp runtime on this box, jail-eligible
     # mcp_servers: [docs]            # MCP servers paseo passes that the agent keeps, by name (default: none)
     # detached_ttl: 1h               # how long an agent with no paseo stream stays up (default 1h)
     # socket: ~/.local/state/conductor/paseo-provider.sock   # the default
   ```

   The runtime must run on this box, not opt out of the jail (`isolation: { mode: none }`, `privileged: true`), and use `use: acp`. `conductor validate` refuses anything else. A paseo session never runs unjailed: if the jail can't be built, the session fails.

2. Restart (or reload) the daemon. It serves the socket and logs `paseo provider: serving on …`.

3. Install the plugin into paseo:

   ```sh
   conductor paseo install-plugin                   # the default paseo home
   conductor paseo install-plugin --paseo-home DIR  # another one
   ```

   The plugin is embedded in the conductor binary, so the plugin paseo runs is always the one this conductor speaks to. The command:
   - writes it under conductor's state (`<state>/paseo-plugin`);
   - has paseo load it from there in place (`paseo plugin install <dir>`);
   - reloads it after a conductor upgrade changed it (`paseo plugin reload conductor`);
   - otherwise does nothing.

   It needs the paseo daemon running, never touches the network, and is safe to run on every deploy. A `conductor` plugin paseo loads from somewhere else is left alone, with an error.

4. In paseo, pick the **Conductor** provider: `paseo run --provider conductor …`, or the provider picker in the app.

## How a session works

The relay speaks only the ACP spec, nothing of paseo's internals.

- **Session id.** conductor answers `session/new` with a session id of its own (`cj2.…`); paseo stores it and echoes it on `session/load`.
  - conductor keeps the session's record in its state (`<state>/paseo-provider/sessions.json`): its cwd, identity, and the agent's own session id.
  - So a session survives anything short of an archive.
- **Lazy launch.** Over ACP, paseo's model listing (a `session/new` straight followed by `session/close`) looks like a real session. So `session/new` is answered from a one-time capability probe of the agent, and **the agent is launched on the session's first real request**: a prompt, or a mode/model change.
  - A model listing launches nothing and leaves no record.
  - A session's first prompt pays the agent's start-up.
- **Identity is the cwd.** conductor creates the workspaces it hands its member and worker agents, so a session's cwd says whose it is (see [Identity](#identity)). The identity keys the agent's tool state and is the jail's `AgentID`. A cwd conductor doesn't know is an identity of its own, the session's.
- **One agent per session.** It sees the session's cwd as its workspace and runs with the runtime's isolation, as a launch with no dispatch target.
- **What crosses unchanged:** everything, except:
  - the session id (conductor's toward paseo, the agent's own toward the agent);
  - paseo's request ids (renumbered, so a dead stream's late answer never reaches a new one);
  - MCP servers (below).

### MCP servers: dropped unless named

ACP's `session/new|load` carries `mcpServers`, and paseo adds its **own** server to every session. That server carries a paseo bearer token, and its tools create and drive agents on any provider, unjailed ones included. ACP can't tell it apart from the operator's own servers, so the rule is deny by default:
- every MCP server paseo passes is dropped, and audited (`mcp_dropped`, by name);
- `paseo_provider.mcp_servers: [docs, …]` keeps the ones it names.

> **Footgun:** `mcp_servers: ["*"]` keeps them all, including paseo's. The jailed agent then holds paseo's credential and can start an unjailed agent and hand it work. Name servers instead.

conductor's own tools (`conductor call`, memory) reach the agent the way they reach any jailed agent; they don't come from paseo.

## Lifecycle

conductor owns it. Over ACP, three different paseo events look the same: a paseo restart, an agent unloaded, and an agent archived in paseo by hand. Each is the stream ending, maybe after a `session/close`. So:

- **A stream ending, or `session/close`, only detaches.** The jailed agent keeps running.
- **`session/load` re-attaches.** It goes to the live agent if there is one. If not, conductor relaunches it and has it load its own session (the agent's transcripts live in its identity's tool state). This holds across a paseo restart, a conductor restart, and an agent ended by the TTL.
- **`detached_ttl` (default 1h)** ends an agent nothing re-attached, and its jail. The session record stays, so the TTL bounds resources and never loses a session.
- **conductor's own archive ends a jail at once.** When conductor archives an agent it created, it calls `ArchivePaseoCwd(cwd)`. That ends every session opened in that cwd and forgets them; a later `session/load` of one is refused.
- An agent archived **by hand in paseo** just detaches, and its jail goes at `detached_ttl`.

Archive works by cwd, so **each live agent gets a cwd of its own**: a worker its workspace, a member-home session its own worktree. The relay audits `cwd_shared` when two live sessions share one; archiving that cwd ends both.

Audit rows (`event: paseo_provider`), by `status`:
- `launched`, `attached`, `resumed` (`relaunched: true|false`);
- `detached`, `expired`, `archived`, `exited`;
- `launch_failed`, `mcp_dropped`, `cwd_shared`.

The jail's own rows (`jail`, `host_command`, `egress`, …) carry the session as their dispatch (`paseo-cj2.…`).

## Identity

The integration point for whatever creates conductor's paseo agents (members):

```go
// internal/controller
func SetPaseoIdentity(lookup func(cwd string) (identity string, ok bool))
func ArchivePaseoCwd(cwd string) int
```

- `SetPaseoIdentity` installs the cwd → identity lookup. It is called once per session, when the session is created; `ok=false` means "not one of ours".
- The identity names the agent's tool state (`<state>/agent-state/paseo-<identity>`, see [[Isolation#per-identity-tool-state]]) and is its jail's `AgentID`.
- `ArchivePaseoCwd` ends and forgets every session opened in cwd, returning how many.

## What it depends on in paseo

The plugin is about 40 lines, half of them the socket-to-ACP-stream plumbing. It uses **only** this paseo surface, so this is the maintenance cost of the provider:

| paseo surface | used for |
|---|---|
| `@getpaseo/plugin/server`: the `PluginServerContext` type, `registerProvider` | registering the provider |
| `@getpaseo/plugin/server/acp`: `runAcpProvider({ id, label, connector })`, the `AcpStream` type | an ACP provider whose stream conductor supplies |
| `paseo-plugin.json`: `id`, `description`, `requirements.paseo` | the manifest; it declares `paseo >=0.10.1`, the first version it was run on |
| the plugin layout: `index.server.ts`, server modules under `server/` | `server/endpoint.json` holds the socket path |
| CLI: `paseo plugin install <dir>`, `paseo plugin reload <id>`, `paseo plugin ls --json` (`id`, `path`), `--home` | `conductor paseo install-plugin` |
| ACP itself: `initialize`, `session/new|load|prompt|close`, `session/update`, `mcpServers` | the relay; nothing paseo-specific |

Nothing else of paseo's: not its session config, env, `_meta`, providerOptions, MCP URLs, or lifecycle hooks. A test (`internal/paseoplugin`) fails if the plugin imports or names anything outside this table.

**The canary** (`test/paseo-canary/run.sh`, workflow `paseo-canary`) runs daily against the **latest published** paseo, and on any change to the provider:
- it installs paseo and the plugin into a throwaway home;
- it runs one session through the relay to a fake ACP agent in the jail.

A paseo release that breaks this surface shows up as a red check, not a broken box. Run it locally with any paseo on `PATH`: `bash test/paseo-canary/run.sh`.

## Limits

- **paseo's ACP provider surface is narrower than its native claude provider:** no rewind of conversation or files, no steer, no output schema, no per-tool permission policy, no subsessions. These are paseo's ACP adapter's limits, not conductor's.
- **Session listing isn't relayed.** paseo's "import an existing session" lists nothing for this provider.
- **Archiving by hand in paseo** ends the jail only at `detached_ttl`, not at once.
- **The agent CLI needs an ACP adapter** (claude-code: `claude-agent-acp`; codex: `codex-acp`). OpenCode is a single shared server, not a per-agent process, so it can't be a provider session.
- **A session's first prompt waits for the agent to start;** `session/new` itself is instant.
