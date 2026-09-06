# Agent Skill & Secret Broker

`skill:` on an agent profile lets a dispatched agent reach back into
conductor over the daemon's unix socket — the same socket the live
[[Memory]] tools ride. It is **off by default**: a profile without a
`skill:` block gets none of this, and every part of it denies unless config
explicitly allows.

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
      identity: bot                # the `as:` skill writes post under (falls back to
                                   #   policy.agent_authored.identity; REQUIRED when
                                   #   verbs admit a write — never silently the operator)
```

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
  is validated at load; the key resolves at issue time.

## How identity is bound

Authorization is bound to the **real dispatch, server-side**. When the
daemon dispatches a skill-enabled profile, it mints a **one-shot claim
code**, records code → (profile, dispatch target, that profile's `skill:`
policy) in memory, and delivers the code via the injected MCP server's
**environment — never argv** (argv is readable by any same-user process
through a process listing). At startup the tool subprocess exchanges the
code over the socket for the real session token: the exchange is
single-use, the code expires after ~2 minutes, and the token then lives
only in that process's memory.

The exchange also **binds the session to the claiming process**: the daemon
reads the connection's kernel peer credentials (Linux `SO_PEERCRED` plus the
process start time) and refuses the token from any other process afterward —
a copied token is useless. Client-asserted identity — like the
`--agent`/`--repo` provenance flags the memory tools carry — is never
consulted for authorization, so an agent (or any other same-user process
that can reach the socket) cannot claim another profile's policy. Sessions
expire after **2 hours** (roughly a dispatch's lifetime — a long-lived
session that ages out loses its skill tools, never gains a stale identity)
and die with the daemon (the table is in-memory).

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
issued-but-never-used or used-after-delay grant is visible).

## Injected guidance

A skill-enabled profile's prompts get a short appended blurb (the same
append path as `agent_guidance`) telling the agent what it has and how to
behave: prefer the verb tools (naming the profile's patterns), use the
broker only as a last resort (naming the allowed secrets, only when
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

## Scope and limitations

- Once a value is redeemed it is in the agent runtime's hands. The broker
  minimizes exposure (one name, one use, short window, full audit) — it
  does not police what an external runtime does with the value afterward.
  If a runtime shouldn't ever hold a secret, don't list any in its
  `allow_secrets`.
- The daemon socket is same-user only (`0600`). Anything running as that
  user can dial it; the token binding means such a process still cannot
  obtain secrets outside a dispatched, skill-enabled profile's policy.
- The encoding-aware redaction of tracked secrets (base64/hex/url forms) is
  a backstop, not a guarantee — see [[Secrets]]. The guarantee is this
  design: prefer verbs-as-tools, and when a value must be handed out, hand
  it out minimized.

## Verbs as tools

The **default path**: the verbs a profile's `skill.verbs` patterns match are
served to the agent as MCP tools — `gh.comment` appears as a `gh_comment`
tool, `rest.*` exposes every declared verb of the `rest` connector, and so
on. The daemon computes the toolset per dispatch from the token-bound
profile, complete with each verb's option schema; the agent calls the tool,
conductor executes the verb with its own credentials, and only inputs and
outputs cross the socket. No credential enters the agent session at all.

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
- **Writes post as a distinguished identity, never as the operator.**
  `skill.identity` (falling back to `policy.agent_authored.identity`) is
  forced onto every verb that takes `as:` — including over an `as` the agent
  supplied — and config validation REQUIRES one of the two whenever
  `skill.verbs` admits an as-taking write verb.
- **Every call is audited** (`event: verb, via: skill`) with the agent,
  target, and redacted options; outputs are redacted before they return to
  the agent.

See [[Verbs]] for the verbs themselves.
