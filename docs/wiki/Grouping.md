# Grouping (event batching)

By default every event is its own run, dispatched immediately — except an
event that declares default batching (github's `new_comment`, below). A trigger
may set `group:` to batch a burst of related events into one run:

```yaml
- on: gh.new_comment
  group: { key: "{{.repo}}#{{.pr}}", window: 15s }
  steps:
    - id: handle
      type: agent
      name: fixer
      prompt: |
        Address the comments on {{.group.key}}:
        {{range .group.events}}- {{.comment_body}}
        {{end}}
    - id: reply
      uses: gh.comment
      options: { repo: "{{.repo}}", number: "{{.pr}}", body: "Addressed {{.group.count}} comment(s)." }
```

## Semantics

- **`key`** — the grouping expression (templated). Default: the event's own
  dedup id, so each event stays its own run. Set it to batch — a poster, a
  PR, a label. A key that fails to render — or renders EMPTY (a typo'd
  template path) — degrades to per-event batching (logged once per trigger)
  rather than collapsing every event into one cross-entity batch.
- **`window`** — the debounce window, default `15s`: it resets on each new
  event and the batch fires once the group goes quiet.
- **`max_wait`** — caps how long a never-quiet group can defer, default 4 ×
  `window`.
- **At most one run per key is in flight.** Events arriving while a key's run
  is going buffer into the next batch, which starts its own debounce cycle
  when the run completes. `group: { key: "{{.pr}}" }` therefore gives
  one-agent-per-PR (no branch collisions) as a natural consequence.
- Distinct keys run independently. `policy.concurrency.max_agents` remains
  the only global cap; exact-duplicate events are still dropped by dedup.
- **Dedup is consumed when the batch FIRES, not when an event buffers.** The
  buffer is in-memory: if conductor restarts mid-window the buffered events
  are lost from memory, but nothing was recorded for them, so the source's
  redelivery (a poll, a webhook redelivery) re-buffers them instead of being
  silently suppressed. A redelivery while the batch is still buffered is
  dropped at flush by signature — the batch never doubles an event.
- One key's buffer holds at most 1000 events; past that the OLDEST drop, so
  a hot key under a stuck run keeps the freshest context instead of growing
  without bound. A panicking batch run is recovered and logged — the key
  keeps batching afterwards.

## Default batching (github `new_comment`)

A reviewer who leaves five comments in a row means one round of feedback, not
five jobs. So `new_comment` batches **by default**: a `gh.new_comment` trigger
with no `group:` of its own behaves as if it had
`group: { window: 15s }` keyed on the PR. A burst of comments on one PR is ONE
run — one agent, one commit, one push — instead of one fixer per comment racing
the same branch. Comments on different PRs are still separate runs.

- **Nothing is dropped.** A step with no `prompt:` is handed the whole burst
  (the event prompt includes it). A step with its own per-comment prompt that
  never reads `{{.group}}` still sees the newest comment in its fields, and the
  rest of the burst is appended to its prompt.
- **Tune or opt out** with the trigger's own `group:` — it always wins:
  `group: { window: 2m }` to wait longer, `group: { key: "{{.author}}" }` to
  batch differently, `group: { enabled: false }` for one run per comment,
  dispatched immediately (the old behavior).
- A **forced** trigger (`conductor run --force`) and a **shadow** trigger are
  never batched by default.
- `{{.group.*}}` is in scope for a `new_comment` trigger with no `group:`, as
  for any grouped trigger.

The window is the event's declaration (connector authors: `EventDecl.Coalesce`,
see [[Authoring-Connectors]]); other events keep "every event is its own run".

**Inline comments of a changes-requested review are not `new_comment` events
at all.** A submitted review that requests changes fires `changes_requested`,
and that one run addresses every inline comment in it (they ride in its
`review_comments` context) and can re-request the reviewer afterwards. Firing
`new_comment` for each of those comments as well would put a fixer per comment
on the branch next to it, so the github connector folds them into the review —
provided a `changes_requested` trigger actually takes that review. If none does
(no such trigger, or its filter rejects that reviewer), or the review's state
can't be read, the comments stay `new_comment` events. Comments on a review that
only commented or approved are `new_comment` events as usual.

## The batch in templates

The run's representative context is the LAST event's (freshest tokens and
facts); the whole burst is under `{{.group.*}}`:

| ref | value |
|---|---|
| `{{.group.key}}` | the resolved key |
| `{{.group.events}}` | the list (each entry is that event's context) |
| `{{.group.count}}` | how many |
| `{{.group.first}}` / `{{.group.last}}` | the boundary events |

Code steps see the same under `ctx.group`.

Related: [[Configuration]] · [[Policy]] · [[Workflows]]
