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
    memory: { scopes: ["team:{{.inputs.team}}", "{{.step}}"], tags: [ci], limit: 10 }
```

Scope keys are **opaque strings** the memory core never interprets — there
are no `repo`/`agent` scope TYPES, and which keys a deployment files memory
under is its own choice: a repo, a team, an owner/org, a label, an input the
workflow was called with. A key you write is a **template** over the step's
facts ([below](#scope-keys-are-templates)). By default the engine adds the
run's context keys (the repo string, the workflow name, the step identity).
The opt-in gates writing as well as reading: a step that did not ask for
memory cannot harvest its output into the shared store.

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
memory: { extra_scopes: ["team:{{.inputs.team}}", "org:{{.owner}}"], limit: 40 }
```

Recall is still one newest-first list under one `limit`, across every key.

## Scope keys are templates

Everywhere you write a scope key — a step's `memory:` `scope`/`scopes`/
`extra_scopes`, a memory verb's `scope:` grant ([below](#limiting-where-a-step-writes)),
and the `scope:` option of a `uses: memory.remember` step or hook — the key is
a template, rendered per run over **the step's facts**:

```yaml
memory:
  extra_scopes:
    - "team:{{.inputs.team}}"          # an input the workflow was called with
    - "org:{{.owner}}"                 # the repo's owner
    - "standard:{{.repo}}"             # the repo — one choice among many
    - "area:{{.steps.triage.outputs.area}}"
```

A key with no `{{` is used verbatim. `${repo}`, `${workflow}` and `${step}`
are kept as aliases for `{{.repo}}`, `{{.workflow}}` and `{{.step}}`.

**The facts** are the data the step's prompt renders against, less two
things — named secrets (`secrets`) and vault values (`vaults`) are never
facts, and a tracked secret value a step output echoed is redacted:

| fact | what it is | who chooses it |
|---|---|---|
| `repo`, `owner`, `name` | the dispatch's own target repo and its halves | the platform |
| `number`, `pr`, `issue` | the issue/PR number | the platform |
| `kind` | the trigger kind conductor resolved | your config |
| `workflow`, `step` | the run's memory context keys | your config |
| `url` | the target's web URL | the platform |
| `head`, `base` | head SHA and base branch | the **PR author** |
| `title`, and the connector's event context (`head_ref`, `author`, `labels`, `comment_body`, …) | copied from the event | the **PR author** (labels: anyone who can label) |
| `inputs.*` | a called workflow's inputs | whoever called it — may carry event text |
| `steps.<id>.outputs.*`, `<id>.*` | earlier steps' outputs | for agent steps, the **agent** |
| `group` | a `group:` batch's events | as above, per event |

The target facts come from the dispatch's **own** target: when a webhook
builds `repo:` from the request body, the sender chose it, and `repo`,
`owner`, `name`, `number`, `pr`, `issue`, `head`, `base` and `url` are empty
for scope keys — a forged repo cannot key another tenant's memory.

**Fail closed.** A key that reads a fact this step has no value for — absent,
`""`, or a zero number — **does not apply**: recall drops it (never the
literal text, never a bare `team:`); a grant entry admits nothing; a
`memory.remember` step refuses rather than write under a partial key. If
every key of an explicit `scopes:` list drops, the step recalls nothing
rather than falling back to the defaults. Supply a fallback where one makes
sense: `team:{{.inputs.team | default "core"}}`.

**What a key may do.** Values and pipelines only — no `if`/`range`/`with`/
`define`/`template`, no variables — and only `default`, `coalesce`, `index`,
`print` and `printf`. No `kv`, `vault`, `secret` or `memory`: rendering a key
reads nothing. A key carrying a control character (written or rendered) is
refused. A malformed key fails `conductor validate` and config load.

**A rendered value is literal.** It is never rendered again (a PR title of
`{{.repo}}` stays that text), and in a grant it matches by equality: a fact
that renders to `*` or `feature/*` matches only that exact key, never as a
glob. Glob intent lives in the static entries you write (`team/*`).

**Environment variables.** `{{…}}` is never touched by config loading. The
`${repo}` / `${workflow}` / `${step}` aliases are left for the engine only in
a memory selector's `scope`/`scopes`/`extra_scopes` and a memory verb's
`scope:` grant; everywhere else — a prompt, a URL, a pack setting value you
pass, the top-level `memory:` section, a selector's `tags:` — they are
ordinary environment references like any `${VAR}`. To hand a key through a
pack setting, write it with `{{…}}`:

```yaml
packs:
  review:
    use: ./packs/review
    settings: { scope_key: "team:{{.inputs.team}}" }   # the pack puts ${settings.scope_key} in extra_scopes
```

**Check a key** with core's renderer — the same code a run uses:

```sh
conductor memory key --template 'team:{{.inputs.team}}' --facts facts.json   # or --fact name=value …
```

It prints the key, or exits non-zero with why it does not apply. It reads no
config and no memory. Test vectors for the grammar are in
`internal/memory/testdata/scope_key_vectors.json`.

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
    memory.remember: { scope: ["team:{{.inputs.team}}", "standard:{{.owner}}", "notes/*"] }
    memory.recall: {}
```

For memory the list is a **restriction**, not a widening: once a step lists
scopes for a memory verb, it may use those and nothing else. The dispatch's
own scope is not implied, an unscoped write is refused, and the daemon-wide
`policy.agent_authored.verbs."memory.*"` list does not add to it — nor does
`trust: full` lift it. The reserved `global` bucket stays refused to
`memory.remember` and `memory.forget` whatever is listed — `*` included — so
a step can neither write nor delete the shared set every opted-in agent is
fed; reading it stays open. The restriction binds every way the step's agent
can write memory: the `memory.remember` verb, the live memory tool, and the
```remember output contract.

Each entry is a [scope key](#scope-keys-are-templates). A static entry keeps
its glob (`notes/*`); a templated one is rendered over the step's facts when
the step is dispatched — the facts exist then, so the session carries the
rendered list — and matches **literally**. An entry that reads a fact the
step has no value for matches nothing. `scope: []` admits nothing.

> **Use facts the PR author cannot choose in a write grant.** A grant entry
> is a security check. `branch:{{.head_ref}}`, `{{.title}}`, `{{.author}}`,
> a label, an input carrying event text or an agent step's output lets
> whoever chose that value choose which key the step may write — a branch
> named `payments` makes `team:{{.head_ref}}` admit `team:payments`. Prefer
> `repo`, `owner`, `name`, `number`, `kind`, `workflow`, `step` and static
> entries; reach for an author-chosen fact only when the key it names is
> meant to be theirs. (Recall keys carry the same caution in the other
> direction: an author-chosen key decides which notes the agent reads.)

A step without a `scope` list keeps the earlier rules: its own scope plus
whatever `policy.agent_authored.verbs."memory.<op>".scope` lists.

**Patterns that match the same verb merge — the union, never the
narrowest.** A verb's list is every `scope` entry from every pattern in the
step's `verbs:` that matches it, the same rule as every other verb's scope
list (listing only ever widens). So `scope: []` is a hard "nothing" only when
it is the sole pattern naming that verb:

```yaml
skill:
  verbs:
    memory.*:        { scope: [] }                          # does NOT lock remember down…
    memory.remember: { scope: ["team:{{.inputs.team}}"] }   # …remember admits team:<team>
```

Here `memory.remember` admits `team:<team>`, and `memory.recall`,
`memory.list` and `memory.forget` (matched only by `memory.*`) admit nothing.
To lock one verb out entirely, grant it with `scope: []` and no other pattern
that matches it — or leave it out of `verbs:`.

## Operator commands

Two commands run on the box against the configured backend (not through the
daemon), and refuse inside an agent session:

```sh
# Move every entry tagged `outcome` from <scope> to outcome:<scope>.
conductor memory rescope --tag outcome --to 'outcome:{scope}'           # dry run: prints the moves
conductor memory rescope --tag outcome --to 'outcome:{scope}' --apply   # writes them

# Archive one scope's entries (a key, * or a glob; combinable with --tag).
conductor memory rescope --scope 'standard:acme/api' --to 'archive:{scope}' --apply
conductor memory rescope --scope 'team:*' --tag stale --to 'archive:{scope}'

# Write a pack carrying these scopes' entries as read-only memory.
conductor memory export --scope acme/api --scope standard:acme/api --scope emphasis:acme/api \
  --out ../acme-training --name acme-training
```

- `rescope` selects by `--tag`, by `--scope` (a literal key, `*` for every
  key, or a glob — `*` does not cross a `/`, so `team:*` matches `team:a`
  but not `team:a/b`; the shared set is `global`), or by both (an entry must
  match both); at least one is required. It keeps each entry's id, text,
  tags, provenance and created time, and replaces `{scope}` with the entry's current key
  (`global` for the shared set). An entry already in the template's shape is
  left alone, so a second run moves nothing. It works on either durable
  backend; a boltdb `store:` is locked while the daemon runs — stop it first.
  Entries a pack ships are never touched.
- `export` writes `conductor-pack.yaml` (`pack:` metadata and `memory: {
  entries: memory }`) and a copy of each entry under `memory/` carrying only
  its id, tags, scope, created time and text. Provenance — the repo, step,
  run, trigger and dispatch that wrote it — stays on this box: a pack is
  meant to be shared, and the scope keys you export are the only names in
  it. It reads the source and never writes it. It refuses to replace an existing manifest
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
