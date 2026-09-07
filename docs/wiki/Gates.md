# Quality gates on agent output

A **gate** (#36 §16) runs checks against an agent's *proposed* change —
tests, lint/build, a critic-agent verdict, any verb with a pass/fail
reading — before the step's result promotes into the run. The verdict drives
**promote / revise / discard**: all checks pass → the step completes; a
failure loops back to the *same* agent as a revise follow-up (bounded by
`max_revisions`); exhausted → the run escalates (`needs_input`), the step
fails, and nothing downstream runs. "The agent did something" becomes "the
agent did something that passes."

## Config

```yaml
checks:                                # named checks — each is ONE ordinary step
  test:   { type: command, command: ["make", "test"] }        # exit 0 = pass
  lint:   { run: sh, code: "golangci-lint run ./..." }        # non-error = pass
  critic:                                                     # agent verdict
    type: agent
    agent: reviewer
    prompt: |
      Review the proposed change in {{.gate.workdir}} (git diff HEAD).
      Output JSON: { "pass": true|false, "reason": "…" }
    output_schema: { type: object, required: [pass], properties: { pass: { type: boolean } } }

triggers:
  - on: gh.review_requested
    gate: { run: [ test, lint ] }      # default for every agent step below
    steps:
      - id: fix
        type: agent
        agent: fixer
        prompt: "Address the review comments…"
        gate:                           # this step's own gate wins
          run: [ test, lint, critic ]
          require: pass                 # the default (and only) criterion
          max_revisions: 2              # revise rounds before escalating (default 3)
```

`gate:` sits on a foreground **agent step**, or on a **trigger** /
**workflow** as the default for its agent steps (the step's own wins).
Checks live in the top-level `checks:` map and reuse the ordinary step
machinery — command, code, verb, or a critic agent. Background hand-off
steps are never gated (the interactive review *is* their gate) and a check
cannot fan out, be a background agent, or carry its own gate — `conductor
validate` enforces all of it.

## Where checks run

Checks run **in the agent's worktree** — command/code checks get it as their
working directory, and a critic agent launches with `workdir` pinned there
(`checkout: none`, no second checkout). Every check's template scope carries:

| field | |
|---|---|
| `{{.gate.workdir}}` | the proposed change's directory |
| `{{.gate.step}}` / `{{.gate.check}}` | the gated step / this check |
| `{{.gate.attempt}}` | 1-based round (revisions re-run the checks) |
| `{{.gate.output}}` | the agent's final output (clipped) |

A command/code check with no local worktree to run in (remote runtime,
`checkout: none`) **fails loudly** — never a silent pass; give such a check
an explicit `workdir:`/`host:` or run the agent locally.

## Verdict reading

- The check step **errors** (non-zero exit, failed verb) → **fail**, with the
  error text as detail.
- Outputs carry `pass: false` → **fail** (detail from `reason`/`detail`/
  `text`/`stderr`).
- A **critic agent** must output `pass: true|false` explicitly — a critic
  with no verdict is a fail, not a shrug (declare it in `output_schema`).
- Anything else that completes → **pass**.

## The revise loop

On failure the gate composes a follow-up carrying every failing check's
name + detail and delivers it to the **same agent**: a session-bound profile
(§10) through its live session, a paseo agent through a captured `send`.
The agent amends its work in the worktree, and the checks re-run. Runtimes
that can't take follow-ups (oneshot CLI recipes) escalate on the first
failure instead — honest, never a guess. Plan sub-agents (§11) run through
the same step machinery, so gated steps inside agent-authored plans revise
the same way.

Every round is audited (`event: gate`, outcome `pass` / `fail` / `revise` /
`escalated`) — the outcome loop (§18) and `conductor report` read these —
and a passing step's outputs carry `gate: { passed: true, rounds: N }`.

Related: [[Workflows]] · [[Agents]] · [[Policy]] · [[Runs]]
