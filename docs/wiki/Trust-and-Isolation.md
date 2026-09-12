# Trust and isolation — what conductor's scoping is, and what it isn't

Conductor has a lot of scoping: [[Policy|`allow_scopes`]], `allow_memory_scopes`,
per-verb [[Agent-Skill|skill grants]], session confinement, the pack
[[Packs|`requires.connectors`]] boundary. It is worth being exact about which
of those are **walls** and which are **seatbelts**, because the answer differs
by who you are worried about, and a seatbelt described as a wall is worse than
no seatbelt at all.

## The short version

| Adversary | Is the scoping a security boundary? |
|---|---|
| **The author of an inbound event** (a PR author, a webhook sender, an issue commenter) | **Yes.** |
| **A third-party pack** you installed | **Yes.** |
| **An external plugin** you installed | Partly — see below. |
| **The agent itself**, when it runs as the daemon's OS user | **No.** Defense in depth only. |
| **The agent itself**, under OS isolation | **Yes**, as strong as the isolation. |

## A hard boundary: event authors

Everything that arrives from outside — a branch name, a PR title, a comment
body, a webhook payload — is treated as attacker-chosen, because it is.

- A target derived from request data is marked untrusted
  (`core.Trigger.TargetTrusted` defaults to **false**), so it gets no implicit
  own-repo, no own memory scope, no say in a session namespace, and none of
  the target-derived facts in a `{{ }}` allowlist entry.
- The facts an allowlist entry may interpolate are a closed set of
  platform-assigned values — `number`, `owner`, `name`, `repo`, `kind` — and
  the rule for that list is one question: *can the author of a pull request
  choose this value?*
- Per-object keys (dedup, the live review hand-off, PR labels) are built from
  the trusted repo, so a forged target cannot collide with a real one.

See [[Settings-and-Templating]] for the templating half.

## A hard boundary: packs

A pack is somebody else's code running in your config. `requires.connectors`
is its capability manifest, and it is enforced on **every** reference the pack
authors — step `uses:`, hook `uses:`, trigger sources, `session.end_on`,
`handoff:`, `approve_via:`, and the `store:` selector — at lint and again at
instantiate. A pack cannot reach a connector, or a store, that its manifest
does not name. `conductor.*` (daemon control) is refused outright.

## NOT a boundary: an agent sharing the daemon's user

**This is the important one.**

By default a dispatched agent runs as the same OS user as the daemon. For the
`paseo` runtime this is not a default, it is mandatory. When that is true,
conductor's agent-facing scoping — memory scopes, session confinement,
`allow_scopes`, skill grants — is **defense in depth, not a security
boundary**.

A process running as your user can, regardless of anything conductor checks:

- read the memory store, the config, the state directory and the audit log
  directly off disk;
- read `conductor.env`, and any secret material those files contain;
- read another dispatch's environment out of `/proc/<pid>/environ`, including
  credentials handed to that dispatch;
- connect to the daemon's socket as any process can, and talk to any other
  same-uid process.

What the scoping *does* buy in that setting is real, and is why it exists:

- it stops an **honest agent** from wandering — the overwhelmingly common
  failure, where a model does something reasonable-looking in the wrong repo;
- it makes a **prompt-injected** agent's easy paths fail, so an attack has to
  escalate from "ask the tool nicely" to "go around the tool", which is
  louder, slower, and far more likely to be noticed;
- it keeps the audit log meaningful: a refusal is recorded with the scope it
  refused;
- and it is a *hard* boundary the moment the agent does not share the user.

The socket itself is authenticated: every tool subprocess gets a
daemon-assigned per-dispatch credential, and the daemon resolves a request's
provenance from that credential rather than believing what the request says
about itself. A request that asserts its own provenance without one is
refused. That closes the trivial forgery — but a same-uid process can read the
credential out of another dispatch's environment, which is exactly the point
above.

**If you need agent scoping to be a security boundary, the agent must not
share the daemon's OS user.** See [[Isolation]] for what conductor can enforce
per runtime. Today that means a non-paseo runtime with an `isolation:` block;
paseo does not support it.

## Partly a boundary: external plugins

A plugin is a separate process, and conductor confines it to its declared
[[Authoring-Connectors|capability manifest]]: the egress hosts, commands and
paths it named, and its own type's credentials only. That is a real
restriction and a visible one — you see the manifest when you install it.

It is not an OS jail. A plugin that spawns a process spawns it as your user.
Read the manifest before installing, the same way you would read a pack's.

## Practical guidance

- Treat `allow_scopes`/`allow_memory_scopes` as **damage control and
  intent**, not as containment of a hostile agent.
- Put an agent you do not fully trust on an isolated runtime, or on a
  different machine ([[Hosts]]).
- Keep `trust: full` for cases where you genuinely do not need the seatbelt;
  it changes nothing about the walls above.
- If an agent's prompt can be steered by event text (a PR body, a comment),
  assume it *will* be, and scope the dispatch as if the attacker wrote the
  agent's instructions — because they can.

## See also

- [[Isolation]] — the per-runtime isolation conductor can enforce
- [[Policy]] — `policy.agent_authored`, `allow_scopes`, `allow_memory_scopes`
- [[Agent-Skill]] — per-verb grants and what the skill surface hands an agent
- [[Packs]] — the pack capability manifest
- [[Secrets]] — the broker, and why acting *through* conductor beats holding a
  credential
