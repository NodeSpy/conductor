# Bots — design notes (#172)

User-facing reference: docs/wiki/Bots.md. This page records the decisions
behind it and the seams left for conductor's agent sandbox.

## The issue's open questions, answered

- **Ledger home.** Conductor's own state (`bots.json` beside
  `affinity.json`/`plans.json`, written through `store.persist`), with paseo as
  a client of the operator CLI. The ledger is what a fresh session resumes
  from and what the gate decides against (case requesters, spend), so it lives
  with the gate. A `bots.json` that does not parse is moved aside and the
  ledger flagged degraded; commit-money is refused while degraded (a vanished
  spend history must not reset a cap).
- **Approval surface without chat.** `conductor bot approve` signs the
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
  can wrap the same repo later; nothing in the bot depends on it.

## Launch paths (no parallel launcher)

- **Home:** the paseo `Dispatcher` (labels, ownership ledger, skill session),
  through the `bot.HomeLauncher` seam (`PaseoHome`). The model resolves
  through the engine's fleet ladder like any step's.
- **Workers:** each bot expands at load into two `on: manual` triggers;
  `spawn` fires one through the same path as `conductor run` — an ordinary run
  on the **cli runtime** (history, watch, budget, guidance, skill card). Only
  `--handoff` uses paseo, through the ordinary background hand-off path.
- cli sessions get their conductor session (endpoint + token) and `step.done`
  resolves controller dispatches through the non-sandbox half of #160's
  `ccc6331`, ported verbatim. Nothing in the worker path assumes a jail: a
  worker is an ordinary cli run, so the sandbox confines it the way it
  confines any cli step.

## One enforcement path

The gate is `flow.BotGate`, consulted inside `RunSkillVerb` after the
skill policy's access and resource checks. Approved execution runs back through
`RunSkillVerb` with the approval in its context; the gate admits exactly the
signed digest of an `approved` proposal, once.

## Binding

`config.Step.BoundBot/BoundBotRole` (`yaml:"-"`) mark the synthesized
worker step; `SkillEnv` stamps the bot, role and the case id from the
daemon-built trigger into the session token (durable, like a hand-off's).
Nothing an agent sends can set them.

## Terraform: conductor applies, bots propose

Bots never hold a write role and never run a write command; there are no
host-command proposals and no grants. An infrastructure change is a
**proposal of a commit** (`bot.Terraform`, proposal kind `terraform`):

- **Inputs are pinned.** Repo (a name from `terraform.repos`, never a URL),
  full commit SHA, root-module dir, workspace, var files, targets. Validation
  refuses anything that escapes the checkout or isn't a full SHA. The gate
  tiers the proposal as `terraform.apply`; a `never` tier refuses it before
  any planning.
- **Conductor plans it** in `<state>/bots/<m>/tf/<proposal>/` (0700; the
  plan file 0600): clone `--no-checkout`, fetch the commit if needed,
  `checkout --detach`, verify `HEAD`; `init`, `workspace select`/`new`,
  `plan -out`, `show -json` → counts and replaced/destroyed addresses,
  `show -no-color` → the operator's text, `sha256(plan.bin)`. Planning runs in
  the background; the proposal is `planning` until it lands as `pending`,
  `refused`, `shadow` or `failed`.
- **Roles come from connector config**, never from the agent:
  `plan_connector` / `apply_connector` are ordinary `terraform` connectors
  whose `engine`/`binary`/`env` conductor reads (`Config.BotTerraformRole`)
  and runs the CLI with directly. Inherited `TF_*`/`AWS_*`/`GOOGLE_*`/`ARM_*`
  are stripped so the connector's `env` alone decides the role. Validation
  makes it an error for any bot `skill.verbs` pattern to reach the apply
  connector. Terragrunt is refused (no single saved plan).
- **The approval covers the plan.** The digest is
  `ActionDigest(bot, terraform, terraform.apply, {repo, commit, dir,
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

Behind `bot.Sandbox` (`Active`, `PaseoProvider`) until conductor's agent
sandbox is on main; `bot.NoSandbox` is the default. The switch is in
`cmd/conductor/bots.go`:

```go
func sandboxSeam() bot.Sandbox {
	return bot.SandboxFuncs{
		IsActive: func() bool { return <the sandbox is enforcing agent launches> },
		Provider: func(base string) (string, bool) { return <the paseo provider entry `conductor paseo setup` made for base, e.g. claude-sandboxed> },
	}
}
func registerSandboxSessions(svc *bot.Service) { <sandbox endpoint>.SetSessionLookup(… svc.SessionForCwd …) }
```

With it: promotion takes effect; the home session launches on the sandboxed
provider entry for its base provider (the model pin unchanged); the sandbox's
wrapper endpoint resolves a launch's cwd through `Service.SessionForCwd`
(`IdentityForCwd` wraps it) — a live home session or hand-off worker, never a
retired one — to mint that session's token with the bot binding. Tokens
under rotation: the retiring session's are revoked at once, the new session
gets fresh ones (`TestWrapperTokensAcrossRotation`). cli workers are launched by
the engine and sandboxed like any cli step; nothing bot-specific is needed.

## Proactive sweeps

- **A sweep is a cli worker**, not a new launcher: the bot's third
  synthesized `on: manual` trigger (`bot:<name>:sweep`, only with
  `proactive:`), the worker step with the sweep's timeout and — when the budget
  caps tokens or dollars — a 24h workflow `policy.budget` the engine enforces
  while the sweep runs.
- **Budgets are in units first.** `config.ProactiveBudget` caps runs, turns,
  tokens and (optionally) dollars; `Exceeded` names the first cap a window has
  reached, and the bot's budget and the team pool both go through it. A sweep
  records its run's turns, tokens and cost (and the same per runtime/model,
  for display) from the run history. Turns are claude-code's `num_turns`, or
  one per agent run. Nothing is required: a subscription user caps in units, or
  not at all. `usage.mode` (default `off`) only changes what is shown; caps
  enforce in every mode.
- **Read-only is the gate's, not the prompt's.** The sweep's token is bound to
  the sweep id (the case slot), its worker record carries class `proactive`
  and `read_only`, and `Gate.caller` resolves both from the ledger.
  `proactive` is outside the autonomy allowlist (`event`, `bot`,
  `operator`), and read-only refuses autonomy on its own: either lock holds
  alone.
- **The notify decision is conductor's.** The agent reports findings with a
  severity and a stable key; `finishSweep` dedupes by key against open cases,
  opens a case (class `proactive`) only at or above `notify_at`, notifies on a
  new case or an escalation past the threshold (the home inbox + the
  `conductor.bot_notify` lifecycle event, via `notify.Publish`), and records
  the rest quietly (ledger + `episodes/proactive/YYYY/MM/<date>.md`). A quiet
  sweep writes one ledger record and one journal line.
- **Budget** counts the ledger's sweeps over a rolling 24h: starts, and cost
  from the run history (`RunCost`, recorded at `run_done`). One running sweep
  at a time (checked inside the ledger transaction that creates the next).
  Quiet hours are local time with an optional IANA zone; an operator's
  explicit `conductor bot sweep` ignores them, never the budget.

## Coordination: parents, children, peers

The rule: **instructions flow down, authority never grows.**

- **Parent → child.** A worker's record names its parent (`home:<session>`, or
  `conductor` for workers conductor starts itself). Follow-ups
  (`bot tell`) are stored on the worker and handed over at its next bot
  op — the cli runtime has no mid-turn injection — and `CheckDone` refuses a
  child's `step.done` while one is unread. They are rendered as instructions,
  not fenced.
- **Child → parent** is data: `step.done` output and `bot report` are fenced
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
  bot's own policy admitted the call, so they can only narrow; `--read-only`
  holds the child to observe. Children cannot spawn, join, tell, approve or ask.
- **Peers** (`bot ask`) need an operator link (both sides' `peers:`, or a
  `bot_links:` pair), arrive as a case of class `peer` (requester
  `bot:<name>`, ranked with a third party), carry only the message text
  (fenced), are audited on both sides, and are capped at 4000 bytes and 20 per
  hour per direction. A reply (`ask --case <peer case>`) is routed into the
  sender's original case.
- **Delegation** (`bot delegate`, `internal/bot/delegate.go`) hands a peer
  work rather than a message, on the same predicate (`BotLinked`, so `ask` and
  `delegate` can never disagree about who reaches whom).
  - The edge (`store.Delegation`) lives in the delegator's ledger; the
    recipient's case carries a `DelegationRef` back to it. The outcome goes back
    from `case close`, a refusal from `bot decline`; either moves the edge out
    of `open` inside a ledger transaction, so it reports once.
  - Provenance follows `assign`: the delegator's case's requester, bounded by
    the delegator's `homeAttribution` unless that is the bot's own (`bot`),
    then `atBot` at the recipient. A result of class `bot` — the delegator's
    own initiative — becomes `peer` (`bot:<delegator>`): `bot` means "this
    bot's own initiative" at a gate, and another bot's is not that.
  - The loop guard takes the ancestry of every open delegated case, not just
    the named one, for the same reason `homeAttribution` takes every open
    case: a home session mixes them, and a fresh case would otherwise launder
    a loop. Depth is the length of that ancestry, checked against the lower of
    the two sides' `delegation.max_depth`.
  - The text is fenced as `DELEGATED TASK`: a request to weigh against the
    recipient's charter, like an event, never an instruction that bypasses it.
    There is deliberately no op that lets a delegator approve, close or cancel
    anything in the recipient.

### Persistent sub-bots: left out

A bot creating a long-lived helper bot does not fit cleanly:

- bots are operator config, and nothing on the agent-facing surface writes
  config — a runtime-created bot would need conductor to rewrite the config
  file, or a second, state-held bot registry beside it;
- "a charter scope that is a subset of the parent's" is free text and cannot be
  checked mechanically;
- there is no bot archive operation to cascade (a bot is removed by
  editing config).

The clean equivalent today is operator-side: declare the helper as its own
bot and link it as a peer (data only), or use ephemeral workers (instructions
down, same authority or less).

## Operator authoring

`internal/bot/opedit.go`. The operator edits a team's knowledge repo or a
bot's memory repo directly through three control-socket ops: `edit`
(put/rm/mv/revert), `file` (read, with the head) and `history`. The dashboard
wraps the same three.

- **Operator-only by construction.** The ops exist only in `OperatorOp`.
  `AgentOp` refuses them by name before its switch, so no token reaches them.
  As with approvals, the boundary is the control socket (0600, same uid), and
  until the agent sandbox is on a same-uid session process could in principle
  dial it. Unlike approvals, edits are not signed: the brief makes the socket
  the boundary, and the sandbox is what closes it.
- **One primitive, `Repo.Commit`.** Under the repo lock it resolves `Base` (a
  hex id only: a ref such as `HEAD` would always match) and refuses anything
  but the head with a `StaleBaseError` carrying the current head. It then
  stages exactly the named paths, commits them as `operator <name>`, and
  refreshes clean session worktrees. Any failure resets to the old head.
- **Revert** is `git revert --no-commit` plus a commit with a `Reverts:`
  trailer. A conflict aborts and resets. A commit touching a path outside the
  layout (the first commit's README), the first commit and merges are
  refused.
- **History** parses `git log` with trailers. The kind is read from the
  author's email domain (`@operators…`, `@bots…`, `@teams…`), which only
  conductor's own committers set.
- **Approve with changes / reasons.** `ApprovalPayload` gained
  `edit-sha256` and `reason-sha256` lines. `execKnowledge` checks the
  original digest first (the proposal is what was approved), then lands the
  operator's text: `landKnowledge` authors it as the operator, and the
  proposal keeps `ProposedSHA256` and `EditedBy`. `OnDecision` fences an
  operator's reason as `OPERATOR REASON`.

### Folders across entity types

Workflows and triggers take `folder:` with the same rule. Three things keep
it inert:

- `contentHandle` drops it, so a trigger instance's identity (its dedup,
  session and outcome state) does not move with it;
- a pack's manifest may not set it (`packSetsNoFolder`, at load and in lint),
  because filing is the operator's organization;
- the operator's `packs.<instance>.folder`, `.workflows.<name>.folder` and
  `.triggers.<name>.folder` are applied at instantiation, the per-name one
  winning.

Provenance (`PackInstance`, `Bot`, both `yaml:"-"`) is only for display: an
unfiled pack entity is listed under `packs/<instance>`, and a bot's
synthesized workers under the bot's folder. `TestEntityFoldersChangeNothing`
compares the effective triggers and workflows, instance names included, with
and without folders.

## Content kinds

`internal/bot/kinds.go`, `internal/config/collections.go`. A repo's kinds are
the built-ins plus its collections, computed from config on each use (so a
reload applies). One chokepoint, `Kinds.CheckWrite(path, text, writer,
wantKind)`, runs on what the file will hold: the kind's home (legacy homes
read-only), its path rule (slug, or `free_paths`), the writer (operator, bots,
proposal, system), the named kind, and the frontmatter schema.

- `Repo.WriteIdent` (bots, conductor, promotions) and `Repo.Commit` (operator
  put, mv, revert) both call it. Promotions are also checked when proposed,
  against the team repo's kinds, so a malformed document is refused with its
  schema errors before it is a proposal.
- **Writers.** log and handoff are `system`. Conductor composes their paths and
  frontmatter (`Repo.AppendLog`). An operator's only touch on the log is
  `annotate`/`flag`, which append a note (and set `flagged_wrong`) without
  rewriting history; a revert that would change a log entry is refused by
  the same writer check.
- **Watch** is a directory, not entries in `watch.md`. One document per item
  gives each its own frontmatter, history and revert, and the chokepoint stays
  one rule per file.
- **Migration** never deletes. `episodes/` is the log's legacy home, still
  read, recalled and consolidated. A seed copies only valid documents (the
  rest go into `SeedSkipped`). `Repo.Inventory` classifies every file (`ok`,
  `invalid`, `legacy`, `unmapped`); conductor reports it at start and `ls`
  shows it.

## Reading memory: index, recall, parts, caps

`internal/bot/memindex.go`, `memread.go`, `memops.go`, `archive.go`,
`internal/config/memlimits.go`. Storage did not change — typed Markdown in
git stays what an operator reads, reviews and reverts, and what promotion
copies. What changed is how a session reads it: the old first prompt said
"read README.md and knowledge/", recall was a substring match returning the
first 20 lines, and the log and the handoff grew without bound. Measured on a
synthetic repo of 500 knowledge documents, 100 runbooks and 2,000 episodes
(`TestSessionStartPromptStaysUnderBudget`), a session start went from the
prompt plus everything it was told to read (~725k characters, ~180k tokens)
to a prompt of ~17k characters (~4.3k tokens) with the index at its 6000-byte
budget and a full handoff.

- **The index is derived, and lives outside the repo.** One SQLite database
  per repo under the state dir (`config.BotIndexPath` / `TeamIndexPath`):
  `docs` (path, archive commit, kind, title, scope, summary, size, last commit
  time) and an FTS5 table (title, scope, summary field, other fields, body;
  porter stemming). `meta` holds the commit it reflects and a signature of the
  kinds. `SyncIndex` catches up with `git diff-tree` from that commit, reading
  content with one `git cat-file --batch` at the target commit — never the
  working tree, so a write in flight cannot tear it. A missing, unreadable or
  non-ancestor commit, or a kinds change, rebuilds it. It runs after each
  commit conductor makes (`indexCommitLocked`, best effort) and before every
  query, so a hand commit is never missed. modernc's SQLite is pure Go and
  already a dependency; FTS5 is compiled in, and the build stays CGO-free.
- **Ranking.** BM25 with column weights 20 (title) / 10 (scope: `applies_to`,
  `when_to_use`, a log's case or sweep, a digest's period and cases) / 6 (an
  explicit `summary` field) / 2 (other fields) / 1 (body). A summary that falls
  back to the body's first sentence is shown in the index but not searched with
  the summary's weight — it is body text already, and weighting it let a body
  that repeats a word outrank a title. All-words matches come before
  any-word matches. Query words are extracted and each quoted, so no FTS5
  syntax reaches `MATCH`.
- **The prompt's index** (`renderIndex`) is pure: entries (everything live
  but the log), the query's ranked paths, a budget. It writes lines while the
  line, the footer for what would remain, and the log-count tail still fit,
  then the footer; the result is cut to the budget regardless. Relevance comes
  from the session's purpose: a worker's case and brief, a sweep's checklist, a
  home session's open cases; the rest is newest first.
- **Reads** cut at `read_max` on a line when that keeps at least half, never
  inside a rune, and report the next offset; offsets reassemble the document
  exactly. Sections follow ATX headings outside fenced code.
- **Caps** are one more rule in `Kinds.CheckWrite` (`ContentKind.CheckSize`),
  applied to every writer but `system`: conductor's log entries and handoffs
  are bounded where they are composed, and a refusal there would lose a
  worker's result. The handoff cap is on the session's text
  (`Service.Handoff`); the episode cap on one entry (`Service.Episode`); a
  prompt carries at most the handoff cap of a note edited by hand
  (`capHandoff`).
- **Consolidation and the archive.** A worker's `Consolidates` records the
  log entries it was given with their git blobs; the bot record's
  `ConsolidatedDocs` gains them only when it finishes done and a commit with
  its `Bot-Dispatch` trailer touched `digests/`. `archivable` picks entries
  whose case is not open (and sweep logs of past days); `Repo.ArchiveIf`
  re-checks every blob under the repo lock and `git rm`s the ones that still
  match, in one commit. Archived entries stay in the index — the catch-up treats
  a removed log entry as archived and indexes it at the archive commit's
  parent; a rebuild finds them with `git log --diff-filter=D` — and
  `ReadPart` reads them back from history. Only conductor removes a log entry
  (the log kind's writers are `system`), so a removed one is an archived one.
  The successful consolidation's own case log is marked consolidated with it,
  so a scheduled consolidation does not wake every week for its own record.
- **Left out.** Semantic (embedding) search: BM25 over typed frontmatter
  ranks well for the vocabulary an operator and a bot share, needs no model
  call, and is deterministic to test. Uncommitted files in a hand-managed
  memory repo are not indexed: conductor's memory is what is committed.

## Folders: display only

`folder:` on a bot or a team (`internal/config/folders.go`) exists so an
operator with many bots can file them (`work/platform`, `personal/home`).
It is deliberately inert: nothing outside the listings reads it — not
`ResolveTeams` (it is not a ceiling and is not inherited), not `BotLinked`,
not the gate, not routing — and `TestFoldersChangeNothing` /
`TestFoldersChangeNoRuntimeBehavior` hold that line. Behavior that wants a
grouping belongs to a team. A bot on a team is displayed under the team's
folder (`Config.BotFolder`), so a bot's own `folder:` there is a validate
warning, not an error: it is harmless, just ignored. Paths are validated by
one function (`NormFolder`) at load, and the CLI's `--folder` filter goes
through the same one.

## Teams

User reference: docs/wiki/Bots.md#teams. A team is operator config like a
bot (`teams:` in `internal/config/teams.go`), and conductor still has no
notion of an org: a team is a set of bots that share ceilings, peer with
each other, may share a knowledge repo, and can be routed work as a unit.

### Ceilings, resolved once at load

- **One effective `BotConfig`.** `Config.ResolveTeams` runs at load, before
  `expandBots` copies a bot's grant into its worker triggers. For each
  bot with a `team:` it checks every ceiling (`checkTeamCeilings`, pure),
  then fills in each default the bot left unset (`inheritTeam`, copying
  values, never aliasing the team's maps). Everything downstream (the gate,
  the service, the home launch, the worker steps) reads the bot's own
  config and needs no team awareness. Inherited values sit exactly at the
  ceiling, so resolving again is a no-op. `validateTeams` re-runs the pure
  check, which also covers a `Config` assembled in code. Either check alone
  refuses a widening.
- **Exactly one team.** `TeamName` decodes only a scalar. A list fails with
  "a bot belongs to at most one team", and YAML itself refuses a repeated
  `team:` key.
- **The comparisons.**
  - Operators: a subset.
  - `autonomy.ceiling: observe`: refuses any `promote` (promotion only ever
    reaches `reversible`).
  - `shadow: true`: refuses `shadow: false`.
  - Spend cap: per-action and per-period no higher, the period no shorter
    (the same cap over less time is a faster rate), the same currency.
  - Sweep budget: per unit (runs, turns, tokens, dollars), no higher than a
    unit the pool caps; an unset unit inherits it.
  - Isolation: each flag no looser.
  - Credentials: `secrets_via` ranked `none < broker < env`; `allow_secrets` a
    subset.
  - Browser: logins a subset, with a `*.domain` entry covering its hosts and
    narrower `*.sub.domain` patterns; credentials a subset.
- **Tiers.** Team tiers merge into the bot's, and the gate's
  strictest-wins rule makes that a floor. A bot pattern a team pattern
  covers (`path.Match(team, bot)`) at a stricter tier is a load error, so
  the operator sees the conflict. Overlaps that check cannot see (two globs
  that only intersect) are still resolved strictly at runtime.

### Runtime

- **Peering** is `Config.BotLinked`: teammates whose team does not set
  `peers: none`, in addition to the existing operator links. `Ask` is
  unchanged, so a teammate's message is the ordinary peer case: data,
  fenced, audited both sides, rate-limited. `Delegate` uses the same
  predicate, so `peers: none` turns both off.
- **Routing.** `bot: { team: … }` reaches `Service.RouteTeam` with the
  event's own fields for the rules. The requester is classified once, against
  the team's operators. `atBot` bounds that at each receiving bot by
  re-classifying an operator against that bot's operators and keeping the
  less trusted result.
- **The lead triages through a read-only worker** that conductor starts on a
  triage case in the lead's ledger, not through the lead's home
  conversation. The worker:
  - is immediate (no waiting for an idle home);
  - is read-only by the existing worker lock;
  - leaves the lead's home attribution and inbox alone (a triage worker's
    `step.done` is not reported home).

  `Assign` claims the triage case inside one ledger transaction (`triage` →
  `assigned`), so it happens once. A failed delivery puts it back. It is
  allowed from the lead's home session or from the worker started on that
  case.
- **Taint on assignment.** The assignee's case is the triage case's
  requester, made less trusted by the assigning session's own attribution
  when that is below `bot`. An untainted lead (`bot`: its own
  initiative, no less-trusted request open) adds nothing, so an operator's
  routed request stays the operator's instead of being reset to the lead's
  trust.
- **Fallback.** A triage case left unassigned goes to `routing.fallback`, on
  its original provenance, when:
  - the triage worker finishes without assigning (in `finishWorker`);
  - `routing.timeout` passes (in the lead's `Tick`);
  - triage cannot start (in `RouteTeam`).
- **Ledger.** `Case.Team` and `Case.Routing` (`via`, the rule's label, the
  lead and its triage case, the assignee and its case, the reason, the
  note), plus `Payload` on a triage case, the text its assignee receives.
- **Knowledge** is a `Repo` with `Team: true`:
  - only `knowledge/` and `runbooks/`;
  - `WriteAs` gives each commit its promoting bot as author.

  A promotion is a proposal of kind `knowledge` carrying the exact text. Its
  digest is `ActionDigest(bot, knowledge, team.promote, {team, from, to,
  sha256})`. `Executor.execKnowledge` re-checks the digest and the hash
  before landing, and `LandApproved` refuses a bot that has left the team.
  `Approvers` is the bot's operators for every other proposal, and the
  team's for a promotion. It is computed from the config at decision time,
  and stored on the proposal for display only. `promotion: direct` lands at
  once only on the `autonomyClass` allowlist (event, bot, operator),
  outside shadow mode, and not from a read-only worker.
- **The pool.** `StartSweep` holds a per-team lock while it snapshots the
  teammates' ledgers and checks the pool. The snapshot is taken outside the
  bot's own ledger transaction, because the store lock is held inside it;
  the team lock keeps it current. It records its sweep inside the
  transaction as before.
- **Isolation.** `sandboxGate` refuses a home launch, a worker and a sweep of
  a bot with `isolation.sandbox: required` while `Sandbox.Active()` is
  false. `Tick` logs that once per reason. `Spawn` refuses `--handoff` under
  `handoff_workers: false`.

### Seams and what is left out

- **Browser scope** is config, inheritance and validation only. The agent
  browser (#182) consumes `BotConfig.Browser` when it lands, next to the
  `requesterFor` wiring. Until then nothing reads it, and `conductor
  validate` says so.
- **`isolation.sandbox: required`** becomes satisfiable when the agent
  sandbox (#179) is on main, through the same `sandboxSeam`.
- **The team spend cap is a per-bot ceiling, not a pool.** Each bot's
  commit-money cap fits inside it, but the team's bots are not summed. A
  shared money pool would need the gate to lock across bots' ledgers at
  proposal and at execution. The proactive pool is the one cross-bot
  budget, as specified.
- **No team-level links.** Cross-team traffic still needs an operator to link
  two bots. A `team_links:` granularity was not added; it would link every
  pair across two teams at once, which is the kind of broad channel the
  bot links were designed to avoid.
