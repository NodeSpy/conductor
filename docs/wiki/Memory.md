# Memory

A durable memory agents share across runs — what one agent learns is
available to the next. Entries are structured, with **provenance and scope**:

```json
{ "id": "m1a2b3c4d5e6", "text": "TestPoll flakes without a fake clock",
  "tags": ["flaky", "ci"], "scope": "repo:acme/api",
  "source": { "agent": "fixer", "run": "flow:failing_checks:acme/api#12",
              "trigger": "failing_checks", "repo": "acme/api" },
  "created": "2026-09-05T12:00:00Z" }
```

A memory records who learned it and from where, and is scoped `global`,
`repo:<owner/repo>`, or `agent:<name>`. Where a scope is written in config or
by an agent, the relative forms `repo` and `agent` resolve against the run
that writes (or reads) it.

## Backends (`memory:`) — pick one, no default

```yaml
memory:
  store: state                          # (a) a durable stores: KV entry
  # or  dir: ~/.config/conductor/memory # (b) one Markdown file per memory
  # or  type: memory                    # (c) ephemeral in-process
```

| backend | where entries live | properties |
|---|---|---|
| `store: <name>` | a `stores:` KV entry (`boltdb`/`redis`/`http`) under the `memory` namespace | durable; redis/http make it **fleet-shared** |
| `dir: <path>` (`type: file`) | one Markdown-with-frontmatter file per memory (`<id>.md`) | human-readable, hand-editable, git-friendly; durable without a store |
| `type: memory` | an in-process map | memory without configuring storage; gone on restart |

All three expose the same verbs, injection, and recall — the backend is just
where entries live. Exactly one is picked; no `memory:` section means no
memory, and every surface says so plainly.

The file backend's format is frontmatter (`id`/`tags`/`scope`/`source`/
`created`) over the text body. Hand-edited files are tolerated: a file
without (or with broken) frontmatter still reads as a memory — its ID is the
filename, its created time the file's mtime, its scope global.

```markdown
---
id: m1a2b3c4d5e6
tags: [flaky, ci]
scope: repo:acme/api
source: { agent: fixer, trigger: failing_checks, repo: acme/api }
created: 2026-09-05T12:00:00Z
---

TestPoll flakes without a fake clock
```

## Verbs

Always-on (like `kv.*`/`sql.*`), audited, and load-checked: a `uses:
memory.*` step or hook in a config without a `memory:` section fails
`conductor validate`.

| verb | options | output |
|---|---|---|
| `memory.remember` | `text` (required), `tags?`, `scope?` | `{ id, scope }` — scope resolved (`repo` → the run's repo) |
| `memory.recall` | `tags?` (ALL must match), `scope?`, `substring?`, `limit?` | `{ memories, count }` — newest first |
| `memory.forget` | `id` (required) | `{ found }` |
| `memory.list` | `scope?` | `{ memories, count }` |

Verb writes carry the run's provenance automatically — the flow runner
stamps `run`/`trigger`/`repo` on every step and hook.

## Three write paths

1. **Output contract** — an agent's final output may carry a `remember:`
   block; conductor parses it post-run and persists with provenance (the
   agent profile's name included). Works for **every** runtime, no
   per-runtime tooling. Two accepted shapes:

   ~~~
   ```remember
   - text: TestPoll flakes without a fake clock
     tags: [flaky, ci]
     scope: repo
   - plain one-line memories work too
   ```
   ~~~

   or, in a structured (JSON / `output_schema`) output, a `remember:` key —
   a string, a list of strings, or a list of `{ text, tags?, scope? }`
   objects. Harvest is best-effort: a malformed block is logged and audited,
   never a step failure; dry-run/shadow never writes.

2. **Workflow steps / hooks** — explicit `uses: memory.remember`, e.g. an
   `at: done` hook storing a summary. Deterministic.

   ```yaml
   hooks:
     - at: done
       uses: memory.remember
       options: { text: "{{.assess.summary}}", tags: [summary], scope: repo }
   ```

3. **Runtime tool surface** — a live `remember`/`recall` tool mid-run, on
   runtimes that support live tool injection. ACP agents get it as an MCP
   server on `session/new` (`conductor mcp memory`, proxying to the daemon
   over a local socket; the dispatch's provenance is baked into the launch
   flags). Runtimes without live tools — today's paseo CLI dispatch, plain
   `cli` runtimes — **fall back to the output contract + steps**: same
   memory, written at the end of the run instead of during it. Remote
   (`host:`) sessions also use the fallback (the daemon's socket is local).

## Injection — opt-in per profile

An agent gets memory in its prompt only when its profile asks:

```yaml
x-templates:
  fixer: &fixer
    type: agent
    memory: true                                    # the shared set + this run's context keys
  reviewer: &reviewer
    type: agent
    memory: { scopes: ["${repo}", "${step}"], tags: [ci], limit: 10 }
```

Scope keys are **opaque strings** the memory core never interprets — there
are no `repo`/`agent` scope TYPES. The engine supplies the run's context keys
by convention (the repo string, the workflow name, the step identity), and
`${repo}` / `${workflow}` / `${step}` are sugar it expands. The opt-in gates
writing as well as reading: a step that did not ask for memory cannot harvest
its output into the shared store.

`memory: true` injects the global, target-repo, and the agent's own scoped
memories (newest first, capped at 20) through the same append path
`agent_guidance` uses; the filter map narrows scopes/tags/limit. Profiles
that don't opt in get **nothing** — no token cost. The injected section also
tells the agent how to leave notes back (the output contract above).

## Recall (v1)

Tags + scope + recency: filter by `tags` (all must be present), `scope`,
optional case-insensitive `substring`; newest first; `limit` N. Pure Go and
deterministic — lexical or semantic ranking can layer on later without
changing this surface.

## Code and templates

- **Code steps** (js / go-embed / risor / lua) get `ctx.memory` mirroring the
  verbs: `ctx.memory.remember(text, tags?, scope?)`, `.recall({tags, scope,
  substring, limit})`, `.forget(id)`, `.list()` (risor: a top-level `memory`
  module; go-embed: `import "conductor/memory"`). Code writes carry no run
  provenance, so relative scopes need their explicit forms there
  (`repo:<owner/repo>`, `agent:<name>`).
- **Templates** — `{{ memory "<scope>" <limit> }}` renders the newest N
  memories of a scope as `- text` lines, read-only:

  ```yaml
  prompt: |
    Triage this. Known context:
    {{ memory "repo:acme/api" 5 }}
  ```

## Audit

Every write is audited: verb calls through the standard verb audit
(`event: verb, connector: memory`), output-contract harvests and live tool
calls as `memory_remember` entries (`via: output` / `via: tool`) with the
agent, scope, and id — so `report` reflects what the fleet is learning
without leaking the text.
