# Bot templates

A **bot template** is a bot a pack ships the way it ships workflows: a
community bot you pull in, not just a design you copy. A template carries
the charter, the documents its memory starts with, the connectors it needs,
the verbs it asks for, and suggested triggers and proactive checks. You
**instantiate** it into a real bot in your config — as many bots as you want
from one template.

Templates travel in [[Packs]]: the pack instance gives them their version,
hold, trust and lockfile. There is no second distribution system.

> **A template only requests.** A bot made from one starts in practice mode
> with only its read-only verbs. Everything above observe is a widening you
> grant yourself, one verb at a time, and the template can never set your
> operators, team, folder, connector bindings, secrets, autonomy, sandbox
> or egress.

## Using one

### Find it

```
conductor bot templates                       # installed packs' templates, then the official registry's
conductor bot templates --search oncall       # every word must match: name, pack, description, tags, inputs, verbs
conductor bot templates --installed           # only what your packs: ship
conductor bot templates --registry ./my-packs # another registry: a directory or a git repo, one pack per top-level dir
conductor bot template sre/sre                # one: its inputs, requested verbs, suggestions, seed and charter
```

A registry template not installed yet comes with the `packs:` block that
installs it. Add it, run `conductor init`, and the template is `<pack
instance>/<template>`.

### Instantiate it

```
conductor bot new platform-sre --from sre/sre
```

`bot new` is a config change, reviewed like any other: conductor writes the
new bot (and the template's suggested triggers) into a **copy** of your
config beside it, runs the full `conductor validate` on the copy, and shows
you what it means and the diff:

```
Bot platform-sre from template sre/sre
  pack:        sre-bot 2.0.0 (github.com/NodeSpy/conductor-packs//sre-bot)
  connectors:  chat → slack, pagerduty → pd, schedule → timer
  practice:    true (nothing above observe runs; it records what it would do)
  read-only:   pd.get_incident, pd.list_incidents
  widenings:   none
  not granted: chat.post (at most propose), pagerduty.acknowledge (at most reversible), …
  note:        input aws is unbound: its verbs are not granted (bind it and grant them when you have one)
  validated on a copy of your config: ok

--- config diff
@@ -9,3 +9,59 @@
+bots:
+  platform-sre:
+    # from template sre/sre (sre-bot 2.0.0): starts in practice mode with only its read-only verbs
+    use: sre/sre
+    grant:
+      pagerduty.get_incident: observe
+      pagerduty.list_incidents: observe
+triggers:
+  # suggested by template sre/sre — disarmed; set enabled: true to route pd.incident into platform-sre
+  - name: platform-sre-page
+    enabled: false
…
Not written: review the diff, then run the same command with --write.
```

Nothing is written without `--write`. Flags:

| flag | what |
|---|---|
| `--connector <input>=<connector>` | bind an input (repeatable); an input you have exactly one connector of the type for binds itself |
| `--grant <verb>=<tier>` | a widening at instantiation (repeatable) — refused past the template's maximum |
| `--folder <path>` | file the bot somewhere else (default: the pack instance's placement) |
| `--proactive` | turn on the template's suggested proactive checks (`proactive: {}`) |

Suggested triggers are written **disarmed** (`enabled: false`); turn on the
ones you want. One whose event another trigger already listens on (a cron
schedule drives exactly one trigger) is left out, with a note.

### What it writes

```yaml
packs:
  sre: { use: conductor-packs/sre-bot, version: "~> 2.0" }

bots:
  platform-sre:
    use: sre/sre                        # <pack instance>/<template>
    connectors: { pagerduty: pd }       # its inputs → your connectors (optional when unambiguous)
    grant:                              # what you allow of what it requests
      pagerduty.get_incident: observe
      pagerduty.list_incidents: observe
      pagerduty.acknowledge: reversible # a widening
```

`use:` is how a pack's other members are named (`call: review/review-flow`),
and the field connectors and runtimes already use for "what implements
this". The rest of the entry is an ordinary bot: anything you set —
description, charter, escalation, operators, team, folder, autonomy,
proactive, Terraform — is yours and wins over the template.

**Grants are in the template's vocabulary** (`<input>.<verb>`), so they stay
the same whichever connector the input binds to. A verb is active only where
`grant:` says so:

| grant tier | allowed when the template's maximum is |
|---|---|
| `observe` | observe |
| `reversible` | observe or reversible |
| `propose` | observe, reversible or propose |
| `commit_money` | commit_money (needs your `autonomy.spend_cap`) |
| `never` | anything — refuse it outright |

A grant looser than the template allows is refused by `bot new` and `bot
grant`; one written by hand is **cut to the template's maximum** at load,
with a notice. A grant of a verb the template does not request is an error:
a verb of your own goes in `skill.verbs` and `autonomy.tiers`, as on any bot.

### Widen it

```
conductor bot grant platform-sre pagerduty.acknowledge=reversible   # plan, validated on a copy, diff
conductor bot grant platform-sre pagerduty.acknowledge=reversible --write
```

Everything a template bot may do beyond its read-only baseline is a
**widening**, and `conductor validate` names every one, every time:

```
notice: bot platform-sre: from template sre/sre (sre-bot 2.0.0): practice mode; read-only: pd.get_incident, pd.list_incidents; WIDENING: pd.acknowledge at reversible; requested, not granted: chat.post (at most propose), …
```

Widenings are: a grant above observe, a verb of your own, practice mode off
(`autonomy.shadow: false`) and promotion (`autonomy.promote`). The dashboard
asks for your passkey before it writes one. [Team ceilings](Bots#defaults-are-ceilings-narrow-never-widen)
apply on top, as for any bot.

### Where it came from

```
conductor bot show platform-sre
  "template": { "ref": "sre/sre", "pack": "sre-bot", "version": "2.0.0", "source": "github.com/NodeSpy/conductor-packs//sre-bot",
                "digest": "c42f7ca79ad2", "bindings": { "pagerduty": "pd" }, "practice": true,
                "read_only": [...], "widenings": [...], "ungranted": {...},
                "applied_version": "2.0.0", "since": "…", "pending_update": "p14" }
```

The memory repo's first commit names it too (`memory: initialize from
template sre/sre (sre-bot 2.0.0)`, with a `Bot-Template:` trailer).

## Updates

Updating the pack (`conductor pack update`, or the daemon's dependency
updates, within your version constraint, never past a `hold:`) changes the
**template**, never the bots' memory directly. A bot's memory starts as its
template's charter and seed documents; from then on it is the bot's.

When a newer template's charter or seed documents differ from what the bot
last took, conductor (at start, and after a reload) opens **one proposal**
holding each change as a diff against the memory **as it is now**:

```
conductor bot proposals platform-sre
conductor bot proposal platform-sre p14
proposal p14 (pending): update memory from template sre/sre 2.0.0 → 2.1.0: update charter/charter.md, update runbooks/page-triage.md (your memory changed it since), new watch/cert-expiry.md

--- a/runbooks/page-triage.md
+++ b/runbooks/page-triage.md  (your memory changed it since the template last did — approving replaces that)
@@ -8,3 +8,4 @@
…
conductor bot approve platform-sre p14       # signed, like every decision
conductor bot deny platform-sre p14 --reason "we tuned that runbook ourselves"
```

- **Approved**, it lands as one commit authored by you (`Template-Update:`,
  `Bot-Proposal:`, `Approved-By:` trailers) — but only where memory still
  holds what the proposal showed; a document the bot changed in between is
  never overwritten, and the next check proposes again against what is there.
- **Denied**, it is remembered: the same changes are not proposed again. A
  later version's are.
- Practice mode does not hold it: a template update is your decision about
  the bot's memory, not the bot acting.
- A newer version while one is pending supersedes it. `conductor bot
  template-check <bot>` compares now.
- **Requested-verb changes are never applied.** A verb the new version
  requests shows up in `conductor validate` as "requested, not granted"; a
  lowered maximum cuts your grant to it, with a notice.

The charter of a template bot lives in its memory repo (`charter/charter.md`),
a document only conductor writes — no bot can change its own charter, and a
`charter:` or `charter_file:` you set in the bot replaces it.

## Writing one

A template is a `bots:` entry in a pack's `conductor-pack.yaml`:

```yaml
pack:
  name: sre-bot
  version: 2.0.0
  description: A first-line SRE bot template
  requires: { conductor: ">=0.66.0" }

bots:
  sre:
    description: First-line SRE — triages pages, investigates read-only, proposes every change.
    charter_file: charter.md            # or charter: | …  (in the pack)
    scope: "Compute, deploys, networking, spend."
    escalation: "Escalate when a sev1 is not mitigated in 15 minutes."
    seed: seed                          # a directory in the pack: knowledge/, runbooks/, watch/, a collection's home
    collections:
      postmortems: { from: collections/postmortems.yaml }   # or inline
    connectors:                         # INPUTS — the operator binds them
      pagerduty: { use: pagerduty, version: ">=1.0.0", desc: "pages and incident verbs" }
      chat:      { use: slack, required: false }
    verbs:                              # REQUESTS, each with the loosest tier the operator may grant
      pagerduty.get_incident: observe
      pagerduty.acknowledge: { max: reversible, why: "stop the re-notifications while it investigates" }
      pagerduty.resolve: propose
      aws.purchase_savings_plan: { max: commit_money, amount: total_commitment_usd }
    triggers:                           # suggested routes, written disarmed by `conductor bot new`
      - name: page
        on: pagerduty.incident          # <input>.<event>
        filter: { event_types: [incident.triggered] }
        title: "page: {{.title}}"
        urgent: true
      - { name: weekly, on: schedule.weekly-consolidate, action: consolidate }
    proactive: { every: 4h, quiet_hours: "22:00-07:00", checklist: { section: "Proactive checks" }, notify_at: high }
    model: claude-opus-5                # a suggested home model (applied when the operator sets none)
    runtime: "a use: cli runtime with claude-code"   # a suggestion, shown — runtimes are the operator's
```

- **Inputs** name a connector type (`use:`, default: the input's name), an
  optional version constraint, and `required:` (default true; an unbound
  optional input turns its verbs off).
- **Requests** are `<input>.<verb>` — a name or a prefix pattern
  (`aws.cost_*`), never every verb of a connector (`aws.*`). The maximum is
  `observe`, `reversible`, `propose` or `commit_money`; a `commit_money`
  verb names its typed `amount:` option. Ask for the least a bot needs and
  say `why:` — operators read it when they grant.
- **Triggers** route one event into the bot: `title`, `text`, `urgent`,
  `requester`, or `action: consolidate|rotate`.
- **Proactive** is used only by a bot whose operator writes `proactive: {}`;
  a template never sets a budget.
- **Seed** documents are typed, like any memory write: each must be a valid
  document of its kind within its [size cap](Bots#size-limits). `conductor
  pack lint` checks every one; a file that would not seed is a lint problem.

### What a template cannot set

Each of these is refused **by name** when the pack loads, and by `conductor
pack lint`:

| key | why |
|---|---|
| `operators`, `approvers` | who approves a bot is the operator's choice |
| `team` | which team a bot joins is the operator's choice |
| `folder` | where a bot is filed is the operator's choice (default: the pack instance's placement) |
| `secrets` | secrets are the operator's environment |
| `skill`, `grant` | a template requests verbs; the operator grants them |
| `autonomy` | tiers, practice mode, promotion and spend caps are the operator's |
| `isolation`, `browser`, `network`, `egress` | a template cannot widen the sandbox, browser logins or egress |
| `memory_repo`, `seed_dir`, `memory` | where memory lives and its limits are the operator's; seed with `seed:` |
| `peers` | linking bots is a channel for data to cross |
| `terraform` | it binds connectors and repos — environment |
| `home`, `worker` | runtimes are environment; suggest a `model:` and a `runtime:` |
| `disabled`, `use` | whether a bot runs is the operator's; a template cannot instantiate another |
| `proactive.budget` | it spends the operator's money |

### Publishing

A template is published with its pack: tag a release, add it to a registry
(the official one is `NodeSpy/conductor-packs`, one pack per top-level
directory), and it appears in `conductor bot templates --search`. A change to
the charter or the seed reaches existing bots as a proposal their operators
review; a change to the requested verbs never reaches them on its own. The
[`sre-bot`](https://github.com/NodeSpy/conductor-packs/tree/main/sre-bot)
pack is the reference.

## For the dashboard

The operator control socket serves the same catalog and plans (`cmd: "bot"`):

| `sub` | args | returns |
|---|---|---|
| `templates` | `search`, `registry`, `installed` | `{ templates: [ { ref, template, pack, version, description, tags, installed, install, inputs, verbs, read_only, above_observe, triggers, proactive, model, runtime, seed, collections, used_by, digest } ] }` |
| `template` | `ref`, `registry` | one template, with its `charter` |
| `template_plan` | `name`, `template`, `connectors`, `grants`, `folder`, `proactive` | `{ bot, template, entry, triggers, diff, valid, error, review, notes, widening }` |
| `grant_plan` | `bot`, `grants` | the same, for a widening |

A plan writes nothing; `widening: true` is the dashboard's cue to ask for the
passkey before it writes the change. The daemon validates a plan by loading
the copy (every config rule); the CLI also runs the full connector build.

Related: [[Bots]] · [[Packs]] · [[Configuration]]
