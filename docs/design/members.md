# Members — design notes (#172)

User-facing reference: docs/wiki/Members.md. This page records the decisions
behind it and the seams left for the agent jail.

## The issue's open questions, answered

- **Ledger home.** Conductor's own state (`members.json` beside
  `affinity.json`/`plans.json`, written through `store.persist`), with paseo as
  a client of the operator CLI. The ledger is what a fresh session resumes
  from and what the gate decides against (case requesters, spend), so it lives
  with the gate. A `members.json` that does not parse is moved aside and the
  ledger flagged degraded; commit-money is refused while degraded (a vanished
  spend history must not reset a cap).
- **Approval surface without chat.** `conductor member approve` signs the
  proposal's digest with the operator's SSH key and sends it over the operator
  control socket; the daemon verifies against `operators.<name>.ssh_key`. Works
  with paseo + the CLI alone. A chat approval path (an `ask` hand-off whose
  answerer maps to an operator alias) can be added later on the same
  `Executor.Decide`.
- **Rotation trigger.** Age, delivered turns, the agent being gone, or an
  operator request. Context size is not observable through paseo today; turns
  are the proxy. Rotation waits for the old session's handoff turn to
  *settle* (paseo `send` returns when the agent stops, which includes a
  permission prompt mid-turn).
- **Memory review flow.** Direct commits by conductor, one per write, with
  trailers; review is `git log`, correction is a commit or a revert. A PR flow
  can wrap the same repo later; nothing in the member depends on it.

## Launch paths (no parallel launcher)

- **Home:** the paseo `Dispatcher` (labels, ownership ledger, skill session),
  through the `member.HomeLauncher` seam (`PaseoHome`). The model resolves
  through the engine's fleet ladder like any step's.
- **Workers:** each member expands at load into two `on: manual` triggers;
  `spawn` fires one through the same path as `conductor run` — an ordinary run
  on the **cli runtime** (history, watch, budget, guidance, skill card). Only
  `--handoff` uses paseo, through the ordinary background hand-off path.
- cli sessions get their conductor session (endpoint + token) and `step.done`
  resolves controller dispatches through the non-jail half of #160's
  `ccc6331`, ported verbatim.

## One enforcement path

The gate is `flow.MemberGate`, consulted inside `RunSkillVerb` after the
grant's access and resource checks. Approved execution runs back through
`RunSkillVerb` with the approval in its context; the gate admits exactly the
signed digest of an `approved` proposal, once.

## Binding

`config.Step.BoundMember/BoundMemberRole` (`yaml:"-"`) mark the synthesized
worker step; `SkillEnv` stamps the member, role and the case id from the
daemon-built trigger into the session token (durable, like a hand-off's).
Nothing an agent sends can set them.

## Agent jail integration (#160 + #175)

Behind `member.Jail` (`Active`, `ArchiveCwd`, `DigestFile`) until both are on main. The switch, in
`cmd/conductor/members.go`:

```go
func jailSeam() member.Jail {
	return member.JailFuncs{IsActive: <jail enabled for paseo sessions>, Archive: controller.ArchivePaseoCwd, Digest: <broker-view sha256>}
}
func registerJailIdentity(svc *member.Service) { controller.SetPaseoIdentity(svc.IdentityForCwd) }
```

`JailFuncs` also takes `Digest` (the jail's "SHA-256 of this path as the broker
reads it, for this identity"): command proposals and grant minting hash through
it, and `NoJail.DigestFile` refuses, so no infra grant exists before the jail.

With it: promotion takes effect; the home session launches on paseo's
`conductor` provider (`conductor paseo install-plugin`); retiring a session
ends its ACP sessions by cwd; a home session's command grants bind to its jail
identity `member-<name>-<session>` (the jail's `AgentID`), and a cli worker's to
its dispatch id; the broker consumes them from the daemon's one
`grant.Store` (`grantStore` in members.go).
