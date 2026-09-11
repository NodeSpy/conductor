# Settings and templating

Two ways to stop repeating yourself in a config, for two different problems.

|  | `${settings.NAME}` | `{{ .fact }}` |
|---|---|---|
| when | at load, once | per dispatch |
| where | anywhere in the config, every imported file | scope allowlist entries |
| for | a value that repeats — a channel, an org, a repo glob | a value that depends on the event |

```yaml
settings:
  review_channel: "#code-reviews"
  org:            acme
  deploy_repo:    "${settings.org}/deploys"   # settings chain
  bot_channel:    "${env.BOT_CHANNEL}"        # from the environment

policy:
  agent_authored:
    allow_scopes:
      channel: ["${settings.review_channel}"]
      repo:    ["${settings.deploy_repo}", "{{.owner}}/docs"]
```

## `settings:` — one place to change it

A top-level `settings:` block is a map of names to values. Every
`${settings.NAME}` in the config is replaced by that value **before the config
is parsed**, so it works in any field — an allowlist entry, a channel, a repo
glob, a prompt, a host name. There is no list of "fields that support
settings".

It also reaches **every imported file**, in both directions: a setting declared
in `conductor.yaml` lands in `triggers/review.yaml`, and one declared in an
import lands in the root file. Split your config however you like; the settings
still apply.

A value can be:

```yaml
settings:
  plain:   "#code-reviews"         # a literal
  fromenv: "${env.REVIEW_CHANNEL}" # the environment — conductor.env included
  built:   "${settings.org}/api"   # another setting
```

`${env.NAME}` reads the process environment, which is where `conductor.env`
ends up: park a value there and the setting picks it up with no other wiring.

### It fails loudly, on purpose

A parameter that silently becomes empty turns an allowlist entry into one that
matches nothing, and you find out during an incident. So each of these is a
**load error**, named:

- `${settings.revue_channel}` when you declared `review_channel` — an
  undeclared reference (one inside a `#` comment is not a reference);
- a setting that resolves to an empty value;
- `${env.X}` where `X` is not set.

Unrelated syntax is untouched: the loader's own `${VAR}` environment expansion
and a step's shell `${VAR}` have no `settings.` prefix, so they keep their
meanings.

A setting supplies a **value**, never structure. Substitution happens inside
the parsed document rather than in its text, so a value containing newlines,
quotes, `#` or `key:` lands as exactly that string — it cannot close a scalar
and open a key you did not write. That matters most for a value you did not
author yourself: a pack's default, or a variable from a shared environment.

> Packs have had this all along ([[Packs]]) — a pack declares `settings:` and
> you supply the values. This is the same mechanism for your own config.

## `{{ }}` in a scope allowlist — per event

A [[Policy|scope allowlist]] entry that contains `{{` is rendered against the
event being dispatched, then matched as usual:

```yaml
skill:
  verbs:
    slack.post: { channel: ["#pr-{{.number}}"] }   # this PR's channel

policy:
  agent_authored:
    allow_scopes:
      repo: ["{{.owner}}/docs"]                    # this org's docs repo
```

On PR 42 the first grant means `#pr-42` and nothing else; on PR 99 it means
`#pr-99`. One line, instead of one line per PR — or a wildcard you didn't want
to write.

**The available facts are a closed set**: `number`, `owner`, `name`, `repo`,
`kind`. That is all of them, and the rule for the list is one question — *can
the author of a pull request choose this value?*

`title`, `head_ref`, `author`, `labels`, `comment_body` and the connector's
enriched event context are therefore **not** available: they are free text the
person who opened the PR writes, so an entry built from one could be forged by
naming a branch (or a PR title) after the resource you wanted. `inputs` is out
for the same reason — an input can carry event text verbatim. An entry
referencing any of them renders empty and matches nothing.

Entries with no `{{` are not rendered at all. A rendered value is matched
**literally**: your glob intent belongs in the pattern you wrote (`acme/*`),
not in a value the event supplied.

### What it deliberately cannot do

A scope allowlist is a **security check**, so the renderer is not the one steps
use:

- **No secrets.** `{{.secrets.x}}` and `{{.vaults…}}` are not in scope, and a
  credential a connector published into its event context is redacted before
  the render sees it. A check that can read a secret can be made to leak it.
- **No `kv`, no `vault`, no side-effecting functions** — only `default` and
  `coalesce`. An authorization check does not perform reads.
- **Nothing the agent supplied.** The option value being checked is never a
  render source. The agent cannot influence what it is checked against.
- **Fail-closed.** A template that errors, names a fact this dispatch doesn't
  have, or renders to empty matches **nothing**. It never falls open.

## Which one do I want?

- The value is the same for every event → **`${settings.X}`**. It works
  everywhere in the config, not just in allowlists.
- The value depends on the event → **`{{ .fact }}`**, in the allowlist entry.
- Both, in the same list, is fine:
  `channel: ["${settings.review_channel}", "#pr-{{.number}}"]`.

## See also

- [[Policy]] — `allow_scopes` and the rest of `policy.agent_authored`
- [[Agent-Skill]] — per-verb grants (`skill.verbs`) and their scoping
- [[Packs]] — a pack's own `settings:` block
- [[Configuration]] — `imports:`, `conductor.env`, and the load order
