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
      allow_secrets: [deploy_key]  # exact `secrets:` names the broker may issue
      verbs: [gh.comment, rest.*]  # verbs exposed as agent tools (see Verbs as tools)
```

- `secrets_via: none` (the default, including when the key is absent): the
  agent gets no secrets at all.
- `secrets_via: broker`: the agent may request the secrets named in
  `allow_secrets` through the broker tools below.
- `secrets_via: env` is **deprecated**: it means you template the secret
  into the step's `env:` yourself, which places the raw value in the
  runtime's environment for the whole run. `conductor validate` warns about
  it.
- `allow_secrets` takes exact names from the `secrets:` block — no
  patterns. Broadening access is a config edit, never an agent request.

## How identity is bound

Authorization is bound to the **real dispatch, server-side**. When the
daemon dispatches a skill-enabled profile, it mints an unguessable session
token, records token → (profile, dispatch target, that profile's `skill:`
policy) in memory, and bakes the token into the MCP tool command it injects
into the agent session. Every broker call authorizes by that token alone.

Client-asserted identity — like the `--agent`/`--repo` provenance flags the
memory tools carry — is never consulted for authorization, so an agent (or
any other same-user process that can reach the socket) cannot claim another
profile's policy. Sessions expire after 24 h and die with the daemon (the
table is in-memory).

## Boundary handles: `{{secret "name"}}`

Config can template a named secret with `{{secret "gh_pat"}}`. It renders as
an **opaque handle** — `«secret:gh_pat»` — everywhere a template renders: an
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
`{{secret}}` names are validated at load time against the `secrets:` block,
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

Skill-enabled profiles can also call conductor's own verbs as agent tools —
the default path, where no credential enters the agent session at all. See
the `verbs:` key above; the toolset is documented alongside [[Verbs]].
