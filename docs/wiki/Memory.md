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

### Adding keys: `extra_scopes`

`scopes:` **replaces** the defaults — write it and you get exactly that list.
`extra_scopes:` **adds** to whichever applies (the defaults, or your
`scopes:`), so a step can pull in a few more keys without restating the shared
set and the run's own keys:

```yaml
memory: { extra_scopes: ["standard:${repo}", "emphasis:${repo}"], limit: 40 }
```

Recall is still one newest-first list under one `limit`, across every key.

### Placeholders inside a key

`${repo}`, `${workflow}` and `${step}` may be the whole key or part of one
(`standard:${repo}`), and a key may carry several. A key whose placeholder has
nothing to expand to on this run — `standard:${repo}` on a cron run with no
repo — is **dropped** for that run: it is never recalled as the literal text,
nor as the bare prefix `standard:`. If every key of an explicit `scopes:` list
drops, the step recalls nothing from it rather than falling back to the
defaults. `${repo}` is the dispatch's **own** repo: a target the event's
sender chose (a webhook `repo:` templated from the body) expands to nothing.

These three are not environment variables: the config loader leaves
`${repo}`, `${workflow}` and `${step}` alone, and every other `${VAR}` still
has to be defined.

## Memory a pack ships

A pack can ship memory **entries** — the same Markdown files the `dir:`
backend keeps — and every config that installs it recalls them read-only,
merged with its own:

```yaml
# conductor-pack.yaml
pack: { name: acme-training, version: 0.3.0, requires: { conductor: ">= 0.70.0" } }
memory:
  entries: memory          # a directory inside the pack: memory/<id>.md
```

- **Recall merges.** Every recall (prompt injection, `memory.recall`, the
  live tool) reads the instance's own entries plus each installed pack's,
  sorts the union newest-first, and applies the limit after the merge. An
  entry whose id the instance also holds is recalled once — the instance's
  copy — so a box that exported a pack and installed it does not see its
  notes twice.
- **Read-only.** Writes always go to the instance's own backend.
  `memory.forget` of a pack entry is refused (`memory: entry is read-only: …
  ships with pack <name>`); change it in the pack. `conductor memory rescope`
  never touches pack entries.
- **Inside the pack.** The directory resolves against the pack's vendored
  copy, whatever the source — a local folder, a public repo, a private one
  fetched with your `auth:`. A path that climbs out of the pack, an absolute
  path, or a symlink pointing out is a load error; `conductor pack lint`
  checks the directory exists.
- **Content, not infrastructure.** `entries:` is the only key a pack's
  `memory:` block may carry. `store:`, `dir:` and `type:` pick this daemon's
  backend and stay bind-only ([[Packs#governing-rule-define-behavior--bind-environment]]).
- A config with no `memory:` section recalls nothing, pack entries included
  (the daemon logs that the pack's entries are unused).

`conductor memory export` writes such a pack from your own entries — below.

## Limiting where a step writes

A step's `skill.verbs` grant can name the scopes the memory verbs may touch,
in the same map form that scopes any other verb:

```yaml
skill:
  verbs:
    memory.remember: { scope: ["{{.repo}}", "standard:{{.repo}}", "emphasis:{{.repo}}"] }
    memory.recall: {}
```

For memory the list is a **restriction**, not a widening: once a step lists
scopes for a memory verb, it may use those and nothing else. The dispatch's
own scope is not implied, an unscoped write is refused, and the daemon-wide
`policy.agent_authored.verbs."memory.*"` list does not add to it — nor does
`trust: full` lift it. The reserved `global` bucket stays refused whatever is
listed. The restriction binds every way the step's agent can write memory:
the `memory.remember` verb, the live memory tool, and the ```remember output
contract.

Entries match like every allowlist entry: a static entry keeps its glob; a
`{{ }}` entry renders against the dispatch's trusted facts and matches
literally. `${repo}` is accepted as a spelling of `{{.repo}}`. An entry that
reads a fact this dispatch has no value for — `standard:{{.repo}}` for a
target the event's sender chose — matches nothing. `scope: []` admits nothing.

A step without a `scope` list keeps the earlier rules: its own scope plus
whatever `policy.agent_authored.verbs."memory.<op>".scope` lists.

## Operator commands

Two commands run on the box against the configured backend (not through the
daemon), and refuse inside an agent session:

```sh
# Move every entry tagged `outcome` from <scope> to outcome:<scope>.
conductor memory rescope --tag outcome --to 'outcome:{scope}'           # dry run: prints the moves
conductor memory rescope --tag outcome --to 'outcome:{scope}' --apply   # writes them

# Write a pack carrying these scopes' entries as read-only memory.
conductor memory export --scope acme/api --scope standard:acme/api --scope emphasis:acme/api \
  --out ../acme-training --name acme-training
```

- `rescope` selects by tag, keeps each entry's id, text, tags, provenance and
  created time, and replaces `{scope}` with the entry's current key
  (`global` for the shared set). An entry already in the template's shape is
  left alone, so a second run moves nothing. It works on either durable
  backend; a boltdb `store:` is locked while the daemon runs — stop it first.
- `export` writes `conductor-pack.yaml` (`pack:` metadata and `memory: {
  entries: memory }`) and a copy of each entry under `memory/`. It reads the
  source and never writes it. It refuses to replace an existing manifest
  without `--force` (which also clears the old `memory/*.md`, so an entry you
  forgot since the last export does not survive), and refuses an entry that
  carries the value of one of the config's `secrets:`. Commit the directory
  to a repo — a private one is fine — and install it like any pack.

Outcome notes are written under `outcome:<repo>`, not the repo key
([[Outcomes#where-it-feeds-back]]); `rescope` is how notes from earlier
versions move there.

## Recall (v1)

Tags + scope + recency: filter by `tags` (all must be present), `scope`,
optional case-insensitive `substring`; newest first; `limit` N. Pure Go and
deterministic — lexical or semantic ranking can layer on later without
changing this surface.

## Code and templates

- **Code steps** (the js / go-embed / risor / lua engine plugins, and a local
  `use: cli` step) get `ctx.memory` mirroring the
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
