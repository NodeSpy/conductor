# RSS connector

Polls RSS 2.0 / Atom feeds; each declared feed name is an event. No webhooks,
no credentials.

```yaml
connectors:
  upstream:
    type: rss
    feeds:
      changelog: { url: https://example.com/releases.atom, interval: 30m }

triggers:
  - on: upstream.changelog
    filters: { match: "(?i)security|breaking" }   # regex over title + summary
    steps:
      - { id: read, type: agent, agent: planner, checkout: none,
          prompt: "Read {{.item.link}} and summarize what affects us." }
```

Context: `item.title`, `item.link`, `item.id`, `item.summary`,
`item.published`, plus `url`. The first poll of a feed seeds the seen-set
without emitting (no backlog flood); later polls emit genuinely-new items.
Dedup across restarts rides the item's GUID/link.

Legacy `integrations: - type: rss` still loads; [[Migration]] moves feeds
onto the connection and each feed's `match:` onto its trigger.

Related: [[Connectors]] · [[Grouping]]
