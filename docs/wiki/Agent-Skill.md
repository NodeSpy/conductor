# Agent Skill & Secret Broker

`skill:` on an agent profile lets a dispatched agent reach back into
conductor — its verbs, the live [[Memory]], and the secret broker — while it
runs. It is **off by default**: a profile without a `skill:` block gets none
of this, and every part of it denies unless config explicitly allows.

## How the surface reaches the agent

The agent reaches conductor one of two ways, chosen automatically from the
runtime. There is nothing to configure for the local case:

| delivery | runtimes | how |
|---|---|---|
| **MCP tools** | ACP runtimes (`agent: gemini`, …); `type: opencode` (native HTTP) | conductor injects an MCP server at launch (`mcpServers` on `session/new`; a per-session `OPENCODE_CONFIG` file for opencode). The verbs, memory, `run_step`, and broker appear as native tools. |
| **`conductor` CLI** | `type: paseo`, `type: agent-deck`, bare `cli` — any local runtime with a shell | the agent shells the `conductor` command. The daemon puts the endpoint + a scoped session token in the agent's **environment** (`CONDUCTOR_ENDPOINT`, `CONDUCTOR_SKILL_TOKEN`), and the injected guidance tells the agent to run `conductor discover` / `conductor call`. No config files, no MCP server. |

A **remote** runtime (`host:`) uses the same CLI face, reached over an SSH
reverse tunnel instead of the local socket — see [Remote agents](#remote-agents)
below. No config; it just works over the SSH conductor already uses.

Only a runtime conductor can't resolve at all leaves a `skill:` profile inert
(no tools, no broker, no injected guidance — promising an absent surface only
breaks agents); `conductor validate` warns about it.

> The earlier design injected a `.mcp.json` into a paseo run's worktree. That
> never worked — the Claude Code agent paseo launches is started with paseo's
> own `--mcp-config` and ignores a project `.mcp.json` in headless mode — so
> paseo (and every other shell runtime) now uses the CLI face instead. It is
> provider-agnostic and writes nothing into the workspace.

## The principle

**The credential never leaves the daemon by default.** The default way for
an agent to act with conductor's credentials is to act *through* conductor —
tool calls that dispatch conductor's own verbs, where the daemon injects the
credential at its own egress boundary and the agent only ever sees inputs
and outputs. When an agent must run a raw tool that itself needs a
credential (a `git push`, a CLI that reads a token from env), the **secret
broker** hands one out as a minimized last resort: scoped to one named
secret, single-use, short-TTL, and fully audited. Secrets are never static
in an agent's prompt or env-at-rest.

## Enabling it

```yaml
agents:
  deployer:
    provider: claude
    model: claude-sonnet-5
    skill:
      secrets_via: broker          # broker | env (deprecated) | none (default)
      allow_secrets: [house/deploy_key]  # exact vault entries (<vault>/<key>) the broker may issue
      verbs: [gh.comment, rest.*]  # verbs exposed as agent tools (see Verbs as tools)
      max_calls: 100               # per-session verb-call cap (default 256)
```

Identity is **not** a skill setting. Each verb carries its own `as:` option (when
it has one), and the connector applies its own default when the agent omits it —
GitHub writes default to `me` (the connector's `identity.write_token`). An agent
may pass `as:` per verb call to choose a configured identity; the skill layer
imposes nothing of its own.

- `secrets_via: none` (the default, including when the key is absent): the
  agent gets no secrets at all.
- `secrets_via: broker`: the agent may request the secrets named in
  `allow_secrets` through the broker tools below.
- `secrets_via: env` is **deprecated**: it means you template the secret
  into the step's `env:` yourself, which places the raw value in the
  runtime's environment for the whole run. `conductor validate` warns about
  it.
- `allow_secrets` takes exact vault entries, named `<vault>/<key>` (the
  same entries `{{ vault "house" "deploy_key" }}` reads) — no patterns.
  Broadening access is a config edit, never an agent request. The vault half
  is validated at load; the key resolves at issue time. Setting
  `allow_secrets` **without** `secrets_via: broker` is a **load error** — the
  broker only issues to a broker session, so the grants would never resolve;
  the config is rejected rather than failing silently at runtime.

## How identity is bound

Authorization is bound to the **real dispatch, server-side**, by a session
token the daemon mints at dispatch and hands the agent in its environment
(never argv, which any same-user process can read from a process listing).
The token maps server-side to (profile, dispatch target, that profile's
`skill:` policy); client-asserted provenance — like the `--agent`/`--repo`
flags the memory tools carry — is never consulted for authorization, so an
agent (or any same-user process that reaches the socket) cannot claim another
profile's policy. Sessions expire after **2 hours** (roughly a dispatch's
lifetime — a session that ages out loses its skill tools, never gains a
stale identity) and die with the daemon (the table is in-memory).

Two binding shapes, matching the two delivery paths:

- **CLI face (session token, uid-bound).** The token is reusable across the
  many short-lived `conductor` processes one dispatch runs. On the local
  socket the daemon reads the caller's kernel uid (`SO_PEERCRED`) and refuses
  a token presented from a **different uid** — a token scraped from one
  agent's env is useless to a process running as someone else.
- **MCP face (one-shot claim, process-bound).** The injected MCP server
  receives a single-use **claim code** (env, ~2-minute TTL) and exchanges it
  over the socket for the session token; the exchange binds the session to
  that exact process (`SO_PEERCRED` + start time), so a copied token is dead
  anywhere else. This tighter binding fits the single long-lived MCP
  subprocess; the CLI's uid binding fits its fan-out of short calls.

A **remote** agent reaches the socket through an SSH reverse tunnel (see
below), so the connection the daemon sees comes from conductor's own ssh
relay, not the agent — the uid check passes trivially and provenance rests on
the session token alone. That's why the memory / `run_step` ops always derive
their `Source` from the token, never from the caller's peer identity. The
one-shot claim exchange stays local-only; a remote agent gets its session
token directly in its env.

## Using it from the agent (the CLI face)

On a shell runtime the agent drives conductor with four commands. Discovery
is **progressive** — the agent never has to swallow the whole verb catalog to
find what it can do, so a profile with a large `verbs:` allowlist doesn't
bloat the prompt:

```
conductor discover                     # connectors this agent may act through
conductor discover gh                  # verbs on the gh connector
conductor discover gh.comment          # one verb's options
conductor discover -s deploy           # search verbs by name/description
conductor call gh.comment --body "…"   # run a verb server-side (see Verbs as tools)
conductor memory recall <query>        # read shared memory  (see Memory)
conductor memory remember <text> [--tags a,b] [--scope s]
conductor secret <name>                # last-resort broker value (see below)
```

Each command authorizes by the `CONDUCTOR_SKILL_TOKEN` in the agent's
environment and is audited daemon-side exactly like the MCP tools. On an
MCP-delivery runtime the agent calls the equivalent tools instead; the
capabilities and the policy behind them are identical.

## Remote agents

An agent dispatched to a **remote runtime** (`host:`) runs on another box and
can't see the daemon's unix socket. Rather than expose conductor on a public
URL, the back-channel rides the **same SSH trust conductor already uses to
launch paseo there**: the daemon holds an `ssh -R <remote.sock>:<daemon.sock>`
reverse tunnel that forwards its own socket onto the remote box, bound to a
`0600` unix socket. The remote agent's `conductor` CLI dials that forwarded
socket exactly as a local agent dials the real one — same commands, same
token, same audit. Nothing binds a public interface; there is **nothing to
configure**.

Mechanics, for the curious:

- The tunnel is a daemon-lifetime process, one per remote host, opened on the
  first remote skill dispatch and supervised (restarted on drop). paseo `run`
  returns immediately — the agent keeps running under paseo's own remote
  daemon — so the channel can't ride the run invocation; it's held
  independently.
- It reuses the host's own `key` / `port` / `known_hosts` from `hosts:`, so it
  is exactly as trusted (and as reachable) as the SSH conductor already uses.
  `StreamLocalBindMask` keeps the remote socket owner-only; `ExitOnForwardFailure`
  makes a failed forward observable rather than silent.
- The agent is handed `CONDUCTOR_ENDPOINT=unix://<forwarded-sock>` + its
  session token in env — a plain `unix://` path, indistinguishable from the
  local case to the CLI.

The security boundary is the SSH channel itself: only conductor can open the
reverse forward, and the forwarded socket is `0600` on the remote box. The
session token still gates every op (deny-by-default, audited); a token leaked
off-box is useless because there is no route to the socket except through
conductor's own tunnel.

## Boundary handles: `{{secret "name"}}`

Config can template a secret with `{{secret "house/gh_pat"}}` (a vault
entry, `<vault>/<key>`). It renders as an **opaque handle** —
`«secret:house/gh_pat»` — everywhere a template renders: an
agent step's prompt, its `env:`, tool arguments, audit entries, logs. The
real value replaces the handle only at conductor's **own egress boundary**:

- a verb invocation's outbound options (`uses:` steps and hooks),
- a code step's `env:`/`args:` (conductor runs the interpreter itself),
- a remote command step's env/argv (conductor SSHes to a config-named host).

Two rules keep the handle from becoming a resolution oracle:

1. **Agent-authored steps never resolve.** Plans, live `run_step` calls, and
   saved workflows keep handles opaque whatever their trust level — a plan
   step that pastes or templates a handle sends the inert text, not the
   value.
2. **Eligibility is keyed to the config-authored template source.** A handle
   only resolves in a step whose own raw template literally calls
   `{{secret "name"}}` for that name. A handle that arrives through *data* —
   an agent echoing its env into a step output that a config step relays —
   is not eligible and passes through as text.

Agent steps have no conductor-side egress (the env goes to the external
runtime), so their handles never resolve at all: env-at-rest carries the
handle, and an agent that truly needs the value uses the broker below.
`{{secret}}` names are validated at load time (the vault must be defined)
and must be literal. Local `type: command` steps run through the agent
runtime, not conductor — use a code step (`run: sh`) when conductor itself
should inject the value.

## The secret broker

Two agent tools, riding the injected MCP server (advertised only when the
dispatch carries a session token):

1. `secret_issue { name }` → `{ grant, expires }`. Issued **only if** the
   dispatching profile's `skill.secrets_via` is `broker` and `name` is in
   its `skill.allow_secrets`. The grant is bound to the issuing session,
   **single-use**, and expires **60 seconds** after issue.
2. `secret_redeem { grant }` → the secret value, exactly once, before the
   grant expires. A second redeem, a redeem after expiry, or a redeem from
   a different session is refused.

The two-step shape is deliberate: the value only materializes at the last
moment, a grant id that leaks into a log or transcript is dead within a
minute, and the audit trail shows issue and use as separate events (so an
issued-but-never-used or used-after-delay grant is visible). On the CLI face
`conductor secret <name>` performs both steps and prints the value once.

## Injected guidance

A skill-enabled profile's prompts get a short appended blurb (the same
append path as `policy.guidance`) telling the agent what it has and how to
behave. On an MCP runtime it names the verb tools and the broker tools; on a
shell runtime it names the `conductor discover`/`call`/`memory`/`secret`
commands (and points at `discover` for the verb list rather than dumping it,
so the prompt stays small). Either way: prefer acting *through* conductor,
use the broker only as a last resort (naming the allowed secrets, only when
`secrets_via: broker`), never echo or store a redeemed value, and pass
`«secret:…»` handles through unchanged. Profiles without `skill:` get
nothing. The whole guidance string is redactor-filtered before injection —
it can never carry a tracked secret value.

## Audit trail

Every broker outcome writes an audit entry (`event: secret_broker`) with an
`action` of:

| action   | meaning                                                        |
|----------|----------------------------------------------------------------|
| `issue`  | a grant was issued (profile, repo, secret name, grant id)      |
| `use`    | a grant was redeemed                                           |
| `deny`   | a refused request, with the reason (policy, unknown token, reuse) |
| `expire` | a grant expired — at redeem time, or unredeemed via the sweeper |

The secret **value** never appears in audit entries, logs, or notifications
— those paths all run through the resolver's redaction as well.

## Related: agent-authored resource allowlists

The skill governs how an agent reaches back into conductor mid-session. The
workflows an agent AUTHORS (plans, live `run_step`, saved workflows) are
separately bounded by `policy.agent_authored`'s resource allowlists —
`allow_secrets` / `allow_stores` / `allow_targets`, deny-by-default with the
triggering repo implicitly allowed and `trust: full` as the lift-everything
escape. See [[Policy]].

## Scope and limitations

- Once a value is redeemed it is in the agent runtime's hands. The broker
  minimizes exposure (one name, one use, short window, full audit) — it
  does not police what an external runtime does with the value afterward.
  If a runtime shouldn't ever hold a secret, don't list any in its
  `allow_secrets`.
- The daemon socket is same-user only (`0600`). Anything running as that
  user can dial it; the token binding means such a process still cannot
  obtain secrets outside a dispatched, skill-enabled profile's policy. For a
  remote agent the same socket is reached through an SSH reverse tunnel only
  conductor can open, bound `0600` on the remote box — so the boundary is the
  SSH channel plus the same per-token policy; nothing is exposed to any network.
- The encoding-aware redaction of tracked secrets (base64/hex/url forms) is
  a backstop, not a guarantee — see [[Secrets]]. The guarantee is this
  design: prefer verbs-as-tools, and when a value must be handed out, hand
  it out minimized.

## Verbs as tools

The **default path**: the verbs a profile's `skill.verbs` patterns match are
exposed to the agent — as MCP tools on an MCP runtime (`gh.comment` appears
as a `gh_comment` tool, `rest.*` exposes every declared verb of the `rest`
connector), or via `conductor call gh.comment --body "…"` on a shell runtime.
Either way the daemon computes the allowed set per dispatch from the
token-bound profile, complete with each verb's option schema; the agent
invokes it, conductor executes the verb with its own credentials, and only
inputs and outputs cross the boundary. No credential enters the agent session
at all.

Ground rules on this surface:

- **Pattern matching is the same as `policy.agent_authored`**: exact names
  and path globs (`gh.comment`, `rest.*`); `conductor.*` verbs are
  exact-match-only and never served here, and `workflow.*` is excluded —
  agent-authored orchestration goes through the `run_step` tool and its
  policy guard instead.
- **The skill surface never exceeds the plan surface.** A verb
  `policy.agent_authored.approve` gates behind human approval cannot be
  served as a skill tool: config validation rejects a `skill.verbs` pattern
  that would admit an approve-gated verb (there is no approval hand-off on
  the tool surface), and the runtime refuses such a call regardless. The
  `no_secret_egress` posture carries over as the unconditional write/relay
  barriers below.
- **Options are literal.** Agent-supplied options are never
  template-rendered (no `{{.secrets…}}` evaluation) and never resolve
  `{{secret}}` handles.
- **The write/relay barriers apply unconditionally**: tracked secret
  material in a tool call's options is refused before it reaches shared
  state or an external connector.
- **Identity is per-verb, defaulting to the connector's default.** A verb's
  own `as:` option travels through as the agent supplies it; when absent, the
  connector applies its own default (GitHub writes default to `me`, the
  `identity.write_token`). The skill layer forces no identity of its own.
- **Every call is audited** (`event: verb, via: skill`) with the agent,
  target, and redacted options; outputs are redacted before they return to
  the agent.
- **Calls are capped per session** (`skill.max_calls`, default 256) — a
  session cannot hammer verbs unbounded within its TTL; the cap refusal is
  audited.
- **Patterns are validated against the real registry**: a literal unknown
  connector (or a `workflow.*`/`conductor.*` pattern, never served here) is
  a load error, and `conductor validate` warns about a pattern that matches
  no verb on this daemon (typo, or a credential-disabled connector).

See [[Verbs]] for the verbs themselves.
