# Configuration

`~/.config/conductor/config.yaml`, secrets in the sibling chmod-600
`conductor.env` (`${VAR}` expands at load; a referenced-but-unset variable is
a load error naming it). `conductor validate` checks everything below —
including every template reference against the scope at its position — before
the daemon runs. The full annotated example ships as `config.example.yaml`.

The LEGACY schema (`integrations:`/`notify:`/`handoffs:`/`controllers:`/
`control:`/`paseo_bin`) still loads and runs unchanged, and auto-migrates on
boot — see [[Migration]] and `config.example.legacy.yaml`.

## Top level

| key | what | reference |
|---|---|---|
| `connectors:` | named service connections: type, credentials, `me:`, default `repos:`, default `options:`, `enabled:`, per-connector `policy:` | [[Connectors]] |
| `triggers:` | the workflows: `on` / `filters` / `steps` / `hooks` (+ `group`, `policy`, `name`, `enabled`, `options`, `repo`, `shadow`) | [[Workflows]], [[Grouping]] |
| `runtimes:` | where agents run: `type`/`agent`, `transport`, `bin`, `host`, `default` | [[Runtimes]] |
| `agents:` | named profiles: `provider`, `model`, `thinking`, `mode`, `runtime`, `workspace`, `wait_timeout`, `archive_when_done`, `labels`, `guidance`, `host` | [[Agents]] |
| `hosts:` | named SSH targets: `host`, `user`, `port`, `key`, `known_hosts`, `cwd`, `env` | [[Hosts]] |
| `workflows:` | reusable step lists with `inputs:` / `outputs:` | [[Workflows]] |
| `policy:` | global controls; also valid on connectors and triggers (most specific wins) | [[Policy]] |
| `secrets:` | named secret references, read as `{{.secrets.<name>}}` | [[Secrets]] |
| `notify:` | daemon lifecycle notifications (unchanged from legacy) | [[Notifications]] |
| `imports:` | split the config across files (globs, deep-merged) | below |
| `store:` | `state_file`, `audit_log`, `state_ttl`, `max_tracked_prs`, `audit_max_size` | |
| `update:` | `auto`, `interval`, `apply` — self-update; migration runs on the new binary's first boot | |
| `dry_run:` | stub every dispatch and verb | |
| `agent_guidance:` | house prompt guidance appended to every agent (per-profile `guidance:` overrides) | [[Agents]] |
| `adopt_open_workspaces:` | route PR feedback to a workspace already on the branch | |

## The trigger grammar in brief

```yaml
triggers:
  - on: <connector>.<event>       # what fires it
    filters: { … }                # whether it fires (event-schema keys, AND-ed)
    group: { key: …, window: 15s }# optional burst batching
    steps: [ … ]                  # agent | command | run: code | uses: verb | use: workflow
    hooks: [ {at: start|done|fail, uses: <conn>.<verb>, options: {…}} ]
    policy: { … }                 # trigger-scoped overrides
```

Steps address the trigger context (`{{.repo}}`, event facts), prior step
outputs (`{{.<id>.<field>}}`), named secrets (`{{.secrets.x}}`), and the
batch (`{{.group.*}}`). `if:` conditions use comparison, `&&`/`||`/`!`,
`contains()`, `exists()`, `default()`, and `coalesce()`; templates may also
call `default`/`coalesce` (`{{.sev | default "low"}}`). See [[Workflows]].

## Bot-authored comments (github)

The github comment/review events (`new_comment`, `changes_requested`)
publish `author` and `author_is_bot` in their context — true when the
webhook's actor account type is `Bot` or the login ends in `[bot]`
(dependabot[bot], cursor[bot]). The matching `author_bot` filter gates a
trigger on it: `filters: { author_bot: false }` fires only for humans,
`true` only for bots, absent for either.

`policy.reply_to_bots` (github connector `policy:`, trigger-overridable,
global default allowed) gates the conversational reply BACK to a bot author.
Fixes, thread resolution, and labels always run — only the reply is gated:

| mode | behavior |
|---|---|
| `decline_only` (default) | the agent is instructed to skip thanks/acknowledgements and reply only to state a concrete reason for not applying a suggestion |
| `off` | the flow runner skips `comment`/`reply` verbs on github connectors for that run (logged and audited) |
| `full` | no gating |

## Splitting the config across files (`imports:`)

Imports live under each section. A map section — `connectors:`, `runtimes:`,
`hosts:`, `agents:`, `workflows:` — takes an `imports:` key listing files or
globs (relative to the importing file) whose entries join that section,
alongside inline entries. The `triggers:` list takes imports as list items.

One vocabulary: **`imports:`** (plural) is a list of file globs, used by
every section — including the `triggers:` list, as a `- imports: [...]` item.
**`import:`** (singular) is exactly one file, only as a named-entry body or a
workflow step ref.

```yaml
connectors:
  imports: [conf.d/connectors/*.yaml]        # entries from these files join the section
  gh: { type: github, … }                    # inline entries mix in
  pd: { import: ./conf.d/pagerduty.yaml }    # a named entry's BODY from its own file
workflows:
  imports: [workflows/*.yaml]
triggers:
  - imports: [triggers/*.yaml]               # spliced at this position
  - on: gh.review_requested                  # inline triggers mix in
    steps: [ { workflow: review-flow } ]
```

An imported section file holds bare entries (`timer: { type: cron, … }`) or
the section-wrapped form (`connectors: { timer: … }`); an entry-body file
holds the body directly; a trigger file holds a bare list or a `triggers:`
block. **Merge, not last-wins:** a name defined in two files (or a file and
inline) fails the load naming the key and both sources. A glob matching no
files is an error, never a silent no-op. Validation runs over the merged
config, so cross-file `{{…}}`/`workflow:`/`uses:` references are checked at load.

A workflow can also be pulled in per step, without a section import — see
`workflow:`/`import:` in [[Workflows]].

The legacy TOP-level `imports:` (whole-document deep merge: maps merge
recursively, lists concatenate, the importing file's keys win) is unchanged,
and auto-migration still walks it, transforming each legacy file with its own
backup.

## Validation and fleet safety

- `conductor validate` (and boot) resolve every `on:` kind, `filters:` key,
  `uses:` verb, option map, workflow input/output, and `{{…}}`/`if:`
  reference against the connectors' published schemas AND the scope at that
  position. A config that validates cannot reference a value that will not
  exist when the step runs.
- A connector whose credentials or secrets fail to resolve is disabled with
  the reason recorded — never a crash loop; the daemon boots and runs the
  rest.
- Introspection: `conductor connectors ls`, `conductor schema <conn>`,
  `conductor secrets check`; dry-run: `conductor replay <event.json>`.

Related: [[Connectors]] · [[Workflows]] · [[Policy]] · [[Secrets]] · [[Migration]] · [[Commands]]
