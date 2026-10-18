# Tickets

Every piece of bot work is a **ticket**. What used to be a bot's case is now a
ticket in its ledger. A ticket has:

- a title and a description, with files on its comments;
- a **status**, a priority (`low`, `normal`, `high`, `urgent`), an optional
  due date, and labels;
- an **assignee**: a bot, a team (its lead or routing picks the bot), or
  nobody yet (the triage queue);
- a requester, with provenance as before (who asked, and at what trust);
- a comment thread, links to the work it caused (workers and their runs,
  approvals, delegations, a team conversation), and a history of who changed
  what.

Each ticket has a conductor-wide id, `T-<n>`, that stays with it when it moves
from a queue to a bot, or from a team's lead to a teammate.

## Statuses

```
draft → ready → in_progress → waiting → done | declined      (and reopen)
```

| status | meaning |
|---|---|
| `draft` | an idea: operators and bots write and edit it here. **Nothing starts on a draft**: no worker, routing or delegation |
| `ready` | approved to start, with an assignee. A bot assignee picks it up at once; a team's routing picks the bot |
| `in_progress` | a bot holds it and is working it |
| `waiting` | waiting on you (the bot set it, or you did) |
| `done`, `declined` | finished. A held ticket's outcome goes to the bot's episodic memory, and a delegated one's goes back to its delegator |

`reopen` brings a done or declined ticket back. An operator's reopen goes to
`ready` (handed out again) when the ticket has an assignee; a bot's goes to
`draft`.

### The ready gate

Only the transition to `ready`, with an assignee set, makes a ticket
eligible to be worked. Conductor enforces this in one place: a worker, a
delegation and a routed delivery all refuse a ticket that isn't ready or
already active ("ticket T-12 is a draft: nothing starts on it until it is
marked ready").

**Only an operator marks a ticket ready**, by default. A bot may mark the
tickets *it created itself* ready only with `tickets.mark_own_ready`, which
is off by default:

```yaml
teams:
  platform:
    tickets: { default_priority: normal, mark_own_ready: false }   # the team's false is a ceiling
bots:
  platform-sre:
    tickets: { mark_own_ready: true }   # a WIDENING: its own ideas can start without you
```

`conductor validate` names `mark_own_ready` as a widening. A bot never
marks a ticket someone else wrote ready. Events and workflows still create
tickets directly as ready (a page routed to a bot is picked up at once), as
they always have.

## Who may do what

| | an operator | a bot (its home session) |
|---|---|---|
| create | any ticket, any assignee, draft or ready | a **draft** (ready only with `mark_own_ready`), assigned to itself, a bot it may delegate to (its teammates unless the team says `peers: none`, or an operator's link), its own team, or nobody |
| mark ready | yes | only its own tickets, only with `mark_own_ready` |
| assign | anywhere | within the same rules, and only tickets it holds or wrote |
| edit, comment | any ticket | tickets it holds or wrote; every change goes in the history with who made it |
| wait, done, decline | any ticket | tickets it holds |

A worker never writes or moves tickets; it reports to its parent.

**Provenance and trust.** A ticket keeps its requester as it moves. An
operator who isn't one of a bot's operators is a third party to that bot. A
bot's own idea carries the trust of what that bot is handling now, never
more, and marking it ready doesn't raise it. Reassigning an active ticket
goes through the ordinary delivery and routing; a bot's reassignment
carries its own trust at most.

**Comments.** Your comment on an active ticket reaches the bot that holds
it, as an instruction (like `conductor bot say --case`). A comment on a
draft is only a note. A bot's comment is recorded for you to read.

## Commands

```
conductor ticket new "<title>" [--description "…" | --description-file f] [--assignee bot:<b>|team:<t>]
                    [--priority p] [--due YYYY-MM-DD] [--label l]… [--ready] [--attach file]…
conductor ticket list [--assignee …|none] [--holder <bot>] [--status s,…] [--priority p] [--label l] [--search "…"] [--all]
conductor ticket show <T-n>                 # where it is, comments, history, links
conductor ticket edit <T-n> [--title …] [--description …] [--priority p] [--due …|none] [--label l]… [--no-labels]
conductor ticket assign <T-n> <bot:<b>|team:<t>|none>
conductor ticket comment <T-n> "<text>" [--attach file]…
conductor ticket ready|wait|resume|done|decline|draft|reopen <T-n> ["<note>"]
```

With several operators configured, add `--as <operator>`. A bot's session
uses `conductor bot ticket …` (the same subcommands). `conductor bot case
open|note|close` still work: opening a case is a ticket the bot is already
working (in progress, assigned to itself), and closing one marks it done.

Files go on a ticket a bot holds, as attachments on a comment (or with `new`
when the assignee is a bot): see [Bots → Attachments](Bots#attachments).

## The operator API

On the operator socket ([Bots → The operator API](Bots#the-operator-api)):

| | |
|---|---|
| `GET /v1/tickets[?assignee=&holder=&status=&priority=&label=&q=]` | tickets (default: not done or declined) |
| `POST /v1/tickets` | create: JSON `{title, description, assignee, priority, due, labels, ready, operator}` |
| `GET /v1/tickets/{id}` | one ticket: holder or queue, comments, history, links |
| `PATCH /v1/tickets/{id}` | `{title, description, priority, due, labels, assignee, status, note, operator}`, applied in that order: edit, assign, status |
| `POST /v1/tickets/{id}/comments` | JSON `{text, operator}`, or multipart with `file` parts |
| `GET /v1/tickets/stream` | live updates: a server-sent `ticket` event naming each ticket that changed |

An illegal transition or a refused authority is a `422`, with the reason.

## Migration

Conductor auto-updates, so stored cases are migrated **in place** at boot:
open → `in_progress`, closed → `done`. Each gets a ticket id (oldest first),
its bot as the assignee, `urgent` or `normal` priority from its urgency, and
who opened it. The ledger is written back once, and later boots change
nothing. Downgrading to a release from before tickets isn't supported: an
older binary reads the new statuses as closed.

Related: [[Bots]] · [[Teams]] · [[Configuration]]
