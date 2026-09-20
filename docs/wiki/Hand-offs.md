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
agent (paseo's interactive surface). The agent is held from the reaper either
way.

## Reactive watch (`watch:`)

A hand-off is held open until a human closes it — but the world can make it
moot first (the PR merges, someone else approves). A `watch:` block on the
step lets the hand-off tear itself down when the reason it existed goes away,
so you don't click through a stale draft.

```yaml
- id: review
  agent: reviewer
  background: true
  handoff: slack
  watch:
    uses: gh.pr_get        # a read verb, polled every `every` (default 60s)
    every: 60s
    on:
      - if: "pr.merged == true"
        uses: handoff.bail
        options: { notify: "PR merged — closing the review" }
      - if: 'pr.review_decision == "APPROVED"'
        uses: handoff.bail
        options: { notify: "approved elsewhere — closing" }
```

The read runs once at hand-off creation to freeze a snapshot, then every
`every`. Each rule's `if:` sees the latest read under its object name (`pr`
by default; set `as:` to rename) and the frozen snapshot under
`handoff.<as>` (e.g. `handoff.pr.head_sha`) — so a rule can compare now
against then. The first rule whose `if:` holds fires its action; a broken
condition is skipped, never fired.

`handoff.bail` cancels the live agent's review loop, closes the draft, and
releases the reaper hold so the workspace is reclaimed. The poll target
(repo/PR) is defaulted from the trigger only when the platform assigned it;
for a sender-chosen target, name `repo`/`pr` in `options:` explicitly.

`watch:` is subject-agnostic — the only PR-specific choice is the read verb
you name in `uses:`. It is operator-owned: an agent-authored step may not set
it.

> `handoff.refresh` (re-run the producer when the subject moves) and
> `handoff.done` (release when the conversation concludes) are declared but
> not yet wired into `watch:`; they land in a later increment.

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
