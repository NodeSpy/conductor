# Hand-offs (`ask` verbs)

A hand-off presents work to a human and returns their answer into the
workflow. In the connectors model it is a request-response verb — `uses:
<conn>.ask` — on the ask-capable connector types: `web`, `slack`, `discord`.
The channel machinery (draft pages, tunnels, TTLs, reply capture) is the
implementation of those verbs.

```yaml
steps:
  - { id: draft,  type: agent, name: critique, checkout: none,
      prompt: "Draft the review for {{.repo}}#{{.pr}}." }
  - { id: review, uses: slack-ops.ask,
      options: { to: dm, user: U0123ABCD, prompt: "Submit this review?", draft: "{{.draft.text}}", timeout: 2h } }
  - { id: submit, if: "{{.review.action}} == approve",
      uses: gh.submit_review, options: { repo: "{{.repo}}", pr: "{{.pr}}", event: COMMENT, body: "{{.review.text}}" } }
```

Outputs of every ask: `{action: approve|revise|discard, text, ref}` — `text`
is the reply (a revision) or the draft on approve; `ref` is where it was
presented. `timeout:` (default 1h) bounds an unanswered ask.

## Channels

- **`web`** — an approve / revise / discard page with an editable draft,
  served on the inbound listener. The default `listen:` binds loopback only
  (`127.0.0.1:8099`) — draft pages carry approve/deny actions and are meant
  to be reached through the tunnel or a same-box reverse proxy; bind wider
  explicitly if you mean to. `base_url:` for a fixed origin, or a `tunnel:`
  provider (`static`, `lan`, `cloudflared`, `ngrok`, `tailscale`, `ssh`,
  `localxpose`, `command`) for a fresh public URL per ask. Links carry a 192-bit token and
  expire (`ttl:`, default 30m). The `tailscale` provider leaves a serve
  mapping that existed before the draft in place at close (it tears down
  only its own).
- **`slack`** — `to: dm` (a user id) or `to: thread` (a channel); the reply is
  captured over the connector's Socket Mode connection. Replies parse as
  approve (`approve`, `lgtm`, `+1`, …), discard (`discard`, `cancel`, …), or
  anything else = a revision. For `to: thread`, an optional `approvers:`
  list of user ids restricts WHO may resolve the ask — without it, anyone
  in the channel can approve an agent's draft; with it, replies from anyone
  else are ignored and the ask keeps waiting. (Also an `options.approvers`
  on the ask verb itself.)
- **`discord`** — same shape (including `approvers:` for `to: thread`);
  conductor runs the bot gateway itself.

## Background review steps

An agent step with `background: true` launches a live agent you drive. Its
`handoff:` names an ask-capable **connector** to present the review loop on
(present → approve/revise/discard → revise re-presents); with none, the
hand-off stays runtime-native — the notification tells you to open the live
agent (paseo's interactive surface). The agent is protected from reclaim either
way.

## Reactive watch (`watch:`)

A hand-off is held open until a human closes it — but the world can make it
moot first (the PR merges, someone else approves). A `watch:` block on the
step lets the hand-off tear itself down when the reason it existed goes away,
so you don't click through a stale draft.

`watch:` is not a trigger — it's **"run these steps every `every`"**. The steps
are an ordinary mini-workflow: a **fact step** gathers state (a read verb under
an `id:`), and later **action steps** react, guarded by `if:` — exactly the step
vocabulary used everywhere else.

```yaml
- id: review
  agent: reviewer
  background: true
  handoff: slack
  watch:
    every: 60s
    steps:
      - id: pr                       # a fact step: read the subject → .pr
        uses: gh.pr_get
      - if: 'pr.merged || pr.state == "closed"'
        uses: step.bail              # tear down, stop watching
      - if: 'pr.review_decision == "APPROVED"'
        uses: step.bail
      - if: 'pr.head_sha != handoff.pr.head_sha'
        workflow: review-flow        # SUPERSEDE: tear down, re-run this workflow
        with: { repo: "{{.repo}}", pr: "{{.number}}" }
```

Each tick runs the fact steps (their outputs land under their `id:`), then the
action steps in order — the first whose `if:` holds fires. Conditions see each
fact step's output by id (`.pr`) and the frozen **`.handoff.<id>`** snapshot
captured once at hand-off creation (`.handoff.pr.head_sha`), so a rule can
compare now against then (a comparison's right side may itself be a data path).
A broken condition is skipped, never fired. A fact step's read target
(repo/PR) is defaulted from the trigger only when the platform assigned it
(TargetTrusted); for a sender-chosen target, name `repo`/`pr` in its `options:`.

The action steps:

- **`uses: step.bail`** — the reason is gone. Tears the hand-off down (cancel
  the agent, close the draft, release the hold) and stops watching. Use
  it for `pr.merged`, `pr.state == "closed"`, or approved-elsewhere.
- **`uses: step.rerun`** — re-running *this step* is enough. **Supersedes**:
  tears down, then re-dispatches the same step on the current state (surface-
  agnostic — agent, Slack, Discord). Optional `options.prompt` is appended to the
  step's prompt ("here's what changed"). Use when the hand-off step is itself the
  producer.
- **`workflow: <name>` + `with:`** — the review must be **done again**. Supersedes:
  tears down, then runs that workflow (a fresh hand-off arms from its own
  background step). The native step form — not a verb — so it's "run whatever you
  want," including a different workflow in a chain. Use when the draft was
  assembled *upstream* (as in pr-review-team), where re-running just this step
  would re-present a stale draft.

Every superseding action **tears the current hand-off down first** — no stale
draft coexists with its replacement — and fires only on a real change of an
in-flight hand-off, so the re-run cost is bounded. `step.done` is a conclusion
signal, not a watch action (see below).

`watch:` is subject-agnostic (the fact step names whatever read verb fits) and
operator-owned: an agent-authored step may not set it.

## Cleaning up a finished hand-off (`step.done` / `idle_timeout`)

> The hand-off lifecycle generalized to every live step: the canonical verbs
> are **`step.done` / `step.bail` / `step.rerun`** on the built-in `step`
> connector. The `handoff.*` spellings keep working as deprecated aliases
> (same handlers), so existing packs and prompts are unaffected.

When the conversation is genuinely over, the hand-off should release its
workspace rather than sit held until you archive it by hand. Two paths:

- **`step.done`** (alias: `handoff.done`) — an agent skill verb, **auto-granted to every dispatch**:
  the agent calls it the moment it has nothing more for you (the guidance
  appended to every hand-off tells it to), no `skill:` block required. It ends
  the review, closes the draft, and drops the hold on the agent's own
  hand-off — the caller can only release its own, since the daemon resolves the
  target from the token identity, never a name the agent passes. (An explicit
  `skill: { verbs: [step.done] }` is harmless and de-duplicated.)
- **`idle_timeout: <duration>`** on the step — the backstop for a hand-off
  nobody closed. Still open after this long → released the same way. Off unless
  set; independent of `watch:`.

```yaml
- id: review
  agent: reviewer
  background: true
  handoff: slack
  idle_timeout: 12h   # step.done is auto-granted; the agent releases early
```

## Legacy `handoffs:`

The legacy named `handoffs:` block still loads and resolves exactly as
before. [[Migration]] converts each entry into a connector of the matching
type (its dm/thread target becomes the connector's default `options:`) and
stamps the default entry's name onto background steps that named none.

Related: [[Verbs]] · [[Connectors]] · [[Runtimes]] · [[Workflows]]

## Diff preview

When the reviewed agent works in a local worktree, every presentation of the
hand-off draft appends its **current proposed diff** (uncommitted + unpushed,
secret-scrubbed, clipped) — refreshed at each present, so after a revision
you see the revised change, not the stale one. See [[Runs]] (#36 §17).
