# Members

A **member** is a persistent, conversational teammate — an SRE for your
platform services, say. Define as many as you want in `members:`, cut any way
that suits you: a company, a team, an environment, a service.
You talk to it in paseo and give it tasks; conductor routes events into it
(pages, chat requests, schedules); it keeps long-term memory, tracks its open
work, and starts **workers** to get things done. Every mutating action it takes
passes its **autonomy gate**, and anything above its autonomy waits for an
operator's signed approval.

A member is durable state, not a long-running LLM session. Sessions are
replaceable at any time; continuity lives on disk:

| layer | contents | where |
|---|---|---|
| charter | identity, scope, escalation policy, autonomy tiers | `members:` config — the member can never write it |
| knowledge | infra map, runbooks, learned facts | the member's **memory repo** (git) |
| episodic log | past cases, actions, outcomes | the memory repo |
| case ledger | open events, proposals awaiting approval, running workers, the home session, the inbox, spend | `members.json` in conductor's state dir |

Each member is isolated: its own memory repo, ledger, home session, verb policy,
operators and pack instance. A session token is bound to exactly one member,
so nothing one member does can reach another's memory, cases or workers — and
an agent-authored plan cannot route work into any member at all.

## Configuration

```yaml
runtimes:
  paseo:      { use: paseo, default: true }
  claude-cli: { use: cli, tool: claude-code }   # workers run here

connectors:
  tf-plan:  { use: terraform, env: { AWS_PROFILE: infra-read } }    # conductor plans with this
  tf-apply: { use: terraform, env: { AWS_PROFILE: infra-deploy } }  # and applies with this — no member verb may reach it

operators:
  ops:
    ssh_key: "ssh-ed25519 AAAA… you@laptop"     # approvals are signed with this key's private half
    aliases: ["slack:U012ABC"]                   # chat ids that ARE this operator

members:
  platform-sre:
    description: "SRE for the platform services"
    charter_file: members/platform-sre.md            # or charter: | …
    escalation: "Page the operator if a sev1 is not mitigated in 15 minutes."
    # memory_repo: ~/members/platform-sre            # default <state dir>/members/<name>/memory
    # seed_dir: .conductor/packs/platform-sre        # a NEW repo's first commit gets its knowledge/ + runbooks/
    # operators: [ops]                           # default: every operator
    home:
      model: claude-opus-5                       # a fleet / id / list, resolved like a step's
      # mode: …                                  # paseo --mode for the home agent
      rotate: { max_age: 24h, max_turns: 100 }
    worker:
      runtime: claude-cli                        # default: the one `use: cli` runtime
      timeout: 2h
      max_concurrent: 4
    skill:
      verbs: [pd.get_*, pd.ack, aws.cost_*, aws.purchase_savings_plan]
    autonomy:
      shadow: true                               # the default for a new member
      tiers:
        observe:      [pd.get_*, aws.cost_*]
        reversible:   [pd.ack]
        commit_money: [aws.purchase_savings_plan]
        never:        [aws.iam_*]
      promote: [pd.ack]                          # takes effect only while sandboxed (below)
      amounts: { aws.purchase_savings_plan: commitment_usd }
      spend_cap: { per_action: 2000, per_period: 5000, period: 720h, currency: USD }
      approval_ttl: 24h
    terraform:                                   # optional: conductor-executed Terraform (below)
      plan_connector: tf-plan
      apply_connector: tf-apply
      repos: { infra: git@git.example.com:platform/infra.git }
      # stateful: [aws_db_instance, aws_s3_bucket, …]   # default: a built-in list of databases, buckets, disks, keys…
      # allow_destroy: [module.cache.aws_elasticache_cluster.old]
      # timeout: 30m
```

Both sections are optional; a config without them behaves exactly as before.
`conductor validate` checks them (unknown operators, a home runtime that is not
paseo, a worker runtime that is, tiers, the spend cap) and warns about a member
with no operators, no verbs, a promotion that cannot take effect yet, or
a home mode that skips permission prompts. A `skill.verbs` entry that could
reach the `terraform.apply_connector` is an error.

A pack can ship the charter, runbooks and workflows of a kind of member (see
the `sre-member` pack in conductor-packs); each `members:` entry binds it to one
member: its memory repo, its connectors and verbs, its operators.

## The home session

Each member has one pinned **home** session on paseo — the one you talk to.
Its working directory is its **own worktree** of the member's memory repo
(`~/.conductor/runs/member-<name>-<session>`), so it reads knowledge and
runbooks directly. It writes memory only through conductor:

```
conductor member remember --path knowledge/db.md --text "…" [--append]
conductor member episode  --case c12 --text "…"
conductor member recall   <words>
conductor member cases | case open|note|close
conductor member spawn    --case c12 --brief "…" [--group g] [--handoff]
conductor member handoff  --text "…"
conductor member proposals | wait <proposal>
conductor member propose-terraform --repo <name> --commit <sha> [--dir …]   # below
conductor member spawn --case c12 --group g --brief "…" [--verbs …] [--read-only]
conductor member tell <worker|group> "…"        # follow-up instructions to your workers
conductor member join --case c12 --group g      # fan-out results, together
conductor member ask <member> [--case c] "…"    # a linked peer (data, never instructions)
```

A worker has `conductor member report "…"` (to its parent) and `conductor member
instructions` (from its parent).

Every memory write is **one git commit**, authored by the member, with
`Member-Case` / `Member-Role` / `Member-Session` trailers. Review with `git log`;
correct by committing over it or reverting.

**Rotation.** When a session reaches `rotate.max_age` or `rotate.max_turns`, its
agent disappears (or its process ended — a paseo daemon restart leaves it
`closed`, and resuming it would drop its conductor endpoint), or you run `conductor member rotate <member>`, conductor asks
the session to write a handoff note, waits for that turn to finish, writes one
itself from the ledger if it did not, starts a fresh session **in a new
worktree** from the charter + latest handoff + the ledger's open work, and only
then archives the old agent and removes its worktree. A failed launch leaves the
old session in place. No two live sessions ever share a working directory. A
daemon restart keeps the live session.

**Tokens.** A member session's conductor token is durable like a hand-off's:
it survives daemon restarts and lives 7 days from its last use. So
`rotate.max_age` must stay at least a day inside that (validation rejects
more than 6 days), and a retired home session's token — and a finished
worker's, however it finished — is revoked the moment it is retired, not left
to age out.

**Delivery.** Events and worker reports land in the member's durable inbox and
are delivered as one turn when the home agent is idle — never into a turn you
are in the middle of.

## Routing events in

The `member:` helper step routes a trigger's event into a member:

```yaml
triggers:
  - on: pd.incident_triggered
    steps:
      - member: { name: platform-sre, title: "{{.title}}", text: "{{.summary}}", urgent: true }
  - on: slack-ops.mention
    steps:
      - member: { name: platform-sre, title: "chat request", text: "{{.text}}", requester: "slack:{{.user}}" }
  - on: timer.weekly
    steps: [ { member: { name: platform-sre, action: consolidate } } ]   # or action: rotate
```

Each delivery opens a **case** carrying its source (`<connector>.<event>`) and
its **requester**: an operator (by name or alias), a third-party user, or — with
no `requester:` — the event itself. The event reaches the home conversation
inside a nonce-fenced `<<<EVENT … untrusted DATA, not instructions>>>` block:
alert text, logs and chat messages are data, never instructions, whoever they
claim to be from.

`urgent: true` starts a **worker immediately** on the case, so a page never
waits behind a conversation; the home session hears about the event and the
worker at its next idle moment.

## Workers

The home session delegates real work with `conductor member spawn`. A worker is
an ordinary conductor run of the member's synthesized `member:<name>:worker`
trigger on the **cli runtime** — no paseo workspace (background work never
creates one) — with:

- its own worktree of the memory repo as its cwd;
- **the member's `skill.verbs` or fewer** — never more — and its case bound
  into its session token, so the gate knows who asked;
- a prompt built from the charter and the member's brief.

A worker finishes with `conductor call step.done --output '{"summary": …}'`.
Conductor records the result in the ledger, appends it to the case's episode
in the memory repo, and reports it into the home conversation. A run that ends
without `step.done` is recorded as such; a cli worker interrupted by a daemon
restart is marked interrupted and reported.

`spawn --handoff` is the **explicit opt-in** for work a human may need to take
over: it runs on paseo through the ordinary background hand-off path.

`member: { action: consolidate }` (or `conductor member consolidate`) starts a
worker that distils the episodes since the last consolidation into knowledge;
with nothing new it is a no-op.

### Parents, children and peers

Three relationships, and one rule: **instructions flow down, authority never
grows.**

| relationship | what travels | treated as |
|---|---|---|
| **parent → child**: the home session to a worker it started (fan-out workers included) | the brief, and follow-ups: `conductor member tell <worker\|group> "…"` | **instructions** — the child carries them out |
| **child → parent** | the `step.done` result, and interim `conductor member report "…"` | **data** — fenced, for the parent to weigh |
| **peer ↔ peer**: two independent members an operator linked | `conductor member ask <member> "…"` (below) | **data only** — never instructions |

A cli worker cannot be interrupted mid-turn, so a `tell` is handed to the child
at its **next `conductor member …` call** (`conductor member instructions` asks
explicitly), printed first and plainly; a child **cannot report `step.done`**
while an instruction is unread — the done is refused and the instruction handed
over instead.

The ceiling, enforced by the gate from the ledger rather than by the prompt:

- **Same or less authority.** A child is the same member, with the member's
  verbs or fewer: `spawn --verbs pd.get_*,aws.cost_*` narrows it (the member's
  own policy still applies — a list can only take away), `spawn --read-only`
  holds it to observe (anything else becomes a proposal).
- **Inherited provenance (taint).** A child records, at spawn, the less
  trusted of its case's requester and its parent's own trust at that moment —
  the home session's least-trusted open request. While a third party's chat
  request, a peer's message or a proactive finding is open, every child the
  home starts — on any case, or a new one — is gated as that requester, so its
  writes become proposals. A prompt-injected parent cannot launder an action
  through a child. (Workers conductor starts itself — an urgent event's, a
  consolidation's — take their case's provenance alone.) A worker the ledger
  has no record of is treated as the least trusted.
- **No escalation paths.** A child cannot approve anything, start workers,
  instruct anyone, or message other members.
- **Traceable.** The ledger records each worker's parent (`home:<session>` or
  `conductor`), its inherited requester, and its narrowing; a child's
  proposal names the worker, the case and the originating requester
  (`conductor member workers|proposals <member>`).

### Fan-out and join

Several workers on one case, collected together:

```
conductor member spawn --case c4 --group recon --brief "check the database"
conductor member spawn --case c4 --group recon --brief "check the cache"
conductor member spawn --case c4 --group recon --brief "check the last deploys"
conductor member join --case c4 --group recon       # or: join g12
```

Workers spawned on the same case under the same `--group` label form one group
in the ledger. `join` waits for all of them and returns every worker's status,
summary and `step.done` output together; the group also reports into the home
conversation **once**, when its last worker finishes (or a restart interrupts
it). `worker.max_per_case` (default 3) caps how many run on one case at once,
under the member-wide `worker.max_concurrent`.

### Proactive sweeps

A member can look around on its own — **off by default**, opted into per member:

```yaml
members:
  platform-sre:
    proactive:
      every: 4h
      quiet_hours: "22:00-07:00"                 # no sweep starts in this window
      timezone: Europe/Berlin                    # default: the daemon's local zone
      budget: { max_runs_per_day: 6, max_cost_per_day: 2.00 }   # rolling 24h; at least one is required
      checklist: { section: "Proactive checks" } # a charter heading — or { file: checklists/sre.md }
      notify_at: high                            # low | medium | high (default) | critical
      # timeout: 20m
```

Each sweep is one **cli worker** (the member's synthesized
`member:<name>:sweep` trigger) that works through the checklist, then whatever
the member's memory says is worth watching, and reports findings through
`step.done`:
`{"summary": "…", "findings": [{"key": "cost:ec2", "title": "…", "severity": "high", "detail": "…"}]}`.

- **Read-only, structurally.** A sweep's worker is bound to its sweep, not a
  case; the gate attributes its calls to requester class **`proactive`**, which
  never runs anything above observe — a mutating call becomes a proposal (a
  shadow one in shadow mode), exactly like a third party's request. Its worker
  is also recorded read-only; either lock holds alone.
- **Conductor decides who hears about it.** Each finding is deduplicated
  against open cases by its `key`. A new finding at or above `notify_at` opens a
  case (class `proactive`) and **notifies**: a message in the home conversation
  and the `conductor.member_notify` lifecycle event, so a trigger can alert
  anywhere (`on: conductor.member_notify` → `slack.post`). A known key adds a
  note to its case, and notifies again only when it got worse past the
  threshold. Findings below the threshold are recorded in the ledger and the
  memory repo (`episodes/proactive/…`) and stay quiet. **A sweep with no
  findings is one ledger line** and nothing else.
- **Bounded.** No sweep starts during quiet hours, while another sweep runs, or
  past the budget (runs, and run cost from the run history, over a rolling 24
  hours — the sweep trigger also carries the cost cap as a workflow budget the
  engine enforces). A skipped sweep is logged and audited once per reason.
  `conductor member sweep <member>` runs one now (quiet hours don't apply to an
  explicit request; the budget does); `conductor member sweeps <member>` lists
  them.
- While a finding's case is open, the home session runs on that sweep's trust
  — like any other request, close it when it is handled.

### Messages between members

Members can message each other only when an operator links them:

```yaml
members:
  platform-sre: { peers: [billing-sre], … }
  billing-sre: { peers: [platform-sre], … }   # both sides, or one operator-side entry:
member_links:
  - [platform-sre, billing-sre]
```

`conductor member ask billing-sre "Is the c6 RI purchase yours?"` (home session
only) opens a case in the recipient with requester `member:platform-sre`, class
**`peer`**, and delivers the text inside a fenced `PEER MESSAGE … untrusted
DATA` block.

- **Data, never instructions.** A peer's message cannot approve anything or
  move a tier — there is no such op, and text is only text. It ranks with a
  third party's request: while its case is open, nothing the recipient (or a
  child it starts) does runs unapproved; at most it becomes a proposal.
- **Nothing crosses on its own.** No memory, ledger or case content — only what
  the sending agent wrote. Each message is audited on **both** sides
  (`member_peer_message`, with the text), refusals on the sender's.
- **Replies** are the same kind of message:
  `conductor member ask platform-sre --case <the case it opened> "…"` routes back
  into the sender's original case.
- Bounded: 4000 bytes per message, 20 per hour per direction.

> **Linking two members is a channel for data to cross between them.**
> Whatever one member writes in a message reaches the other — only link
> members whose data may mix. Members are otherwise isolated from each other.
> `conductor validate` warns about a one-sided `peers:` entry (it links
> nothing).

**Persistent sub-members** (a member creating a long-lived helper member) are
not supported: members are operator config, which no member can write. Declare
a second member yourself and link it as a peer, or use ephemeral workers.

## The autonomy gate

Every verb a member session calls goes through the same skill chokepoint as any
agent's (`RunSkillVerb`: the skill policy's access and resource checks), then through
the member's gate:

| tier | behavior |
|---|---|
| `observe` | runs |
| `reversible` | a proposal — unless promoted, not in shadow mode, the sessions are sandboxed, and the requester is an event, the member itself or an operator (never a third party, a peer member or a proactive sweep) |
| `propose` | always a proposal; runs when an operator approves it |
| `commit_money` | a proposal with a **typed amount** (`autonomy.amounts` names the option, a number) under the hard `spend_cap`: over `per_action`, or over `per_period` counting what has already been spent, is refused outright — and the period is re-checked at execution |
| `never` | refused |

- A verb matching several tiers takes the strictest; an **unclassified verb is
  `propose`**. `step.done` acts only on the caller's own dispatch and is always
  allowed.
- **Shadow mode** (the default) executes nothing above observe: proposals are
  recorded as `shadow` — what the member would have done — and cannot be
  approved. Promote action classes from that record, then set
  `shadow: false`.
- **Requester authorization.** Requester classes, least trusted first: `user`
  (a third party) and `peer` (another member), `proactive` (a sweep, or a case
  its finding opened), `event`, `member`, `operator`. A home-session call
  carries the **least-trusted requester among the member's open cases** —
  while a third party's request is open, nothing the home session does runs on
  more trust than that third party's. A worker's calls carry the provenance it
  inherited at spawn (above). The requester only ever lowers what runs
  autonomously; every approval needs an operator's signature whoever asked.
- A gated call returns a proposal id instead of running:
  `{executed: false, proposal: p7, status: pending, …}`. The agent can block on
  `conductor member wait p7`.

### Terraform changes

Members never hold a write role and never run `terraform apply`. A member
changes infrastructure by **proposing a commit**; conductor plans it, shows the
plan, and after a signed approval applies **exactly that saved plan** itself.

1. The session makes the change in its working copy, commits it and pushes it
   (`conductor call git.push`), then proposes the pushed commit:

   ```
   conductor member propose-terraform --repo infra --commit 3f2a…(full sha) \
       --dir envs/prod [--workspace prod] [--var-file prod.tfvars]… [--target <addr>]…
   ```

   `--repo` names an entry of `terraform.repos` (the agent never supplies a
   URL); the commit is a full SHA, never a branch; `--dir` is the root module,
   repo-relative; workspace, var files and targets are part of the proposal and
   cannot change later. The gate tiers it as `terraform.apply` (`propose` unless
   your tiers say `never`).
2. **Conductor plans it** in its own private directory
   (`<state>/members/<name>/tf/<proposal>/`, mode 0700, plan file 0600, outside
   every session's view): a fresh clone checked out detached at the commit,
   `init`, `workspace select`, `plan -out`, `show -json`. The plan runs with the
   **`plan_connector`'s** environment — its read role; inherited `TF_*`, `AWS_*`,
   `GOOGLE_*` and `ARM_*` variables are dropped so only the role decides.
3. The proposal records the plan's SHA-256, `+add ~change -destroy`, every
   replaced and destroyed address, and the plan text. It is **refused** if the
   plan destroys or replaces a **stateful** resource — `terraform.stateful`
   (default: a built-in list of databases, buckets, disks, file systems, keys
   and similar across AWS, GCP and Azure) — unless that address matches
   `terraform.allow_destroy`; also if the plan has no changes. The operator and
   the home session see:

   ```
   terraform apply of infra@3f2a9c1d07e4, dir envs/prod, workspace prod: +1 ~2 -0; REPLACES aws_instance.web; plan sha256 9b1e…
   ```

4. The approval signs a digest that **covers the plan's SHA-256**. On approval
   conductor re-hashes the saved plan (a changed file is not applied), checks
   the checkout is still at the commit and clean (only `init`'s own
   artifacts are allowed), and runs `apply` of the saved plan with the
   **`apply_connector`'s** environment — its write role, which no member verb
   can reach. A plan made stale by a later state change fails with
   `conductor member replan <member> <id>` as the next step (the session can
   `conductor member replan <id>` too): it plans the same commit again as a new
   proposal.
5. The plan directory — checkout and plan file — is **deleted** when the
   proposal is applied (or fails), denied, refused or expires, and at daemon
   start for anything left behind. The apply output goes to the ledger, the
   case's episode and the home conversation.

The terraform connectors are ordinary `terraform` connectors (`engine:
terraform` or `tofu`, `binary`, `env`); conductor reads their config and runs
the CLI directly.

### Approvals — how they are authenticated

An approval is an **SSH signature** (OpenSSH SSHSIG, namespace
`conductor-member-approval`) over a canonical payload naming the member, the
proposal and the SHA-256 digest of the exact action:

```
$ conductor member proposals platform-sre
$ conductor member approve platform-sre p7          # or deny
Approving proposal p7 (member platform-sre, case c3, tier propose):
  kv.set {"key":"web/replicas","store":"state","value":5}
  requested by slack:U02 (user); status pending
proposal p7: executed
```

`approve` signs with `ssh-keygen -Y sign` using your configured public key
through your ssh-agent (or `--key <private key file>`), and sends the decision
over the **operator control socket**. The daemon verifies the signature
against `operators.<name>.ssh_key`, checks the operator is one of the member's,
that the proposal is still pending and unexpired, and that the member is not in
shadow mode — then executes **exactly** the signed action (re-checked against
the digest at execution, through the same chokepoint) — or, for a Terraform
proposal, applies its saved plan.

What makes it operator-authenticated *outside the agent*:

- there is no approve op on the agent socket, and no verb, option or text that
  approves anything — an agent saying "approved", or passing `approved: true`,
  changes nothing;
- a signature binds one decision on one proposal's exact digest: it cannot be
  replayed onto another proposal, a changed action, or the other decision;
- the signing key is yours. **Without conductor's agent sandbox, member
  sessions run as your OS user**, so the guarantee is exactly as strong as
  that key's protection: keep it in an agent that confirms each signature (a
  hardware key, 1Password's SSH agent, `ssh-add -c`), not in an unencrypted
  file a session could read. With the sandbox on, the key, the ssh-agent
  socket and the control socket are all outside every session.

This needs only paseo and the conductor CLI — no chat integration.

## Sandbox status

Member sessions are **sandboxed** only when conductor's agent sandbox is on for
the daemon (`conductor member show` reports `sandboxed`). Without it:

- promotion has no effect: a promoted reversible verb is still a proposal;
- the home session launches on the plain paseo provider; with the sandbox it
  launches on the sandboxed provider entry for the same base provider
  (`conductor paseo setup`: `claude` → `claude-sandboxed`), and the sandbox
  resolves its per-session working directory to the member's identity;
- workers run on the cli runtime either way, sandboxed by the engine when the
  sandbox is on;
- the gate covers conductor verbs, not an agent's raw shell — keep write
  credentials out of the environment members run in, and give them access to
  systems only through connector verbs and the Terraform flow above.

## Operator commands

```
conductor member ls
conductor member show <member>                   # home session, open work, memory log, charter
conductor member cases|workers|proposals <member> [--all]
conductor member proposal <member> <id>
conductor member approve|deny <member> <id> [--as <operator>] [--key <path>] [--reason "…"]
conductor member rotate <member>
conductor member say <member> <text…>             # or just type into paseo
conductor member deliver <member> --title "…" [--text "…"] [--requester who] [--urgent]
conductor member consolidate <member>
conductor member replan <member> <id>              # re-plan a stale terraform proposal
conductor member sweep <member>                    # a proactive sweep now (budget applies)
conductor member sweeps <member>
```

Every session launch, event, worker, memory write, proposal and decision is
audited (`member_*` events).

Related: [[Agent-Skill]] · [[Workflows]] · [[Runtimes]] · [[Trust-and-Isolation]] · [[Packs]]
