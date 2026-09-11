# Parameterizing scope allowlists (and config generally)

Resource scoping (docs/design/skill-verb-scope.md) gave conductor allowlists
keyed by connector-declared dimensions. They work, and they are static: every
value is a literal written into the config. Two things that literal can't be:

- **the same value in twenty places.** A review channel, an org name, a repo
  glob — change it once and you change it everywhere, or you miss one and the
  miss is a refusal at 3am.
- **a value that depends on the event.** "The channel for THIS PR" is one
  sentence and no number of literals expresses it.

Those are different problems with different answers, and conflating them is
how a config language ends up with one mechanism that is both underpowered and
unsafe. So: two mechanisms, one resolved at LOAD and one at DISPATCH.

| | `${settings.NAME}` | `{{ .fact }}` |
|---|---|---|
| resolved | at load, once | per dispatch, at the check |
| source | the config's own `settings:` block (literal, `${env.X}`, or another setting) | the dispatch's trusted facts |
| varies by | nothing — same for every event | the event |
| scope | the WHOLE config body, every field, every imported file | scope allowlist entries only |
| authored by | the operator (or a pack consumer) | the operator |

```yaml
settings:
  review_channel: "#code-reviews"
  org:            acme
  deploy_repo:    "${settings.org}/deploys"    # chains
  bot_channel:    "${env.BOT_CHANNEL}"         # from the environment

policy:
  agent_authored:
    allow_scopes:
      channel: ["${settings.review_channel}"]  # static: one place to change it
      repo:    ["{{.owner}}/docs"]             # dynamic: this dispatch's org

triggers:
  - on: gh.review_requested
    steps:
      - type: agent
        skill:
          verbs:
            slack.post: { channel: ["#pr-{{.number}}"] }   # this PR's channel
```

## A. `${settings.NAME}` — load-time substitution

Packs already had this: a manifest declares `settings:` and the consumer's
values are substituted into the manifest body before it is decoded. The
mechanism was right and its scope was wrong — it was available to a pack
author and not to the operator writing the config the pack lands in.

It is now a top-level block of the main config, going through the SAME
functions (`internal/config/settings.go`), so the syntax, the iteration bound,
and the unknown-reference rule cannot drift between the two.

- **Text substitution on the body, before the strict decode.** The value lands
  wherever the reference sits — an allowlist entry, a channel, a prompt — with
  no per-field plumbing and no list of "fields that support settings".
- **Across every imported file.** The import path makes two passes over the
  same traversal: pass 1 merges to discover what `settings:` the graph
  declares, pass 2 re-runs it with those resolved, substituting each file as it
  is read. So a setting declared in `conductor.yaml` reaches
  `triggers/review.yaml`, and one declared in an import reaches the root — and
  no traversal logic is duplicated to make that true.
- **A value may come from the environment** — `${env.NAME}` — which is also
  how a value parked in `conductor.env` arrives, since the CLI loads that file
  into the environment before the config is read. Settings chain, bounded at 8
  passes so a self-reference terminates.
- **Resolution order**: env → settings chaining → body substitution.

### Failure modes are loud, deliberately

A parameter that silently becomes `""` turns `channel: ["${settings.x}"]` into
an allowlist entry matching nothing, and the operator learns about it from a
refusal in the middle of an incident. So:

- a `${settings.X}` that survives into a REAL FIELD and names nothing declared
  is a load error (the scan runs on the re-marshaled struct, so a reference in
  a comment is not a reference);
- a setting that resolves to EMPTY — including an `${env.X}` that expands to
  nothing — is a load error;
- an undefined environment variable behind a setting is a load error naming
  the variable and the setting.

`${VAR}` (the loader's own env expansion) and a step's shell `${VAR}` are
untouched: neither carries the `settings.` prefix.

## B. `{{ .fact }}` — dispatch-time rendering

An allowlist entry containing `{{` is rendered immediately before matching,
then matched exactly as before (literal or glob). Entries without `{{` are
returned untouched — the fast path, so existing configs behave identically and
the common case renders nothing at all.

This is where the design has to be careful, because **a scope allowlist is a
security check that happens to be a template**. Three rules follow, and they
are non-negotiable:

### The render context is trusted facts only

`scopeRenderData` builds it from the dispatch: the target, the event, and the
workflow's `inputs` (operator-authored plumbing for this run). What it
deliberately excludes:

- **the option value being checked.** It is the thing matched, never a render
  source. If the agent's own value could influence what the allowlist renders
  to, the check would be checking the agent's claim against itself.
- **previous-step outputs.** An agent step's output is agent-authored text; an
  allowlist that could be steered by it is not an allowlist.
- **secret material.** The `secrets`/`vaults` scopes are absent (baseData is
  built with no secrets map), credential-bearing trigger-context keys are
  dropped by name, and the whole map goes through the secret redactor — so a
  token that reached the trigger context under a name nobody thought to list
  comes out as its redaction marker rather than its value.

### The function set is restricted

`default` and `coalesce`. Nothing else — no `kv`, no `vault`, no `secret`.
They are pinned in `scopeTemplateFuncs` rather than derived from the step
renderer's set, so adding a side-effecting function to the step renderer can
never silently hand it to a security check. A check that performs a read is a
check that can be made to do work; a check that reads a secret is one that can
be made to leak it a character at a time, by asking whether `#{{.secrets.x}}`
matched.

### Failure is closed

A template that errors, references a fact this dispatch doesn't carry, or
renders to empty contributes NOTHING to the allowlist. It never falls open,
and it never matches an empty pattern.

## The security summary

One sentence for each half:

- `${...}` is operator-authored config, resolved at load, from sources the
  operator controls (literals, their own environment, their own other
  settings).
- `{{...}}` renders at dispatch against trusted event facts only — no secrets,
  no agent input, no side-effecting functions — and fails closed.

And the invariant both share: **the agent-supplied option value is only ever
the thing checked, never a source of what it is checked against.**

## Tests

- Part A: a main-config setting into a scope list; an env-backed setting;
  chaining; substitution reaching an imported file (both directions); an
  undeclared reference and an empty value as load errors; a reference in a
  comment that is not one.
- Part B: `#pr-{{.number}}` allows `#pr-42` on PR 42 and refuses `#pr-99`; the
  plan surface renders the same way; six fail-closed shapes (secret reference,
  unparseable template, missing fact, `kv`/`vault` calls, empty render); the
  render context carries no `secrets`, no `vaults`, no credential-bearing
  context key, no step outputs, and no raw tracked value; five injection
  attempts through the agent's own options, all refused.
- The scope meta-test gains a third axis: for every scoped option of every
  connector, an allowlist carrying BOTH a `${settings.X}` and a `{{.fact}}`
  entry enforces both — loaded through the real `config.Load`, so the static
  half is what a config file really produces.
