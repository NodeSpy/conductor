# Bots

A **bot** is a persistent, conversational teammate — an SRE for your
platform services, say. Define as many as you want in `bots:`, cut any way
that suits you: a system, a function, an environment, a service — and group
them into [teams](#teams) the same way.
You talk to it in paseo and give it tasks; conductor routes events into it
(pages, chat requests, schedules); it keeps long-term memory, tracks its open
work, and starts **workers** to get things done. Every mutating action it takes
passes its **autonomy gate**, and anything above its autonomy waits for an
operator's signed approval.

A bot is either **solo** or **on a team**, where teammates coordinate as
[peers](#teammates-are-peers): they ask each other questions and hand each
other work.

(Not to be confused with GitHub bot *accounts* — `author_bot`,
`reply_to_bots`, `as: bot` — see [[Configuration]].)

A bot is durable state, not a long-running LLM session. Sessions are
replaceable at any time; continuity lives on disk:

| layer | contents | where |
|---|---|---|
| charter | identity, scope, escalation policy, autonomy tiers | `bots:` config — the bot can never write it |
| knowledge | infra map, runbooks, what to watch, learned facts — [typed documents](#content-kinds) | the bot's **memory repo** (git) |
| log | past cases, actions, outcomes — summarized into digests, then [archived](#consolidation-and-the-archive) | the memory repo's `log/` and `digests/` |
| recall index | what a session searches and its prompt's [memory index](#reading-memory) is drawn from — derived from the repo | conductor's state dir |
| case ledger | open events, proposals awaiting approval, running workers, the home session, the inbox, spend | `bots.json` in conductor's state dir |

Each bot is isolated: its own memory repo, ledger, home session, verb policy,
operators and pack instance. A session token is bound to exactly one bot,
so nothing one bot does can reach another's memory, cases or workers — and
an agent-authored plan cannot route work into any bot at all. What a
[team](#teams) shares is explicit: messages between teammates (data only), and
a knowledge repo nothing writes but an approved promotion.

## Configuration

```yaml
runtimes:
  paseo:      { use: paseo, default: true }
  claude-cli: { use: cli, tool: claude-code }   # workers run here

connectors:
  tf-plan:  { use: terraform, env: { AWS_PROFILE: infra-read } }    # conductor plans with this
  tf-apply: { use: terraform, env: { AWS_PROFILE: infra-deploy } }  # and applies with this — no bot verb may reach it

operators:
  ops:
    ssh_key: "ssh-ed25519 AAAA… you@laptop"     # approvals are signed with this key's private half
    aliases: ["slack:U012ABC"]                   # chat ids that ARE this operator

bots:
  platform-sre:
    description: "SRE for the platform services"
    charter_file: bots/platform-sre.md            # or charter: | …
    escalation: "Page the operator if a sev1 is not mitigated in 15 minutes."
    # memory_repo: ~/bots/platform-sre            # default <state dir>/bots/<name>/memory
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
      shadow: true                               # the default for a new bot
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
paseo, a worker runtime that is, tiers, the spend cap) and warns about a bot
with no operators, no verbs, a promotion that cannot take effect yet, or
a home mode that skips permission prompts. A `skill.verbs` entry that could
reach the `terraform.apply_connector` is an error.

### Community bots

A pack can ship a **bot template** — a community bot you pull in: its
charter, the documents its memory starts with, the connectors it needs, the
verbs it requests (each with a maximum tier), and suggested triggers and
proactive checks. You instantiate it into as many bots as you want:

```yaml
packs:
  sre: { use: conductor-packs/sre-bot, version: "~> 2.0" }
bots:
  platform-sre:
    use: sre/sre                          # <pack instance>/<template>
    grant: { pagerduty.get_incident: observe, pagerduty.acknowledge: reversible }
```

```
conductor bot templates --search sre          # installed and registry templates
conductor bot new platform-sre --from sre/sre # planned on a copy of your config, shown as a diff; --write
conductor bot grant platform-sre pagerduty.acknowledge=reversible
```

A template only requests: the bot starts in practice mode with only its
read-only verbs, every widening is your grant (named by `conductor validate`),
and the template can never set operators, team, folder, connector bindings,
secrets, autonomy, sandbox or egress. A newer pack version reaches a bot's
memory only as a proposal you approve. See [[Bot-Templates]].

## The home session

Each bot has one pinned **home** session on paseo — the one you talk to.
Its working directory is its **own worktree** of the bot's memory repo
(`~/.conductor/runs/bot-<name>-<session>`). It starts from its
[memory index](#reading-memory) and reads memory through conductor — recall
first, then a section at a time — and writes it only through conductor:

```
conductor bot kinds                                                     # the document kinds, their schemas and caps
conductor bot remember --kind knowledge --title "DB primary" --text "<document>" [--append]
conductor bot episode  --case c12 --text "…"
conductor bot recall   <words> [--limit N] [--kind <kind>]               # ranked, with a snippet each
conductor bot read     <path> [--section "<heading>"] [--offset N]      # at most read_max bytes
conductor bot index    [--kind <kind> | --all]                          # the memory index
conductor bot cases | case open|note|close
conductor bot spawn    --case c12 --brief "…" [--group g] [--handoff]
conductor bot handoff  --text "…"
conductor bot proposals | wait <proposal>
conductor bot propose-terraform --repo <name> --commit <sha> [--dir …]  # below
conductor bot spawn --case c12 --group g --brief "…" [--verbs …] [--read-only]
conductor bot tell <worker|group> "…"                                   # follow-up instructions to your workers
conductor bot join --case c12 --group g                                 # fan-out results, together
conductor bot ask <bot> [--case c] "…"                                  # a linked peer (data, never instructions)
conductor bot delegate <bot> [--case c] "<task>"                        # hand a peer work (below)
conductor bot decline <case> "<reason>"                                 # refuse work delegated to you
conductor bot team [recall <words> | read <path>]                       # your team (below)
conductor bot team promote --path knowledge/<f>.md [--to …] [--note "…"]
conductor bot assign <case> <bot> [--note "…"]                          # a team lead's triage
```

A worker has `conductor bot report "…"` (to its parent) and `conductor bot
instructions` (from its parent).

Every memory write is **one git commit**, authored by the bot, with
`Bot-Case` / `Bot-Role` / `Bot-Session` trailers. Review with `git log`;
correct by committing over it or reverting.

**Rotation.** When a session reaches `rotate.max_age` or `rotate.max_turns`, its
agent disappears (or its process ended — a paseo daemon restart leaves it
`closed`, and resuming it would drop its conductor endpoint), or you run `conductor bot rotate <bot>`, conductor asks
the session to write a handoff note, waits for that turn to finish, writes one
itself from the ledger if it did not, starts a fresh session **in a new
worktree** from the charter + latest handoff + the ledger's open work, and only
then archives the old agent and removes its worktree. A failed launch leaves the
old session in place. No two live sessions ever share a working directory. A
daemon restart keeps the live session.

**Tokens.** A bot session's conductor token is durable like a hand-off's:
it survives daemon restarts and lives 7 days from its last use. So
`rotate.max_age` must stay at least a day inside that (validation rejects
more than 6 days), and a retired home session's token — and a finished
worker's, however it finished — is revoked the moment it is retired, not left
to age out.

**Delivery.** Events and worker reports land in the bot's durable inbox and
are delivered as one turn when the home agent is idle — never into a turn you
are in the middle of.

## Routing events in

The `bot:` helper step routes a trigger's event into a bot:

```yaml
triggers:
  - on: pd.incident_triggered
    steps:
      - bot: { name: platform-sre, title: "{{.title}}", text: "{{.summary}}", urgent: true }
  - on: slack-ops.mention
    steps:
      - bot: { name: platform-sre, title: "chat request", text: "{{.text}}", requester: "slack:{{.user}}" }
  - on: timer.weekly
    steps: [ { bot: { name: platform-sre, action: consolidate } } ]   # or action: rotate
```

`bot: { team: platform, … }` routes to a [team](#routing-work-to-a-team)
instead: its rules, its lead or its fallback pick the bot.

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

The home session delegates real work with `conductor bot spawn`. A worker is
an ordinary conductor run of the bot's synthesized `bot:<name>:worker`
trigger on the **cli runtime** — no paseo workspace (background work never
creates one) — with:

- its own worktree of the memory repo as its cwd;
- **the bot's `skill.verbs` or fewer** — never more — and its case bound
  into its session token, so the gate knows who asked;
- a prompt built from the charter and the bot's brief.

A worker finishes with `conductor call step.done --output '{"summary": …}'`.
Conductor records the result in the ledger, appends it to the case's episode
in the memory repo, and reports it into the home conversation. A run that ends
without `step.done` is recorded as such; a cli worker interrupted by a daemon
restart is marked interrupted and reported.

`spawn --handoff` is the **explicit opt-in** for work a human may need to take
over: it runs on paseo through the ordinary background hand-off path.

`bot: { action: consolidate }` (or `conductor bot consolidate`) starts a
worker that summarizes the oldest unconsolidated log entries into a digest
(and knowledge, where something durable emerged); with nothing new it is a
no-op. Once it finishes, the entries it summarized leave the working copy:
see [Consolidation and the archive](#consolidation-and-the-archive).

### Parents, children and peers

Three relationships, and one rule: **instructions flow down, authority never
grows.**

| relationship | what travels | treated as |
|---|---|---|
| **parent → child**: the home session to a worker it started (fan-out workers included) | the brief, and follow-ups: `conductor bot tell <worker\|group> "…"` | **instructions** — the child carries them out |
| **child → parent** | the `step.done` result, and interim `conductor bot report "…"` | **data** — fenced, for the parent to weigh |
| **peer ↔ peer**: two independent bots an operator linked | `conductor bot ask <bot> "…"` (below) | **data only** — never instructions |
| **peer → peer, work**: the same peers | `conductor bot delegate <bot> "<task>"` ([below](#delegating-work-to-a-peer)) | a **request** the recipient works under its own authority, at the original requester's trust; the outcome comes back as **data** |

A cli worker cannot be interrupted mid-turn, so a `tell` is handed to the child
at its **next `conductor bot …` call** (`conductor bot instructions` asks
explicitly), printed first and plainly; a child **cannot report `step.done`**
while an instruction is unread — the done is refused and the instruction handed
over instead.

The ceiling, enforced by the gate from the ledger rather than by the prompt:

- **Same or less authority.** A child is the same bot, with the bot's
  verbs or fewer: `spawn --verbs pd.get_*,aws.cost_*` narrows it (the bot's
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
  instruct anyone, or message other bots.
- **Traceable.** The ledger records each worker's parent (`home:<session>` or
  `conductor`), its inherited requester, and its narrowing; a child's
  proposal names the worker, the case and the originating requester
  (`conductor bot workers|proposals <bot>`).

### Fan-out and join

Several workers on one case, collected together:

```
conductor bot spawn --case c4 --group recon --brief "check the database"
conductor bot spawn --case c4 --group recon --brief "check the cache"
conductor bot spawn --case c4 --group recon --brief "check the last deploys"
conductor bot join --case c4 --group recon  # or: join g12
```

Workers spawned on the same case under the same `--group` label form one group
in the ledger. `join` waits for all of them and returns every worker's status,
summary and `step.done` output together; the group also reports into the home
conversation **once**, when its last worker finishes (or a restart interrupts
it). `worker.max_per_case` (default 3) caps how many run on one case at once,
under the bot-wide `worker.max_concurrent`.

### Proactive sweeps

A bot can look around on its own — **off by default**, opted into per bot:

```yaml
bots:
  platform-sre:
    proactive:
      every: 4h
      quiet_hours: "22:00-07:00"                 # no sweep starts in this window
      timezone: Europe/Berlin                    # default: the daemon's local zone
      budget: { max_runs_per_day: 6, max_turns_per_day: 120, max_tokens_per_day: 2m }   # rolling 24h; any, or none
      checklist: { section: "Proactive checks" } # a charter heading — or { file: checklists/sre.md }
      notify_at: high                            # low | medium | high (default) | critical
      # timeout: 20m
```

Each sweep is one **cli worker** (the bot's synthesized
`bot:<name>:sweep` trigger) that works through the checklist, then whatever
the bot's memory says is worth watching, and reports findings through
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
  and the `conductor.bot_notify` lifecycle event, so a trigger can alert
  anywhere (`on: conductor.bot_notify` → `slack.post`). A known key adds a
  note to its case, and notifies again only when it got worse past the
  threshold. Findings below the threshold are recorded in the ledger and the
  memory repo (`log/proactive/…`) and stay quiet. **A sweep with no
  findings is one ledger line** and nothing else.
- **Bounded.** No sweep starts during quiet hours, while another sweep runs, or
  past the budget. The budget is in **units** — `max_runs_per_day`,
  `max_turns_per_day` (model turns), `max_tokens_per_day` (`500k` / `2m`
  accepted) — and, only if you are billed per token, `max_cost_per_day`
  (dollars of model spend). Set any combination, or none (`every:` already
  bounds the schedule); over a rolling 24 hours, the **first cap reached**
  stops the next sweep. Turns and tokens come from the run history (what the
  runtime reported: claude-code's `num_turns` and token usage; one turn per run
  when a runtime reports none). Tokens and dollars also bound a sweep **while it
  runs**: the sweep trigger carries them as a workflow budget the engine
  enforces. Caps enforce whatever `usage.mode` says (display only, see
  [[Configuration]]). A skipped sweep is logged and audited once per reason.
  `conductor bot sweep <bot>` runs one now (quiet hours don't apply to an
  explicit request; the budget does); `conductor bot sweeps <bot>` lists
  them.
- While a finding's case is open, the home session runs on that sweep's trust
  — like any other request, close it when it is handled.

### Messages between bots

Bots can message each other only when they are [teammates](#teammates-are-peers)
or an operator links them:

```yaml
bots:
  platform-sre: { peers: [billing-sre], … }
  billing-sre: { peers: [platform-sre], … }   # both sides, or one operator-side entry:
bot_links:
  - [platform-sre, billing-sre]
```

`conductor bot ask billing-sre "Is the c6 RI purchase yours?"` (home session
only) opens a case in the recipient with requester `bot:platform-sre`, class
**`peer`**, and delivers the text inside a fenced `PEER MESSAGE … untrusted
DATA` block.

- **Data, never instructions.** A peer's message cannot approve anything or
  move a tier — there is no such op, and text is only text. It ranks with a
  third party's request: while its case is open, nothing the recipient (or a
  child it starts) does runs unapproved; at most it becomes a proposal.
- **Nothing crosses on its own.** No memory, ledger or case content — only what
  the sending agent wrote. Each message is audited on **both** sides
  (`bot_peer_message`, with the text), refusals on the sender's.
- **Replies** are the same kind of message:
  `conductor bot ask platform-sre --case <the case it opened> "…"` routes back
  into the sender's original case.
- Bounded: 4000 bytes per message, 20 per hour per direction.

> **Linking two bots is a channel for data to cross between them.**
> Whatever one bot writes in a message reaches the other — only link
> bots whose data may mix (the same goes for putting them in one team).
> Bots are otherwise isolated from each other.
> `conductor validate` warns about a one-sided `peers:` entry (it links
> nothing).

### Delegating work to a peer

`ask` carries questions and information. To hand a peer **work**, the home
session delegates it:

```
conductor bot delegate platform-data --case c6 "check replica lag on db-main and say whether it explains the 502s"
```

Conductor opens a case in the recipient, linked to the delegator's case
(`--case`; without it, a new case is opened for it), and delivers the task
inside a fenced `DELEGATED TASK` block. The recipient works it like any other
case, under **its own** charter, verbs and autonomy, and finishes it with:

```
conductor bot case close <case> --outcome "lag is 3s from a long vacuum; not the 502s"   # the outcome goes back
conductor bot decline <case> "web tier is not in my scope"                               # or a refusal, with a reason
```

Either one reaches the delegator's case as a fenced `DELEGATION RESULT` /
`DELEGATION DECLINED` block: data for the delegator to weigh, like a child's
report, delivered once.

- **Who may delegate to whom** is exactly who may `ask` whom: teammates,
  unless their team sets `peers: none`; anyone else only with an operator's
  link. A refusal is audited. Only a home session delegates (a worker cannot).
- **Provenance is inherited, never raised** — the same rule as the lead's
  `assign` and a child worker. The recipient's case carries the less trusted
  of the delegator's case's requester and the delegator's own trust (its
  least-trusted open request), and the recipient re-checks an operator
  against its own operators. A third party's request delegated (and delegated
  onward) is still that third party's request at every gate. Work a bot
  delegates on its **own** initiative arrives as a `peer`'s request
  (`bot:<delegator>`): a bot's own trust never crosses to another bot.
- **No loops.** A delegation cannot reach a bot already in its chain, and a
  chain is at most `delegation.max_depth` hops (default 3; the lower of the
  two sides' teams applies). A home session's conversation mixes its open
  cases, so a delegation carries the ancestry of **every** delegated case it
  has open: opening a fresh case does not reset the chain. Once the delegated
  case is closed, the chain is gone.
- **Bounded and recorded.** 4000 bytes per task, 10 per hour per direction.
  Each side audits it (`bot_delegation`: `sent` / `received`, then `done` or
  `declined` and `…_received`, with the text). The delegator's ledger keeps
  the edge — who handed what to whom, on which cases, at what provenance, how
  it ended — shown by `conductor bot delegations <bot>` and in
  `conductor bot team <team>`.

```yaml
teams:
  platform:
    delegation: { max_depth: 2 }   # optional; 1–10, default 3
```

**Persistent sub-bots** (a bot creating a long-lived helper bot) are
not supported: bots are operator config, which no bot can write. Declare
a second bot yourself and link it as a peer, or use ephemeral workers.

## Content kinds

Every file in a memory or knowledge repo is a **typed document**: a kind
with a fixed home, a path derived from a slug, and a YAML frontmatter schema.
Conductor checks all of it on **every** write — a bot's `remember`, conductor's
own log and handoff entries, an operator's edits, a promotion landing in a
team's repo — at one chokepoint. Nothing is written anywhere else.

| kind | home | frontmatter | written by |
|---|---|---|---|
| **knowledge** | `knowledge/` | `title`, `applies_to` (list), `evidence`, `confidence` (low \| medium \| high) | the bot (its memory) or an operator; a team's: an operator or a promotion |
| **runbook** | `runbooks/` | `title`, `when_to_use`, `prerequisites` (list), `steps` (an ordered list), `verification`, `rollback`, `owner` | as knowledge |
| **watch** | `watch/` | `what`, `signal`, `threshold`, `why` | as knowledge |
| **digest** | `digests/` | `title`, `summary`, `period`, `cases` (list) | the bot (a consolidation writes one) or an operator |
| **log** | `log/` (`episodes/`: older entries, read-only) | `title`, `case` or `sweep` | conductor, per case (`conductor bot episode`); an operator may **annotate** or **flag wrong**, never rewrite |
| **handoff** | `handoff/` | `session` | conductor only (`conductor bot handoff`) |
| **charter** | `charter/` | `template` | conductor only — a [template bot](Bot-Templates)'s charter: seeded with its memory, changed only by an approved template update |

Knowledge, runbooks and watch also take an optional one-line `summary`:
what the [memory index](#reading-memory) shows for the document (its body's
first sentence when there is none). Every field is required except
`applies_to`, `prerequisites`, `summary`, the digest's `period`/`cases`, and
the log's `case`/`sweep`. A team's knowledge repo has knowledge, runbooks and
watch. `conductor bot kinds` (a session) or
`conductor bot knowledge|memory kinds <team|bot>` (an operator) prints each
kind's home, path rule, writers, fields and a skeleton.

- **Paths are derived**: `<home>/<slug>.md`, the slug from `--slug` or the title
  (a watch: its `what`). A document of one kind cannot be written in another's
  home (`--kind runbook` on a `knowledge/` path is refused), and sub-paths are
  allowed only in a collection with `free_paths: true`.
- **Schema errors are precise**: every missing field, wrong type, bad enum
  value and unknown field, in one message with the kind's field list.
- **Watch is a directory**, one document per thing to watch. Each item then has
  its own frontmatter, history and revert, and nothing has to parse several
  items out of one file.
- **One topic per document.** Each kind has a byte cap
  ([Size limits](#size-limits)): a larger document is refused, with its size
  and the cap — split it.

**Custom collections** add kinds of your own, on a bot (its memory) or a team
(its knowledge repo), or shipped by a pack and used with `from:`:

```yaml
teams:
  platform:
    collections:
      postmortems:
        dir: postmortems
        fields:
          title:    { type: string, required: true }
          severity: { type: enum, values: [sev1, sev2, sev3], required: true }
          action_items: { type: list }
        template: "## Timeline\n\n## Cause\n\n## Follow-ups\n"
        free_paths: false
        writers: [operator, proposal]      # a team's: operator, proposal; a bot's: operator, bots
        max_bytes: 16k                     # one document (default 8k)
bots:
  platform-sre:
    collections:
      postmortems: { from: .conductor/packs/platform-sre/collections/postmortems.yaml }
```

Field types: `string` (one line), `text`, `list`, `steps` (a non-empty list),
`enum` (with `values:`), `number`, `bool`. A collection cannot take a built-in
kind's home, or overlap another collection.

**Existing memory is never deleted.** A seed (`seed_dir`) copies only valid
documents of a kind and reports the rest. At start, conductor reports every
file that is not a valid document — outside every kind's home (unmapped), or
failing its kind's schema — in its log and the `bot_memory_inventory` audit
event. Those files stay readable; the next write to one must be a valid
document. `ls` shows each file's kind and status (`ok`, `invalid`, `legacy`,
`unmapped`). Older `episodes/` entries are read as the log's legacy home:
recall and consolidation still see them. A document already over its kind's
cap stays readable; the next write to it must fit.

## Reading memory

A memory repo grows to hundreds of documents and thousands of log entries.
A session never reads it wholesale: it starts from its **memory index**,
finds documents with **recall**, and **reads sections**, not whole
documents. Every prompt says so:

> read your memory index; recall before you open a file; read sections, not
> whole documents.

### The memory index

Every session's first prompt — home, worker, sweep — carries the bot's
memory index: one line per document, generated by conductor from the typed
frontmatter.

```
## Your memory index (612 documents)

- runbooks/db-failover.md · runbook · DB failover · for: the primary is down · Promote the replica and repoint the app.
- knowledge/db-primary.md · knowledge · DB primary · for: db-main, prod · The primary is db-1 in us-east-1a.
- digests/episodes-2026-10-01-to-2026-10-14.md · digest · Episodes 2026-10-01 to 2026-10-14 · for: c41, c42 · Two failovers, both from disk pressure.
- … 590 more not shown (402 knowledge, 96 runbook, 92 digest): find them with `conductor bot recall <words>`, list them with `conductor bot index --kind <kind>`

log/: 37 raw case logs in your working copy, 1904 archived after consolidation — digests/ summarize them; `conductor bot recall` searches all of them.
```

- **A hard budget.** The whole section stays within `memory.index_budget`
  (default 6000 bytes, about 1,500 tokens), whatever the repo holds.
- **The most relevant first.** A worker's index is ranked against its brief
  and case, a sweep's against its checklist, a home session's against its
  open cases; the rest follow, most recently changed first. Past the budget
  it stops and counts what it left out, by kind.
- **Digests, not raw logs.** The index lists knowledge, runbooks, watch,
  digests and collections. Raw case logs are only counted: recall finds
  them, digests summarize them.
- A bot on a team with a knowledge repo gets the **team's knowledge index**
  too, within the team's `knowledge.index_budget` (default 2000 bytes).
- `conductor bot index` prints it; `--kind <kind>` (or `--all`) lists every
  document, a page at a time (`--offset`).

### Recall

```
conductor bot recall db failover
1. runbooks/db-failover.md (runbook) — DB failover
   …the primary is down: promote the «replica» and repoint…
2. knowledge/db-primary.md (knowledge) — DB primary
   …
```

`conductor bot recall <words>` returns the best-matching documents — the top
`memory.recall_limit` (default 8; `--limit` up to 25), each with its path,
kind, title and a short snippet. It ranks with BM25 over a full-text index,
weighting where a word appears: the title most, then the scope fields
(`applies_to`, `when_to_use`, a log's case), the `summary`, the other
frontmatter fields, and the body least. Documents matching every word come
before those matching some. `--kind` narrows it to one kind. Words are only
words: quotes, operators and column names in a query are searched as text.

It searches everything: knowledge, runbooks, watch, digests, collections,
the log — including entries archived out of the working copy (marked
`[archived <commit>]`). Handoff notes are not searched; a session's first
prompt carries the latest.

**The index.** One SQLite file per repo, in conductor's state dir
(`<state>/bots/<bot>/recall-<id>.db`, `<state>/teams/<team>/recall-<id>.db`,
the id derived from the repo's location) — never in the repo, where a session
could read it. It is derived from the repo: it
records the commit it reflects, catches up from git after every commit
conductor makes and before every query (an operator's own commits included),
and is rebuilt from scratch when it is missing, unreadable, behind a
rewritten history, or the kinds changed. It indexes committed content only.
SQLite is the pure-Go driver conductor already uses; the build stays
CGO-free.

### Reading in parts

```
conductor bot read runbooks/db-failover.md --section "Rollback"
conductor bot read knowledge/network.md --offset 12000
```

A read returns the document — or one section: a Markdown heading through the
next heading at its level or above — at most `memory.read_max` bytes
(default 12000). Past that it stops at a line, and says so first:

```
== knowledge/network.md (31240 bytes) ==
TRUNCATED: showing bytes 0–11987 of 31240 (read_max 12000). Read on with --offset 11987, or read one section with --section "<heading>".
…
== sections: Overview | Subnets | Peering | Firewall rules | History ==
```

An unknown section is an error that lists the document's sections. An
archived log entry reads back from git history, marked as archived.
`--json` prints the raw result.

### Size limits

On by default; a bigger value loosens one.

| what | default | knob |
|---|---|---|
| a knowledge document | 8 KiB | `memory.max_bytes.knowledge` |
| a runbook | 12 KiB | `memory.max_bytes.runbook` |
| a watch document | 4 KiB | `memory.max_bytes.watch` |
| a digest | 8 KiB | `memory.max_bytes.digest` |
| a collection's document | 8 KiB | `collections.<name>.max_bytes` |
| a handoff note (the session's text) | 4 KiB | `memory.max_bytes.handoff` |
| one `conductor bot episode` entry | 4 KiB | `memory.max_bytes.episode` |
| one `conductor bot read` | 12000 bytes | `memory.read_max` |
| the memory index in a prompt | 6000 bytes | `memory.index_budget` |
| a recall | 8 documents (at most 25) | `memory.recall_limit` |

A team's knowledge repo takes the same knobs beside the rest of
`knowledge:` (`index_budget` defaults to 2000 there; `max_bytes` for
knowledge, runbook and watch):

```yaml
bots:
  platform-sre:
    memory:
      index_budget: 8k
      read_max: 16k
      max_bytes: { knowledge: 16k, handoff: 6k }
      # archive: false                 # keep consolidated logs in the working copy
teams:
  platform:
    knowledge: { promotion: proposal, index_budget: 3k, max_bytes: { runbook: 20k } }
```

Caps are checked where every document is checked — on what the file will
hold, an `--append` included — for a bot's own writes, an operator's edits
and a promotion (refused when it is proposed). Conductor's own log entries
and handoffs are bounded where it composes them. A refusal gives the exact
numbers and the knob, in the same style as a schema error:

```
knowledge/db.md: knowledge document is 9312 bytes, over its cap of 8192 bytes (memory.max_bytes.knowledge) — keep one topic per document: split it into focused documents, or trim it
handoff is 5230 bytes, over its cap of 4096 bytes (memory.max_bytes.handoff) — rewrite it shorter and run `conductor bot handoff` again: …
```

A session's prompt carries the latest handoff note's body, never more than
its cap (one an operator edited by hand is cut, with the path to read the
rest). Values are validated at load: nothing negative, `max_bytes` and
`read_max` at most 256 KiB, `index_budget` at most 64 KiB, `recall_limit` at
most 25.

### Consolidation and the archive

Raw case logs grow without limit; what a session should see of its past is
their summaries.

1. **A consolidation** (`conductor bot consolidate <bot>`, or a trigger's
   `bot: { action: consolidate }`) starts a worker given the oldest log
   entries not yet consolidated — at most 60; it says how many wait. It reads
   them a section at a time, writes **one digest** summarizing them, and
   updates knowledge where something durable emerged.
2. **Only a finished one counts.** When that worker finishes done *and* has
   committed a digest, its entries count as consolidated — each at the exact
   content it was given. A consolidation that fails, ends without reporting,
   or writes no digest consolidates nothing.
3. **Conductor archives** the consolidated entries whose case is closed
   (and past days' sweep logs): one commit, `memory: archive N consolidated
   log entries`, removes them from the tree, so they leave every session's
   working copy. Git history keeps them; recall still finds them; `conductor
   bot read <path>` reads them back. Revert the commit to bring them back.

An entry never leaves the working copy unless a finished consolidation
covered exactly what it holds now. Anything appended since (a late note, a
case's closing line), anything not yet consolidated, and an open case's log
all stay, and the next consolidation takes what changed. `memory.archive:
false` keeps consolidated entries in place (they are still consolidated
once). The audit records each consolidation's outcome
(`bot_consolidation`) and each archive (`bot_memory_archive`).

## Folders

Folders organize bots and teams for display, nothing more:

```yaml
teams:
  platform: { folder: work/platform, … }
bots:
  home-helper: { folder: personal/home, … }   # a solo bot files itself
```

`conductor bot ls` prints a tree grouped by folder, then team, then bot;
`--folder work` shows that folder and everything under it, `--flat` one line
per bot (with its folder and team).

- **Folders have no behavior.** A folder confers no permissions, defaults,
  isolation, peering or routing; moving a bot or a team to another folder
  changes nothing it does. **Teams own all behavior.** Two bots in one folder
  are no more linked than two bots in different folders.
- **A bot on a team is listed under its team's folder.** A bot that sets its
  own `folder:` while on a team gets a `conductor validate` warning, and the
  team's folder wins.
- A folder is a `/`-separated path: segments are trimmed, may hold letters,
  digits, `.`, `-` and `_`, and may not be empty, `.` or `..` (at most 8
  levels, 64 characters a segment). No folder means the root.

Folders are **one organization across entity types**: workflows and triggers
take `folder:` too ([[Workflows]]), and a bot's synthesized workers are listed
under the bot's (or its team's) folder. `conductor folders ls [path]` lists
everything in a folder — teams, bots, workflows, triggers — and below it.

## Teams

A **team** is a group of bots you define in `teams:` (not the `team:`
*step*, a planner/worker fan-out — that is [[Teams]]) and cut however you
like: by system (`platform`), function (`billing`), environment, on-call
rotation. Conductor has no notion of an org; a team is one more thing in your
config. A bot names **at most one** team; a bot without one behaves
exactly as before, and `teams:` is optional.

```yaml
teams:
  platform:
    description: "Platform services: the shared runtime, its data stores and deploys"
    operators: [ops, oncall]          # who approves for the team; default: every operator
    # peers: none                     # default team: teammates may `ask` each other
    autonomy:
      ceiling: reversible             # observe | reversible: the highest tier any bot may be PROMOTED to
      shadow: true                    # every bot in shadow mode until the team says otherwise
      tiers: { never: [aws.iam_*] }   # classified into every bot; the strictest tier wins
    budget:
      spend_cap: { per_action: 500, per_period: 2000, period: 720h }
      proactive: { max_runs_per_day: 12, max_tokens_per_day: 5m }   # a POOL for the whole team, in any units
    isolation: { sandbox: required, handoff_workers: false, skip_permission_prompts: false }
    credentials: { secrets_via: broker, allow_secrets: [house/pd-token] }
    browser: { logins: [status.example.com], credentials: [statuspage] }
    knowledge: { promotion: proposal }          # a shared repo; repo:, seed_dir: optional
    routing:
      rules:
        - { name: data, match: { source: "pd.*", service: "db-*" }, to: platform-data }
      lead: platform-lead
      fallback: platform-sre
bots:
  platform-lead: { team: platform, … }
  platform-sre:  { team: platform, operators: [oncall], … }   # narrower than the team: fine
  platform-data: { team: platform, … }
  billing-sre:   { team: billing, … }
```

### Defaults are ceilings: narrow, never widen

Each team setting is a **ceiling**, the same rule packs follow. A bot
**inherits** every one it leaves unset and may set it **narrower**, never wider.
A bot setting past its team's is a **load error** naming both values:

```
config: bots.platform-sre: autonomy.promote [pd.ack] promotes to reversible, above team platform's autonomy.ceiling observe
config: bots.platform-sre: autonomy.spend_cap.per_action 2000 exceeds team platform's budget.spend_cap.per_action 500
```

| team setting | a bot inherits it as | narrower | refused |
|---|---|---|---|
| `operators` | `operators` | a subset | an operator outside the team's |
| `autonomy.ceiling` | — | — | `observe`: any `autonomy.promote` |
| `autonomy.shadow: true` | `autonomy.shadow` | — | `shadow: false` |
| `autonomy.tiers` | merged into its tiers (the strictest wins) | a stricter tier | a bot pattern the team classes stricter (`observe: [kv.set]` under the team's `propose: [kv.*]`) |
| `budget.spend_cap` | `autonomy.spend_cap` | a lower `per_action`/`per_period`, a longer `period` | higher, shorter, another currency |
| `budget.proactive` | `proactive.budget` (and it is a pool, below) | lower, **per unit**: each of runs / turns / tokens / dollars the pool caps | higher in any unit the pool caps |
| `isolation.sandbox: required` | `isolation.sandbox` | — | `optional` |
| `isolation.handoff_workers: false` | likewise | — | `true` |
| `isolation.skip_permission_prompts: false` | likewise | — | `true`, or a `home.mode` that skips prompts |
| `credentials` | `skill.secrets_via` / `skill.allow_secrets` | `none` under `broker`; a subset | `env` under `broker`; a secret outside the list |
| `browser` | `browser` | a subset of logins (a host under a `*.domain`) and credentials | a site or credential outside |

What the isolation settings do:
- **`sandbox: required`**: the bot's sessions, home, workers and sweeps,
  start only while conductor's agent sandbox is enforcing agent launches.
  Until the sandbox is on the daemon, such a bot does not start at all.
  `conductor validate` warns about this, and the daemon logs it once.
- **`handoff_workers: false`**: refuses `spawn --handoff`, so a bot uses cli
  workers only.

The `browser:` scope narrows which saved logins and sign-in credentials of
conductor's agent browser a bot's sessions may use. It never grants
anything. Until the agent browser is on the daemon, it is checked at load and
reported by `conductor bot show`, but nothing consumes it.

### Teammates are peers

Inside a team, bots are **peers by default**. Any bot may
`conductor bot ask` any teammate, with the ordinary peer rules (see
[Messages between bots](#messages-between-bots)):
- the message is data only, fenced, and arrives as a `peer` case;
- it is audited on both sides;
- it is capped at 4000 bytes and 20 per hour per direction.

Teammates can also hand each other **work** with `conductor bot delegate`
([Delegating work to a peer](#delegating-work-to-a-peer)), on the same rule.

`peers: none` turns both off for a team. Then teammates exchange messages and
delegate only where an operator linked them.

**Across teams, nothing changes.** Bots of different teams (or a bot
without a team) are isolated unless an operator links them explicitly
(`bot_links:`, or both sides' `peers:`). A team is a channel for data to
cross between its bots: put bots in one team only where their data may
mix.

### Shared knowledge

`knowledge:` gives the team a **knowledge repo**. It is git, like bot
memory, at `<state>/teams/<name>/knowledge` by default, with `knowledge/` and
`runbooks/`. Every bot on the team reads it through conductor:

```
conductor bot team recall <words>
conductor bot team read knowledge/databases.md
```

Its sessions see the team's [knowledge index](#the-memory-index) in their
prompts, and recall and read it like their own memory (`conductor bot team
read <path> --section "<heading>"`, `conductor bot team index`), under the
team's [limits](#size-limits).

No bot writes it directly (an operator can: [below](#editing-knowledge-and-memory-yourself)).
A bot **proposes** promoting one of its own
`knowledge/` or `runbooks/` files:

```
conductor bot team promote --path knowledge/db.md --to knowledge/databases.md --note "the team keeps asking"
```

- With **`promotion: proposal`** (the default), it becomes a `knowledge`
  proposal holding the exact text. Any **operator of the team** approves it,
  with the ordinary signed `conductor bot approve <bot> <id>`. Only then
  does it land.
- With **`promotion: direct`**, it lands at once, but only on a provenance a
  promoted verb could run on: an event's, the bot's own initiative, or an
  operator's. While a third party's, a peer's or a sweep's request is open, a
  promotion is still a proposal. The same applies to one from a read-only
  worker, and to any promotion in shadow mode (there, a shadow proposal).
- **It lands exactly what was approved.** The digest covers the team, the
  source and destination paths, and the text's SHA-256. A changed text or path
  fails rather than lands.
- **Each landing is one commit, authored by the promoting bot** (by the
  operator, when it was [approved with changes](#approving-with-changes-denying-with-a-reason)).
  Its trailers are `Bot-Team`, `Promoted-From: <path>@<memory commit>`,
  `Bot-Proposal` and `Approved-By`.

**Bot memory stays private to the bot.** Only what a bot promotes
reaches the team, and bots of other teams cannot read it.

### Editing knowledge and memory yourself

An operator writes a team's knowledge repo, or a bot's memory repo,
**directly**: no proposal, one commit per change, authored by you.

```
conductor bot knowledge ls platform                      # its files, and the head to write against
conductor bot knowledge cat platform knowledge/databases.md
conductor bot knowledge put platform knowledge/databases.md --from-file db.md --base 3f2a9c1 -m "the primary moved"
conductor bot knowledge new platform --kind runbook --title "DB failover" --field when_to_use="the primary is down" \
    --field 'steps=[promote the replica, repoint the app]' --field verification="writes succeed" \
    --field rollback="repoint back" --field owner=platform-data --base 3f2a9c1
conductor bot knowledge mv  platform knowledge/databases.md knowledge/data-stores.md --base 7d1e0b4
conductor bot knowledge rm  platform knowledge/old.md --base 9a4b691
conductor bot knowledge log platform [knowledge/data-stores.md]   # per repo, or per file (following moves)
conductor bot knowledge ls platform --kind runbook                # one kind; every file shows its kind and status
conductor bot knowledge revert platform 9a4b691 --base c0ffee1    # undo one commit, as a new commit

conductor bot memory new|put|rm|mv|revert|ls|cat|log|kinds platform-sre …   # the same for a bot's memory [--case <id>]
conductor bot memory index platform-sre [--kind runbook | --all]       # the memory index its sessions see
conductor bot memory recall platform-sre db failover                   # ranked recall, as its sessions run it
conductor bot memory annotate platform-sre log/2026/10/…-c12-….md --text "the cache, not the db" --base …
conductor bot memory flag     platform-sre log/2026/10/…-c12-….md --text "wrong root cause" --base …
```

- **Only an operator does this.** The ops live on the operator control socket;
  a bot session's token cannot reach them (the agent socket refuses them, and
  the CLI refuses in a bot session's shell). Bots keep their own paths:
  proposals into team knowledge, and writes to their own memory. An operator
  of the **team** edits its knowledge, an operator of the **bot** its memory;
  name yourself with `--as` when more than one could.
- **Nothing is silently overwritten.** Every write names the commit you read
  (`--base`, a commit id — `HEAD` or a branch name is not accepted). If the repo
  moved on since, the write is refused with the current head; re-read and
  retry against it.
- **Provenance.** Each commit is authored `operator <name>` with `Operator:` and
  `Operator-Edit: put|rm|mv|revert` trailers (and `Bot-Case:` when you link a
  bot's case with `--case`). `log` shows, per commit, the author, its kind
  (operator, bot, or the team repo's own first commit), and the case or
  proposal it is linked to.
- **A bot whose memory you change is told** (a short note naming the path and
  commit, not the content) and re-reads it through conductor.
- **Revert** makes a new commit that undoes one commit. A revert that
  conflicts with later edits is refused (edit the file instead), as is
  reverting the repo's first commit.

### Approving with changes, denying with a reason

A knowledge promotion can be approved **with your changes**: the final text
is yours, the record keeps what the bot proposed.

```
conductor bot proposal platform-sre p15              # its proposed text; write your final version to a file
conductor bot approve platform-sre p15 --edit-file final.md
conductor bot deny platform-sre p16 --reason "db-prod-1 was renamed; promote the new name"
```

- **With changes,** the commit is authored by you, with trailers naming the
  proposing bot (`Bot:`, `Bot-Proposal:`), your edit (`Edited-By:`) and the
  hash of what the bot proposed (`Proposed-SHA256:`). The proposal records
  both hashes. The bot is told its text was changed, and reads what landed.
  Only a knowledge promotion can be approved with changes — its text is the
  content; a verb or a Terraform plan is approved exactly or not at all.
- **A reason** (on a denial, or an approval) reaches the proposing bot's
  inbox fenced as `OPERATOR REASON`: data, like everything else it is told.
- Your signature covers both: the signed payload includes the SHA-256 of the
  edited text and of the reason, so neither can be swapped after you sign.

### Routing work to a team

A trigger can target a team instead of a bot:

```yaml
triggers:
  - on: pd.incident_triggered
    steps:
      - bot: { team: platform, title: "{{.title}}", text: "{{.summary}}", urgent: true }
```

The team picks who handles it, in order:

1. **Rules** (`routing.rules`). These are deterministic, with no model. Each
   rule's `match:` holds globs against `source`, `title`, `text`, `requester`,
   or any field of the event (dotted for nested ones: `labels.env`). All of
   them must match, and the first matching rule wins.
2. **The lead's triage** (`routing.lead`). Conductor opens a triage case in
   the lead and starts a **read-only** worker of the lead: observe-tier verbs
   and memory reads only. That worker picks a teammate:
   `conductor bot assign <case> <bot> [--note "…"]`. The lead's home
   session can assign too.
3. **The fallback** (`routing.fallback`, required) takes what no rule matched
   with no lead. It also takes what the lead did not assign within
   `routing.timeout` (default 10m), what its triage worker finished without
   assigning, and what it could not start triaging.

**Requester provenance is preserved end to end.** The requester is classified
once, at intake, against the team's operators. Each hop can only lower that
trust:
- The assignee's case carries the **original** requester, source, text and
  urgency.
- An operator of the team who is not one of the assignee's operators is a
  third party there.
- A lead acting while a less-trusted request is open passes that taint on.

**Assignment never upgrades trust.** A third party's request routed through
the lead to another bot is still that third party's request at that
bot's gate: its writes become proposals attributed to the third party.
Only the team's lead assigns, only its own triage cases, only to a bot of
the team, and only once. The lead's note reaches the assignee fenced, as data.

**The ledger** records the team, the routing decision (`rule` with its name,
`lead` with the lead's triage case, or `fallback` with why) and the assignee on
every routed case. The lead's triage case records where it went.
`conductor bot team platform` lists recent routed cases, the pool's use and
pending knowledge proposals.

### Approvers

Every proposal names the operators who may decide it, and the proposing
bot's team:
- a bot's proposals: the bot's operators (a subset of the team's);
- a knowledge promotion: the team's operators.

The daemon re-checks this against the config at decision time, so an operator
removed from the team can no longer approve.

### The team's proactive pool

`budget.proactive` is one pool for the whole team. It counts every bot's
sweeps together over a rolling 24h, in each unit it caps: runs, turns, tokens,
and (if set) dollars. No sweep starts once the team has used up any one of
them, even if the sweeping bot's own budget has room. A bot's own budget is
narrower **per unit**: a unit the pool caps can only be capped lower, and one
the bot leaves unset inherits the pool's. A unit the pool doesn't cap stays the
bot's own choice. A pool with no cap at all is a `conductor validate` warning
(it limits nothing).

Model spend is optional throughout: on a subscription, cap in units and leave
the dollars out. (`commit_money`'s `spend_cap` is different: it caps **cloud
spend** — purchases a bot proposes — and is unchanged.)

### Teams and packs

A pack can ship a commented team for the bots it defines. The `sre-bot`
pack has one: a lead and two bots, disarmed, with routing examples.

## The autonomy gate

Every verb a bot session calls goes through the same skill chokepoint as any
agent's (`RunSkillVerb`: the skill policy's access and resource checks), then through
the bot's gate:

| tier | behavior |
|---|---|
| `observe` | runs |
| `reversible` | a proposal — unless promoted, not in shadow mode, the sessions are sandboxed, and the requester is an event, the bot itself or an operator (never a third party, a peer bot or a proactive sweep) |
| `propose` | always a proposal; runs when an operator approves it |
| `commit_money` | a proposal with a **typed amount** (`autonomy.amounts` names the option, a number) under the hard `spend_cap`: over `per_action`, or over `per_period` counting what has already been spent, is refused outright — and the period is re-checked at execution. **Unattended** only where you opt a verb in with `autonomy.unattended` (below) |
| `never` | refused |

- A verb matching several tiers takes the strictest; an **unclassified verb is
  `propose`**. `step.done` acts only on the caller's own dispatch and is always
  allowed.
- **Shadow mode** (the default) executes nothing above observe: proposals are
  recorded as `shadow` — what the bot would have done — and cannot be
  approved. Promote action classes from that record, then set
  `shadow: false`.
- **Requester authorization.** Requester classes, least trusted first: `user`
  (a third party) and `peer` (another bot), `proactive` (a sweep, or a case
  its finding opened), `event`, `bot`, `operator`. A home-session call
  carries the **least-trusted requester among the bot's open cases** —
  while a third party's request is open, nothing the home session does runs on
  more trust than that third party's. A worker's calls carry the provenance it
  inherited at spawn (above). The requester only ever lowers what runs
  autonomously; every approval needs an operator's signature whoever asked.
- A gated call returns a proposal id instead of running:
  `{executed: false, proposal: p7, status: pending, …}`. The agent can block on
  `conductor bot wait p7`.

### Purchases (`browser.purchase`)

A bot's purchase through the agent browser ([[Browser]], "Payment cards and
purchases") is the verb `browser.purchase` at its gate: classify it
`commit_money` with `amounts: { browser.purchase: total }`, and every purchase
is a proposal under `spend_cap`. Approving it (`conductor bot approve`)
approves the purchase; the card is filled later, once, on its merchant, after
conductor re-reads the total.

`autonomy.unattended` lets it run without a per-purchase approval, narrowly.
It's never the default, and `conductor validate` names it as a widening:

```yaml
autonomy:
  tiers: { commit_money: [browser.purchase] }
  amounts: { browser.purchase: total }
  spend_cap: { per_action: 50, per_period: 200 }
  unattended:
    browser.purchase: { per_action: 10, per_period: 30, period: 24h, merchants: ["https://shop.example.com"] }
```

- It must fit inside `spend_cap`, which is checked too.
- A team's bot needs the team's `autonomy.ceiling: commit_money`.
- It runs only when all of these hold:
  - the bot is out of shadow mode and its sessions are sandboxed;
  - the requester is the operator or the bot itself;
  - the charge isn't recurring;
  - the merchant is one you named;
  - it fits the unattended cap (its own spend) and `spend_cap` (all spend).

  Both caps are checked in the ledger write that records the spend.
- Anything else is a proposal.
- The card's own merchants and limits apply either way.

### Terraform changes

Bots never hold a write role and never run `terraform apply`. A bot
changes infrastructure by **proposing a commit**; conductor plans it, shows the
plan, and after a signed approval applies **exactly that saved plan** itself.

1. The session makes the change in its working copy, commits it and pushes it
   (`conductor call git.push`), then proposes the pushed commit:

   ```
   conductor bot propose-terraform --repo infra --commit 3f2a…(full sha) \
       --dir envs/prod [--workspace prod] [--var-file prod.tfvars]… [--target <addr>]…
   ```

   `--repo` names an entry of `terraform.repos` (the agent never supplies a
   URL); the commit is a full SHA, never a branch; `--dir` is the root module,
   repo-relative; workspace, var files and targets are part of the proposal and
   cannot change later. The gate tiers it as `terraform.apply` (`propose` unless
   your tiers say `never`).
2. **Conductor plans it** in its own private directory
   (`<state>/bots/<name>/tf/<proposal>/`, mode 0700, plan file 0600, outside
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
   **`apply_connector`'s** environment — its write role, which no bot verb
   can reach. A plan made stale by a later state change fails with
   `conductor bot replan <bot> <id>` as the next step (the session can
   `conductor bot replan <id>` too): it plans the same commit again as a new
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
`conductor-bot-approval`) over a canonical payload naming the bot, the
proposal and the SHA-256 digest of the exact action:

```
$ conductor bot proposals platform-sre
$ conductor bot approve platform-sre p7          # or deny
Approving proposal p7 (bot platform-sre, case c3, tier propose):
  kv.set {"key":"web/replicas","store":"state","value":5}
  requested by slack:U02 (user); status pending
proposal p7: executed
```

`approve` signs with `ssh-keygen -Y sign` using your configured public key
through your ssh-agent (or `--key <private key file>`), and sends the decision
over the **operator control socket**. The daemon verifies the signature
against `operators.<name>.ssh_key`, checks the operator is one of the bot's,
that the proposal is still pending and unexpired, and that the bot is not in
shadow mode — then executes **exactly** the signed action (re-checked against
the digest at execution, through the same chokepoint) — or, for a Terraform
proposal, applies its saved plan.

What makes it operator-authenticated *outside the agent*:

- there is no approve op on the agent socket, and no verb, option or text that
  approves anything — an agent saying "approved", or passing `approved: true`,
  changes nothing;
- a signature binds one decision on one proposal's exact digest: it cannot be
  replayed onto another proposal, a changed action, or the other decision;
- the signing key is yours. **Without conductor's agent sandbox, bot
  sessions run as your OS user**, so the guarantee is exactly as strong as
  that key's protection: keep it in an agent that confirms each signature (a
  hardware key, 1Password's SSH agent, `ssh-add -c`), not in an unencrypted
  file a session could read. With the sandbox on, the key, the ssh-agent
  socket and the control socket are all outside every session.

This needs only paseo and the conductor CLI — no chat integration.

## Sandbox status

Bot sessions are **sandboxed** only when conductor's agent sandbox is on for
the daemon (`conductor bot show` reports `sandboxed`). Without it:

- promotion has no effect: a promoted reversible verb is still a proposal;
- the home session launches on the plain paseo provider; with the sandbox it
  launches on the sandboxed provider entry for the same base provider
  (`conductor paseo setup`: `claude` → `claude-sandboxed`), and the sandbox
  resolves its per-session working directory to the bot's identity;
- workers run on the cli runtime either way, sandboxed by the engine when the
  sandbox is on;
- the gate covers conductor verbs, not an agent's raw shell — keep write
  credentials out of the environment bots run in, and give them access to
  systems only through connector verbs and the Terraform flow above.

## Operator commands

```
conductor bot ls [--folder <path>] [--flat]   # a tree: folder, team, bot
conductor bot show <bot>         # home session, open work, memory log, charter
conductor bot cases|workers|proposals <bot> [--all]
conductor bot proposal <bot> <id>
conductor bot approve|deny <bot> <id> [--as <operator>] [--key <path>] [--reason "…"]
conductor bot rotate <bot>
conductor bot say <bot> <text…>  # or just type into paseo
conductor bot deliver <bot> --title "…" [--text "…"] [--requester who] [--urgent]
conductor bot consolidate <bot>
conductor bot replan <bot> <id>  # re-plan a stale terraform proposal
conductor bot sweep <bot>        # a proactive sweep now (budget applies)
conductor usage [--since 24h]    # model usage per runtime/model (per usage.mode; see [[Configuration]])
conductor bot sweeps <bot>
conductor bot templates [--search …] [--registry …]   # community bots: templates packs ship ([[Bot-Templates]])
conductor bot new <name> --from <instance>/<template> [--grant verb=tier]… [--write]   # instantiate one (planned on a copy, a diff)
conductor bot grant <bot> <verb>=<tier>… [--write]    # widen a template bot
conductor bot template-check <bot>                    # compare its memory with its template now
conductor bot teams              # every team: bots, routing, pool, knowledge
conductor bot team <team>        # …and recent routed cases, delegations, knowledge log, pending promotions
conductor bot delegations <bot> [--all]  # its delegation edges: handed out, and held
conductor bot knowledge put|rm|mv|revert|ls|cat|log <team> …   # edit a team's knowledge (above)
conductor bot memory put|rm|mv|revert|ls|cat|log <bot> …       # edit a bot's memory
conductor bot memory|knowledge index|recall <bot|team> …       # what its sessions see and find
conductor bot approve <bot> <id> --edit-file <f>               # approve a promotion with your changes
```

Every session launch, event, worker, memory write, proposal and decision is
audited (`bot_*` events).

Related: [[Agent-Skill]] · [[Workflows]] · [[Runtimes]] · [[Trust-and-Isolation]] · [[Packs]]
