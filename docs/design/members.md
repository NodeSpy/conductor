# Members — design notes (#172)

User-facing reference: docs/wiki/Members.md. This page records the decisions
behind it and the seams left for conductor's agent sandbox.

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
  resolves controller dispatches through the non-sandbox half of #160's
  `ccc6331`, ported verbatim. Nothing in the worker path assumes a jail: a
  worker is an ordinary cli run, so the sandbox confines it the way it
  confines any cli step.

## One enforcement path

The gate is `flow.MemberGate`, consulted inside `RunSkillVerb` after the
skill policy's access and resource checks. Approved execution runs back through
`RunSkillVerb` with the approval in its context; the gate admits exactly the
signed digest of an `approved` proposal, once.

## Binding

`config.Step.BoundMember/BoundMemberRole` (`yaml:"-"`) mark the synthesized
worker step; `SkillEnv` stamps the member, role and the case id from the
daemon-built trigger into the session token (durable, like a hand-off's).
Nothing an agent sends can set them.

## Terraform: conductor applies, members propose

Members never hold a write role and never run a write command; there are no
host-command proposals and no grants. An infrastructure change is a
**proposal of a commit** (`member.Terraform`, proposal kind `terraform`):

- **Inputs are pinned.** Repo (a name from `terraform.repos`, never a URL),
  full commit SHA, root-module dir, workspace, var files, targets. Validation
  refuses anything that escapes the checkout or isn't a full SHA. The gate
  tiers the proposal as `terraform.apply`; a `never` tier refuses it before
  any planning.
- **Conductor plans it** in `<state>/members/<m>/tf/<proposal>/` (0700; the
  plan file 0600): clone `--no-checkout`, fetch the commit if needed,
  `checkout --detach`, verify `HEAD`; `init`, `workspace select`/`new`,
  `plan -out`, `show -json` → counts and replaced/destroyed addresses,
  `show -no-color` → the operator's text, `sha256(plan.bin)`. Planning runs in
  the background; the proposal is `planning` until it lands as `pending`,
  `refused`, `shadow` or `failed`.
- **Roles come from connector config**, never from the agent:
  `plan_connector` / `apply_connector` are ordinary `terraform` connectors
  whose `engine`/`binary`/`env` conductor reads (`Config.MemberTerraformRole`)
  and runs the CLI with directly. Inherited `TF_*`/`AWS_*`/`GOOGLE_*`/`ARM_*`
  are stripped so the connector's `env` alone decides the role. Validation
  makes it an error for any member `skill.verbs` pattern to reach the apply
  connector. Terragrunt is refused (no single saved plan).
- **The approval covers the plan.** The digest is
  `ActionDigest(member, terraform, terraform.apply, {repo, commit, dir,
  workspace, var_files, targets, plan_sha256})`; the SSHSIG path is unchanged.
- **Apply checks, in order:** `sha256(plan.bin)` equals the approved hash; the
  apply role resolves; `HEAD` is the commit; `git status --porcelain
  --untracked-files=all` shows nothing except `init` artifacts inside the
  module dir (`.terraform/`, `.terraform.lock.hcl`, local `terraform.tfstate*`).
  Then `apply -input=false -lock=true plan.bin`. Terraform's own check makes a
  stale plan fail; the reason names `replan`, which plans the same inputs as a
  new proposal (`ReplanOf`).
- **Destroys.** Any destroy or replace of a resource whose type matches
  `terraform.stateful` (default `config.DefaultStatefulTypes`) is refused at
  plan time unless the address matches `terraform.allow_destroy`. A plan with
  no changes is refused too.
- **Plan retention: none.** A plan file can embed secrets from variables, so
  the work dir is deleted on every terminal outcome (applied, failed, denied,
  expired, refused, shadow) and orphans are removed at daemon start
  (`Recover`, which also fails proposals left `planning`/`approved` by a
  restart). The ledger keeps the hash, the counts and the text.
- **How a change becomes a commit (seam).** Conductor only fetches from the
  configured repo; how the commit gets there is the session's job, through the
  `git.push` verb once it exists (the guidance names it, and a missing commit
  fails with "push it first (git.push)"). Until then an operator pushes for it.

## Agent sandbox integration

Behind `member.Sandbox` (`Active`, `PaseoProvider`) until conductor's agent
sandbox is on main; `member.NoSandbox` is the default. The switch is in
`cmd/conductor/members.go`:

```go
func sandboxSeam() member.Sandbox {
	return member.SandboxFuncs{
		IsActive: func() bool { return <the sandbox is enforcing agent launches> },
		Provider: func(base string) (string, bool) { return <the paseo provider entry `conductor paseo setup` made for base, e.g. claude-sandboxed> },
	}
}
func registerSandboxSessions(svc *member.Service) { <sandbox endpoint>.SetSessionLookup(… svc.SessionForCwd …) }
```

With it: promotion takes effect; the home session launches on the sandboxed
provider entry for its base provider (the model pin unchanged); the sandbox's
wrapper endpoint resolves a launch's cwd through `Service.SessionForCwd`
(`IdentityForCwd` wraps it) — a live home session or hand-off worker, never a
retired one — to mint that session's token with the member binding. Tokens
under rotation: the retiring session's are revoked at once, the new session
gets fresh ones (`TestWrapperTokensAcrossRotation`). cli workers are launched by
the engine and sandboxed like any cli step; nothing member-specific is needed.

## Proactive sweeps

- **A sweep is a cli worker**, not a new launcher: the member's third
  synthesized `on: manual` trigger (`member:<name>:sweep`, only with
  `proactive:`), the worker step with the sweep's timeout and — when the budget
  sets a cost — a 24h workflow `policy.budget` the engine enforces.
- **Read-only is the gate's, not the prompt's.** The sweep's token is bound to
  the sweep id (the case slot), its worker record carries class `proactive`
  and `read_only`, and `Gate.caller` resolves both from the ledger.
  `proactive` is outside the autonomy allowlist (`event`, `member`,
  `operator`), and read-only refuses autonomy on its own: either lock holds
  alone.
- **The notify decision is conductor's.** The agent reports findings with a
  severity and a stable key; `finishSweep` dedupes by key against open cases,
  opens a case (class `proactive`) only at or above `notify_at`, notifies on a
  new case or an escalation past the threshold (the home inbox + the
  `conductor.member_notify` lifecycle event, via `notify.Publish`), and records
  the rest quietly (ledger + `episodes/proactive/YYYY/MM/<date>.md`). A quiet
  sweep writes one ledger record and one journal line.
- **Budget** counts the ledger's sweeps over a rolling 24h: starts, and cost
  from the run history (`RunCost`, recorded at `run_done`). One running sweep
  at a time (checked inside the ledger transaction that creates the next).
  Quiet hours are local time with an optional IANA zone; an operator's
  explicit `conductor member sweep` ignores them, never the budget.

## Coordination: parents, children, peers

The rule: **instructions flow down, authority never grows.**

- **Parent → child.** A worker's record names its parent (`home:<session>`, or
  `conductor` for workers conductor starts itself). Follow-ups
  (`member tell`) are stored on the worker and handed over at its next member
  op — the cli runtime has no mid-turn injection — and `CheckDone` refuses a
  child's `step.done` while one is unread. They are rendered as instructions,
  not fenced.
- **Child → parent** is data: `step.done` output and `member report` are fenced
  `WORKER REPORT` blocks in the home inbox. A fan-out group (`spawn --group`)
  reports once, decided inside the ledger transaction of its last finish.
- **Provenance is inherited.** At spawn the worker records the less trusted of
  its case's requester and the parent's `homeAttribution` (the least-trusted
  open case) — computed inside the same ledger transaction. The gate reads a
  worker's class from its record (by dispatch), not from the case, and fails
  closed (`user`) without one. Conductor-started workers (urgent events,
  consolidation) take the case's provenance alone: their parent is not a
  conversation that could have been steered.
- **The ceiling.** `--verbs` patterns are checked by the gate after the
  member's own policy admitted the call, so they can only narrow; `--read-only`
  holds the child to observe. Children cannot spawn, join, tell, approve or ask.
- **Peers** (`member ask`) need an operator link (both sides' `peers:`, or a
  `member_links:` pair), arrive as a case of class `peer` (requester
  `member:<name>`, ranked with a third party), carry only the message text
  (fenced), are audited on both sides, and are capped at 4000 bytes and 20 per
  hour per direction. A reply (`ask --case <peer case>`) is routed into the
  sender's original case.

### Persistent sub-members: left out

A member creating a long-lived helper member does not fit cleanly:

- members are operator config, and nothing on the agent-facing surface writes
  config — a runtime-created member would need conductor to rewrite the config
  file, or a second, state-held member registry beside it;
- "a charter scope that is a subset of the parent's" is free text and cannot be
  checked mechanically;
- there is no member archive operation to cascade (a member is removed by
  editing config).

The clean equivalent today is operator-side: declare the helper as its own
member and link it as a peer (data only), or use ephemeral workers (instructions
down, same authority or less).
