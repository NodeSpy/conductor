# Verbs

A verb is a connector's outbound action, invoked from a step or a hook as an
**action unit**: `{ uses, options, if, id }`.

```yaml
steps:
  - id: announce
    uses: slack-ops.post
    options: { channel: "#releases", text: "released {{.tag_name}}: {{.url}}" }
hooks:
  - { at: start, uses: slack-ops.react, options: { channel: "{{.slack.channel}}", ts: "{{.slack.ts}}", emoji: eyes } }
```

## Two kinds

- **Fire-and-forget** — `post`, `react`, `comment`, `rerequest_review`,
  `add_labels`, … They return a small acknowledgment (ids, urls) as the
  step's outputs.
- **Request-response (`ask`)** — present to a human and block for the answer.
  Outputs: `{action: approve|revise|discard, text, ref}`. See [[Hand-offs]].

## Options

Rendered templates (`{{…}}`) work in any option value; a value that is
exactly one reference keeps its underlying type (`pr: "{{.pr}}"` stays a
number). Options merge over the connector's default `options:` — the call
wins, nested maps merge. `conductor validate` rejects unknown option keys and
missing required ones (a required key satisfied by a connector default
passes); `conductor schema <conn>` prints each verb's option and output
schemas.

## Identity (`as:`)

GitHub verbs accept `as: me` (default — your token: `identity.write_token`,
`gh auth token`, or the connector's `token:`) or `as: bot` (an App
installation token; requires `app:` credentials). Set it once on the
connector's `options:` or per call.

## Failure, rate limits, audit

- A failing **step** verb stops the workflow (unless the step sets
  `continue_on_error: true` or a `retry:`) and fires the `at: fail` hooks.
- A failing **hook** verb is best-effort: logged and audited, never fatal.
- The connector policy's `rate_limits: { per_minute: N }` caps its verb
  calls; invocations past the cap wait for the rolling window.
- Every invocation is written to the audit log — connector, verb, options
  (with **secret values redacted**), outcome — so `conductor report` reflects
  cross-boundary activity without leaking credentials.

## Dry-run

`conductor replay <event.json>` (and `shadow:`/`dry_run:`) stubs every verb:
the audit records "stubbed" and later steps see zero-valued outputs shaped by
the verb's output schema plus `stubbed: true`.

Related: [[Connectors]] · [[Workflows]] · [[Hand-offs]] · [[Policy]]
