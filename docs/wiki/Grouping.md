# Grouping (event batching)

By default every event is its own run, dispatched immediately. A trigger may
set `group:` to batch a burst of related events into one run:

```yaml
- on: gh.new_comment
  group: { key: "{{.repo}}#{{.pr}}", window: 15s }
  steps:
    - id: handle
      type: agent
      agent: fixer
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
  PR, a label.
- **`window`** — the debounce window, default `15s`: it resets on each new
  event and the batch fires once the group goes quiet.
- **`max_wait`** — caps how long a never-quiet group can defer, default 4 ×
  `window`.
- **At most one run per key is in flight.** Events arriving while a key's run
  is going buffer into the next batch, which starts its own debounce cycle
  when the run completes. `group: { key: "{{.pr}}" }` therefore gives
  one-agent-per-PR (no branch collisions) as a natural consequence.
- Distinct keys run independently. `policy.concurrency.max_agents` remains
  the only global cap; exact-duplicate events are still dropped by dedup — a
  separate mechanism that runs before grouping.

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
